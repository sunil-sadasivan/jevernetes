"""Regression checks for native-only source packaging; no application Python imports."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from tools.check_parity import RETIRED, check


class NativeReleaseTests(unittest.TestCase):
    def fixture(self, root):
        (root / 'tests').mkdir()
        contracts = {}
        for name in ('context', 'dashboard', 'grouping', 'prompt', 'review', 'search'):
            path = f'tests/test_{name}_ui.cjs'
            (root / path).write_text('// browser contract\n')
            contracts[path] = hashlib.sha256((root / path).read_bytes()).hexdigest()
        inventory = {'runtime': 'go', 'retirement_allowed': True, 'browser_contracts': contracts,
                     'workflows': [{'status': 'equivalent', 'assets': list(contracts), 'commands': ['go test ./...']}]}
        (root / 'tests/parity.json').write_text(json.dumps(inventory))

    def test_retired_sources_rejected_and_native_binary_allowed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            (root / 'jevernetes').write_bytes(b'native build placeholder')
            check(root)
            (root / 'jevernetes').unlink()
            for name in ('jevernetes', *RETIRED):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                if name == 'jevernetes':
                    path.mkdir()
                else:
                    path.write_text('retired source')
                with self.assertRaises(AssertionError):
                    check(root)
                path.rmdir() if path.is_dir() else path.unlink()

    def test_browser_contract_mutation_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            (root / 'tests/test_search_ui.cjs').write_text('')
            with self.assertRaises(AssertionError):
                check(root)
