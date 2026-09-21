import base64
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

from jevernetes.dashboard import Dashboard, make_server, scan_args


class DashboardTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.app = Dashboard(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def payload(self):
        return {"kind": "files", "offline": True,
                "files": [{"name": "app.log", "data": base64.b64encode(b"ERROR failed password=topsecret\nINFO ready\n").decode()}]}

    def wait_job(self):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            with self.app.lock:
                if self.app.job["status"] != "running":
                    return dict(self.app.job)
            time.sleep(.01)
        self.fail("Job timed out")

    def test_upload_analysis_persists_redacted_report(self):
        self.app.start(self.payload())
        job = self.wait_job()
        self.assertEqual(job["status"], "complete")
        report = self.app.report(job["report_id"])
        self.assertEqual(report["summary"]["events"], 2)
        self.assertEqual(report["scope"]["files"], ["app.log"])
        self.assertEqual(report["events"][0]["source"]["path"], "app.log")
        self.assertNotIn("topsecret", json.dumps(report))
        self.assertNotIn("jevernetes-logs-", json.dumps(report))
        self.assertEqual((Path(self.temp.name) / job["report_id"]).stat().st_mode & 0o777, 0o600)

    def test_single_active_job(self):
        self.app.job = {"status": "running"}
        with self.assertRaisesRegex(RuntimeError, "already running"):
            self.app.start(self.payload())

    def test_invalid_inputs_are_rejected(self):
        for payload in [[], {"kind": "shell"}, {"kind": "files", "offline": True, "files": []}, {"kind": "files", "offline": True, "files": [{"name": "app.log", "data": "!!!"}]}, {"kind": "kubernetes", "offline": True, "context": "--help"}, {"kind": "kubernetes", "offline": True, "since": "yesterday"}, {"kind": "files", "offline": "false"}]:
            with self.subTest(payload=payload), self.assertRaises(ValueError):
                self.app.start(payload)

    def test_report_paths_and_symlinks_are_rejected(self):
        for name in ["../secrets.json", "/tmp/report.json", "example.txt"]:
            with self.assertRaises(ValueError):
                self.app.report(name)
        path = Path(self.temp.name) / "link.json"
        path.symlink_to(Path(self.temp.name) / "outside.json")
        with self.assertRaises(ValueError):
            self.app.report(path.name)

    def test_kubernetes_runs_through_shared_engine_and_reports_failure(self):
        with patch("jevernetes.dashboard.analyze", side_effect=ValueError("Cannot inventory Kubernetes pods: timed out")) as analyze:
            self.app.start({"kind": "kubernetes", "offline": True, "context": "test-cluster", "namespace": "prod", "since": "30m"})
            job = self.wait_job()
            self.assertEqual(job["status"], "error")
            args = analyze.call_args.args[0]
            self.assertEqual(args.context, "test-cluster")
            self.assertEqual(args.namespace, "prod")
            self.assertEqual(args.since, "30m")
            self.assertFalse(args.no_previous)

    def test_ai_analysis_uses_existing_engine(self):
        def judge(_client, batch):
            return [{"importance": "important", "importance_confidence": .98, "severity": "impact", "category": "dependency"} for event in batch]
        with patch.dict("os.environ", {"TYPESAFE_API_KEY": "test-key"}), patch("jevernetes.jev.Jev.judge", judge):
            payload = self.payload()
            payload["offline"] = False
            self.app.start(payload)
            job = self.wait_job()
            report = self.app.report(job["report_id"])
            self.assertEqual(report["mode"], "jev")
            self.assertEqual(report["summary"]["important"], 2)

    def test_metadata_cache_refreshes_on_changed_file(self):
        self.app.start(self.payload())
        job = self.wait_job()
        first = self.app.reports()
        with patch.object(self.app, "_report", side_effect=AssertionError("should use metadata cache")):
            self.assertEqual(self.app.reports(), first)
        report = self.app.report(job["report_id"])
        report["summary"]["important"] = 15
        (Path(self.temp.name) / job["report_id"]).write_text(json.dumps(report))
        self.assertEqual(self.app.reports()[0]["summary"]["important"], 15)


class HttpTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.server = make_server(cls.temp.name, 0)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.url = f"http://127.0.0.1:{cls.server.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()
        cls.temp.cleanup()

    def request(self, path, payload=None, headers=None):
        data = json.dumps(payload).encode() if payload is not None else None
        req = urllib.request.Request(self.url + path, data=data, headers=headers or {})
        try:
            with urllib.request.urlopen(req, timeout=3) as response:
                return response.status, response.headers, response.read()
        except urllib.error.HTTPError as response:
            with response:
                return response.code, response.headers, response.read()

    def test_static_assets_and_security_headers(self):
        for path in ["/", "/app.js", "/context.js", "/prompt.js", "/search.js", "/style.css", "/favicon.svg"]:
            status, headers, body = self.request(path)
            self.assertEqual(status, 200)
            self.assertGreater(len(body), 100)
            self.assertIn("frame-ancestors 'none'", headers["Content-Security-Policy"])
            self.assertEqual(headers["Cache-Control"], "no-store")

    def test_foreign_host_and_cross_origin_posts_blocked(self):
        self.assertEqual(self.request("/api/state", headers={"Host": "evil.example"})[0], 403)
        self.assertEqual(self.request("/api/analyze", {"kind": "files"})[0], 403)
        self.assertEqual(self.request("/api/context", {"event_id": "fake"})[0], 403)
        self.assertEqual(self.request("/api/review", {"action": "expected"})[0], 403)
        self.assertEqual(self.request("/api/search", {"query": "db issues"})[0], 403)
        self.assertEqual(self.request("/api/search/stop", {"id": "fake"})[0], 403)
        state = json.loads(self.request("/api/state")[2])
        self.assertEqual(self.request("/api/analyze", {}, {"X-Jev-Token": state["csrf"], "Origin": "https://evil.example", "Content-Type": "application/json"})[0], 403)

    def test_full_http_upload_status_and_export(self):
        state = json.loads(self.request("/api/state")[2])
        payload = {"kind": "files", "offline": True, "files": [{"name": "http.log", "data": base64.b64encode(b"ERROR failed\n").decode()}]}
        status, _, body = self.request("/api/analyze", payload, {"X-Jev-Token": state["csrf"], "Content-Type": "application/json", "Origin": self.url})
        self.assertEqual(status, 202)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            state = json.loads(self.request("/api/state")[2])
            if state["job"]["status"] != "running":
                break
            time.sleep(.01)
        self.assertEqual(state["job"]["status"], "complete")
        status, _, body = self.request("/api/report?id=" + state["job"]["report_id"])
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["summary"]["important"], 1)
        event = json.loads(body)["events"][0]
        headers = {"X-Jev-Token": state["csrf"], "Content-Type": "application/json"}
        status, _, body = self.request("/api/search", {"report_id": state["job"]["report_id"], "event_ids": [event["id"]], "query": "failed", "mode": "literal"}, headers)
        self.assertEqual(status, 202)
        search_id = json.loads(body)["id"]
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            status, _, body = self.request("/api/search?id=" + search_id)
            result = json.loads(body)
            if result["status"] != "running":
                break
            time.sleep(.01)
        self.assertEqual(status, 200)
        self.assertEqual(result["matched_groups"], 1)
        self.assertEqual(result["usage"]["request_attempts"], 0)
        self.assertEqual(result["results"][0]["event_id"], event["id"])
        self.assertEqual(self.request("/api/search?id=" + search_id + "&offset=-1")[0], 404)
        status, _, _ = self.request("/api/review", {"action": "acknowledge", "report_id": state["job"]["report_id"], "event_id": event["id"]}, headers)
        self.assertEqual(status, 200)
        reviewed = json.loads(self.request("/api/report?id=" + state["job"]["report_id"])[2])
        self.assertEqual(reviewed["summary"]["important"], 0)
        self.assertEqual(reviewed["events"][0]["review"]["status"], "acknowledged")
        self.assertEqual(reviewed["events"][0]["original_judgment"]["importance"], "important")
        self.assertEqual(self.request("/api/report?id=../outside.json")[0], 404)

    def test_rule_inspection_and_review_errors(self):
        status, _, body = self.request("/api/rules")
        self.assertEqual(status, 200)
        self.assertEqual(len(json.loads(body)["baseline"]), 3)
        headers = {"X-Jev-Token": self.server.app.token, "Content-Type": "application/json"}
        for path in ("/api/context", "/api/review", "/api/search", "/api/search/stop"):
            self.assertEqual(self.request(path, {}, headers)[0], 400)


if __name__ == "__main__":
    unittest.main()
