"""Exercise the built Go dashboard over real loopback HTTP with synthetic logs."""
import base64
import gzip
import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

binary = Path(sys.argv[1]).resolve()
with tempfile.TemporaryDirectory(prefix='jev-go-http-') as directory:
    root = Path(directory)
    env = {k: v for k, v in os.environ.items() if not k.startswith(('TYPESAFE', 'KUBECONFIG'))}
    child = subprocess.Popen([str(binary), 'dashboard', '--offline', '--listen', '127.0.0.1:0', '--reports-dir', str(root / 'reports')], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=env)
    try:
        assert select.select([child.stderr], [], [], 10)[0], 'Dashboard did not start'
        line = child.stderr.readline().decode().strip()
        assert line.startswith('Dashboard: http://127.0.0.1:'), line
        url = line.split(' ', 1)[1]
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def request(path, payload=None, token=None, headers=None):
            extra = {'Content-Type': 'application/json', **(headers or {})}
            if token is not None:
                extra['X-Jev-Token'] = token
            raw = json.dumps(payload).encode() if payload is not None else None
            req = urllib.request.Request(url + path, data=raw, headers=extra)
            try:
                with opener.open(req, timeout=5) as response:
                    content = response.read()
                    return response.status, json.loads(content) if 'application/json' in response.headers.get('Content-Type', '') else content
            except urllib.error.HTTPError as error:
                return error.code, json.loads(error.read())
        code, state = request('/api/state')
        assert code == 200 and not state['jev_available']
        token = state['csrf']
        assert request('/api/state', headers={'Origin': 'http://untrusted.invalid'})[0] == 403
        assert request('/api/review', {}, '')[0] == 403
        for asset in ('/', '/app.js', '/search.js', '/context.js', '/prompt.js', '/style.css', '/controller/'):
            assert request(asset)[0] == 200, asset
        evidence = b'2026-01-01T00:00:00Z ERROR database unavailable\n  at synthetic.go:12\n2026-01-01T00:00:01Z INFO ready\n'
        code, job = request('/api/analyze', {'kind': 'files', 'offline': True, 'files': [{'name': 'synthetic.log', 'data': base64.b64encode(evidence).decode()}]}, token)
        assert code == 202, job
        deadline = time.monotonic() + 10
        while True:
            _, state = request('/api/state')
            if state['job']['status'] not in ('running', 'stopping'):
                break
            assert time.monotonic() < deadline, 'Scan did not finish'
            time.sleep(.03)
        assert state['job']['status'] == 'complete', state['job']
        report_id = state['job']['report_id']
        _, report = request('/api/report?id=' + report_id)
        assert report['runtime'] == 'go' and report['summary']['events'] == 2, report['summary']
        assert report['summary']['api_requests'] == 0
        first = report['events'][0]
        assert first['source']['path'] == 'synthetic.log' and 'synthetic.go:12' in first['text']
        reference = {'report_id': report_id, 'event_id': first['id']}
        assert request('/api/review', {**reference, 'action': 'acknowledge'}, token)[0] == 200
        _, reviewed = request('/api/report?id=' + report_id)
        assert reviewed['events'][0]['review']['status'] == 'acknowledged'
        assert request('/api/review', {**reference, 'action': 'unacknowledge'}, token)[0] == 200
        assert request('/api/review', {**reference, 'action': 'expected', 'pattern': 'database unavailable'}, token)[0] == 200
        _, rules = request('/api/rules')
        assert request('/api/review', {'action': 'toggle', 'rule_id': rules['expected'][0]['id'], 'enabled': False}, token)[0] == 200
        _, restored = request('/api/report?id=' + report_id)
        assert restored['events'][0]['importance'] == first['importance']
        assert 'original_judgment' not in restored['events'][0]
        code, search = request('/api/search', {'report_id': report_id, 'query': 'database', 'mode': 'literal'}, token)
        assert code == 202, search
        deadline = time.monotonic() + 5
        while True:
            _, result = request('/api/search?id=' + search['id'])
            if result['status'] != 'running':
                break
            assert time.monotonic() < deadline
            time.sleep(.03)
        assert result['matching_instances'] == 1 and not result['partial'], result
        assert request('/api/search', {'report_id': report_id, 'query': 'database', 'mode': 'jev'}, token)[0] == 400
        assert request('/api/report?id=../bad.json')[0] == 400
        saved = root / 'reports' / report_id
        assert saved.stat().st_mode & 0o777 == 0o600
        assert json.loads(saved.read_text())['events'][0]['importance'] == first['importance']
        assert request('/api/analyze', {'kind': 'files', 'files': [{'name': '../escape.log', 'data': ''}]}, token)[0] == 400
        code, compressed = request('/api/analyze', {'kind': 'files', 'offline': True, 'files': [{'name': 'archive.log.gz', 'data': base64.b64encode(gzip.compress(evidence)).decode()}]}, token)
        assert code == 202, compressed
        deadline = time.monotonic() + 10
        while True:
            _, state = request('/api/state')
            if state['job']['status'] not in ('running', 'stopping'):
                break
            assert time.monotonic() < deadline
            time.sleep(.03)
        assert state['job']['status'] == 'complete', state['job']
        _, compressed_report = request('/api/report?id=' + state['job']['report_id'])
        assert compressed_report['summary']['events'] == 2
        assert compressed_report['events'][0]['source']['path'] == 'archive.log.gz'
        print('PASS: Go dashboard HTTP: upload, report history, assets, CSRF/origin, exact search, reversible reviews, private persistence')
    finally:
        child.terminate()
        child.wait(timeout=10)
