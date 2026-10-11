import io
import copy
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

from jevernetes.dashboard import Dashboard
from jevernetes.events import parse_stream
from jevernetes.jev import Jev, decode_search, search_request
from jevernetes.report import build_report, write_report
from jevernetes.search import SearchCache, SearchRun, validate_options


def events(text):
    data=parse_stream(io.BytesIO(text.encode()),{'type':'file','path':'synthetic.log'},100000)[0]
    for e in data: e.update(importance='routine',severity='info',category='routine')
    return data


class FakeJev:
    calls=[]
    def __init__(self, key, usage, stop_event):
        self.usage,self.stop_event=usage,stop_event
    def search(self, data, query):
        self.calls.append((data,query))
        self.usage.begin()
        self.usage.record(json.dumps({'usage':{'input_tokens':100,'output_tokens':10}}))
        return [{'relevance':'match' if 'pool exhausted' in e['text'] else 'unrelated','confidence':.95} for e in data]


class SearchTests(unittest.TestCase):
    def setUp(self):
        FakeJev.calls=[]

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_ip_questions_default_to_jev_semantic_search(self,_):
        query=SearchRun(events('GET / ip=192.0.2.1\n'),'192.0.2.1',client_factory=FakeJev)
        query.run()
        self.assertEqual(query.snapshot()['mode'],'jev')
        self.assertEqual(query.snapshot()['usage']['request_attempts'],1)
        self.assertEqual(FakeJev.calls[0][1],'192.0.2.1')
        for mode in ('auto','ip'):
            with self.assertRaises(ValueError):SearchRun([], '192.0.2.1', mode=mode)

    def test_literal_search_includes_routine_and_reports_no_matches(self):
        query=SearchRun(events('INFO successful DB connection\nINFO ready\n'),'db connection',mode='literal')
        query.run()
        self.assertEqual(query.snapshot()['matched_groups'],1)
        query=SearchRun(events('INFO ready\n'),'missing',mode='literal')
        query.run()
        self.assertEqual(query.snapshot()['matched_groups'],0)
        self.assertFalse(query.snapshot()['partial'])

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_semantic_search_groups_caches_and_preserves_occurrence_metadata(self,_):
        data=events('ERROR db pool exhausted\nERROR db pool exhausted\nINFO database recovered\n')
        original=json.dumps(data)
        cache=SearchCache()
        query=SearchRun(data,'major issue with db',cache=cache,client_factory=FakeJev)
        query.run()
        result=query.snapshot()
        self.assertEqual(result['matched_groups'],1)
        self.assertEqual(result['matching_instances'],2)
        self.assertEqual(result['examined_groups'],2)
        self.assertEqual(result['usage']['request_attempts'],1)
        self.assertEqual(len(FakeJev.calls[0][0]),2)
        self.assertEqual(FakeJev.calls[0][0][0]['search_occurrences']['count'],2)
        self.assertEqual(json.dumps(data),original)
        repeat=SearchRun(data,'major issue with db',cache=cache,client_factory=FakeJev)
        repeat.run()
        self.assertEqual(repeat.snapshot()['cache_hits'],2)
        self.assertEqual(repeat.snapshot()['usage']['request_attempts'],0)
        changed=SearchRun(data,'recovered db',cache=cache,client_factory=FakeJev)
        changed.run()
        self.assertEqual(changed.snapshot()['cache_hits'],0)

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_budget_shortlist_is_explicitly_partial(self,_):
        data=events(''.join(f'INFO ready {i}\n' for i in range(20))+'ERROR db pool exhausted\n')
        query=SearchRun(data,'major db issue',max_batches=1,client_factory=FakeJev)
        query.run()
        result=query.snapshot()
        self.assertEqual(result['examined_groups'],8)
        self.assertEqual(result['unexamined_groups'],13)
        self.assertTrue(result['partial'])
        self.assertEqual(result['matched_groups'],1)

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_failures_uncertainty_and_truncation_never_become_silent_nonmatches(self,_):
        class Uncertain(FakeJev):
            def search(self,data,query):
                return [{'relevance':'unrelated','confidence':.3} for _ in data]
        data=events('INFO recovered\n')
        search=SearchRun(data,'db failure',client_factory=Uncertain)
        search.run()
        self.assertEqual(search.snapshot()['possible_groups'],1)
        class Failure(FakeJev):
            def search(self,data,query):raise ValueError('Jev HTTP 503')
        cache=SearchCache()
        search=SearchRun(data,'db failure',cache=cache,client_factory=Failure)
        search.run()
        self.assertEqual(search.snapshot()['failed_groups'],1)
        self.assertTrue(search.snapshot()['partial'])
        self.assertFalse(cache.entries)
        data[0]['truncated']=True
        search=SearchRun(data,'db failure',client_factory=FakeJev)
        search.run()
        self.assertEqual(search.snapshot()['possible_groups'],1)

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_cost_limit_and_missing_usage_stop_new_batches(self,_):
        class Expensive(FakeJev):
            def search(self,data,query):
                result=super().search(data,query)
                self.usage.begin();self.usage.record(json.dumps({'usage':{'input_tokens':1000000}}))
                return result
        data=events(''.join(f'INFO ready {i}\n' for i in range(200)))
        search=SearchRun(data,'db failure',client_factory=Expensive)
        search.run()
        self.assertLessEqual(search.snapshot()['examined_groups'],32)
        self.assertTrue(search.snapshot()['partial'])
        class Unmetered(FakeJev):
            def search(self,data,query):
                self.usage.begin();self.usage.record(None)
                return [{'relevance':'unrelated','confidence':.9} for _ in data]
        search=SearchRun(data,'db failure',client_factory=Unmetered)
        search.run()
        self.assertLessEqual(search.snapshot()['examined_groups'],32)
        self.assertFalse(search.snapshot()['usage']['cost_complete'])

    def test_cache_bounds_expiry_and_cancellation(self):
        clock=[0];cache=SearchCache(limit=1,ttl=5,clock=lambda:clock[0])
        cache.put('a',{'relevance':'match'});cache.put('b',{'relevance':'match'})
        self.assertIsNone(cache.get('a'))
        clock[0]=5;self.assertIsNone(cache.get('b'))
        search=SearchRun(events('INFO ready\n'),'ready',mode='literal')
        search.stop();search.run()
        self.assertEqual(search.snapshot()['status'],'cancelled')
        self.assertEqual(search.snapshot()['examined_groups'],0)

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_cancellation_during_requests_preserves_results_and_stops_scheduling(self,_):
        started,release=threading.Event(),threading.Event()
        class Blocking(FakeJev):
            def search(self,data,query):
                started.set()
                if not release.wait(3):raise ValueError('Synthetic timeout')
                return super().search(data,query)
        search=SearchRun(events(''.join(f'ERROR db pool exhausted shard={i}\n' for i in range(80))),
                         'db failure',client_factory=Blocking)
        thread=threading.Thread(target=search.run)
        thread.start()
        try:
            self.assertTrue(started.wait(2))
            search.stop()
        finally:
            release.set();thread.join(3)
        self.assertFalse(thread.is_alive())
        result=search.snapshot()
        self.assertEqual(result['status'],'cancelled')
        self.assertGreater(result['matched_groups'],0)
        self.assertLessEqual(result['examined_groups'],32)
        self.assertTrue(result['partial'])
        with patch('jevernetes.search.time.monotonic',return_value=search.finished+100):
            self.assertEqual(search.snapshot()['elapsed_seconds'],result['elapsed_seconds'])

    def test_query_validation_and_redaction(self):
        for query,mode,budget in [('', 'jev',16),('x'*501,'jev',16),('x\n','jev',16),('x','shell',16),('x','jev',True),('x','jev',65)]:
            with self.assertRaises(ValueError):validate_options(query,mode,budget)
        self.assertNotIn('synthetic-secret',validate_options('find password=synthetic-secret'))
        data=events('INFO ready\n');data[0]['text']='token=synthetic-secret'
        search=SearchRun(data,'token',mode='literal')
        self.assertNotIn('synthetic-secret',str(search.groups))

    def test_typed_payload_and_invalid_responses(self):
        payload=search_request(events('ERROR pool exhausted\n'),'db failure','jev-latest')
        self.assertIn('untrusted',payload['questions']['e0_relevance']['instructions'])
        self.assertEqual(payload['state']['query'],'db failure')
        raw={'answers':{'e0_relevance':{'type':'choice','choice':'match','confidence':.9}}}
        self.assertEqual(decode_search(json.dumps(raw),1)[0]['relevance'],'match')
        for field,value in [('choice','execute'),('confidence',True),('confidence',float('nan')),('confidence',-1),('type','text')]:
            bad=json.loads(json.dumps(raw));bad['answers']['e0_relevance'][field]=value
            with self.assertRaises(ValueError):decode_search(json.dumps(bad),1)
        client=Jev('synthetic')
        raw['usage']={'input_tokens':100,'output_tokens':10}
        with patch('urllib.request.OpenerDirector.open',return_value=io.BytesIO(json.dumps(raw).encode())):
            self.assertEqual(client.search(events('ERROR pool exhausted\n'),'db failure')[0]['relevance'],'match')
        self.assertEqual(client.usage.snapshot()['request_attempts'],1)

    def test_dashboard_resolves_trusted_evidence_and_search_ids(self):
        with tempfile.TemporaryDirectory() as directory:
            app=Dashboard(directory);data=events('INFO ip=192.0.2.1\nINFO other\n')
            write_report(Path(directory)/'sample.json',build_report(data,[],{},'offline-rules',0,0))
            payload={'report_id':'sample.json','event_ids':[e['id'] for e in data],'query':'192.0.2.1','mode':'literal'}
            job=app.start_search(payload)
            deadline=time.monotonic()+2
            while app.search_result(job['id'])['status']=='running' and time.monotonic()<deadline:time.sleep(.01)
            result=app.search_result(job['id'])
            self.assertEqual(result['matched_groups'],1)
            self.assertEqual(result['results'][0]['event_id'],data[0]['id'])
            with self.assertRaises(ValueError):app.search_result('wrong')
            with self.assertRaises(ValueError):app.stop_search({'id':'wrong'})
            with self.assertRaises(ValueError):app.start_search({**payload,'scope':{'namespace':[]}})
            with self.assertRaises(ValueError):app.start_search({**payload,'live_id':'ambiguous'})
            with self.assertRaises(ValueError):app.search_result(job['id'],-1)
            app.search_run.status='running'
            with self.assertRaisesRegex(RuntimeError,'already running'):app.start_search(payload)
            app.stop_search({'id':job['id']})
            self.assertTrue(app.search_run.stop_event.is_set())

    @patch('jevernetes.search.api_key',return_value='synthetic')
    def test_live_search_captures_current_scoped_evidence_after_old_ids_expire(self,_):
        old=events('INFO old request\n')
        current=events('ERROR db pool exhausted\nINFO ready\n')
        for event,namespace in zip(current,('production','other')):
            event['source']={'type':'kubernetes','namespace':namespace,'pod':'api','container':'web'}
        class Session:
            def snapshot(self):return copy.deepcopy({'events':current,'tail_events':[]})
        def classify(client,batch,query):
            client.usage.begin();client.usage.record(json.dumps({'usage':{'input_tokens':10}}))
            return [{'relevance':'match','confidence':.95} for e in batch]
        with tempfile.TemporaryDirectory() as directory, patch('jevernetes.jev.Jev.search',classify):
            app=Dashboard(directory);app.job={'id':'live'};app.live_session=Session()
            payload={'live_id':'live','query':'192.0.2.1','scope':{'namespace':'production'}}
            # Old clients may still send IDs; search uses the current scoped window.
            job=app.start_search({**payload,'event_ids':[old[0]['id']]})
            deadline=time.monotonic()+2
            while app.search_result(job['id'])['status']=='running' and time.monotonic()<deadline:time.sleep(.01)
            result=app.search_result(job['id'])
            self.assertEqual(result['mode'],'jev')
            self.assertEqual(result['total_events'],1)
            self.assertEqual(result['results'][0]['event_id'],current[0]['id'])
            self.assertEqual(job['report']['events'],[current[0]])
            captured=job['report']['events'][0]['text']
            current[0]['text']='changed';current.clear()
            self.assertEqual(job['report']['events'][0]['text'],captured)
            with self.assertRaisesRegex(ValueError,'no longer retained'):
                app.selected_events(payload,[old[0]['id']])
            with self.assertRaisesRegex(ValueError,'No logs are currently retained'):
                app.start_search(payload)
            with self.assertRaises(ValueError):app.start_search({**payload,'live_id':'different'})
