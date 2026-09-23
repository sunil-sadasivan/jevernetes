# Rust validation

Required checks (CI uses Rust 1.94):

```sh
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --all-features
cargo build --release --locked
printf 'ERROR synthetic failure\nINFO ready\n' | target/release/jevernetes files - --offline --json
```

Rust tests use synthetic inputs and loopback HTTP fixtures, never live Kubernetes or Jev credentials. They cover byte/event bounds, multiline/redaction, typed choices/confidence, safe HTTP failures/retries, usage, exact reuse/expiry, reconnect multiplicity and cursor bounds, queue drops, retention and CLI reports/permissions. See docs/architecture.md for benchmarks and authorized cluster validation still needed. Legacy validation below describes the retained companion only.

The controller adds deterministic checks for SQLite reopen, exclusive ownership, full contract
invalidation, TTL reduction/rescore/pruning, unsafe-verdict rejection, policy thresholds,
review/abstention, cooldown and recurrence, changed-decision replay, atomic outbox rollback,
crash leases, persisted rate limiting, attempts/dead letters, sanitized webhook requests,
redirect rejection and body bounds, state-failure visibility, independent delivery shutdown,
health semantics and CLI validation. Independent-review regressions add database/sidecar
path alias rejection before state creation, schema-1 migration/rollback/version checks,
Review-to-Notify promotion after restart and suppressed replay, and blocked stdout isolation
from SQLite and runtime shutdown. The real-stdout regression uses a child with an undrained
pipe; deterministic injected writers cover blocked write/flush, timeout, cancellation, busy
retries, dead letters and recovery without modifying process-global stdout. Existing queue,
provider and collector tests remain enabled.

```sh
cargo test --all-features controller::tests::offline_controller_smoke_and_shutdown
python3 tools/check_controller_artifacts.py
kubectl kustomize deploy/base > /tmp/controller-manifests.yaml
python3 -m unittest discover -s tests -v
node tests/test_context_ui.cjs
node tests/test_prompt_ui.cjs
node tests/test_review_ui.cjs
node tests/test_grouping_ui.cjs
node tests/test_search_ui.cjs
python3 tools/check_release.py
git diff --check
```

The offline controller smoke seeds a synthetic typed verdict, feeds redacted events through
the real bounded analysis lane, verifies persistent reuse and novelty/cooldown suppression,
delivers to a local synthetic webhook, checks the durable delivery checkpoint and cancels
idle workers. It makes no Kubernetes or Jev request and loads no credentials. Other tests use
synthetic loopback provider/Kubernetes responses. A sandbox must permit local port binding.

Artifact checks use Python's standard JSON parser plus explicit RBAC/security/storage/build
invariants. Local Kustomize rendering checks resource composition. These are not live API
schema/admission checks or proof an image runs on the target cluster. The Dockerfile requires
operator-selected base-image digests and was not used to publish an image. Production-volume
power-loss testing, actual CPU/RSS/load measurements, real detection accuracy/calibration,
live-cluster checks and fleet scaling remain deferred. See [controller limits](docs/controller.md).

Original controller-commit verification passed formatting, Clippy with warnings denied,
50 Rust tests (43 library + 7 CLI), release compilation, the offline controller/webhook smoke,
the release-binary offline/redaction smoke, 117 Python tests, five JavaScript helper suites,
JavaScript syntax checks, controller artifact checks, local Kustomize rendering and staged
private-data/artifact review. No image build, live Kubernetes/provider evaluation, push or
deployment was performed.

Independent-review fix verification (2026-09-23): all three focused defect regressions
failed before the fixes and passed afterward. The final matrix passed `cargo fmt --check`,
`cargo clippy --all-targets --all-features -- -D warnings`, `cargo test --all-features`
(64 tests: 54 library + 10 CLI), `cargo build --release --locked`, controller artifact checks,
local `kubectl kustomize deploy/base`, release-content checks, release-binary offline/redaction
smoke and `git diff --check`. Cargo used cached dependencies with no live service access;
loopback fixture tests required execution outside the port-binding-restricted sandbox.
Legacy implementation files were unchanged, so legacy suites were not rerun for this fix.
No push, PR, deployment or credential access was performed.

# Legacy validation

Run the automated checks from the project directory:

```sh
python3 -m unittest discover -s tests -v
python3 -m compileall -q jevernetes
node --check jevernetes/web/app.js
node tests/test_context_ui.cjs
node tests/test_prompt_ui.cjs
node tests/test_review_ui.cjs
```

The Python suite covers parsing, multiline grouping, redaction, bounded collection, model response validation, retry and token accounting, Kubernetes inventory errors, live-stream reconnection and cleanup, pending classifications, queue overflow, report persistence, and dashboard HTTP access controls. Kubernetes and provider behavior use synthetic fixtures or mocks; these tests do not establish connectivity to a real cluster.

Bulk review checks cover atomic validation and persistence, scoped acknowledgments, exact matching for short messages, legacy rules, and live-session changes. Browser checks with a synthetic local backend verified selection across filters, bulk acknowledgment and expected rules, single-event saves, and stable layout during delayed live polling. Pattern suggestion checks cover multiline structured messages and standalone braces.

Context and review regression checks cover time windows, redaction, pod replacement, previous-container selection, restart races, literal scope matching, acknowledgment isolation, rule persistence, restoration of original judgments, skipping AI for expected events, and authorization on the new endpoints. JavaScript helper checks cover source filtering, routine neighbors, file windows, instance separation and selected-event preservation. The theme defaults to dark and uses browser-local preference storage.

## Reproducible local checks

The included `examples/mixed.log` is synthetic. Analyze it without network requests:

```sh
python3 -m jevernetes files examples/mixed.log --offline
```

For a semantic analysis check, configure your own `TYPESAFE_API_KEY` and omit `--offline`. Results and token usage can vary between model versions and requests; this fixture is not an accuracy or cost benchmark.

## Kubernetes checks

Use a configured context with pod-list and pod-log read permissions. These commands use the current `kubectl` context:

```sh
kubectl config current-context
kubectl --request-timeout=8s get pods --all-namespaces
python3 -m jevernetes k8s --offline --since 5m --tail 100
python3 -m jevernetes k8s -f --offline --duration 30 --tail 0
```

Add `--namespace my-namespace` to the analyzer commands and `--namespace my-namespace` in place of `--all-namespaces` for `kubectl` when access is namespace-scoped. Live mode with `--tail 0` only receives new lines emitted after attachment, so a quiet workload may produce no events.

Inspect collection coverage alongside judgments. A timeout, denied permission, or unavailable container log should appear as a collection gap. Live inventory attempts have a 12-second deadline and retry every 15 seconds. Reports and real cluster logs are local runtime data and are not distributed with the project.

## Release security review

The initial release adds bounded decompression and credential-redaction regression cases. See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) for scanner versions, reviewed findings and scope.
