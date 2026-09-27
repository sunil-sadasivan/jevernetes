# Rust validation

## Drain release-blocker fixes (2026-09-27)

The [implemented design](docs/drain-template-mining.md) now uses a clean-room,
Apache-2.0 in-repository core. `logdrain`, `bincode` and `dashmap` are absent from
both the lockfile and `cargo tree --locked`. No replacement dependency was added.
Only allowlisted opaque UUID/hex IDs generalize; IPs and numeric telemetry remain
literal. Exact grouping remains the default. Drain honors configured batch size
and classifies pending observations independently, publishing verdicts only after
responses with current template ID/version tickets.

The sequential socket-free demo measured **1,000 raw events, 2 synthetic
classifications, 998 reuses (99.8%), 1 created template, 1 changed template,
998 unchanged matches, 0 fallbacks and 1,000 retained events**. Only request IDs
vary; source metadata and operational values remain fixed. It makes **zero network
calls**. This is a classification reduction check, not a request-count, accuracy
or billing benchmark. A prefilled batch incurs additional warm-up classifications.

All 13 focused Drain tests pass. New controller regressions first establish a
routine verdict and a successful reuse, then prove `127.0.0.1` → `0.0.0.0` and
`duration_ms=1` → `86400000` each trigger classification, a non-routine security
verdict, policy Notify and durable outbox evidence. The `max_batches=2`,
`batch_size=8` multiline regression classifies/retains all 16 events and produces
16 policy/incident/outbox records. Tests also cover independent batched templates,
failed batches, no speculative reuse, ID grammar, stable template IDs, stale
responses, bounded capacity, TTL/rescore, exact-cache isolation and recurrence.

Cargo dependency operations used `CARGO_NET_OFFLINE=true` (or `--offline` for the
initial lockfile pruning). All dependencies were cached; no network fetch occurred.

| Exact validation | Result in this session |
| --- | --- |
| `cargo fmt --all --check` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| `cargo test --locked --all-features` | 54 library tests passed; 15 existing loopback fixtures failed at bind with `Operation not permitted`. Full suite remains blocked by the sandbox. |
| `cargo test --locked --all-features drain` | All 13 focused tests passed. |
| `cargo test --locked --all-features --test cli` | All 11 CLI tests passed separately. |
| `cargo test --locked --all-features --doc` | Passed (0 doctests). |
| `cargo build --locked --release` | Passed. |
| `cargo run --locked --example drain_reduction` | Passed with the counts above. |
| `cargo test --locked --all-features controller::tests::offline_controller_smoke_and_shutdown` | Blocked at synthetic loopback listener bind, `Operation not permitted`. |
| Release-binary offline CLI smoke from CI | Passed: 2 events, 1 important, 1 uncertain. |
| Release-binary Drain offline CLI smoke | Passed: 2 events, 0 reuses/provider attempts; local rules remain unchanged. |
| `python3 tools/check_controller_artifacts.py` | Passed. |
| `kubectl kustomize deploy/base` | Passed; local rendering only, no cluster/configuration access. |
| `python3 tools/check_release.py` | Passed: 102 tracked files checked. |
| `cargo tree --locked` | Confirmed no `logdrain`, `bincode` or `dashmap`. |
| `cargo audit --deny warnings` | Unavailable: `cargo-audit` is not installed. No tooling installation or advisory database download was attempted. |
| `git diff --check` | Passed. |

The 15 full-suite failures are loopback listener bind failures:

- `controller::tests::{health_readiness_is_local_and_shutdown_interrupts_idle, offline_controller_smoke_and_shutdown, persistent_lookup_precedes_jev_and_only_new_groups_are_batched, webhook_never_follows_redirects, webhook_sanitized_request_status_and_bounds}`
- `jev::tests::{request_timeout_is_safe_and_cancellation_accounts_inflight_attempt, response_bound_and_invalid_verdict_still_account_usage, retries_and_safe_http_errors_no_redirects}`
- `kubernetes::tests::{live_http_reconnect_deduplicates_and_shutdown_joins, snapshot_backpressure_does_not_expire_transport_deadline, snapshot_cancellation_accounts_for_buffered_evidence, snapshot_http_contract_is_read_only_and_scoped, watch_discovery_starts_bounded_streams_and_cancels}`
- `runtime::tests::{actual_http_grouping_reuses_across_batches_and_preserves_occurrences, failed_judgments_retry_next_batch_and_never_claim_reuse}`

Rerun the full suite, controller smoke and `cargo audit --deny warnings` in an
authorized host environment with loopback fixtures and audit tooling available.
No live Jev/Kubernetes calls, credential reads, deployments, commits, pushes, PRs,
main-checkout edits or Git-index changes occurred. All edits remain unstaged.
Legacy Python/JavaScript suites were not rerun for this Rust-only implementation.
Templates remain process-local, and scope can span workloads sharing the same
namespace/container name. See the design for scope and equivalence limitations.

## Required checks

Required checks (CI uses Rust 1.94):

```sh
cargo fmt --all --check
cargo clippy --locked --all-targets --all-features -- -D warnings
cargo test --locked --all-features
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
cargo test --locked --all-features controller::tests::offline_controller_smoke_and_shutdown
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
`cargo clippy --locked --all-targets --all-features -- -D warnings`, `cargo test --locked --all-features`
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
