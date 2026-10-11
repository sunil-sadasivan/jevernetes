from concurrent.futures import ThreadPoolExecutor
import io
import tempfile
import threading
import time
import unittest

from jevernetes.cli import parser
from jevernetes.dashboard import Dashboard, scan_args
from jevernetes.events import parse_stream
from jevernetes.grouping import GroupedJudge, group_id
from jevernetes.jev import classify
from jevernetes.live import LiveSession
from jevernetes.report import build_report
from jevernetes.usage import UsageMeter


def events(text='ERROR status=503\n' * 3):
    return parse_stream(io.BytesIO(text.encode()), {'type': 'file', 'path': 'synthetic.log'}, 10000)[0]


class Client:
    def __init__(self):
        self.batches = []
        self.usage = UsageMeter()

    def judge(self, batch):
        self.batches.append(batch)
        return [dict(importance='important', importance_confidence=.99,
                     severity='impact', category='dependency') for e in batch]


class GroupingTests(unittest.TestCase):
    def test_snapshot_groups_before_budget_and_preserves_instances(self):
        data = events('2026-09-20T12:00:00Z ERROR status=503\n'
                      '2026-09-20T12:01:00Z ERROR status=503\n'
                      '2026-09-20T12:02:00Z ERROR status=500\n')
        client = Client()
        classify(data, client, batch_size=1, max_requests=1)
        self.assertEqual([len(b) for b in client.batches], [1])
        self.assertEqual([e['importance'] for e in data], ['important', 'important', 'unknown'])
        self.assertTrue(data[1]['analysis_reused'])
        self.assertNotEqual(data[0]['id'], data[1]['id'])
        self.assertNotEqual(data[0]['timestamp'], data[1]['timestamp'])
        report = build_report(data, [], {}, 'jev', 1, 0)
        self.assertEqual(report['summary']['reused_events'], 1)
        self.assertEqual(report['important_groups'][0]['event_ids'], [e['id'] for e in data[:2]])

    def test_opt_out_sends_every_occurrence(self):
        client = Client()
        classify(events(), client, grouping=False)
        self.assertEqual(len(client.batches[0]), 3)
        self.assertTrue(parser().parse_args(['files', '--no-grouping', 'x']).no_grouping)
        self.assertTrue(scan_args({'kind':'files', 'offline':True, 'grouping':False}).no_grouping)
        with self.assertRaises(ValueError):
            scan_args({'kind':'files', 'offline':True, 'grouping':'false'})

    def test_source_numbers_ids_stack_and_truncation_are_not_erased(self):
        a, b = events()[:2]
        self.assertEqual(group_id(a), group_id(b))
        for change in [dict(text='ERROR status=500'), dict(text='ERROR status=503\n  at other.py:2'),
                       dict(source={**a['source'], 'path':'other.log'}),
                       dict(source={**a['source'], 'context':'other'}),
                       dict(source={**a['source'], 'restart_count':1}), dict(truncated=True)]:
            self.assertNotEqual(group_id(a), group_id({**b, **change}))
        a['truncated'] = b['truncated'] = True
        self.assertNotEqual(group_id(a), group_id(b))

    def test_cache_reuses_within_and_across_batches_then_expires(self):
        client, clock = Client(), [0]
        judge = GroupedJudge(client.judge, ttl=300, clock=lambda:clock[0])
        data = events()
        result = judge(data)
        self.assertEqual(len(client.batches[0]), 1)
        self.assertEqual([r['analysis_reused'] for r in result], [False, True, True])
        self.assertTrue(judge([data[0]])[0]['analysis_reused'])
        clock[0] = 300
        self.assertFalse(judge([data[0]])[0]['analysis_reused'])
        self.assertEqual(len(client.batches), 2)

    def test_inflight_duplicates_share_one_request(self):
        entered, release = threading.Event(), threading.Event()
        client = Client()
        def slow(batch):
            entered.set()
            self.assertTrue(release.wait(3))
            return client.judge(batch)
        judge = GroupedJudge(slow)
        data = events()
        with ThreadPoolExecutor(max_workers=2) as pool:
            first = pool.submit(judge, [data[0]])
            self.assertTrue(entered.wait(2))
            second = pool.submit(judge, data[1:])
            release.set()
            self.assertFalse(first.result(timeout=3)[0]['analysis_reused'])
            self.assertTrue(all(r['analysis_reused'] for r in second.result(timeout=3)))
        self.assertEqual(len(client.batches), 1)

    def test_failures_and_truncated_events_are_never_cached(self):
        attempts = []
        def fail(batch):
            attempts.append(batch)
            raise ValueError('Jev HTTP 503')
        judge = GroupedJudge(fail)
        for _ in range(2):
            result = judge(events())
            self.assertTrue(all(r['importance'] == 'unknown' and not r['analysis_reused'] for r in result))
        self.assertEqual(len(attempts), 2)
        self.assertFalse(judge.cache)
        client = Client()
        judge = GroupedJudge(client.judge)
        data = events()
        for e in data: e['truncated'] = True
        judge(data); judge(data)
        self.assertEqual([len(b) for b in client.batches], [3,3])

    def test_cache_is_bounded_and_does_not_store_mutable_event_reviews(self):
        client = Client()
        judge = GroupedJudge(client.judge, limit=1)
        a, b = events('ERROR first\nERROR second\n')
        verdict = judge([a])[0]
        verdict['importance'] = 'routine'
        self.assertEqual(judge([a])[0]['importance'], 'important')
        judge([b]); judge([a])
        self.assertEqual(len(client.batches), 3)
        self.assertEqual(len(judge.cache), 1)

    def test_live_repeats_skip_budget_and_still_record_every_instance(self):
        with tempfile.TemporaryDirectory() as directory:
            args = parser().parse_args(['k8s', '--batch-size','1', '--max-batches','1',
                                       '--rules-file',directory+'/rules.json'])
            client = Client()
            session = LiveSession(args, client=client)
            worker = threading.Thread(target=session.worker)
            worker.start()
            try:
                for e in events(): session.emit(e)
                deadline = time.monotonic() + 3
                while session.snapshot()['summary']['events'] < 3 and time.monotonic() < deadline:
                    time.sleep(.01)
                report = session.snapshot()
                self.assertEqual(report['summary']['events'], 3)
                self.assertEqual(report['summary']['reused_events'], 2)
                self.assertEqual(report['live']['batches'], 1)
                self.assertEqual(len(report['tail_events']), 3)
                self.assertEqual(len({e['group_id'] for e in report['tail_events']}), 1)
                self.assertFalse(session.stop_event.is_set())
                # Review one occurrence; cached Jev judgments must remain intact.
                session.reviews.acknowledge(session.review_key, report['events'][0]['id'])
                session.snapshot()
                later = {**events()[0], 'id':'later'}
                session.emit(later)
                deadline = time.monotonic() + 2
                while session.snapshot()['summary']['events'] < 4 and time.monotonic() < deadline:
                    time.sleep(.01)
                self.assertEqual(later['importance'], 'important')
                self.assertNotIn('review', later)
                self.assertEqual(len(client.batches), 1)
            finally:
                session.stop()
                worker.join(3)
            self.assertFalse(worker.is_alive())

    def test_dashboard_exposes_the_whole_retained_window(self):
        with tempfile.TemporaryDirectory() as directory:
            app = Dashboard(directory)
            args = parser().parse_args(['k8s', '--offline', '--retain-events','1200',
                                       '--rules-file',directory+'/rules.json'])
            app.live_session = LiveSession(args)
            data = events('ERROR repeated\n' * 1100)
            for e in data: e['importance'] = 'important'
            app.live_session.record(data)
            self.assertEqual(len(app.live_report()['events']), 1100)


if __name__ == '__main__':
    unittest.main()
