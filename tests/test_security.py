import gzip
import io
import unittest

from jev_log_analyzer.dashboard import scan_args
from jev_log_analyzer.events import parse_stream, redact
from jev_log_analyzer.live import Grouper


class SecurityRegressionTests(unittest.TestCase):
    def test_embedded_json_escaped_credentials_are_redacted(self):
        line = r'INFO request {"password":"first\" second", "access_key_id":"test-access-value", "private_key":"test-private-value"}'
        safe = redact(line)
        for value in ('first', 'second', 'test-access-value', 'test-private-value'):
            self.assertNotIn(value, safe)

    def test_multiline_private_key_removed_from_files_and_live(self):
        lines = ['INFO before', '-----BEGIN PRIVATE KEY-----', 'synthetic-private-material', '-----END PRIVATE KEY-----', 'INFO after']
        raw = ('\n'.join(lines)+'\n').encode()
        events, _ = parse_stream(io.BytesIO(raw), {}, 100)
        self.assertNotIn('synthetic-private-material', str(events))
        result = []
        grouper = Grouper({}, result.append)
        for line in lines:
            grouper.feed(('2026-09-20T12:00:00Z '+line+'\n').encode())
        grouper.flush()
        self.assertNotIn('synthetic-private-material', str(result))
        self.assertIn('INFO after', str(result))

    def test_decompressed_byte_limit_including_blank_and_oversized_lines(self):
        for content in (b'\n'*10000, b'x'*10000, b'INFO ready\n'*1000):
            raw = gzip.compress(content)
            with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
                _, warnings = parse_stream(stream, {}, 10000, max_bytes=100)
                self.assertLessEqual(stream.tell(),101)
            self.assertTrue(any('byte limit' in w for w in warnings))
        events, warnings = parse_stream(io.BytesIO(b'one\n'),{},10,max_bytes=4)
        self.assertFalse(warnings)
        self.assertEqual(len(events),1)

    def test_leading_space_cannot_disguise_kubectl_options(self):
        for key in ('context','namespace','selector'):
            with self.subTest(key=key), self.assertRaises(ValueError):
                scan_args({'kind':'kubernetes','offline':True,key:'  --server=example.invalid'})
