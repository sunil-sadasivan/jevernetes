import argparse
import gzip
import io
import json
import math
from pathlib import Path
import re
import sys
import time

from .events import MAX_INPUT_BYTES, baseline, parse_stream
from .jev import Jev, api_key, classify
from .kubernetes import collect
from .report import TailPrinter, build_report, render, write_report


def positive(value):
    result = int(value)
    if result <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return result


def nonnegative_float(value):
    result = float(value)
    if not math.isfinite(result) or result < 0:
        raise argparse.ArgumentTypeError("must be a finite nonnegative number")
    return result


def nonnegative_int(value):
    result = int(value)
    if result < 0:
        raise argparse.ArgumentTypeError("must be nonnegative")
    return result


def parser():
    root = argparse.ArgumentParser(description="Investigate Kubernetes logs and operationally important events with Jev")
    commands = root.add_subparsers(dest="command", required=True)
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--offline", action="store_true", help="Use local keyword rules; no Jev calls")
    common.add_argument("--rules-file", type=Path, default=Path(".runs/.review-rules.json"), help="Local expected-event rules shared with the dashboard")
    common.add_argument("--output", type=Path, help="Save full JSON report with mode 0600")
    common.add_argument("--json", action="store_true", help="Emit full report to stdout")
    common.add_argument("--model", default="jev-latest")
    common.add_argument("--batch-size", type=positive, default=8)
    common.add_argument("--workers", type=positive, default=4, help="Concurrent Jev requests (max 16)")
    common.add_argument("--max-batches", type=positive, default=500, help="AI batch budget; up to 3 attempts per batch")
    common.add_argument("--max-events", type=positive, default=100000)
    common.add_argument("--top", type=positive, default=15)
    files = commands.add_parser("files", parents=[common], help="Analyze text, JSONL, gzip or stdin")
    files.add_argument("--max-file-bytes", type=positive, default=MAX_INPUT_BYTES, help="Decompressed byte limit per file or stdin (default 32 MiB)")
    files.add_argument("paths", nargs="+", help="Log paths or - for stdin")
    kube = commands.add_parser("kubernetes", aliases=["k8s"], parents=[common], help="Scan logs across all namespaces by default")
    kube.add_argument("--context", help="Default: current kubectl context (pinned for the entire scan)")
    namespace = kube.add_mutually_exclusive_group()
    namespace.add_argument("--namespace", "-n", help="Restrict to one namespace; default is all")
    namespace.add_argument("--all-namespaces", "-A", action="store_true", help="Scan every namespace (the default)")
    kube.add_argument("--selector", "-l", help="Pod label selector")
    kube.add_argument("--since", default="1h", help="Time window per stream, e.g. 30m, 6h (default 1h)")
    kube.add_argument("--tail", type=nonnegative_int, default=500, help="Recent lines per stream; 0 starts live mode with new lines only")
    kube.add_argument("--max-bytes", type=positive, default=2 * 1024 * 1024, help="Maximum bytes per stream")
    kube.add_argument("--no-previous", action="store_true", help="Skip previous instances of restarted containers")
    kube.add_argument("--collect-workers", type=positive, default=8)
    kube.add_argument("--live", "--follow", "-f", action="store_true", help="Live tail: follow containers and print arriving logs with Jev judgments")
    kube.add_argument("--duration", type=positive, help="Stop live mode after this many seconds")
    kube.add_argument("--max-streams", type=positive, default=128)
    kube.add_argument("--queue-size", type=positive, default=2000)
    kube.add_argument("--retain-events", type=positive, default=2000)
    kube.add_argument("--discovery-interval", type=positive, default=15)
    kube.add_argument("--max-cost", type=nonnegative_float, default=0.25, help="Live stop threshold in estimated USD; in-flight/unmetered requests may exceed it")
    kube.add_argument("--input-price", type=nonnegative_float, default=0.042, help="USD per million input tokens")
    kube.add_argument("--output-price", type=nonnegative_float, default=0.0, help="USD per million output tokens")
    dashboard = commands.add_parser("dashboard", help="Open a local dashboard for reports and new scans")
    dashboard.add_argument("--port", type=positive, default=8792)
    dashboard.add_argument("--reports-dir", type=Path, default=Path(".runs"))
    return root


def analyze(args, notify=None):
    emit = notify or (lambda message: print(message, file=sys.stderr))
    if args.workers > 16 or args.batch_size > 16:
        raise ValueError("--workers and --batch-size must be at most 16")
    if args.command != "files":
        if args.tail == 0:
            raise ValueError("--tail 0 requires --live / -f")
        if args.collect_workers > 32:
            raise ValueError("--collect-workers must be at most 32")
        if not re.fullmatch(r"(?:\d+(?:\.\d+)?[smh])+", args.since):
            raise ValueError("--since must be a duration such as 30m or 1h")
    started = time.monotonic()
    client = None if args.offline else Jev(api_key(), args.model)
    events, coverage = [], []

    def consume(source, stream, warnings=None):
        remaining = args.max_events - len(events)
        if remaining <= 0:
            coverage.append({"source": source, "status": "skipped", "warnings": ["Global event limit reached"]})
            return
        parsed, parse_warnings = parse_stream(stream, source, remaining, max_bytes=getattr(args, "max_file_bytes", MAX_INPUT_BYTES))
        events.extend(parsed)
        issues = (warnings or []) + parse_warnings
        coverage.append({"source": source, "status": "limited" if issues else "ok", "events": len(parsed), "warnings": issues})

    if args.command == "files":
        for path in args.paths:
            source = {"type": "file", "path": path}
            try:
                if path == "-":
                    consume(source, sys.stdin.buffer)
                else:
                    with (gzip.open(path, "rb") if path.endswith(".gz") else open(path, "rb")) as stream:
                        consume(source, stream)
            except (OSError, EOFError):
                coverage.append({"source": source, "status": "error", "warnings": ["Cannot read log file or invalid gzip stream"]})
        scope = {"files": args.paths}
    else:
        emit("Collecting Kubernetes logs across the selected scope…")

        def on_stream(source, data, error):
            if error:
                coverage.append({"source": source, "status": "error", "warnings": [error]})
            else:
                issues = []
                if len(data) >= args.max_bytes:
                    issues.append("Byte limit reached; stream may be truncated")
                if len(data.splitlines()) >= args.tail:
                    issues.append("Tail limit reached; earlier lines in the window may be omitted")
                consume(source, io.BytesIO(data), issues)
            if len(coverage) % 25 == 0:
                emit(f"Collected {len(coverage)} streams / {len(events)} events")

        scope = collect(args, on_stream)
    from .reviews import ReviewStore
    reviews = getattr(args, "review_store", None) or ReviewStore(args.rules_file)
    pending = []
    for event in events:
        event["baseline"] = baseline(event)
        if not reviews.apply(event):
            pending.append(event)
    if args.offline:
        for event in pending:
            event.update(importance="important" if event["baseline"]["important"] else "uncertain",
                         severity="unknown", category="unknown")
    else:
        last_progress = 0

        def progress(done, total):
            nonlocal last_progress
            now = time.monotonic()
            if now - last_progress > 5 or done == total:
                emit(f"Jev processed {done}/{total} events")
                last_progress = now

        emit(f"Analyzing {len(events)} events with {args.model} (redacted text sent to TypeSafe)…")
        classify(pending, client, args.batch_size, args.workers, args.max_batches, progress)
    report = build_report(events, coverage, scope, "offline-rules" if args.offline else "jev", client.requests if client else 0, time.monotonic() - started)
    if client:
        report["usage"] = client.usage.snapshot()
    return report


def run(args):
    if getattr(args, "live", False):
        from .live import LiveSession
        if args.workers > 16 or args.batch_size > 16 or args.max_streams > 256:
            raise ValueError("Live workers/batch size must be <=16; streams <=256")
        if args.max_cost == 0 and not args.offline:
            raise ValueError("--max-cost must be greater than zero for Jev live mode")
        if not re.fullmatch(r"(?:\d+(?:\.\d+)?[smh])+", args.since):
            raise ValueError("--since must be a duration such as 30s or 1h")
        session = LiveSession(args)
        tail_printer = TailPrinter(sys.stdout, sys.stderr)
        def publish(report):
            if args.json:
                print(json.dumps(report), flush=True)
            else:
                tail_printer.publish(report)
        try:
            report = session.run(publish)
        except KeyboardInterrupt:
            session.stop()
            report = session.snapshot()
            if args.output:
                write_report(args.output, report)
        publish(report)
        return 2 if report['summary']['unknown'] or report['live']['dropped'] or report['live']['unfollowed_streams'] or report['summary']['coverage_gaps'] else 0
    report = analyze(args)
    if args.output:
        write_report(args.output, report)
        print(f"Saved {args.output}", file=sys.stderr)
    print(json.dumps(report, ensure_ascii=False) if args.json else render(report, args.top))
    return 2 if report["summary"]["coverage_gaps"] or report["summary"]["unknown"] else 0


def main(argv=None):
    args = parser().parse_args(argv)
    try:
        if args.command == "dashboard":
            from .dashboard import serve
            return serve(args.reports_dir, args.port)
        return run(args)
    except (ValueError, OSError) as error:
        print(f"jevernetes: {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("Interrupted", file=sys.stderr)
        return 130
