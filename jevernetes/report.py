import collections
import datetime
import json
import os
from pathlib import Path
import re
import tempfile

from .events import ANSI
from .grouping import group_id


def build_report(events, coverage, scope, mode, requests, elapsed):
    counts = collections.Counter(e["importance"] for e in events)
    grouped = {}
    for event in events:
        event['group_id'] = group_id(event)
        if event["importance"] != "important":
            continue
        # Exact text grouping avoids merging unrelated causes or erasing numbers.
        source = event["source"]
        fingerprint = event['group_id']
        if fingerprint not in grouped:
            grouped[fingerprint] = {"id": fingerprint, "source": source, "text": event["text"],
                                    "severity": event.get("severity", "unknown"), "category": event.get("category", "unknown"), "count": 0, "event_ids": []}
        grouped[fingerprint]["count"] += 1
        grouped[fingerprint]["event_ids"].append(event["id"])
    ranks = {"outage": 4, "impact": 3, "degraded": 2, "info": 1, "noise": 0}
    groups = sorted(grouped.values(), key=lambda g: (-ranks.get(g["severity"], 0), -g["count"], g["id"]))
    gaps = sum(c["status"] != "ok" for c in coverage)
    return {"schema_version": 1, "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "mode": mode, "scope": scope,
            "summary": {"events": len(events), "lines": sum(e["line_count"] for e in events),
                        **{k: counts[k] for k in ("important", "routine", "uncertain", "unknown")},
                        "streams": len(coverage), "coverage_gaps": gaps, "api_requests": requests,
                        "reused_events": sum(bool(e.get('analysis_reused')) for e in events),
                        "elapsed_seconds": round(elapsed, 2),
                        "complete_within_window": gaps == 0 and counts["unknown"] == 0},
            "important_groups": groups, "coverage": coverage, "events": events}


def write_report(path, report):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, temporary = tempfile.mkstemp(prefix=".report-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(report, output, ensure_ascii=False, indent=2)
            output.write("\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def safe_console(text):
    # Strip terminal control bytes, including malicious ANSI escape sequences.
    return re.sub(r"[\x00-\x1f\x7f-\x9f]", " ", ANSI.sub("", text))


class TailPrinter:
    """Append log text once; print later classification updates on stderr."""
    def __init__(self, output, status):
        self.output, self.status = output, status
        self.sequence = 0
        self.judgments = {}
        self.last_status = None
        self.last_status_at = 0

    def publish(self, report):
        events = report.get("tail_events", [])
        if events and events[0]["sequence"] > self.sequence + 1:
            print(f"[tail] {events[0]['sequence'] - self.sequence - 1} events exceeded the display buffer", file=self.status, flush=True)
        for event in events:
            seq, importance = event["sequence"], event["importance"]
            if seq > self.sequence:
                source = event["source"]
                name = "/".join(source.get(k, "") for k in ("namespace", "pod", "container"))
                prefix = f"{event.get('timestamp') or '-'} [{name}] [{importance}] #{seq} "
                for i, line in enumerate(event["text"].splitlines()):
                    print(safe_console(prefix + line if i == 0 else "    " + line), file=self.output, flush=True)
                self.sequence = seq
            elif self.judgments.get(event["id"]) == "pending" and importance != "pending":
                print(safe_console(f"[jev] #{seq} {importance} · {event.get('severity', 'unknown')} · {event.get('category', 'unknown')}"), file=self.status, flush=True)
        self.judgments = {e["id"]: e["importance"] for e in events}
        usage, live = report["usage"], report["live"]
        import time
        status = f"[tail] {live['status']} · {live['active_streams']} streams · queue {live['queue_depth']} · dropped {live['dropped']} · estimated ${usage['estimated_cost_usd']:.8f} · unmetered {usage['unmetered_requests']}"
        if live.get('inventory_error') or live['status'] in ('starting', 'reconnecting', 'stopping'):
            status += " · " + safe_console(live.get('message', ''))
        now = time.monotonic()
        if status != self.last_status or now - self.last_status_at >= 10:
            print(status, file=self.status, flush=True)
            self.last_status, self.last_status_at = status, now


def render(report, top=15):
    summary = report["summary"]
    lines = [f"jevernetes · {report['mode']}",
             f"{summary['events']} events / {summary['lines']} lines / {summary['streams']} streams",
             f"Important {summary['important']} · Routine {summary['routine']} · Uncertain {summary['uncertain']} · Unknown {summary['unknown']}",
             f"Coverage gaps {summary['coverage_gaps']} · API requests {summary['api_requests']} · {summary['elapsed_seconds']}s"]
    for group in report["important_groups"][:top]:
        source = group["source"]
        name = "/".join(source.get(k, "") for k in ("namespace", "pod", "container")) if source["type"] == "kubernetes" else source["path"]
        lines += [f"\n[{group['severity']}/{group['category']}] {name} ×{group['count']}", "  " + group["text"][:350]]
    for c in [c for c in report["coverage"] if c["status"] != "ok"][:10]:
        lines.append("\nCoverage: " + json.dumps(c, ensure_ascii=False))
    return "\n".join(safe_console(line) for line in lines)
