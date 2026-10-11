"""Run the companion suites against an installed wheel outside the source tree."""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('wheel', type=Path)
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
wheel = args.wheel.resolve()
with zipfile.ZipFile(wheel) as archive:
    for path in (root / 'jevernetes' / 'web').glob('*'):
        if path.is_file():
            assert archive.read('jevernetes/web/' + path.name) == path.read_bytes(), path.name
with tempfile.TemporaryDirectory(prefix='jevernetes-package-') as temporary:
    directory = Path(temporary)
    environment = dict(os.environ, PIP_CONFIG_FILE=os.devnull, PIP_NO_INDEX='1')
    environment.pop('PYTHONPATH', None)
    subprocess.run([sys.executable, '-m', 'venv', str(directory / 'venv')], check=True)
    python = directory / 'venv' / 'bin' / 'python'
    subprocess.run([str(python), '-m', 'pip', 'install', '--no-index', '--no-deps', str(wheel)],
                   cwd=directory, env=environment, check=True)
    subprocess.run([str(python), '-c', 'import jevernetes; assert "site-packages" in jevernetes.__file__'],
                   cwd=directory, env=environment, check=True)
    subprocess.run([str(directory / 'venv' / 'bin' / 'jevernetes-legacy'), '--help'],
                   cwd=directory, env=environment, check=True, stdout=subprocess.DEVNULL)
    subprocess.run([str(python), '-m', 'unittest', 'discover', '-s', str(root / 'tests'), '-q'],
                   cwd=directory, env=environment, check=True)
print('Installed companion package, console entry point, browser assets and all Python acceptance tests passed')
