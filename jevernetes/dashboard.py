"""Loopback dashboard. Reuses the CLI engine; never exposes provider credentials."""
import base64
import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import secrets
import tempfile
import threading
from urllib.parse import urlsplit, parse_qs

from .cli import analyze, parser
from .jev import api_key
from .report import write_report
from .live import LiveSession
from .context import fetch_context
from .reviews import ReviewStore
from .events import BASELINE_RULES

MAX_BODY = 16 * 1024 * 1024
MAX_REPORT = 64 * 1024 * 1024
WEB = Path(__file__).with_name("web")


def scan_args(payload, paths=None):
    """Construct only known arguments. Browser inputs cannot become CLI options."""
    kind = payload.get("kind")
    if kind not in ("files", "kubernetes"):
        raise ValueError("Choose log files or Kubernetes")
    args = parser().parse_args(["files", "placeholder"] if kind == "files" else ["kubernetes"])
    if type(payload.get("offline", False)) is not bool:
        raise ValueError("Invalid analysis mode")
    args.offline = payload.get("offline", False)
    if type(payload.get("live", False)) is not bool:
        raise ValueError("Invalid live mode")
    args.live = payload.get("live", False)
    if args.live and kind != "kubernetes":
        raise ValueError("Live mode requires Kubernetes")
    if args.live:
        import math
        for key, default, limit in [("max_streams", 128, 256), ("retain_events", 2000, 5000)]:
            value = payload.get(key, default)
            if type(value) is not int or not 1 <= value <= limit:
                raise ValueError(f"Invalid {key}")
            setattr(args, key, value)
        cost = payload.get("max_cost", .25)
        if type(cost) not in (int, float) or not math.isfinite(cost) or cost <= 0 or cost > 100:
            raise ValueError("Live cost threshold must be greater than zero and at most $100")
        args.max_cost = cost
    for key, default, limit in [("max_batches", 500, 12500), ("tail", 500, 10000)]:
        value = payload.get(key, default)
        minimum = 0 if key == "tail" and args.live else 1
        if type(value) is not int or not minimum <= value <= limit:
            raise ValueError(f"{key} must be between {minimum} and {limit}")
        setattr(args, key, value)
    if kind == "files":
        args.paths = paths or []
    else:
        for key in ("context", "namespace", "selector", "since"):
            value = payload.get(key, "1h" if key == "since" else "")
            if isinstance(value, str):
                value = value.strip()
            if not isinstance(value, str) or len(value) > 256 or any(ord(c) < 32 for c in value) or value.startswith("-"):
                raise ValueError(f"Invalid {key}")
            setattr(args, key, value.strip() or ("1h" if key == "since" else None))
        import re
        if not re.fullmatch(r"(?:\d+(?:\.\d+)?[smh])+", args.since):
            raise ValueError("Time window must be a duration such as 30m or 1h")
    if not args.offline:
        api_key()
    return args


class Dashboard:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.token = secrets.token_hex(32)
        self.lock = threading.Lock()
        self.job = None
        self.cache = {}
        self.metadata = {}
        self.review_metadata = {}
        self.report_lock = threading.RLock()
        self.live_session = None
        self.context_slots = threading.BoundedSemaphore(2)
        self.reviews = ReviewStore(self.root / ".review-rules.json")

    def report(self, name):
        with self.report_lock:
            return self.reviews.project(self._report(name), name)

    def _report(self, name):
        if not isinstance(name, str) or Path(name).name != name or not name.endswith(".json"):
            raise ValueError("Invalid report ID")
        path = self.root / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_REPORT:
            raise ValueError("Report unavailable or too large")
        stat = path.stat()
        key = (stat.st_mtime_ns, stat.st_size)
        if name in self.cache and self.cache[name][0] == key:
            return self.cache[name][1]
        report = json.loads(path.read_text())
        if not isinstance(report, dict) or report.get("schema_version") != 1 or not isinstance(report.get("events"), list) or not isinstance(report.get("summary"), dict):
            raise ValueError("Unsupported report")
        # Cache one complete report; report listings retain small metadata only.
        self.cache = {name: (key, report)}
        self.metadata[name] = (key, {"id": name, **{k: report.get(k) for k in ("created_at", "mode", "scope", "summary")}})
        return report

    def reports(self):
        result = []
        self.reviews.snapshot()  # Refresh the revision if another process changed local rules.
        revision = self.reviews.stamp
        for path in sorted(self.root.glob("*.json"), key=lambda p: p.stat().st_mtime, reverse=True)[:100]:
            try:
                if path.is_symlink() or path.name.startswith("."):
                    continue
                stat = path.stat()
                with self.report_lock:
                    cached = self.metadata.get(path.name)
                    if not cached or cached[0] != (stat.st_mtime_ns, stat.st_size):
                        self._report(path.name)
                    key = (stat.st_mtime_ns, stat.st_size, revision)
                    reviewed = self.review_metadata.get(path.name)
                    if not reviewed or reviewed[0] != key:
                        report = self.report(path.name)
                        reviewed = (key, {"id": path.name, **{k: report.get(k) for k in ("created_at", "mode", "scope", "summary")}})
                        self.review_metadata[path.name] = reviewed
                    result.append(reviewed[1])
            except (OSError, ValueError):
                continue
        return result

    def state(self):
        with self.lock:
            job = dict(self.job) if self.job else None
            session = self.live_session
        if job and job.get("live") and job["status"] in ("running", "stopping") and session:
            info = session.status_info()
            job.update(message=info["message"], phase=info["status"])
        try:
            api_key()
            available = True
        except ValueError:
            available = False
        return {"csrf": self.token, "jev_available": available, "job": job, "reports": self.reports()}

    def start(self, payload):
        if not isinstance(payload, dict):
            raise ValueError("Expected an object")
        args = scan_args(payload)
        uploads = []
        if args.command == "files":
            files = payload.get("files")
            if not isinstance(files, list) or not 1 <= len(files) <= 20:
                raise ValueError("Choose between 1 and 20 log files")
            for file in files:
                if not isinstance(file, dict) or not isinstance(file.get("name"), str) or not isinstance(file.get("data"), str):
                    raise ValueError("Invalid upload")
                name = Path(file["name"]).name
                if not name or name in (".", "..") or len(name) > 180 or any(ord(c) < 32 for c in name):
                    raise ValueError("Invalid filename")
                try:
                    data = base64.b64decode(file["data"], validate=True)
                except ValueError:
                    raise ValueError("Invalid file encoding") from None
                uploads.append((name, data))
            if sum(len(data) for _, data in uploads) > 10 * 1024 * 1024:
                raise ValueError("Uploads are limited to 10 MiB per scan")
        with self.lock:
            if self.job and self.job["status"] in ("running", "stopping"):
                raise RuntimeError("A scan is already running")
            job_id = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d-%H%M%S") + "-" + secrets.token_hex(3)
            args.review_store = self.reviews
            args.review_key = job_id + ".json"
            self.live_session = LiveSession(args) if args.live else None
            self.job = {"id": job_id, "status": "running", "message": "Starting analysis…", "report_id": None, "live": args.live}
        threading.Thread(target=self._run, args=(args, uploads, job_id), daemon=True).start()
        return {"id": job_id}

    def _run(self, args, uploads, job_id):
        def progress(message):
            with self.lock:
                self.job["message"] = message
        try:
            if args.live:
                report = self.live_session.run()
                report_id = job_id + ".json"
                write_report(self.root / report_id, report)
                with self.lock:
                    self.job.update(status="complete", message=report["live"]["message"], report_id=report_id)
                return
            with tempfile.TemporaryDirectory(prefix="jevernetes-logs-") as temporary:
                names = {}
                if uploads:
                    for i, (name, data) in enumerate(uploads):
                        path = Path(temporary) / str(i) / name
                        path.parent.mkdir(mode=0o700)
                        path.write_bytes(data)
                        path.chmod(0o600)
                        names[str(path)] = name
                    args.paths = list(names)
                report = analyze(args, notify=progress)
                # Preserve uploaded names rather than internal temporary paths.
                if uploads:
                    report["scope"]["files"] = list(names.values())
                    for item in report["events"] + report["coverage"] + report["important_groups"]:
                        source = item["source"]
                        source["path"] = names.get(source.get("path"), source.get("path"))
                report_id = job_id + ".json"
                write_report(self.root / report_id, report)
            partial = report["summary"]["coverage_gaps"] or report["summary"]["unknown"]
            with self.lock:
                self.job.update(status="complete", message="Analysis finished with gaps to review" if partial else "Analysis complete", report_id=report_id)
        except Exception as error:
            # Expected analyzer errors are sanitized. Never return an unexpected traceback.
            message = str(error) if isinstance(error, ValueError) else "Analysis failed. Check the terminal and input files."
            with self.lock:
                self.job.update(status="error", message=message)

    def live_report(self):
        with self.lock:
            session = self.live_session
        if session is None:
            raise ValueError("No live session")
        return session.snapshot(event_limit=1000)

    def selected_event(self, payload):
        if not isinstance(payload, dict):
            raise ValueError("Choose an event from a saved report or live session")
        events, key = self.selected_events(payload, [payload.get("event_id")])
        return events[0], key

    def selected_events(self, payload, ids):
        if (not isinstance(ids, list) or not 1 <= len(ids) <= 50
                or any(not isinstance(value, str) or not value for value in ids)
                or len(set(ids)) != len(ids)):
            raise ValueError("Select between 1 and 50 distinct events")
        if payload.get("live_id") and payload.get("report_id"):
            raise ValueError("Choose one saved report or live session")
        if payload.get("live_id"):
            with self.lock:
                if not self.job or self.job["id"] != payload["live_id"] or self.live_session is None:
                    raise ValueError("The live session changed; open its saved report")
                session = self.live_session
            report = session.snapshot()
        else:
            report = self.report(payload.get("report_id"))
        # Read one snapshot, and resolve every ID before allowing any writes.
        available = {e['id']: e for e in report.get('tail_events', [])}
        available.update({e['id']: e for e in report.get('events', [])})
        if any(value not in available for value in ids):
            raise ValueError("A selected event is no longer retained; open a saved report containing it")
        return [available[value] for value in ids], payload["live_id"] + ".json" if payload.get("live_id") else payload["report_id"]

    def review(self, payload):
        if not isinstance(payload, dict):
            raise ValueError("Invalid review request")
        action = payload.get("action")
        if action == "toggle":
            self.reviews.set_enabled(payload.get("rule_id"), payload.get("enabled"))
        elif action in ("acknowledge", "unacknowledge", "expected"):
            entries = payload.get("events", [payload])
            if (not isinstance(entries, list) or not 1 <= len(entries) <= 50
                    or any(not isinstance(item, dict) for item in entries)):
                raise ValueError("Select events to review")
            events, key = self.selected_events(payload, [item.get("event_id") for item in entries])
            if action == "expected":
                self.reviews.add_many([(event, item.get("pattern")) for event, item in zip(events, entries)], payload.get("scope", "workload"))
            else:
                self.reviews.acknowledge_many(key, [event["id"] for event in events], action == "acknowledge")
        else:
            raise ValueError("Unknown review action")
        return {"status": "saved"}

    def context(self, payload):
        event, _ = self.selected_event(payload)
        if not self.context_slots.acquire(blocking=False):
            raise RuntimeError("Context lookups are busy; try again shortly")
        try:
            return fetch_context(event, payload.get("window_seconds", 120))
        finally:
            self.context_slots.release()

    def stop_live(self):
        with self.lock:
            if self.live_session is None or self.live_session.done.is_set():
                raise ValueError("No active live session")
            self.live_session.stop()
            self.job.update(status="stopping", message="Stopping streams and finishing in-flight requests…")
        return {"status": "stopping"}


def handler_for(app):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def setup(self):
            super().setup()
            self.connection.settimeout(15)

        def allowed(self):
            port = self.server.server_port
            return self.headers.get("Host") in (f"127.0.0.1:{port}", f"localhost:{port}")

        def send(self, status, body, content_type="application/json"):
            if not isinstance(body, bytes):
                body = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Referrer-Policy", "no-referrer")
            self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if not self.allowed():
                return self.send(403, {"error": "Invalid host"})
            url = urlsplit(self.path)
            try:
                if url.path == "/api/state":
                    return self.send(200, app.state())
                if url.path == "/api/rules":
                    return self.send(200, {"baseline": BASELINE_RULES, "expected": app.reviews.snapshot()["rules"]})
                if url.path == "/api/live":
                    return self.send(200, app.live_report())
                if url.path == "/api/report":
                    name = parse_qs(url.query).get("id", [""])[0]
                    return self.send(200, app.report(name))
                assets = {"/": ("index.html", "text/html; charset=utf-8"), "/app.js": ("app.js", "text/javascript; charset=utf-8"), "/context.js": ("context.js", "text/javascript; charset=utf-8"), "/prompt.js": ("prompt.js", "text/javascript; charset=utf-8"), "/style.css": ("style.css", "text/css; charset=utf-8")}
                if url.path in assets:
                    name, kind = assets[url.path]
                    return self.send(200, (WEB / name).read_bytes(), kind)
                return self.send(404, {"error": "Not found"})
            except (ValueError, OSError):
                return self.send(404, {"error": "Report unavailable"})

        def do_POST(self):
            origin = self.headers.get("Origin")
            if (not self.allowed() or self.headers.get("X-Jev-Token") != app.token or
                    (origin is not None and origin != "http://" + self.headers.get("Host", ""))):
                return self.send(403, {"error": "Request not authorized"})
            if self.path not in ("/api/analyze", "/api/live/stop", "/api/context", "/api/review"):
                return self.send(404, {"error": "Not found"})
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if self.headers.get("Content-Type") != "application/json" or not 0 < length <= MAX_BODY:
                    return self.send(413, {"error": "Expected JSON, at most 16 MiB"})
                payload = json.loads(self.rfile.read(length))
                if self.path in ("/api/context", "/api/review"):
                    try:
                        return self.send(200, app.context(payload) if self.path == "/api/context" else app.review(payload))
                    except ValueError as error:
                        return self.send(400, {"error": str(error)})
                if self.path == "/api/live/stop":
                    return self.send(200, app.stop_live())
                return self.send(202, app.start(payload))
            except (ValueError, OSError):
                return self.send(400, {"error": "Invalid request. Check file size, duration, scan limits and Jev credentials."})
            except RuntimeError as error:
                return self.send(409, {"error": str(error)})
    return Handler


def make_server(root, port=8792):
    app = Dashboard(root)
    server = ThreadingHTTPServer(("127.0.0.1", port), handler_for(app))
    server.app = app
    return server


def serve(root, port):
    if port > 65535:
        raise ValueError("Port must be at most 65535")
    server = make_server(root, port)
    print(f"jevernetes dashboard: http://127.0.0.1:{server.server_port}", flush=True)
    print("Local access only. Press Ctrl-C to stop.", flush=True)
    try:
        server.serve_forever()
    finally:
        if server.app.live_session:
            server.app.live_session.stop("Dashboard stopped")
            server.app.live_session.done.wait(40)
        server.server_close()
    return 0
