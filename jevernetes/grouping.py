"""Exact, source-scoped groups and bounded reuse of successful Jev judgments."""
from collections import OrderedDict
from concurrent.futures import Future
import hashlib
import json
import threading
import time


def group_id(event):
    # Keep numbers, IDs, stack traces and all source metadata intact. Parsed
    # timestamps/line positions are occurrence metadata, not message content.
    identity = [event['source'], event['text'], bool(event.get('truncated'))]
    if event.get('truncated'):
        identity.append(event['id'])  # Missing content cannot establish equality.
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()[:20]


def reusable(result):
    return result.get('importance') in ('important', 'routine', 'uncertain') and not result.get('analysis_error')


class GroupedJudge:
    """Share in-flight work; expire judgments after five minutes, within a session.

    Only verdicts are cached, never events or local review decisions. Completed
    entries use a bounded LRU; in-flight entries are bounded by worker batches.
    """
    def __init__(self, judge, limit=2000, ttl=300, clock=time.monotonic):
        self.judge, self.limit, self.ttl, self.clock = judge, limit, ttl, clock
        self.cache = OrderedDict()
        self.pending = {}
        self.lock = threading.Lock()

    def __call__(self, events):
        owned, references = [], []
        with self.lock:
            now = self.clock()
            for key in list(self.cache):
                if now - self.cache[key][0] >= self.ttl:
                    del self.cache[key]
            for event in events:
                key = group_id(event)
                if key in self.cache:
                    _, result, representative = self.cache[key]
                    self.cache.move_to_end(key)
                    future = Future()
                    future.set_result((result, representative))
                    reused = True
                elif key in self.pending:
                    future = self.pending[key]
                    reused = True
                else:
                    future = self.pending[key] = Future()
                    owned.append((key, event, future))
                    reused = False
                references.append((future, reused))
        if owned:
            try:
                results = self.judge([event for _, event, _ in owned])
                if len(results) != len(owned):
                    raise ValueError('Jev returned an incomplete batch')
            except (ValueError, OSError) as error:
                results = [dict(importance='unknown', analysis_error=str(error)) for _ in owned]
            with self.lock:
                for (key, event, future), result in zip(owned, results):
                    result = dict(result)
                    if reusable(result) and not event.get('truncated'):
                        self.cache[key] = (self.clock(), result, event['id'])
                    future.set_result((result, event['id']))
                    del self.pending[key]
                while len(self.cache) > self.limit:
                    self.cache.popitem(last=False)
        results = []
        for future, reused in references:
            result, representative = future.result()
            results.append({**result, 'analysis_reused': reused and reusable(result),
                            'analysis_representative_id': representative})
        return results
