import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from jevernetes.cli import parser, analyze
from jevernetes.context import fetch_context, MAX_BYTES
from jevernetes.dashboard import Dashboard
from jevernetes.events import baseline, BASELINE_RULES, parse_stream
from jevernetes.live import LiveSession
from jevernetes.report import build_report, write_report
from jevernetes.reviews import ReviewStore


def event():
    source = {'type':'kubernetes','context':'test-cluster','namespace':'demo','pod':'api-abc',
              'pod_uid':'uid-one','container':'app','restart_count':2,'previous':False}
    e = parse_stream(io.BytesIO(b'2026-09-20T12:00:00Z INFO optional record not found id=42\n'), source, 10)[0][0]
    e.update(importance='important',importance_confidence=.27,severity='degraded',category='data')
    return e


def pod(uid='uid-one', restarts=2):
    return json.dumps({'metadata':{'uid':uid},'status':{'containerStatuses':[{'name':'app','restartCount':restarts}]}}).encode()


class ContextTests(unittest.TestCase):
    def test_window_redaction_bounds_and_no_ai(self):
        calls=[]
        def run(args, **kw):
            calls.append((args,kw))
            if 'get' in args: return pod()
            return b'2026-09-20T11:59:00Z INFO password=secret\n2026-09-20T12:00:00Z ERROR issue\n2026-09-20T12:03:00Z INFO outside\n'
        result=fetch_context(event(),120,run)
        self.assertEqual(len(result['events']),2)
        self.assertNotIn('secret',json.dumps(result))
        self.assertTrue(all(e['context_only'] and e['importance']=='unclassified' for e in result['events']))
        command=calls[1][0]
        self.assertIn('--since-time=2026-09-20T11:58:00Z',command)
        self.assertIn('--tail=-1',command)
        self.assertEqual(calls[1][1]['max_bytes'],MAX_BYTES)

    def test_previous_instance_and_replaced_pod(self):
        commands=[]
        def run(args, **kw):
            commands.append(args)
            return pod(restarts=3) if 'get' in args else b''
        self.assertTrue(fetch_context(event(),30,run)['previous'])
        self.assertIn('--previous=true',commands[1])
        with self.assertRaisesRegex(ValueError,'replaced'):
            fetch_context(event(),30,lambda *a,**k:pod('replacement'))
        with self.assertRaisesRegex(ValueError,'no longer retained'):
            fetch_context(event(),30,lambda *a,**k:pod(restarts=4))

    def test_restart_race_rejected(self):
        responses=iter([pod(),b'2026-09-20T12:00:00Z INFO hello\n',pod(restarts=3)])
        with self.assertRaisesRegex(ValueError,'changed while'):
            fetch_context(event(),30,lambda *a,**k:next(responses))

    def test_invalid_window_and_source_never_run(self):
        def fail(*a,**kw): self.fail('kubectl must not run')
        with self.assertRaises(ValueError): fetch_context(event(),True,fail)
        bad=event();bad['source']['pod']='--all-pods'
        with self.assertRaises(ValueError): fetch_context(bad,30,fail)
        bad=event();bad['timestamp']=None
        with self.assertRaises(ValueError): fetch_context(bad,30,fail)

    def test_legacy_identity_warning_and_retention_limits(self):
        e=event();e['source'].pop('pod_uid');e['source'].pop('restart_count')
        result=fetch_context(e,30,lambda args,**kw:pod() if 'get' in args else b'')
        self.assertTrue(any('cannot be verified' in w for w in result['warnings']))
        self.assertTrue(any('No retained logs' in w for w in result['warnings']))


class ReviewTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.store=ReviewStore(Path(self.temp.name)/'.review-rules.json')
    def tearDown(self): self.temp.cleanup()

    def test_expected_scope_and_disable_restore_original(self):
        e=event();original=copy.deepcopy(e)
        rule=self.store.add(e,'optional record not found')
        self.assertTrue(self.store.apply(e))
        self.assertEqual(e['importance'],'routine')
        self.assertEqual(e['review']['status'],'expected')
        self.assertEqual(e['original_judgment']['importance_confidence'],.27)
        sibling=copy.deepcopy(original);sibling['source']['pod']='api-new'
        self.assertTrue(self.store.apply(sibling))
        other=copy.deepcopy(original);other['source']['namespace']='other'
        self.assertFalse(self.store.apply(other))
        self.store.set_enabled(rule['id'],False)
        self.assertFalse(self.store.apply(e))
        self.assertEqual(e,original)
        self.assertEqual(self.store.path.stat().st_mode & 0o777,0o600)

    def test_literal_pattern_and_exact_pod_scope(self):
        e=event();e['text']='INFO expected [optional].* record missing'
        self.store.add(e,'[optional].*','source')
        self.assertTrue(self.store.apply(e))
        e2=event();e2['text']='INFO expected optional anything'
        self.assertFalse(self.store.apply(e2))
        e2=copy.deepcopy(e);e2['source']['pod']='other'
        self.assertFalse(self.store.apply(e2))
        for pattern in ('INFO','not in this event', 'x'*501):
            with self.assertRaises(ValueError):self.store.add(event(),pattern)

    def test_short_pattern_matches_only_whole_message(self):
        e=event();e['text']='  } '
        rule=self.store.add(e,'}')
        self.assertEqual(rule['match'],'exact')
        self.assertTrue(self.store.apply(e))
        for text in ('ERROR failed {"status":500}', 'ERROR failed\n}', '}}', 'INFO }'):
            other=event();other['text']=text
            self.assertFalse(self.store.apply(other),text)
        other=event();other['text']='}'
        other['source']['namespace']='another'
        self.assertFalse(self.store.apply(other))
        reloaded=ReviewStore(self.store.path)
        other=event();other['text']='}\n'
        self.assertTrue(reloaded.apply(other))
        reloaded.set_enabled(rule['id'],False)
        self.assertFalse(reloaded.apply(other))
        self.assertEqual(other['importance'],'important')

    def test_bulk_rules_validate_before_writing_and_deduplicate(self):
        e=event();short=event();short['id']='short';short['text']='}'
        before=self.store.snapshot()
        with self.assertRaises(ValueError):
            self.store.add_many([(e,'optional record not found'),(short,'missing text')])
        self.assertEqual(self.store.snapshot(),before)
        self.assertFalse(self.store.path.exists())
        with patch.object(self.store,'_save',wraps=self.store._save) as save:
            rules=self.store.add_many([(e,'optional record not found'),(e,'optional record not found'),(short,'}')])
            self.assertEqual(save.call_count,1)
        self.assertEqual(len(self.store.snapshot()['rules']),2)
        self.assertEqual(rules[0]['id'],rules[1]['id'])
        self.store.set_enabled(rules[0]['id'],False)
        self.store.add_many([(e,'optional record not found')])
        self.assertTrue(self.store.snapshot()['rules'][0]['enabled'])

    def test_invalid_empty_or_control_patterns_and_failed_save_leave_no_changes(self):
        for value in ('','   ','a\nb','\t}',None):
            with self.assertRaises(ValueError): self.store.add(event(),value)
        with patch.object(self.store,'_save',side_effect=OSError('disk full')):
            with self.assertRaises(OSError): self.store.add(event(),'optional record not found')
        self.assertEqual(self.store.snapshot()['rules'],[])

    def test_legacy_rules_keep_literal_contains_matching(self):
        rule=self.store.add(event(),'optional record not found')
        data=self.store.snapshot();data['rules'][0].pop('match')
        write_report(self.store.path,data)
        self.assertTrue(ReviewStore(self.store.path).apply(event()))

    def test_short_expected_event_skips_live_ai(self):
        e=event();e['text']='}';self.store.add(e,'}')
        args=parser().parse_args(['k8s','--offline','--live']);args.review_store=self.store
        session=LiveSession(args);session.emit(e)
        self.assertTrue(session.queue.empty())
        self.assertEqual(session.snapshot()['events'][0]['review']['status'],'expected')

    def test_acknowledgment_is_event_and_report_scoped_and_reversible(self):
        e=event();original=copy.deepcopy(e)
        self.store.acknowledge('one.json',e['id'])
        self.assertFalse(self.store.apply(e,'two.json'))
        self.assertTrue(self.store.apply(e,'one.json'))
        self.assertEqual(e['review']['status'],'acknowledged')
        self.store.acknowledge('one.json',e['id'],False)
        self.assertFalse(self.store.apply(e,'one.json'))
        self.assertEqual(e,original)

    def test_rules_survive_reload_and_skip_live_ai_queue(self):
        e=event();self.store.add(e,'optional record not found')
        args=parser().parse_args(['k8s','--offline','--live'])
        args.review_store=ReviewStore(self.store.path)
        session=LiveSession(args)
        e.pop('importance');e.pop('importance_confidence');session.emit(e)
        self.assertTrue(session.queue.empty())
        report=session.snapshot()
        self.assertEqual(report['summary']['routine'],1)
        self.assertEqual(report['summary']['api_requests'],0)
        self.assertEqual(report['events'][0]['review']['status'],'expected')
        rule=self.store.snapshot()['rules'][0];self.store.set_enabled(rule['id'],False)
        report=session.snapshot()
        self.assertEqual(report['summary']['unknown'],1)
        self.assertEqual(report['summary']['routine'],0)

    def test_live_review_updates_counts_once_and_can_restore(self):
        args=parser().parse_args(['k8s','--offline','--live']);args.review_store=self.store
        session=LiveSession(args);session.record([event()])
        rule=self.store.add(event(),'optional record not found')
        for _ in range(3):
            s=session.snapshot()['summary'];self.assertEqual(s['important'],0);self.assertEqual(s['routine'],1)
        self.store.set_enabled(rule['id'],False)
        s=session.snapshot()['summary'];self.assertEqual(s['important'],1);self.assertEqual(s['routine'],0)

    def test_saved_projection_does_not_mutate_evidence(self):
        e=event();report=build_report([e],[],{},'jev',1,1);original=copy.deepcopy(report)
        self.store.add(e,'optional record not found')
        projected=self.store.project(report,'one.json')
        self.assertEqual(projected['summary']['important'],0)
        self.assertEqual(projected['important_groups'],[])
        self.assertEqual(report,original)

    def test_offline_file_scan_applies_saved_rules(self):
        path=Path(self.temp.name)/'app.log';path.write_text('ERROR expected optional record not found\n')
        args=parser().parse_args(['files',str(path),'--offline']);args.review_store=self.store
        e=parse_stream(io.BytesIO(path.read_bytes()),{'type':'file','path':str(path)},10)[0][0]
        self.store.add(e,'optional record not found')
        report=analyze(args)
        self.assertEqual(report['summary']['routine'],1)
        self.assertTrue(report['events'][0]['baseline']['important'])

    def test_baseline_rules_are_the_rules_that_execute(self):
        self.assertEqual(len(BASELINE_RULES),3)
        self.assertEqual(baseline({'text':'ERROR HTTP/1.1 503 warning'})['signals'],[r['id'] for r in BASELINE_RULES])


class DashboardReviewTests(unittest.TestCase):
    def test_bulk_acknowledge_and_undo_are_report_scoped(self):
        with tempfile.TemporaryDirectory() as temp:
            app=Dashboard(temp);a=event();b=event();b['id']='second'
            report=build_report([a,b],[],{},'jev',1,1)
            for name in ('one.json','two.json'):write_report(Path(temp)/name,report)
            payload={'report_id':'one.json','events':[{'event_id':a['id']},{'event_id':b['id']}]}
            with patch.object(app.reviews,'_save',wraps=app.reviews._save) as save:
                app.review({**payload,'action':'acknowledge'})
                self.assertEqual(save.call_count,1)
            self.assertEqual(app.report('one.json')['summary']['important'],0)
            self.assertEqual(app.report('two.json')['summary']['important'],2)
            app.review({**payload,'action':'unacknowledge'})
            self.assertEqual(app.report('one.json')['summary']['important'],2)

    def test_bulk_expected_uses_trusted_sources_and_rejects_partial_batches(self):
        with tempfile.TemporaryDirectory() as temp:
            app=Dashboard(temp);a=event();b=event();b['id']='brace';b['text']='}'
            write_report(Path(temp)/'one.json',build_report([a,b],[],{},'jev',1,1))
            payload={'report_id':'one.json','action':'expected','events':[
                {'event_id':a['id'],'pattern':'optional record not found','source':{'namespace':'untrusted'}},
                {'event_id':b['id'],'pattern':'}'}]}
            bad=copy.deepcopy(payload);bad['events'][1]['event_id']='evicted'
            with self.assertRaises(ValueError):app.review(bad)
            self.assertFalse(app.reviews.path.exists())
            bad=copy.deepcopy(payload);bad['events'][1]['pattern']='not present'
            with self.assertRaises(ValueError):app.review(bad)
            self.assertFalse(app.reviews.path.exists())
            app.review(payload)
            self.assertEqual(app.report('one.json')['summary']['important'],0)
            rules=app.reviews.snapshot()['rules']
            self.assertEqual(len(rules),2)
            self.assertTrue(all(r['scope']['namespace']=='demo' for r in rules))
            self.assertEqual(rules[1]['match'],'exact')

    def test_large_group_selection_reviews_every_instance_atomically(self):
        with tempfile.TemporaryDirectory() as temp:
            app = Dashboard(temp)
            data = [{**event(), 'id':f'instance-{i}'} for i in range(85)]
            write_report(Path(temp)/'group.json', build_report(data, [], {}, 'jev', 1, 1))
            payload = {'report_id':'group.json', 'events':[{'event_id':e['id']} for e in data]}
            with patch.object(app.reviews, '_save', wraps=app.reviews._save) as save:
                app.review({**payload, 'action':'acknowledge'})
                self.assertEqual(save.call_count, 1)
            reviewed = app.report('group.json')
            self.assertTrue(all(e['review']['status'] == 'acknowledged' for e in reviewed['events']))
            app.review({**payload, 'action':'unacknowledge'})
            self.assertTrue(all('review' not in e for e in app.report('group.json')['events']))
            with self.assertRaises(ValueError):
                app.review({**payload, 'action':'acknowledge', 'events':payload['events']+[{'event_id':'missing'}]})
            self.assertFalse(app.reviews.snapshot()['acknowledged']['group.json'])
            expected = {'report_id':'group.json', 'action':'expected',
                        'events':[{'event_id':data[0]['id'], 'pattern':data[0]['text']}],
                        'selected_event_ids':[e['id'] for e in data]}
            with self.assertRaises(ValueError):
                app.review({**expected, 'selected_event_ids':expected['selected_event_ids']+['missing']})
            self.assertFalse(app.reviews.snapshot()['rules'])
            app.review(expected)
            self.assertEqual(len(app.reviews.snapshot()['rules']), 1)
            self.assertTrue(all(e['review']['status'] == 'expected' for e in app.report('group.json')['events']))

    def test_bulk_request_limits_and_ambiguous_sources(self):
        with tempfile.TemporaryDirectory() as temp:
            app=Dashboard(temp)
            for entries in ([],None,'invalid',[None],[{'event_id':False}],
                            [{'event_id':str(i)} for i in range(100001)],
                            [{'event_id':'same'},{'event_id':'same'}]):
                with self.assertRaises(ValueError):
                    app.review({'action':'acknowledge','report_id':'one.json','events':entries})
            with self.assertRaises(ValueError):
                app.review({'action':'acknowledge','report_id':'one.json','live_id':'live','events':[{'event_id':'one'}]})
            self.assertFalse(app.reviews.path.exists())

    def test_bulk_live_snapshot_once_and_changed_session_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            app=Dashboard(temp);a=event();b=event();b['id']='second'
            args=parser().parse_args(['k8s','--offline','--live'])
            args.review_store=app.reviews;args.review_key='live.json'
            session=LiveSession(args);session.record([a,b]);app.live_session=session;app.job={'id':'live'}
            payload={'live_id':'live','action':'acknowledge','events':[{'event_id':a['id']},{'event_id':b['id']}]}
            with patch.object(session,'snapshot',wraps=session.snapshot) as snapshot:
                app.review(payload);self.assertEqual(snapshot.call_count,1)
            self.assertEqual(session.snapshot()['summary']['important'],0)
            before=app.reviews.snapshot()
            with self.assertRaises(ValueError):app.review({**payload,'live_id':'previous'})
            self.assertEqual(app.reviews.snapshot(),before)

    def test_event_lookup_is_server_side_and_pinned_to_report(self):
        with tempfile.TemporaryDirectory() as temp:
            app=Dashboard(temp);e=event();write_report(Path(temp)/'test.json',build_report([e],[],{},'jev',1,1))
            payload={'report_id':'test.json','event_id':e['id'],'window_seconds':120,'source':{'context':'untrusted'}}
            with patch('jevernetes.dashboard.fetch_context',return_value={'events':[]}) as fetch:
                app.context(payload)
                self.assertEqual(fetch.call_args.args[0]['source']['context'],'test-cluster')
            app.review({**payload,'action':'expected','pattern':'optional record not found'})
            self.assertEqual(app.report('test.json')['summary']['important'],0)
            self.assertEqual(len(app.reports()),1)
            self.assertEqual(app.reports()[0]['summary']['important'],0)
            with self.assertRaises(ValueError):app.context({**payload,'event_id':'unknown'})
            with self.assertRaises(ValueError):app.context({**payload,'live_id':'another-session'})
            with self.assertRaises(ValueError):app.context({**payload,'report_id':'../outside.json'})
