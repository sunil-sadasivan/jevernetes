"""Check tracked source and release archives without printing suspected secret values."""
import argparse
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import zipfile

PRIVATE_PARTS = {'.security-venv', '.runs', 'reports', '.kube', '__pycache__', '.venv', 'build', 'dist', 'target', '.git'}
TOKENS = re.compile(rb'(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9_-]{40,})')
PERSONAL_PATH = re.compile(rb'/(?:Users|home)/[A-Za-z0-9_.-]+/')


def check(name, data):
    path = PurePosixPath(name)
    if any(part in PRIVATE_PARTS for part in path.parts):
        raise ValueError(f'Runtime/build directory in release: {name}')
    if path.name.startswith(('.env', 'kubeconfig')) or path.name == '.review-rules.json' or path.suffix in ('.pyc', '.pyo', '.pem', '.key'):
        raise ValueError(f'Private configuration or generated file in release: {name}')
    if TOKENS.search(data) or PERSONAL_PATH.search(data):
        raise ValueError(f'Potential credential or personal filesystem path in: {name}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dist', type=Path)
    args = parser.parse_args()
    tracked = subprocess.check_output(['git', 'ls-files', '-z']).decode().split('\0')
    count = 0
    for name in filter(None, tracked):
        path = Path(name)
        if path.is_symlink():
            raise ValueError(f'Symlink in release source: {name}')
        check(name, path.read_bytes())
        count += 1
    if not count:
        raise ValueError('No tracked source files to check')
    if args.dist:
        archives = list(args.dist.glob('*.whl')) + list(args.dist.glob('*.tar.gz'))
        if len(archives) < 2:
            raise ValueError('Expected both a wheel and source archive')
        for archive in archives:
            if archive.suffix == '.whl':
                with zipfile.ZipFile(archive) as package:
                    for name in package.namelist():
                        check(name, package.read(name))
            else:
                with tarfile.open(archive) as package:
                    for member in package.getmembers():
                        if member.issym() or member.islnk():
                            raise ValueError('Links are not allowed in release archives')
                        if member.isfile():
                            check(member.name, package.extractfile(member).read())
        print(f'Checked {count} tracked files and {len(archives)} release archives')
    else:
        print(f'Checked {count} tracked files')


if __name__ == '__main__':
    main()
