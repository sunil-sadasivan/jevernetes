import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from jevernetes.migrate import MAX_BYTES, import_reviews


class MigrationBoundsTests(unittest.TestCase):
    def test_600000_fixed_width_acknowledgements_rerun(self):
        value = {'version': 1, 'rules': [], 'acknowledged': {
            'report': [f'ack-{i:016d}' for i in range(600000)]}}
        raw = json.dumps(value, separators=(',', ':')).encode()
        self.assertLess(len(raw), MAX_BYTES)
        self.assertGreater(len(json.dumps(value, indent=2).encode()), MAX_BYTES)
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / 'source', Path(directory) / 'destination'
            source.write_bytes(raw)
            before = source.stat()
            self.assertTrue(import_reviews(source, destination))
            published = destination.read_bytes()
            self.assertLessEqual(len(published), MAX_BYTES)
            stamp = destination.stat()
            self.assertFalse(import_reviews(source, destination))
            rerun = destination.stat()
            for field in ('st_ino', 'st_mtime_ns', 'st_size', 'st_mode'):
                self.assertEqual(getattr(rerun, field), getattr(stamp, field))
            self.assertEqual(json.loads(published), value)
            self.assertEqual(stamp.st_mode & 0o777, 0o600)
            self.assertEqual(source.read_bytes(), raw)
            self.assertEqual(source.stat().st_mtime_ns, before.st_mtime_ns)
            self.assertEqual(source.stat().st_ino, before.st_ino)
            self.assertEqual(list(Path(directory).glob('.review-import-*')), [])

    def test_serialization_expansion_rejected_before_publication(self):
        # Extension metadata is preserved exactly; number normalization can grow
        # even a compact encoding. Exercise the actual byte boundary cheaply.
        raw = b'{"version":1,"rules":[],"acknowledged":{},"extension":1e9}'
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / 'source', Path(directory) / 'destination'
            source.write_bytes(raw)
            with patch('jevernetes.migrate.MAX_BYTES', len(raw)):
                with self.assertRaises(ValueError):
                    import_reviews(source, destination)
            self.assertFalse(destination.exists())
            self.assertEqual(source.read_bytes(), raw)
            self.assertEqual(list(Path(directory).glob('.review-import-*')), [])

    def test_nonfinite_extension_cannot_publish_unreadable_json(self):
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / 'source', Path(directory) / 'destination'
            raw = b'{"version":1,"rules":[],"acknowledged":{},"extension":1e999}'
            source.write_bytes(raw)
            with self.assertRaises(ValueError):
                import_reviews(source, destination)
            self.assertFalse(destination.exists())
            self.assertEqual(source.read_bytes(), raw)

    def test_no_clobber_race_with_bounded_output(self):
        import os
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / 'source', Path(directory) / 'destination'
            raw = b'{"version":1,"rules":[],"acknowledged":{}}'
            source.write_bytes(raw)
            link = os.link
            for winner in [raw, b'{"version":1,"rules":[],"acknowledged":{"other":["id"]}}']:
                def race(temporary, target, **kwargs):
                    destination.write_bytes(winner)
                    return link(temporary, target, **kwargs)
                with patch('jevernetes.migrate.os.link', side_effect=race):
                    if winner == raw:
                        self.assertFalse(import_reviews(source, destination))
                    else:
                        with self.assertRaises(ValueError):
                            import_reviews(source, destination)
                self.assertEqual(destination.read_bytes(), winner)
                self.assertEqual(source.read_bytes(), raw)
                self.assertEqual(list(Path(directory).glob('.review-import-*')), [])
                destination.unlink()
