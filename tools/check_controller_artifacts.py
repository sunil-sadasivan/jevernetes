"""Offline, standard-library checks for controller deployment and build invariants.

This is a targeted static check, not a substitute for cluster-version schema validation
or building operator-selected, digest-pinned base images.
"""
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
objects = json.loads((root / 'deploy/base/resources.json').read_text())['items']
by_kind = {obj['kind']: obj for obj in objects}
deploy = by_kind['Deployment']['spec']
assert deploy['replicas'] == 1 and deploy['strategy']['type'] == 'Recreate'
pod = deploy['template']['spec']
assert pod['securityContext']['runAsNonRoot']
assert pod['securityContext']['seccompProfile']['type'] == 'RuntimeDefault'
container = pod['containers'][0]
assert container['image'] == 'example.invalid/jevernetes:REPLACE_ME'
assert container['securityContext'] == {
    'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
    'capabilities': {'drop': ['ALL']},
}
assert container['args'][-2:] == ['--selector', 'app!=jevernetes']
assert container['resources']['requests'] and container['resources']['limits']
assert container['readinessProbe']['httpGet']['path'] == '/readyz'
assert container['livenessProbe']['httpGet']['path'] == '/livez'
assert by_kind['Role']['rules'] == [
    {'apiGroups': [''], 'resources': ['pods'], 'verbs': ['get', 'list', 'watch']},
    {'apiGroups': [''], 'resources': ['pods/log'], 'verbs': ['get']},
]
assert by_kind['PersistentVolumeClaim']['spec']['accessModes'] == ['ReadWriteOnce']
assert 'Secret' not in by_kind
assert all(e['name'].endswith('_FILE') for e in container['env'])
assert all(e['value'].startswith('/var/run/jevernetes-secrets/') for e in container['env'])
policy = json.loads(by_kind['ConfigMap']['data']['policy.json'])
assert policy['categories'] == ['security', 'fraud'] and policy['min_confidence'] == 0.85
text = (root / 'Dockerfile').read_text()
assert text.count('\nFROM ') == 2
assert 'FROM ${RUST_IMAGE} AS build' in text
assert 'FROM ${RUNTIME_IMAGE} AS runtime' in text
assert 'cargo build --locked --release' in text
assert 'COPY --from=build' in text and 'USER 65532:65532' in text
assert json.loads(text.split('ENTRYPOINT ', 1)[1].strip()) == ['/usr/local/bin/jevernetes']
print('Controller manifests/Dockerfile static invariants passed (no deployment).')
