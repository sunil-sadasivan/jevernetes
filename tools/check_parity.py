"""Executable acceptance inventory. --run executes each unique hermetic suite."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--run', action='store_true')
args = p.parse_args()
inventory = json.loads(Path('tests/parity.json').read_text())
assert inventory['retirement_allowed'] is False
commands = []
for row in inventory['workflows']:
    assert row['status'] in ('exact', 'equivalent', 'coexistence', 'unverified')
    assert row['commands'] or row['status'] == 'unverified'
    for asset in row['assets']:
        assert Path(asset).exists(), f"Inactive parity asset: {asset}"
    for command in row['commands']:
        if command not in commands:
            commands.append(command)
# The base's runnable Python/Rust modules and tests cannot disappear during coexistence.
base = subprocess.check_output(['git', 'ls-tree', '-r', '--name-only', inventory['base']], text=True)
for name in base.splitlines():
    if name.startswith(('src/', 'jevernetes/', 'tests/')):
        assert Path(name).is_file(), f"Retired before replacement acceptance: {name}"
if args.run:
    for command in commands:
        print(command, flush=True)
        subprocess.run(shlex.split(command), check=True)
print(f"Parity inventory: {len(inventory['workflows'])} workflows; {len(commands)} executable suites; retirement disabled")
