"""Bounded kubectl follow streams, batched judgments and session accounting."""
from collections import Counter, deque
import datetime
import hashlib
import json
import os
import queue
import re
import selectors
import subprocess
import threading
import time
import uuid

from .events import StreamRedactor, CONTINUATION, MAX_EVENT, MAX_LINE, TIMESTAMP, baseline, redact
from .jev import Jev, api_key
from .grouping import GroupedJudge, group_id
from .kubernetes import kubectl
from .report import build_report, write_report
from .usage import UsageMeter


class Cursor:
    """Inclusive timestamp reconnects preserve repeated identical occurrences."""
    def __init__(self):
        self.timestamp = None
        self.key = ""
        self.counts = Counter()
        self.replay = Counter()

    def reconnect(self):
        self.replay = self.counts.copy()

    def accept(self, raw):
        text = raw.decode("utf-8", "replace")
        match = re.match(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z ", text)
        if not match:
            return True
        timestamp = match[0].strip()
        key = match[1] + "." + (match[2] or "").ljust(9, "0")
        fingerprint = hashlib.sha256(raw).digest()
        if key < self.key:
            return False
        if key > self.key:
            self.timestamp, self.key = timestamp, key
            self.counts.clear()
            self.replay.clear()
        if self.replay[fingerprint]:
            self.replay[fingerprint] -= 1
            return False
        self.counts[fingerprint] += 1
        return True


class Grouper:
    def __init__(self, source, emit):
        self.source, self.emit = source, emit
        self.redactor = StreamRedactor()
        self.pending = None
        self.line = 0
        self.last = 0

    def feed(self, raw, truncated=False):
        self.line += 1
        text = raw.decode("utf-8", "replace").rstrip("\r\n")
        match = TIMESTAMP.match(text)
        timestamp = match[0].strip() if match else None
        body = text[match.end():] if match else text
        if not body:
            return
        safe = self.redactor.redact(body)
        if self.pending and CONTINUATION.match(body):
            combined = self.pending["text"] + "\n" + safe
            self.pending.update(text=combined[:MAX_EVENT], line_end=self.line,
                                line_count=self.pending["line_count"] + 1,
                                truncated=self.pending["truncated"] or truncated or len(combined) > MAX_EVENT)
        else:
            self.flush()
            self.pending = {"id": uuid.uuid4().hex[:20], "source": self.source,
                            "timestamp": timestamp, "text": safe[:MAX_EVENT], "line_start": self.line,
                            "line_end": self.line, "line_count": 1, "truncated": truncated or len(safe) > MAX_EVENT}
        self.last = time.monotonic()

    def flush(self):
        if self.pending:
            event, self.pending = self.pending, None
            self.emit(event)


def running_targets(pods):
    result = {}
    for pod in pods:
        meta = pod["metadata"]
        for kind, key in [("container", "containerStatuses"), ("init", "initContainerStatuses"), ("ephemeral", "ephemeralContainerStatuses")]:
            for status in pod.get("status", {}).get(key, []):
                if "running" not in status.get("state", {}):
                    continue
                identity = (meta["uid"], status["name"], status.get("restartCount", 0))
                result[identity] = {"type": "kubernetes", "namespace": meta["namespace"], "pod": meta["name"],
                                    "pod_uid": meta["uid"], "container": status["name"], "kind": kind,
                                    "restart_count": status.get("restartCount", 0), "previous": False}
    return result


class LiveSession:
    def __init__(self, args, client=None, run_kubectl=kubectl, popen=subprocess.Popen):
        self.args = args
        from .reviews import ReviewStore
        self.reviews = getattr(args, "review_store", None) or ReviewStore(getattr(args, "rules_file", ".runs/.review-rules.json"))
        self.review_key = getattr(args, "review_key", "")
        self.stop_event = threading.Event()
        self.done = threading.Event()
        self.lock = threading.RLock()
        self.usage = UsageMeter(args.input_price, args.output_price)
        self.client = None if args.offline else (client or Jev(api_key(), args.model, usage=self.usage, stop_event=self.stop_event))
        if client is not None:
            self.usage = client.usage
        self.run_kubectl, self.popen = run_kubectl, popen
        self.queue = queue.Queue(maxsize=args.queue_size)
        self.recent = deque(maxlen=args.retain_events)
        self.tail = deque(maxlen=args.retain_events)
        self.tail_index = {}
        self.counts = Counter()
        self.received = self.dropped = self.lines = self.batches = self.reconnects = 0
        self.streams = {}
        self.history = deque(maxlen=500)
        self.unfollowed = 0
        self.status, self.message = "starting", "Discovering running containers…"
        self.started = time.monotonic()
        self.created_at = datetime.datetime.now(datetime.timezone.utc).isoformat()
        self.context = args.context
        self.inventory_error = "Awaiting initial Kubernetes inventory"
        self.discovery_attempts = 0
        self.next_discovery_at = None
        self.last_inventory_success = None
        self.grouped_judge = GroupedJudge(self.judge_batch, limit=args.retain_events)
        self.reused = 0

    def judge_batch(self, batch):
        with self.lock:
            if self.stop_event.is_set():
                raise ValueError("Stopped before classification")
            if self.batches >= self.args.max_batches:
                self.stop("AI batch budget reached")
                raise ValueError("AI batch budget reached")
            self.batches += 1
        return self.client.judge(batch)

    def status_info(self):
        with self.lock:
            retry = max(0, round(self.next_discovery_at - time.monotonic())) if self.next_discovery_at else 0
            return {"status": self.status, "message": self.message,
                    "inventory_error": self.inventory_error, "discovery_attempts": self.discovery_attempts,
                    "retry_in_seconds": retry, "last_inventory_success": self.last_inventory_success}

    def stop(self, reason="Stopped by user"):
        with self.lock:
            if not self.stop_event.is_set():
                self.message = reason
            self.stop_event.set()
            if not self.done.is_set():
                self.status = "stopping"

    def emit(self, event):
        with self.lock:
            self.received += 1
            event["sequence"] = self.received
            event['group_id'] = group_id(event)
            if len(self.tail) == self.tail.maxlen:
                self.tail_index.pop(self.tail[0]["id"], None)
            pending = {**event, "importance": "pending"}
            self.tail.append(pending)
            self.tail_index[event["id"]] = pending
            if self.reviews.apply(event, self.review_key):
                self.record([event])
                return
            try:
                self.queue.put_nowait(event)
            except queue.Full:
                self.dropped += 1
                pending.update(importance="dropped", analysis_error="Analysis queue full; event was not sent to Jev")

    def record(self, batch):
        with self.lock:
            for event in batch:
                event["baseline"] = baseline(event)
                self.reviews.apply(event, self.review_key)
                self.counts[event["importance"]] += 1
                self.reused += bool(event.get("analysis_reused"))
                self.lines += event["line_count"]
                self.recent.append(event)
                if event["id"] in self.tail_index:
                    self.tail_index[event["id"]].clear()
                    self.tail_index[event["id"]].update(event)

    def worker(self):
        while not self.stop_event.is_set() or not self.queue.empty():
            try:
                batch = [self.queue.get(timeout=.2)]
            except queue.Empty:
                continue
            until = time.monotonic() + .35
            while len(batch) < self.args.batch_size and time.monotonic() < until:
                try:
                    batch.append(self.queue.get(timeout=max(.001, until - time.monotonic())))
                except queue.Empty:
                    break
            queue_count = len(batch)
            expected = [event for event in batch if self.reviews.apply(event, self.review_key)]
            self.record(expected)
            batch = [event for event in batch if event not in expected]
            if not batch:
                for _ in range(queue_count):
                    self.queue.task_done()
                continue
            error = None
            with self.lock:
                if self.stop_event.is_set():
                    error = "Stopped before classification"
            if error:
                for event in batch:
                    event.update(importance="unknown", analysis_error=error)
            elif self.args.offline:
                for event in batch:
                    event.update(importance="important" if baseline(event)["important"] else "uncertain", severity="unknown", category="unknown")
            else:
                try:
                    judge = self.judge_batch if self.args.no_grouping else self.grouped_judge
                    results = judge(batch)
                    for event, result in zip(batch, results, strict=True):
                        event.update(result)
                        if event["truncated"] or (event["importance"] == "routine" and event["importance_confidence"] < .7):
                            event["importance"] = "uncertain"
                except (ValueError, OSError) as error:
                    for event in batch:
                        event.update(importance="unknown", analysis_error=str(error))
                if self.usage.snapshot()["estimated_cost_usd"] >= self.args.max_cost:
                    self.stop("Estimated cost threshold reached")
            self.record(batch)
            for _ in range(queue_count):
                self.queue.task_done()

    def discover(self):
        try:
            if not self.context:
                self.context = self.run_kubectl(["config", "current-context"], timeout=12).decode().strip()
            if not self.context or self.context.startswith("-"):
                raise ValueError("A Kubernetes context is required")
            while not self.stop_event.is_set():
                command = ["--context", self.context, "get", "pods", "-o", "json"]
                command += ["--namespace", self.args.namespace] if self.args.namespace else ["--all-namespaces"]
                if self.args.selector:
                    command += ["--selector", self.args.selector]
                with self.lock:
                    self.discovery_attempts += 1
                    self.next_discovery_at = None
                    if not self.stop_event.is_set():
                        self.message = f"Listing pods in {self.context} (attempt {self.discovery_attempts}, timeout 12s)…"
                try:
                    wanted = running_targets(json.loads(self.run_kubectl(command, timeout=12))["items"])
                    with self.lock:
                        self.inventory_error = None
                        self.last_inventory_success = datetime.datetime.now(datetime.timezone.utc).isoformat()
                        for identity in list(self.streams):
                            if identity not in wanted:
                                self.streams[identity]["retired"].set()
                        # Only retire entries once their thread has exited: no orphaned processes.
                        for identity, item in list(self.streams.items()):
                            if item["retired"].is_set() and not item["thread"].is_alive():
                                self.history.append({"source": item["source"], "status": "ended", "warnings": ["Container stopped or was replaced"]})
                                del self.streams[identity]
                        for identity, source in wanted.items():
                            if identity in self.streams or len(self.streams) >= self.args.max_streams:
                                continue
                            source["context"] = self.context
                            item = {"source": source, "retired": threading.Event(), "status": "connecting", "error": None}
                            thread = threading.Thread(target=self.follow, args=(item,), daemon=True)
                            item["thread"] = thread
                            self.streams[identity] = item
                            thread.start()
                        self.unfollowed = len(set(wanted) - set(self.streams))
                        if not self.stop_event.is_set():
                            self.status = "running"
                            self.message = f"Following {len(self.streams)} containers; discovering changes every {self.args.discovery_interval}s"
                except (ValueError, KeyError, TypeError, OSError) as error:
                    with self.lock:
                        self.inventory_error = "Kubernetes inventory unavailable: " + (str(error) if isinstance(error, ValueError) and not isinstance(error, json.JSONDecodeError) else "Invalid or unavailable pod inventory")
                        if not self.stop_event.is_set():
                            self.status = "reconnecting"
                            self.message = f"{self.inventory_error}. Retrying in {self.args.discovery_interval}s."
                with self.lock:
                    self.next_discovery_at = time.monotonic() + self.args.discovery_interval
                self.stop_event.wait(self.args.discovery_interval)
        except (ValueError, OSError):
            self.inventory_error = "Unable to resolve Kubernetes context"
            self.stop("Unable to resolve Kubernetes context")

    def follow(self, item):
        cursor = Cursor()
        grouper = Grouper(item["source"], self.emit)
        attempt = 0
        while not self.stop_event.is_set() and not item["retired"].is_set():
            source = item["source"]
            command = ["kubectl", "--context", self.context, "--request-timeout=0", "logs", source["pod"],
                       "--namespace", source["namespace"], "--container", source["container"],
                       "--follow=true", "--timestamps=true", "--pod-running-timeout=10s"]
            if cursor.timestamp:
                command += ["--since-time=" + cursor.timestamp, "--tail=-1"]
            else:
                command += ["--since=" + self.args.since, "--tail=" + str(self.args.tail)]
            cursor.reconnect()
            process = None
            try:
                process = self.popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
                with self.lock:
                    item.update(status="following", error=None)
                with selectors.DefaultSelector() as selector:
                    selector.register(process.stdout, selectors.EVENT_READ)
                    buffer = b""
                    truncated = False
                    while not self.stop_event.is_set() and not item["retired"].is_set():
                        ready = selector.select(.2)
                        if not ready:
                            if grouper.pending and time.monotonic() - grouper.last > .5:
                                grouper.flush()
                            if process.poll() is not None:
                                break
                            continue
                        data = os.read(process.stdout.fileno(), MAX_LINE)
                        if not data:
                            if buffer:
                                if cursor.accept(buffer):
                                    grouper.feed(buffer, truncated)
                            break
                        for chunk in data.splitlines(keepends=True):
                            room = MAX_LINE - len(buffer)
                            buffer += chunk[:room]
                            truncated |= len(chunk) > room
                            if chunk.endswith(b"\n"):
                                if cursor.accept(buffer):
                                    grouper.feed(buffer, truncated)
                                buffer, truncated = b"", False
                if not self.stop_event.is_set() and not item["retired"].is_set():
                    with self.lock:
                        item.update(status="reconnecting", error="Log stream disconnected; reconnecting from last timestamp")
            except (OSError, ValueError):
                with self.lock:
                    item.update(status="error", error="Cannot follow container logs; check access and connectivity")
            finally:
                if process is not None:
                    if process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=2)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                    if process.stdout:
                        process.stdout.close()
                grouper.flush()
            if self.stop_event.is_set() or item["retired"].is_set():
                break
            with self.lock:
                self.reconnects += 1
            attempt += 1
            self.stop_event.wait(min(30, 2 ** min(attempt, 5)))

    def snapshot(self, event_limit=None):
        usage = self.usage.snapshot()
        with self.lock:
            for event in self.recent:
                before = event["importance"]
                self.reviews.apply(event, self.review_key)
                if event["importance"] != before:
                    self.counts[before] -= 1
                    self.counts[event["importance"]] += 1
            for event in self.tail:
                self.reviews.apply(event, self.review_key)
            events = list(self.recent)
            tail_events = list(self.tail)
            if event_limit is not None:
                events = events[-event_limit:]
                tail_events = tail_events[-event_limit:]
            coverage = [{"source": i["source"], "status": "ok" if i["status"] in ("following", "ended") else i["status"],
                         "warnings": [i["error"]] if i["error"] else []} for i in self.streams.values()]
            if self.unfollowed:
                coverage.append({"source": {"type": "kubernetes", "context": self.context}, "status": "limited", "warnings": [f"{self.unfollowed} running containers exceed the stream limit"]})
            if self.inventory_error:
                coverage.append({"source": {"type": "kubernetes", "context": self.context}, "status": "error", "warnings": [self.inventory_error]})
            scope = {"context": self.context, "namespace": self.args.namespace or "*", "selector": self.args.selector,
                     "since": self.args.since, "tail": self.args.tail, "live": True}
            elapsed = time.monotonic() - self.started
            report = build_report(events, coverage, scope, "offline-rules" if self.args.offline else "jev", usage["request_attempts"], elapsed)
            report["created_at"] = self.created_at
            report["summary"].update({key: self.counts[key] for key in ("important", "routine", "uncertain", "unknown")})
            report["summary"].update(events=sum(self.counts.values()), lines=self.lines,
                                      streams=len(self.streams), complete_within_window=False,
                                      reused_events=self.reused)
            report["usage"] = usage
            report["tail_events"] = [dict(event) for event in tail_events]
            report["live"] = {"status": self.status, "message": self.message, "received": self.received,
                              "queue_depth": self.queue.qsize(), "dropped": self.dropped, "batches": self.batches,
                              "active_streams": sum(i["status"] == "following" for i in self.streams.values()),
                              "unfollowed_streams": self.unfollowed, "reconnects": self.reconnects,
                              "events_per_second": round(sum(self.counts.values()) / max(1, elapsed), 2),
                              "retained_events": len(events), "retention_limit": self.args.retain_events,
                              "max_batches": self.args.max_batches, "cost_threshold_usd": self.args.max_cost,
                              "initial_history_limited": True}
            report["live"].update(self.status_info())
            report["live"]["max_streams"] = self.args.max_streams
            return report

    def run(self, publish=None):
        workers = [threading.Thread(target=self.worker, daemon=True) for _ in range(self.args.workers)]
        discover = threading.Thread(target=self.discover, daemon=True)
        for thread in workers:
            thread.start()
        discover.start()
        try:
            while not self.stop_event.wait(1):
                if self.args.duration and time.monotonic() - self.started >= self.args.duration:
                    self.stop("Session duration reached")
                if publish:
                    publish(self.snapshot())
        finally:
            self.stop()
            discover.join(timeout=36)
            for item in list(self.streams.values()):
                item["thread"].join(timeout=4)
            for thread in workers:
                thread.join(timeout=35)
            # Producers may flush a final event after workers have exited.
            leftovers = []
            while True:
                try:
                    event = self.queue.get_nowait()
                    event.update(importance="unknown", analysis_error="Stopped before classification")
                    leftovers.append(event)
                except queue.Empty:
                    break
            self.record(leftovers)
            with self.lock:
                self.status = "stopped"
                for item in self.streams.values():
                    if item["status"] == "following":
                        item["status"] = "ended"
            self.done.set()
        report = self.snapshot()
        if self.args.output:
            write_report(self.args.output, report)
        return report
