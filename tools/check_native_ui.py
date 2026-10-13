"""Run the retained browser acceptance fixtures against the Go embedded assets."""
from pathlib import Path
import subprocess
for name in ('context', 'prompt', 'review', 'grouping', 'search'):
    code = Path(f'tests/test_{name}_ui.cjs').read_text().replace('jevernetes/web/', 'internal/investigation/web/')
    subprocess.run(['node', '-e', code], check=True)
