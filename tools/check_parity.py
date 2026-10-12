"""Executable acceptance inventory. --run executes each unique hermetic suite."""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import stat
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
# Compare raw git blobs and git file modes, independently of the index and
# core.fileMode. New tests may be added; reviewed repairs pin replacement bytes.
packages = {'Cargo.toml', 'Cargo.lock', 'pyproject.toml', 'MANIFEST.in',
            'requirements-dev.txt'}
# The historical workflow base remains above; retained_base pins the exact
# accepted coexistence tree, including its package/test adaptations and additions.
base = subprocess.check_output(['git', 'ls-tree', '-rz', inventory.get('retained_base', inventory['base'])])
# Explicit repairs retain the original blob identity and pin the reviewed new
# bytes. No path is excluded from mode, symlink, existence, or content checks.
repairs = inventory.get('retained_repairs', {})
seen_repairs = set()
retained = 0
for entry in base.split(b'\0'):
    if not entry:
        continue
    metadata, raw_name = entry.split(b'\t', 1)
    mode, kind, oid = metadata.split()
    name = raw_name.decode()
    if not (name.startswith(('src/', 'jevernetes/', 'tests/')) or name in packages):
        continue
    if name == 'tests/parity.json':
        continue  # This gate's evolving inventory, not a retained runtime asset.
    path = Path(name)
    assert path.exists() and not path.is_symlink(), f"Retained asset missing or aliased: {name}"
    info = path.lstat()
    expected_mode = int(mode, 8) & 0o7777
    assert kind == b'blob' and stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == expected_mode, f"Retained asset mode changed: {name}"
    expected = subprocess.check_output(['git', 'cat-file', 'blob', oid.decode()])
    if name in repairs:
        repair = repairs[name]
        assert repair['base_blob'] == oid.decode(), f"Repair base changed: {name}"
        assert hashlib.sha256(path.read_bytes()).hexdigest() == repair['sha256'], f"Retained repair bytes changed: {name}"
        seen_repairs.add(name)
    else:
        assert path.read_bytes() == expected, f"Retained asset bytes changed: {name}"
    retained += 1
assert seen_repairs == set(repairs), "Repair does not name a retained asset"
if args.run:
    for command in commands:
        print(command, flush=True)
        subprocess.run(shlex.split(command), check=True)
print(f"Parity inventory: {len(inventory['workflows'])} workflows; {len(commands)} executable suites; {retained} retained assets byte/mode verified; retirement disabled")
