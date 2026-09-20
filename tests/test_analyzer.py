import argparse
import gzip
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from jev_log_analyzer.cli import main
from jev_log_analyzer.events import MAX_EVENT, MAX_LINE, baseline, parse_stream, redact
from jev_log_analyzer.jev import CATEGORY, IMPORTANCE, SEVERITY, Jev, build_request, classify, decode
from jev_log_analyzer.kubernetes import collect, kubectl, kubectl_error, targets
from jev_log_analyzer.report import build_report, safe_console, write_report


def events(text="INFO ready\n"):
    return parse_stream(io.BytesIO(text.encode()), {"type": "file", "path": "test.log"}, 100)[0]


def response(count=1):
    answers = {}
    for i in range(count):
        for name, choice in [("importance", "routine"), ("severity", "info"), ("category", "routine")]:
            answers[f"e{i}_{name}"] = {"type": "choice", "choice": choice, "confidence": 0.95}
    return {"answers": answers}


class ParsingTests(unittest.TestCase):
    def test_timestamped_stack_is_one_event(self):
        es = events("2026-09-20T12:00:00Z ERROR failed\n2026-09-20T12:00:01Z   at main.js:1\nCaused by: RuntimeException: failure\n2026-09-20T12:00:02Z INFO recovered\n")
        self.assertEqual(len(es), 2)
        self.assertEqual(es[0]["line_count"], 3)
        self.assertEqual(es[0]["line_end"], 3)
        self.assertEqual(es[0]["timestamp"], "2026-09-20T12:00:00Z")

    def test_event_limit_is_visible(self):
        es, warnings = parse_stream(io.BytesIO(b"one\ntwo\nthree\n"), {}, 2)
        self.assertEqual(len(es), 2)
        self.assertTrue(warnings)

    def test_exact_limit_does_not_mark_truncated(self):
        es, warnings = parse_stream(io.BytesIO(b"one\ntwo\n"), {}, 2)
        self.assertEqual(len(es), 2)
        self.assertFalse(warnings)

    def test_huge_line_is_one_truncated_event(self):
        es = events("x" * (MAX_LINE * 3) + "\nINFO next\n")
        self.assertEqual(len(es), 2)
        self.assertEqual(len(es[0]["text"]), MAX_EVENT)
        self.assertTrue(es[0]["truncated"])
        self.assertEqual(es[1]["line_start"], 2)

    def test_utf8_damage_is_tolerated(self):
        es, _ = parse_stream(io.BytesIO(b"INFO \xff\n"), {}, 10)
        self.assertIn("\ufffd", es[0]["text"])

    def test_credentials_are_redacted_in_text_and_nested_json(self):
        cases = [
            'password=secret123 token="secret123"',
            'Authorization: Bearer secret123',
            'postgres://user:secret123@db/app',  # trufflehog:ignore -- synthetic password-redaction fixture
            '{"request":{"api_key":"secret123"},"headers":{"authorization":"Bearer secret123"}}',
            '{"message":"failed token=secret123"}',
            'url=https://service/path?token=secret123&next=1',
        ]
        for value in cases:
            with self.subTest(value=value):
                self.assertNotIn("secret123", redact(value))

    def test_baseline_is_kept_separate(self):
        self.assertTrue(baseline(events("ERROR retry succeeded")[0])["important"])
        self.assertFalse(baseline(events("INFO backup bytes_written=0")[0])["important"])

    def test_color_codes_removed_before_analysis(self):
        self.assertEqual(events("\x1b[32minfo\x1b[39m: ready\n")[0]["text"], "info: ready")


class JevTests(unittest.TestCase):
    def test_payload_has_typed_question_for_every_event(self):
        payload = build_request(events("one\ntwo\n"), "jev-latest")
        self.assertEqual(len(payload["questions"]), 6)
        self.assertIn("untrusted", payload["questions"]["e0_importance"]["instructions"])

    def test_valid_response(self):
        self.assertEqual(decode(json.dumps(response()), 1)[0]["importance"], "routine")

    def test_missing_or_invalid_answers_are_not_noise(self):
        for field, bad in [("type", "text"), ("choice", "execute_shell"), ("confidence", -1), ("confidence", 2), ("confidence", True), ("confidence", float("nan"))]:
            data = response()
            data["answers"]["e0_importance"][field] = bad
            with self.subTest(field=field, bad=bad), self.assertRaises(ValueError):
                decode(json.dumps(data), 1)
        with self.assertRaises(ValueError):
            decode('{"answers":{}}', 1)

    def test_failures_and_budget_are_unknown(self):
        class Failed:
            def judge(self, batch):
                raise ValueError("Jev HTTP 429")
        es = events("one\ntwo\nthree\n")
        classify(es, Failed(), batch_size=1, workers=1, max_requests=2)
        self.assertTrue(all(e["importance"] == "unknown" for e in es))
        self.assertIn("budget", es[-1]["analysis_error"])

    def test_uncertain_on_truncation_or_low_routine_confidence(self):
        class Routine:
            def judge(self, batch):
                return [{"importance": "routine", "importance_confidence": 0.6} for e in batch]
        es = events("one\ntwo\n")
        es[0]["truncated"] = True
        classify(es, Routine())
        self.assertTrue(all(e["importance"] == "uncertain" for e in es))

    def test_http_errors_do_not_leak_body_or_credentials(self):
        import urllib.error
        error = urllib.error.HTTPError("https://example.com", 401, "secret", {}, io.BytesIO(b"provider-secret"))
        with patch("urllib.request.OpenerDirector.open", side_effect=error):
            with self.assertRaisesRegex(ValueError, "^Jev HTTP 401$"):
                Jev("key-secret").judge(events())


class KubernetesTests(unittest.TestCase):
    def test_actionable_errors_never_echo_credentials(self):
        for text, expected in [
            ('private-token context deadline exceeded', 'firewall allowlist'),
            ('private-token no such host', 'DNS'),
            ('private-token x509: certificate signed by unknown authority', 'TLS'),
            ('private-token Forbidden', 'permission'),
            ('private-token Unauthorized', 'authentication'),
            ('private-token connection refused', 'unreachable'),
        ]:
            message = kubectl_error(text.encode())
            self.assertIn(expected, message)
            self.assertNotIn('private-token', message)

    def pod(self):
        return {"metadata": {"namespace": "prod", "name": "api-123"},
                "spec": {"containers": [{"name": "api"}, {"name": "proxy"}], "initContainers": [{"name": "init"}], "ephemeralContainers": [{"name": "debug"}]},
                "status": {"containerStatuses": [{"name": "api", "restartCount": 2}], "initContainerStatuses": [{"name": "init", "restartCount": 1}]}}

    def test_all_container_kinds_and_previous(self):
        streams = targets([self.pod()])
        self.assertEqual(len(streams), 6)
        self.assertEqual(sum(s["previous"] for s in streams), 2)
        self.assertEqual(len(targets([self.pod()], False)), 4)

    def test_namespace_context_limits_and_partial_failures(self):
        calls, output = [], []
        def fake(args, **kwargs):
            calls.append(args)
            if args == ["config", "current-context"]:
                return b"test-cluster\n"
            if "get" in args:
                return json.dumps({"items": [self.pod()]}).encode()
            if "--previous=true" in args:
                raise ValueError("previous unavailable")
            return b"2026-09-20T12:00:00Z INFO ready\n"
        args = argparse.Namespace(context=None, namespace=None, selector="app=api", no_previous=False,
                                  since="1h", tail=500, max_bytes=10000, collect_workers=2)
        scope = collect(args, lambda *items: output.append(items), run=fake)
        self.assertEqual(scope["streams"], 6)
        self.assertEqual(sum(bool(x[2]) for x in output), 2)
        self.assertIn("--all-namespaces", calls[1])
        self.assertIn("app=api", calls[1])
        for call in calls[2:]:
            self.assertIn("--context", call)
            self.assertIn("test-cluster", call)
            self.assertIn("--tail=500", call)
            self.assertEqual(call[2], "logs")

    def test_subprocess_timeout_is_bounded(self):
        with patch("subprocess.run", side_effect=subprocess.TimeoutExpired("kubectl", 35)):
            with self.assertRaisesRegex(ValueError, "timed out"):
                kubectl(["get", "pods"])


class ReportAndCliTests(unittest.TestCase):
    def test_report_permissions_and_exact_grouping(self):
        es = events("ERROR count=1\nERROR count=1\nERROR count=2\n")
        for event in es:
            event.update(importance="important", severity="impact", category="data")
        report = build_report(es, [], {}, "test", 0, 0)
        self.assertEqual(len(report["important_groups"]), 2)
        self.assertEqual(report["important_groups"][0]["count"], 2)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            write_report(path, report)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(path.read_text())["summary"]["events"], 3)

    def test_terminal_escape_is_removed(self):
        self.assertEqual("error", safe_console("\x1b[31merror"))

    def test_gzip_and_missing_file_create_partial_report(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "app.log.gz"
            with gzip.open(log, "wb") as output:
                output.write(b"ERROR failed\nINFO ready\n")
            path = Path(directory) / "report.json"
            with patch("sys.stdout", new=io.StringIO()), patch("sys.stderr", new=io.StringIO()):
                code = main(["files", str(log), str(log) + "missing", "--offline", "--output", str(path)])
            self.assertEqual(code, 2)
            report = json.loads(path.read_text())
            self.assertEqual(report["summary"]["coverage_gaps"], 1)
            self.assertEqual(report["summary"]["events"], 2)

    def test_missing_api_key_fails_before_collection(self):
        with patch.dict(os.environ, {}, clear=True), patch("sys.stderr", new=io.StringIO()):
            self.assertEqual(main(["kubernetes"]), 1)

    def test_cli_stdin_end_to_end(self):
        result = subprocess.run([sys.executable, "-m", "jev_log_analyzer", "files", "-", "--offline", "--json"], input="ERROR fail\nINFO ready\n", capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        report = json.loads(result.stdout)
        self.assertEqual(report["summary"]["important"], 1)
        self.assertEqual(report["summary"]["uncertain"], 1)


if __name__ == "__main__":
    unittest.main()
