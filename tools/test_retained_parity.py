"""Retained coexistence assets must match the pinned git tree, not just exist."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class RetainedParityTests(unittest.TestCase):
    def test_retained_bytes_and_modes(self):
        root = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=repo)
            git('init', '-b', 'fixture')
            git('config', 'user.name', 'Fixture')
            git('config', 'user.email', 'fixture@example.invalid')
            names = ['src/lib.rs', 'jevernetes/cli.py', 'tests/check.py',
                     'Cargo.toml', 'Cargo.lock', 'pyproject.toml', 'MANIFEST.in',
                     'requirements-dev.txt', 'tests/run.py']
            for name in names:
                path = repo / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'retained bytes\n')
                path.chmod(0o755 if name == 'tests/run.py' else 0o644)
            git('add', '.')
            git('-c', 'commit.gpgsign=false', 'commit', '-m', 'base')
            (repo / 'tests/parity.json').write_text(json.dumps({
                'base': git('rev-parse', 'HEAD').decode().strip(),
                'retirement_allowed': False, 'workflows': []}))
            shutil.copy(root / 'tools/check_parity.py', repo / 'check_parity.py')
            def parity():
                return subprocess.run([sys.executable, 'check_parity.py'], cwd=repo,
                                      capture_output=True, text=True)
            self.assertEqual(parity().returncode, 0)
            for name in names:
                path = repo / name
                mode = path.stat().st_mode & 0o777
                for mutation in ('empty', 'mode', 'permissions', 'symlink', 'missing'):
                    with self.subTest(name=name, mutation=mutation):
                        if mutation == 'empty':
                            path.write_bytes(b'')
                        elif mutation == 'mode':
                            path.chmod(mode ^ 0o111)
                        elif mutation == 'permissions':
                            path.chmod(mode ^ 0o020)
                        elif mutation == 'symlink':
                            target = repo / 'replacement'
                            target.write_bytes(b'retained bytes\n')
                            path.unlink()
                            path.symlink_to(target)
                        else:
                            path.unlink()
                        self.assertNotEqual(parity().returncode, 0,
                                            'retained asset replacement passed')
                        if path.is_symlink():
                            path.unlink()
                        path.write_bytes(b'retained bytes\n')
                        path.chmod(mode)
