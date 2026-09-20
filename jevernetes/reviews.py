"""Local, explicit review decisions. Expected patterns are literal, never executable."""
import copy
import json
from pathlib import Path
import secrets
import threading

from .report import write_report

JUDGMENT_KEYS = ('importance', 'severity', 'category', 'importance_confidence', 'severity_confidence', 'category_confidence', 'analysis_error')


class ReviewStore:
    def __init__(self, path):
        self.path = Path(path)
        self.lock = threading.RLock()
        self.stamp = None
        self.data = {'version': 1, 'rules': [], 'acknowledged': {}}

    def _load(self):
        try:
            stamp = self.path.stat().st_mtime_ns
        except FileNotFoundError:
            self.data = {'version': 1, 'rules': [], 'acknowledged': {}}
            self.stamp = None
            return
        if stamp != self.stamp:
            try:
                data = json.loads(self.path.read_text())
                if data.get('version') != 1 or not isinstance(data.get('rules'), list) or not isinstance(data.get('acknowledged'), dict):
                    raise ValueError()
                self.data, self.stamp = data, stamp
            except (ValueError, AttributeError):
                raise ValueError('Cannot read local review rules; repair or remove the review rules file') from None

    def snapshot(self):
        with self.lock:
            self._load()
            return copy.deepcopy(self.data)

    def _save(self):
        write_report(self.path, self.data)
        self.stamp = self.path.stat().st_mtime_ns

    @staticmethod
    def expected_rule(event, pattern, scope_mode):
        if not isinstance(pattern, str) or not 1 <= len(pattern.strip()) <= 500 or any(ord(c) < 32 for c in pattern):
            raise ValueError('Use a literal message pattern between 1 and 500 characters')
        pattern = pattern.strip()
        match = 'exact' if len(pattern) < 8 else 'contains'
        if match == 'exact' and pattern != event['text'].strip():
            raise ValueError('Patterns shorter than 8 characters must match the entire event')
        if pattern not in event['text']:
            raise ValueError('The pattern must occur in the selected event')
        if scope_mode not in ('workload', 'source'):
            raise ValueError('Choose workload or exact source scope')
        source = event['source']
        keys = ('type', 'context', 'namespace', 'container') if source.get('type') == 'kubernetes' else ('type', 'path')
        scope = {key: source[key] for key in keys if key in source}
        if source.get('type') == 'kubernetes' and scope_mode == 'source':
            scope['pod'] = source['pod']
        return {'pattern': pattern, 'scope': scope, 'match': match}

    def _commit(self, updated):
        previous = self.data
        self.data = updated
        try:
            self._save()
        except OSError:
            self.data = previous
            raise

    def add(self, event, pattern, scope_mode='workload'):
        return self.add_many([(event, pattern)], scope_mode)[0]

    def add_many(self, entries, scope_mode='workload'):
        # Validate every rule before saving any of them.
        candidates = [self.expected_rule(event, pattern, scope_mode) for event, pattern in entries]
        with self.lock:
            self._load()
            updated = copy.deepcopy(self.data)
            saved = []
            for candidate in candidates:
                rule = next((r for r in updated['rules'] if r['pattern'] == candidate['pattern']
                             and r['scope'] == candidate['scope'] and r.get('match', 'contains') == candidate['match']), None)
                if rule is None:
                    rule = {'id': secrets.token_hex(8), **candidate}
                    updated['rules'].append(rule)
                rule['enabled'] = True
                saved.append(rule)
            self._commit(updated)
            return copy.deepcopy(saved)

    def set_enabled(self, rule_id, enabled):
        if type(enabled) is not bool:
            raise ValueError('Expected enabled to be true or false')
        with self.lock:
            self._load()
            for rule in self.data['rules']:
                if rule['id'] == rule_id:
                    rule['enabled'] = enabled
                    self._save()
                    return
            raise ValueError('Expected-event rule not found')

    def acknowledge(self, key, event_id, enabled=True):
        self.acknowledge_many(key, [event_id], enabled)

    def acknowledge_many(self, key, event_ids, enabled=True):
        with self.lock:
            self._load()
            updated = copy.deepcopy(self.data)
            ids = set(updated['acknowledged'].get(key, []))
            if enabled:
                ids.update(event_ids)
            else:
                ids.difference_update(event_ids)
            updated['acknowledged'][key] = sorted(ids)
            self._commit(updated)

    @staticmethod
    def matches(rule, text):
        if rule.get('match', 'contains') == 'exact':
            return text.strip() == rule['pattern']
        return rule['pattern'] in text

    def apply(self, event, key=''):
        with self.lock:
            self._load()
            if 'original_judgment' in event:
                for name in JUDGMENT_KEYS:
                    event.pop(name, None)
                event.update(event.pop('original_judgment'))
            event.pop('review', None)
            rule = next((r for r in self.data['rules'] if r.get('enabled') and self.matches(r, event['text'])
                         and all(event['source'].get(k) == v for k, v in r['scope'].items())), None)
            acknowledged = event['id'] in self.data['acknowledged'].get(key, [])
            if not rule and not acknowledged:
                return False
            event['original_judgment'] = {k: event[k] for k in JUDGMENT_KEYS if k in event}
            if event['original_judgment'].get('importance') in (None, 'pending'):
                event['original_judgment'] = {'importance': 'unknown', 'severity': 'unknown', 'category': 'unknown', 'analysis_error': 'AI analysis was skipped by a local expected-event rule'}
            event['importance'] = 'routine'
            for name in ('importance_confidence', 'severity_confidence', 'category_confidence', 'analysis_error'):
                event.pop(name, None)
            event['review'] = {'status': 'expected' if rule else 'acknowledged'}
            if rule:
                event['review']['rule_id'] = rule['id']
            return True

    def project(self, report, key):
        report = copy.deepcopy(report)
        for event in report.get('events', []):
            before = event.get('importance', 'unknown')
            self.apply(event, key)
            after = event.get('importance', 'unknown')
            if before != after:
                report['summary'][before] = max(0, report['summary'].get(before, 0) - 1)
                report['summary'][after] = report['summary'].get(after, 0) + 1
        for event in report.get('tail_events', []):
            self.apply(event, key)
        from .report import build_report
        report['important_groups'] = build_report(report['events'], [], {}, report.get('mode'), 0, 0)['important_groups']
        return report
