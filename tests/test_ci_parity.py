"""Local-only reproduction of checkout history needed by the parity gate."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class CheckoutParityTests(unittest.TestCase):
    def test_full_history_resolves_fixed_base(self):
        root = Path(__file__).resolve().parents[1]
        workflow = (root / '.github/workflows/ci.yml').read_text()
        go_job = workflow.split('  go:', 1)[1].split('  rust:', 1)[0]
        checkout = go_job.split('uses: actions/checkout@', 1)[1].split('- uses:', 1)[0]
        self.assertRegex(checkout, r'(?m)^\s+fetch-depth: 0\s*$')
        with tempfile.TemporaryDirectory() as directory:
            source, clone = Path(directory) / 'source', Path(directory) / 'clone'
            source.mkdir()

            def git(*args, cwd=source, check=True):
                return subprocess.run(['git', *args], cwd=cwd, check=check,
                                      text=True, capture_output=True)

            git('init', '-b', 'fixture')
            git('config', 'user.name', 'Hermetic Fixture')
            git('config', 'user.email', 'fixture@example.invalid')
            (source / 'src').mkdir()
            (source / 'src/retained.rs').write_text('// retained companion\n')
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-m', 'base')
            base = git('rev-parse', 'HEAD').stdout.strip()
            (source / 'tests').mkdir()
            (source / 'tests/parity.json').write_text(json.dumps({
                'base': base, 'retirement_allowed': False, 'workflows': []}))
            shutil.copy(root / 'tools/check_parity.py', source / 'check_parity.py')
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-m', 'head')
            git('clone', '--depth=1', source.as_uri(), str(clone))

            def parity():
                return subprocess.run([sys.executable, 'check_parity.py'], cwd=clone,
                                      text=True, capture_output=True)

            self.assertNotEqual(parity().returncode, 0, 'shallow reproduction failed')
            git('fetch', '--unshallow', cwd=clone)
            result = parity()
            self.assertEqual(result.returncode, 0, result.stderr)
            (clone / 'src/retained.rs').unlink()
            self.assertNotEqual(parity().returncode, 0, 'retirement gate was bypassed')
