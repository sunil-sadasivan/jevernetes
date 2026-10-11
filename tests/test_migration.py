import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from jevernetes.migrate import import_reviews
from jevernetes.reviews import ReviewStore


class ReviewMigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source, self.dest = self.root / 'old.json', self.root / '.review-rules.json'
        self.value = {'version': 1, 'rules': [
            {'id': 'disabled', 'enabled': False, 'pattern': 'expected literal', 'match': 'contains',
             'scope': {'type': 'file', 'path': 'exact-file'}},
            {'id': 'enabled', 'enabled': True, 'pattern': 'expected literal', 'match': 'contains',
             'scope': {'type': 'kubernetes', 'context': 'demo', 'namespace': 'ns', 'pod': 'pod', 'container': 'app'}}],
            'acknowledged': {'original-report.json': ['original-event-id', 'original-event-id']}}
        self.source.write_text(json.dumps(self.value))

    def test_roundtrip_rerun_scope_disabled_acknowledgement_and_reversible_projection(self):
        original = self.source.read_bytes()
        self.assertTrue(import_reviews(self.source, self.dest))
        stamp = self.dest.stat().st_mtime_ns
        self.assertFalse(import_reviews(self.source, self.dest))
        self.assertEqual(self.dest.stat().st_mtime_ns, stamp)
        self.assertEqual(self.dest.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.source.read_bytes(), original)
        store = ReviewStore(self.dest)
        self.assertEqual(store.snapshot(), self.value)
        event = {'id': 'original-event-id', 'text': 'expected literal',
                 'source': {'type': 'file', 'path': 'exact-file'},
                 'importance': 'important', 'severity': 'impact', 'category': 'dependency'}
        baseline = copy.deepcopy(event)
        self.assertFalse(store.apply(event))  # disabled scope stays disabled
        self.assertTrue(store.apply(event, 'original-report.json'))
        self.assertEqual(event['importance'], 'routine')
        store.acknowledge('original-report.json', event['id'], False)
        self.assertFalse(store.apply(event, 'original-report.json'))
        self.assertEqual(event, baseline)
        event['source'] = copy.deepcopy(self.value['rules'][1]['scope'])
        self.assertTrue(store.apply(event))
        event['source']['pod'] = 'replacement'
        self.assertFalse(store.apply(event))  # exact pod scope preserved
        self.assertEqual(event['importance'], 'important')

    def test_conflict_malformed_duplicates_and_symlinks_never_overwrite(self):
        import_reviews(self.source, self.dest)
        original = self.dest.read_bytes()
        for raw in ['{', '{"version":1,"version":1,"rules":[],"acknowledged":{}}',
                    json.dumps({**self.value, 'acknowledged': {'x': 'bad'}}),
                    json.dumps({**self.value, 'rules': [self.value['rules'][0], {**self.value['rules'][0], 'enabled': True}]}),
                    json.dumps({**self.value, 'rules': []})]:
            self.source.write_text(raw)
            with self.assertRaises(ValueError):
                import_reviews(self.source, self.dest)
            self.assertEqual(self.dest.read_bytes(), original)
        link = self.root / 'link'
        link.symlink_to(self.dest)
        with self.assertRaises(OSError):
            import_reviews(link, self.root / 'new')

    def test_interrupted_publish_and_rerun(self):
        with patch('jevernetes.migrate.os.link', side_effect=OSError('interruption')):
            with self.assertRaises(OSError):
                import_reviews(self.source, self.dest)
        self.assertFalse(self.dest.exists())
        self.assertEqual(list(self.root.glob('.review-import-*')), [])
        # A crash leftover is never interpreted as a completed destination.
        (self.root / '.review-import-interrupted').write_text('{')
        self.assertTrue(import_reviews(self.source, self.dest))
        self.assertFalse(import_reviews(self.source, self.dest))
        self.assertEqual(json.loads(self.dest.read_text()), self.value)

    def test_identical_duplicate_rules_preserved(self):
        self.value['rules'].append(copy.deepcopy(self.value['rules'][0]))
        self.source.write_text(json.dumps(self.value))
        self.assertTrue(import_reviews(self.source, self.dest))
        self.assertEqual(json.loads(self.dest.read_text()), self.value)
