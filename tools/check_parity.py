"""Check the native workflow inventory and ensure retired runtimes cannot ship."""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import subprocess

RETIRED = ('src', 'tests/rust', 'Cargo.toml', 'Cargo.lock', 'pyproject.toml', 'MANIFEST.in', 'requirements-dev.txt')


def check(root=Path('.')):
    inventory = json.loads((root / 'tests/parity.json').read_text())
    assert inventory['runtime'] == 'go' and inventory['retirement_allowed'] is True
    assert not (root / 'jevernetes').is_dir(), 'Retired Python runtime returned'
    for name in RETIRED:
        assert not (root / name).exists(), f'Retired runtime/package returned: {name}'
    commands = []
    for row in inventory['workflows']:
        assert row['status'] in ('exact', 'equivalent', 'unverified')
        assert row['commands'] or row['status'] == 'unverified'
        for asset in row['assets']:
            assert (root / asset).exists(), f'Inactive parity asset: {asset}'
        for command in row['commands']:
            assert 'cargo ' not in command and 'python3 -m jevernetes' not in command
            if command not in commands:
                commands.append(command)
    contracts = inventory['browser_contracts']
    assert len(contracts) == 6, 'All six browser acceptance suites must remain active'
    for name, digest in contracts.items():
        path = root / name
        assert not path.is_symlink() and path.is_file(), f'Missing browser contract: {name}'
        assert hashlib.sha256(path.read_bytes()).hexdigest() == digest, f'Browser contract changed: {name}'
    return inventory, commands


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    args = parser.parse_args()
    inventory, commands = check()
    if args.run:
        for command in commands:
            print(command, flush=True)
            subprocess.run(shlex.split(command), check=True)
    print(f"Native parity inventory: {len(inventory['workflows'])} workflows; {len(commands)} executable suites; six browser contracts verified; Python/Rust runtimes retired")


if __name__ == '__main__':
    main()
