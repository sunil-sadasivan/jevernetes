from collections import Counter
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
from unittest.mock import patch

from jev_log_analyzer.cli import parser
from jev_log_analyzer.dashboard import Dashboard, scan_args
from jev_log_analyzer.events import parse_stream
from jev_log_analyzer.jev import Jev
from jev_log_analyzer.live import Cursor, Grouper, LiveSession, running_targets
from jev_log_analyzer.usage import UsageMeter
from jev_log_analyzer.report import TailPrinter


def options(*extra):
    return parser().parse_args(['k8s', '--live', '--offline', '--duration', '2', '--discovery-interval', '1', '--workers', '1', *extra])


def pod(uid='uid1', restarts=0):
    return {'metadata': {'uid': uid, 'namespace': 'prod', 'name': 'api'}, 'status': {'containerStatuses': [{'name': 'app', 'restartCount': restarts, 'state': {'running': {}}}], 'initContainerStatuses': [{'name': 'init', 'state': {'terminated': {}}}]}}


def inventory(command, **kwargs):
    if 'current-context' in command:
        return b'test-context'
    return json.dumps({'items': [pod()]}).encode()


class UsageTests(unittest.TestCase):
    def test_precision_missing_usage_and_concurrency(self):
        meter = UsageMeter()
        def record():
            meter.begin()
            meter.record(json.dumps({'usage': {'input_tokens': 1000, 'output_tokens': 20}}))
        threads = [threading.Thread(target=record) for _ in range(20)]
        for thread in threads: thread.start()
        for thread in threads: thread.join()
        s = meter.snapshot()
        self.assertEqual(s['input_tokens'], 20000)
        self.assertAlmostEqual(s['estimated_cost_usd'], .00084)
        self.assertTrue(s['cost_complete'])
        meter.begin()
        self.assertEqual(meter.snapshot()['in_flight'], 1)
        meter.record(None)
        self.assertFalse(meter.snapshot()['cost_complete'])
        self.assertEqual(meter.snapshot()['unmetered_requests'], 1)

    def test_invalid_usage_never_invents_tokens(self):
        meter = UsageMeter()
        for value in [True, -1, 1.2, '500', None]:
            meter.begin()
            meter.record(json.dumps({'usage': {'input_tokens': value}}))
        self.assertEqual(meter.snapshot()['input_tokens'], 0)
        self.assertEqual(meter.snapshot()['unmetered_requests'], 5)
        with self.assertRaises(ValueError): UsageMeter(float('nan'))

    def test_bad_verdict_still_counts_billed_usage(self):
        client = Jev('fake')
        raw = json.dumps({'answers': {}, 'usage': {'input_tokens': 200, 'output_tokens': 0}}).encode()
        with patch('urllib.request.OpenerDirector.open', return_value=io.BytesIO(raw)):
            with self.assertRaises(ValueError): client.judge([{'source': {}, 'text': 'hello', 'truncated': False}])
        self.assertEqual(client.usage.snapshot()['input_tokens'], 200)
        self.assertEqual(client.usage.snapshot()['request_attempts'], 1)

    def test_error_attempts_missing_usage_are_visible(self):
        client = Jev('fake')
        error = urllib.error.HTTPError('https://example.com', 401, 'private-error', {}, io.BytesIO(b'private-body'))
        with patch('urllib.request.OpenerDirector.open', side_effect=error):
            with self.assertRaisesRegex(ValueError, 'HTTP 401'):
                client.judge([{'source': {}, 'text': 'hello', 'truncated': False}])
        self.assertEqual(client.usage.snapshot()['unmetered_requests'], 1)

    def test_stopped_client_does_not_issue_requests(self):
        event = threading.Event(); event.set()
        client = Jev('fake', stop_event=event)
        with self.assertRaisesRegex(ValueError, 'stopped'):
            client.judge([])
        self.assertEqual(client.usage.snapshot()['request_attempts'], 0)

    def test_retries_count_each_attempt_and_missing_usage(self):
        client = Jev('fake')
        error = urllib.error.HTTPError('https://example.com', 429, 'rate limit', {}, io.BytesIO(b'no usage'))
        response = {'answers': {f'e0_{k}': {'type':'choice', 'choice':v, 'confidence':.9} for k,v in [('importance','routine'),('severity','info'),('category','routine')]}, 'usage':{'input_tokens':100,'output_tokens':10}}
        with patch('urllib.request.OpenerDirector.open', side_effect=[error, io.BytesIO(json.dumps(response).encode())]), patch.object(client.stop_event, 'wait', return_value=False):
            client.judge([{'source':{},'text':'hello','truncated':False}])
        s = client.usage.snapshot()
        self.assertEqual(s['request_attempts'], 2)
        self.assertEqual(s['unmetered_requests'], 1)
        self.assertEqual(s['input_tokens'], 100)


class StreamTests(unittest.TestCase):
    def test_tail_is_visible_before_classification_then_updates_in_place(self):
        session = LiveSession(options())
        event = parse_stream(io.BytesIO(b'ERROR failed token=secret\n'), {}, 1)[0][0]
        session.emit(event)
        pending = session.snapshot()['tail_events']
        self.assertEqual(pending[0]['importance'], 'pending')
        self.assertEqual(pending[0]['sequence'], 1)
        self.assertNotIn('secret', pending[0]['text'])
        self.assertEqual(session.snapshot()['summary']['events'], 0)
        event.update(importance='important')
        session.record([event])
        resolved = session.snapshot()['tail_events']
        self.assertEqual(len(resolved), 1)
        self.assertEqual(resolved[0]['importance'], 'important')
        self.assertEqual(pending[0]['importance'], 'pending')

    def test_tail_overflow_and_eviction_are_bounded(self):
        session = LiveSession(options('--queue-size','1','--retain-events','1'))
        first = parse_stream(io.BytesIO(b'INFO first\n'), {}, 1)[0][0]
        second = parse_stream(io.BytesIO(b'INFO second\n'), {}, 1)[0][0]
        session.emit(first); session.emit(second)
        report = session.snapshot()
        self.assertEqual(len(session.tail_index),1)
        self.assertEqual(len(report['tail_events']),1)
        self.assertEqual(report['tail_events'][0]['importance'],'dropped')
        self.assertEqual(report['live']['dropped'],1)

    def test_terminal_tail_prints_lines_once_and_later_judgments(self):
        output, status = io.StringIO(), io.StringIO()
        printer = TailPrinter(output, status)
        session = LiveSession(options())
        event = parse_stream(io.BytesIO(b'ERROR unique-message\n'), {'namespace':'prod','pod':'api','container':'app'}, 1)[0][0]
        session.emit(event)
        printer.publish(session.snapshot())
        event.update(importance='important', severity='impact', category='dependency')
        session.record([event])
        printer.publish(session.snapshot()); printer.publish(session.snapshot())
        self.assertEqual(output.getvalue().count('unique-message'),1)
        self.assertIn('[prod/api/app]',output.getvalue())
        self.assertEqual(status.getvalue().count('[jev] #1 important'),1)

    def test_follow_alias_and_new_lines_only(self):
        args = parser().parse_args(['k8s','-f','--tail','0'])
        self.assertTrue(args.live)
        self.assertEqual(args.tail,0)
        self.assertEqual(scan_args({'kind':'kubernetes','live':True,'offline':True,'tail':0}).tail,0)
        with self.assertRaises(ValueError): scan_args({'kind':'kubernetes','offline':True,'tail':0})

    def test_reconnect_preserves_repeated_identical_occurrences(self):
        cursor = Cursor()
        a = b'2026-09-20T12:00:00.000000123Z INFO same\n'
        self.assertTrue(cursor.accept(a)); self.assertTrue(cursor.accept(a))
        cursor.reconnect()
        self.assertFalse(cursor.accept(a)); self.assertFalse(cursor.accept(a)); self.assertTrue(cursor.accept(a))
        self.assertTrue(cursor.accept(b'2026-09-20T12:00:00.000000124Z INFO next\n'))
        self.assertFalse(cursor.accept(a))

    def test_nanosecond_and_no_fraction_timestamp_order(self):
        cursor = Cursor()
        self.assertTrue(cursor.accept(b'2026-09-20T12:00:00Z hi\n'))
        self.assertTrue(cursor.accept(b'2026-09-20T12:00:00.1Z hi\n'))
        self.assertTrue(cursor.accept(b'2026-09-20T12:00:00.12Z hi\n'))

    def test_multiline_redaction_and_event_flush(self):
        result = []
        group = Grouper({'type': 'kubernetes'}, result.append)
        group.feed(b'2026-09-20T12:00:00Z ERROR failed token=secret\n')
        group.feed(b'2026-09-20T12:00:00Z   at checkout.js:12\n')
        group.flush()
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]['line_count'], 2)
        self.assertNotIn('secret', result[0]['text'])

    def test_pod_restart_and_replacement_have_distinct_stream_ids(self):
        self.assertNotEqual(set(running_targets([pod()])), set(running_targets([pod(restarts=1)])))
        self.assertNotEqual(set(running_targets([pod()])), set(running_targets([pod(uid='new')])) )
        self.assertEqual(len(running_targets([pod()])), 1)

    def test_follow_is_incremental_and_process_is_stopped(self):
        commands, processes = [], []
        def spawn(command, **kwargs):
            commands.append(command)
            proc = subprocess.Popen([sys.executable, '-u', '-c', 'import time; print("2026-09-20T12:00:00Z ERROR database unavailable"); time.sleep(60)'], **kwargs)
            processes.append(proc)
            return proc
        session = LiveSession(options(), run_kubectl=inventory, popen=spawn)
        observed = []
        report = session.run(lambda r: observed.append(r['summary']['events']))
        self.assertTrue(any(count > 0 for count in observed))
        self.assertEqual(report['summary']['important'], 1)
        self.assertEqual(report['live']['status'], 'stopped')
        self.assertTrue(all(p.poll() is not None for p in processes))
        self.assertIn('--follow=true', commands[0])
        self.assertIn('--context', commands[0])

    def test_bounded_queue_and_retained_window_have_full_counters(self):
        session = LiveSession(options('--queue-size', '1', '--retain-events', '1'))
        event = parse_stream(io.BytesIO(b'ERROR failed\n'), {}, 1)[0][0]
        session.emit(dict(event)); session.emit(dict(event))
        self.assertEqual(session.snapshot()['live']['dropped'], 1)
        session.record([{**event, 'importance': 'important'}, {**event, 'importance': 'routine'}])
        report = session.snapshot()
        self.assertEqual(report['summary']['events'], 2)
        self.assertEqual(len(report['events']), 1)

    def test_cost_threshold_stops_and_marks_pending_unknown(self):
        args = options('--max-cost', '0.000001', '--batch-size', '1')
        args.offline = False
        class Client:
            usage = UsageMeter()
            def judge(self, batch):
                self.usage.begin(); self.usage.record(json.dumps({'usage': {'input_tokens': 1000, 'output_tokens': 10}}))
                return [{'importance': 'important', 'importance_confidence': .99} for _ in batch]
        session = LiveSession(args, client=Client())
        event = parse_stream(io.BytesIO(b'ERROR failed\n'), {}, 1)[0][0]
        session.emit(dict(event)); session.emit(dict(event))
        thread = threading.Thread(target=session.worker); thread.start(); thread.join(3)
        self.assertFalse(thread.is_alive())
        self.assertTrue(session.stop_event.is_set())
        self.assertEqual(session.snapshot()['summary']['unknown'], 1)
        self.assertEqual(session.snapshot()['usage']['request_attempts'], 1)

    def test_dashboard_rejects_live_files_and_bad_budget(self):
        for p in [{'kind':'files', 'live':True}, {'kind':'kubernetes', 'live':True, 'offline':True, 'max_cost':float('nan')}]:
            with self.assertRaises(ValueError): scan_args(p)

    def test_failed_inventory_is_preserved_after_stop(self):
        def unavailable(*args, **kwargs):
            raise ValueError('unavailable')
        session = LiveSession(options('--context','test','--duration','1'), run_kubectl=unavailable)
        report = session.run()
        self.assertEqual(report['live']['status'], 'stopped')
        self.assertEqual(report['summary']['coverage_gaps'], 1)
        self.assertIn('inventory unavailable', report['coverage'][0]['warnings'][0])
        self.assertEqual(report['summary']['streams'], 0)
        self.assertGreater(report['live']['discovery_attempts'], 0)

    def test_dashboard_banner_uses_current_live_status(self):
        with tempfile.TemporaryDirectory() as directory:
            app = Dashboard(directory)
            app.live_session = LiveSession(options())
            app.live_session.status = 'reconnecting'
            app.live_session.message = 'Kubernetes API connection timed out; check firewall allowlist'
            app.job = {'id':'test','live':True,'status':'running','message':'Starting analysis…'}
            state = app.state()
            self.assertEqual(state['job']['phase'], 'reconnecting')
            self.assertIn('firewall',state['job']['message'])

    def test_terminal_reports_connection_error_without_repeating_each_tick(self):
        output, status = io.StringIO(), io.StringIO()
        printer = TailPrinter(output,status)
        session = LiveSession(options())
        session.status = 'reconnecting'
        session.message = 'Kubernetes API connection timed out; check firewall allowlist'
        printer.publish(session.snapshot()); printer.publish(session.snapshot())
        self.assertIn('firewall allowlist',status.getvalue())
        self.assertEqual(status.getvalue().count('[tail]'),1)

    def test_dashboard_live_start_poll_stop_and_save(self):
        def spawn(command, **kwargs):
            return subprocess.Popen([sys.executable,'-u','-c','import time; print("2026-09-20T12:00:00Z ERROR example"); time.sleep(60)'], **kwargs)
        def factory(args):
            return LiveSession(args,run_kubectl=inventory,popen=spawn)
        with tempfile.TemporaryDirectory() as directory, patch('jev_log_analyzer.dashboard.LiveSession', factory):
            app = Dashboard(directory)
            app.start({'kind':'kubernetes','offline':True,'live':True})
            until = time.monotonic() + 4
            while time.monotonic() < until:
                if app.live_report()['summary']['events']:
                    break
                time.sleep(.02)
            self.assertEqual(app.live_report()['summary']['important'], 1)
            self.assertEqual(app.stop_live()['status'],'stopping')
            self.assertTrue(app.live_session.done.wait(4))
            until = time.monotonic() + 2
            while app.state()['job']['status'] != 'complete' and time.monotonic() < until:
                time.sleep(.02)
            self.assertEqual(app.state()['job']['status'],'complete')
            saved = app.report(app.state()['job']['report_id'])
            self.assertEqual(saved['live']['status'],'stopped')
            self.assertIn('estimated_cost_usd', saved['usage'])


if __name__ == '__main__': unittest.main()
