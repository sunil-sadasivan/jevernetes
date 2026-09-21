"""Bounded evidence search: exact local lookup and cached Jev relevance decisions."""
from collections import OrderedDict
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
import copy
import hashlib
import json
import re
import secrets
import threading
import time

from .events import redact
from .grouping import group_id
from .jev import Jev, api_key
from .usage import UsageMeter

STOPWORDS = set('a an the me find show get all logs log lines line events event requests request against from for to with ip address major issue issues any are there what which is in of and'.split())
ALIASES = ({'db','database','postgres','postgresql','mysql','sql','mongo','mongodb','redis'},
           {'error','errors','failure','failed','failing','unavailable','exhausted','deadlock','timeout','timeouts','issue','issues'})


def validate_options(query, mode='jev', max_batches=16):
    if not isinstance(query, str) or not 1 <= len(query.strip()) <= 500 or any(ord(c) < 32 for c in query):
        raise ValueError('Enter a question or literal search between 1 and 500 characters')
    if mode not in ('jev','literal'):
        raise ValueError('Choose Jev semantic search or Exact text')
    if type(max_batches) is not int or not 1 <= max_batches <= 64:
        raise ValueError('Search batch budget must be between 1 and 64')
    return redact(query.strip())


def rank(group, query):
    words = set(re.findall(r'[a-z0-9_]+', query.lower()))
    terms = words - STOPWORDS
    for aliases in ALIASES:
        if words & aliases:
            terms |= aliases
    text = (group['event']['text']+' '+json.dumps(group['event']['source'])).lower()
    return sum(term in text for term in terms)


class SearchCache:
    def __init__(self, limit=2048, ttl=300, clock=time.monotonic):
        self.entries, self.lock = OrderedDict(), threading.Lock()
        self.limit, self.ttl, self.clock = limit, ttl, clock

    def get(self, key):
        with self.lock:
            found = self.entries.get(key)
            if found and self.clock()-found[0] < self.ttl:
                self.entries.move_to_end(key)
                return dict(found[1])
            self.entries.pop(key, None)

    def put(self, key, value):
        with self.lock:
            self.entries[key] = (self.clock(), dict(value))
            self.entries.move_to_end(key)
            while len(self.entries) > self.limit:
                self.entries.popitem(last=False)


class SearchRun:
    def __init__(self, events, query, mode='jev', max_batches=16, cache=None, client_factory=Jev):
        self.query = validate_options(query, mode, max_batches)
        self.mode = mode
        self.id, self.started = secrets.token_hex(12), time.monotonic()
        self.finished = None
        self.stop_event, self.lock = threading.Event(), threading.RLock()
        self.cache, self.client_factory = cache or SearchCache(), client_factory
        self.usage = UsageMeter()
        self.max_batches, self.max_cost = max_batches, .01
        self.status, self.message = 'running', 'Searching the frozen log window…'
        self.results, self.checked, self.failed, self.cache_hits = [], 0, 0, 0
        self.groups = {}
        unique = {event['id']: event for event in events}
        self.total_events = len(unique)
        for event in unique.values():
            event = {**event, 'text':redact(event['text'])}
            key = group_id(event)
            if key not in self.groups:
                self.groups[key] = {'id':key, 'event':copy.deepcopy(event), 'count':0, 'first':None, 'last':None}
            group = self.groups[key]
            group['count'] += 1
            stamp = event.get('timestamp')
            if stamp:
                group['first'] = min(group['first'] or stamp, stamp)
                group['last'] = max(group['last'] or stamp, stamp)
        for group in self.groups.values():
            group['event']['search_occurrences'] = {'count':group['count'], 'first_seen':group['first'], 'last_seen':group['last']}
        self.candidates = len(self.groups)

    def cache_key(self, group):
        return hashlib.sha256(json.dumps(['search-v1', 'jev-latest', self.query, group['id'], group['event']['search_occurrences']],sort_keys=True).encode()).hexdigest()

    def record(self, group, decision, cached=False):
        with self.lock:
            self.checked += 1
            self.cache_hits += cached
            self.failed += decision['relevance'] == 'unknown'
            if decision['relevance'] != 'unrelated':
                self.results.append({'group_id':group['id'], 'event_id':group['event']['id'],
                                     'count':group['count'], **decision, 'cached':cached})

    def snapshot(self, offset=0, limit=20):
        with self.lock:
            ranks = {'match':0,'possible':1,'unknown':2}
            results = sorted(self.results, key=lambda r:(ranks[r['relevance']], -(r.get('confidence') or 0), -r['count'], r['group_id']))
            return {'id':self.id, 'query':self.query, 'mode':self.mode, 'status':self.status, 'message':self.message,
                    'total_events':self.total_events, 'total_groups':len(self.groups), 'candidate_groups':self.candidates,
                    'examined_groups':self.checked, 'unexamined_groups':len(self.groups)-self.checked,
                    'failed_groups':self.failed, 'cache_hits':self.cache_hits,
                    'matched_groups':sum(r['relevance']=='match' for r in results),
                    'possible_groups':sum(r['relevance']=='possible' for r in results),
                    'matching_instances':sum(r['count'] for r in results if r['relevance']=='match'),
                    'partial':self.checked<len(self.groups) or self.failed>0,
                    'usage':self.usage.snapshot(), 'max_cost_usd':self.max_cost,
                    'elapsed_seconds':round((self.finished or time.monotonic())-self.started,2),
                    'result_count':len(results), 'offset':offset, 'results':copy.deepcopy(results[offset:offset+limit])}

    def stop(self):
        self.stop_event.set()

    def run(self):
        try:
            self._run()
        except (ValueError, OSError) as error:
            with self.lock:
                self.status, self.message = 'error', str(error)
        except Exception:
            with self.lock:
                self.status, self.message = 'error', 'Search failed; retry with a narrower query'
        finally:
            with self.lock:
                self.finished = time.monotonic()
                if self.status != 'error':
                    self.status = 'cancelled' if self.stop_event.is_set() else 'complete'
                    if self.stop_event.is_set():
                        self.message = 'Search stopped. Results returned so far are preserved.'
                    elif self.checked < len(self.groups) or self.failed:
                        self.message = 'Partial search: some groups were not evaluated or could not be classified.'
                    else:
                        self.message = 'Finished searching the frozen log window.'

    def _run(self):
        groups = list(self.groups.values())
        if self.mode != 'jev':
            for group in groups:
                if self.stop_event.is_set():
                    break
                text = group['event']['text']
                match = self.query.casefold() in text.casefold()
                self.record(group, {'relevance':'match' if match else 'unrelated', 'confidence':None})
            return
        pending = []
        for group in groups:
            cached = self.cache.get(self.cache_key(group))
            if cached:
                self.record(group,cached,True)
            else:
                pending.append(group)
        pending.sort(key=lambda g:-rank(g,self.query))
        selected = pending[:self.max_batches*8]
        self.candidates = self.checked+len(selected)
        if not selected or self.stop_event.is_set():
            return
        client = self.client_factory(api_key(), usage=self.usage, stop_event=self.stop_event)
        batches = iter([selected[i:i+8] for i in range(0,len(selected),8)])
        def evaluate(batch):
            decisions = client.search([g['event'] for g in batch],self.query)
            if len(decisions) != len(batch):
                raise ValueError('Jev returned an incomplete search batch')
            return decisions
        with ThreadPoolExecutor(max_workers=4) as pool:
            active = {}
            def submit():
                usage = self.usage.snapshot()
                if self.stop_event.is_set() or usage['estimated_cost_usd'] >= self.max_cost or usage['unmetered_requests']:
                    return
                batch = next(batches,None)
                if batch:
                    active[pool.submit(evaluate,batch)] = batch
            for _ in range(4): submit()
            while active:
                done,_ = wait(active,return_when=FIRST_COMPLETED)
                for future in done:
                    batch = active.pop(future)
                    try:
                        decisions = future.result()
                    except (ValueError,OSError) as error:
                        decisions = [{'relevance':'unknown','confidence':None,'error':str(error)} for _ in batch]
                    for group,decision in zip(batch,decisions):
                        # A shaky "unrelated" answer must not silently hide evidence.
                        decision = dict(decision)
                        if decision['relevance'] != 'unknown' and (decision['confidence'] < .7 or group['event'].get('truncated')):
                            decision['relevance'] = 'possible'
                        if decision['relevance'] != 'unknown':
                            self.cache.put(self.cache_key(group),decision)
                        self.record(group,decision)
                    submit()
