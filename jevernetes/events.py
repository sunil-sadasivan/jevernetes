"""Bounded event parsing and best-effort credential redaction."""
import hashlib
import json
import re

MAX_LINE = 65536
MAX_EVENT = 16000
MAX_INPUT_BYTES = 32 * 1024 * 1024
ANSI = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))")
TIMESTAMP = re.compile(r"^\d{4}-\d\d-\d\d[T ][\d:.]+(?:Z|[+-]\d\d:\d\d)?[ \t]")
CONTINUATION = re.compile(r"^(?:\s+\S|Caused by:|Suppressed:|Traceback |[\w.]+(?:Error|Exception):|\s*\.\.\. \d+ more)")
SECRET_FIELD = re.compile(r"(?i)(password|passwd|secret|token|authorization|cookie|api[-_]?key|access[-_]?key(?:[-_]?id)?|private[-_]?key|credential)")
SECRET_VALUE = re.compile(r'''(?ix)(["']?(?:password|passwd|secret|(?:access_|refresh_)?token|api[-_]?key|access[-_]?key(?:[-_]?id)?|private[-_]?key|authorization|cookie|credentials?)["']?\s*[:=]\s*)("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|[^\s,;]+)''')


def redact(text):
    text = ANSI.sub("", text)
    def clean(value):
        if isinstance(value, dict):
            return {k: "[REDACTED]" if SECRET_FIELD.search(k) else clean(v) for k, v in value.items()}
        if isinstance(value, list):
            return [clean(v) for v in value]
        return scrub(value) if isinstance(value, str) else value

    def scrub(value):
        value = re.sub(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+", r"\1 [REDACTED]", value)
        value = re.sub(r"(\b[a-zA-Z][a-zA-Z0-9+.-]{0,31}://)[^\s/@:]+:[^\s/@]+@", r"\1[REDACTED]@", value)
        value = re.sub(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b", "[REDACTED]", value)
        return SECRET_VALUE.sub(lambda m: m[1] + '"[REDACTED]"', value)

    try:
        return json.dumps(clean(json.loads(text)), ensure_ascii=False)
    except (ValueError, TypeError, RecursionError):
        return scrub(text)


class StreamRedactor:
    """Suppress a PEM private-key block even when its lines arrive separately."""
    begin = re.compile(r"-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----")
    end = re.compile(r"-----END (?:[A-Z0-9]+ )?PRIVATE KEY-----")

    def __init__(self):
        self.private_key = False

    def redact(self, text):
        if self.private_key or self.begin.search(text):
            self.private_key = not bool(self.end.search(text))
            return "[REDACTED PRIVATE KEY]"
        return redact(text)


def parse_stream(stream, source, max_events, max_bytes=MAX_INPUT_BYTES):
    events, warnings = [], []
    pending = None
    line_no = 0
    remaining = max_bytes
    redactor = StreamRedactor()
    checked_limit = False

    def read_line():
        nonlocal remaining, checked_limit
        if remaining <= 0:
            if not checked_limit and stream.read(1):
                warnings.append("Decompressed input byte limit reached; remaining input was not analyzed")
            checked_limit = True
            return b""
        raw = stream.readline(min(MAX_LINE + 1, remaining))
        remaining -= len(raw)
        return raw

    while True:
        raw = read_line()
        if not raw:
            break
        line_no += 1
        oversize = (len(raw) > MAX_LINE or remaining == 0) and not raw.endswith(b"\n")
        if oversize:
            while raw_tail := read_line():
                if raw_tail.endswith(b"\n"):
                    break
        text = raw.decode("utf-8", errors="replace").rstrip("\r\n")
        if not text:
            continue
        match = TIMESTAMP.match(text)
        timestamp = match[0].strip() if match else None
        body = text[match.end():] if match else text
        if pending and CONTINUATION.match(body):
            pending["line_end"] = line_no
            pending["line_count"] += 1
            combined = pending["text"] + "\n" + redactor.redact(body)
            pending["truncated"] |= oversize or len(combined) > MAX_EVENT
            pending["text"] = combined[:MAX_EVENT]
        else:
            if pending:
                events.append(pending)
            if len(events) >= max_events:
                warnings.append("Event limit reached; remaining input was not analyzed")
                pending = None
                break
            safe = redactor.redact(body)
            pending = dict(source=source, timestamp=timestamp, text=safe[:MAX_EVENT],
                           line_start=line_no, line_end=line_no, line_count=1,
                           truncated=oversize or len(safe) > MAX_EVENT)
    if pending:
        events.append(pending)
    if any(e["truncated"] for e in events):
        warnings.append("Oversized lines/events were truncated; their classification requires review")
    for event in events:
        event["id"] = hashlib.sha256(json.dumps([source, event["line_start"], event["text"]], sort_keys=True).encode()).hexdigest()[:20]
    return events, warnings


BASELINE_RULES = [
    {"id": "error_or_failure_keyword", "description": "Failure or resource-exhaustion keywords", "pattern": r"(?i)\b(error|fatal|panic|exception|critical|oomkilled|out of memory|crashloopbackoff)\b"},
    {"id": "http_5xx", "description": "HTTP status in the 500–599 range", "pattern": r'(?i)(?:status["\s:=]+|HTTP/\d(?:\.\d)?["\s]+)5\d\d\b'},
    {"id": "warning_level", "description": "Warning-level keywords", "pattern": r'(?i)\b(warn|warning)\b'},
]


def baseline(event):
    signals = [rule["id"] for rule in BASELINE_RULES if re.search(rule["pattern"], event["text"])]
    return {"important": bool(signals), "signals": signals}
