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

    def add(self, event, pattern, scope_mode='workload'):
        if not isinstance(pattern, str) or not 8 <= len(pattern.strip()) <= 500 or any(ord(c) < 32 for c in pattern):
            raise ValueError('Use a literal message pattern between 8 and 500 characters')
        pattern = pattern.strip()
        if pattern not in event['text']:
            raise ValueError('The pattern must occur in the selected event')
        if scope_mode not in ('workload', 'source'):
            raise ValueError('Choose workload or exact source scope')
        source = event['source']
        keys = ('type', 'context', 'namespace', 'container') if source.get('type') == 'kubernetes' else ('type', 'path')
        scope = {key: source[key] for key in keys if key in source}
        if source.get('type') == 'kubernetes' and scope_mode == 'source':
            scope['pod'] = source['pod']
        with self.lock:
            self._load()
            for rule in self.data['rules']:
                if rule['pattern'] == pattern and rule['scope'] == scope:
                    rule['enabled'] = True
                    self._save()
                    return copy.deepcopy(rule)
            rule = {'id': secrets.token_hex(8), 'pattern': pattern, 'scope': scope, 'enabled': True}
            self.data['rules'].append(rule)
            self._save()
            return copy.deepcopy(rule)

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
        with self.lock:
            self._load()
            ids = set(self.data['acknowledged'].get(key, []))
            if enabled:
                ids.add(event_id)
            else:
                ids.discard(event_id)
            self.data['acknowledged'][key] = sorted(ids)
            self._save()

    def apply(self, event, key=''):
        with self.lock:
            self._load()
            if 'original_judgment' in event:
                for name in JUDGMENT_KEYS:
                    event.pop(name, None)
                event.update(event.pop('original_judgment'))
            event.pop('review', None)
            rule = next((r for r in self.data['rules'] if r.get('enabled') and r['pattern'] in event['text']
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
