# Reviewed external transport timestamps (2026-09-27)

Started on `feat/provider-template-learning` at exact clean HEAD
`a383eef26b883e4e3b9766a7b0fa80b421aa1995`. The only production behavior change
removes the blanket `Event.timestamp.is_some()` rejection from `reviewed::Matcher::key`.
An external parsed timestamp is occurrence metadata; `Event.text` must still
independently match the exact compiled prefix identity, clock grammar, complete
shape and required/protected literals, and the source must match the reviewed scope.
A parser-stripped application envelope cannot be supplied by timestamp metadata.
Whole-line JSON still requires the empty prefix identity. This supersedes the earlier
blanket parser-timestamp fallback documented below. Redaction, sensitivity,
truncation, line-count and risk-publication checks remain unchanged, as do controller
policy, generic cacheability, Drain and shadow learning. Dependencies are unchanged.

The new Parser-to-Matcher regression and extended runtime/controller replay failed
before the fix and pass afterward. Tests cover nanosecond Kubernetes-style transport
stamps with retained application envelopes, whole-line JSON, dated application
prefixes behind transport stamps, and all three provider risk-contract keys. Timestamp
changes alone (including absence and changes between preparation and completion)
do not change reviewed risk equivalence. Pending occurrences classify independently;
reuse starts only after publication. Serialized events preserve evidence, source,
timestamps, IDs and line metadata; only the expected analysis fields may change.
Wrong/missing/stripped prefixes, changed grammar, malformed suffixes, source/shape/
literal changes and unsafe evidence still fail. The existing adversarial risk and
batch-invalidation matrix now also covers timestamp-bearing completions, including
line-count/newline changes and delayed tickets.

The synthetic two-pod runtime/controller replay runs with and without transport
stamps at batch sizes 1 and 8. Both retain all 200 events: **200/3/197** sequentially
and **200/10/190** in batches (retained/classifications/reuses). Both security events
remain independently classified, with complete evidence and timestamps in their
Notify outbox records. Policy remains 198 Review and two Notify decisions, with
zero persistent verdicts. These are deterministic injected-judgment checks, not a
private real-log replay, model-accuracy result or production acceptance claim.

All Cargo dependency operations used `CARGO_NET_OFFLINE=true` and `--locked`.
No private Pingdex reports, Kubernetes commands/APIs, live providers, real credentials
or network were accessed. No dependency changes, installations, push, PR or deployment
were performed.

| Check | Result |
| --- | --- |
| `cargo fmt --all --check`; `git diff --check` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| `cargo test --locked --all-features --lib reviewed` | 19 passed. |
| `cargo test --locked --all-features` with the 17 documented loopback fixtures explicitly skipped | 98 library + 15 CLI tests passed; main and doctests passed (zero tests). |
| `cargo build --locked --release --examples --bin jevernetes` | Passed. |
| All three release examples | Passed: reviewed 300/1/299 and 200/3/197; shadow 304 classifications/zero reuse; Drain 1,000/2/998. |
| Release offline CLI smoke | Passed: two retained synthetic events, one important and one uncertain. |
| Python unittest discovery excluding the four `test_dashboard.HttpTests` loopback cases | 113 passed. |
| Python compileall; five JS helper suites; app/search JS syntax | Passed. |
| `tools/check_controller_artifacts.py`; `tools/check_release.py` | Passed; 113 tracked files checked, no Kubernetes command or deployment. |
| Python build/security tooling | build, setuptools, Bandit and pip-audit unavailable; no installation attempted. |

The 17 Rust fixture names are the 15 listed under prior Drain validation plus the
two provider transport fixtures listed under provider/shadow validation below. The
four Python exclusions are the `test_dashboard.HttpTests` cases. These loopback
fixtures were deliberately not attempted under the no-network constraint; no
unrestricted full-suite pass is claimed. Browser integration, private real-log replay,
live providers and production validation remain unevaluated. Historical validation
below applies to its stated snapshots, not this fix.

# Reviewed routine risk-equivalence predicate (2026-09-27)

Preflight passed with a clean worktree at exact HEAD
`5de0c6fc342785e6bc69f2159fa0676a4ef847d8`. A private predicate in `reviewed.rs`
now governs reviewed publication and batch invalidation only. It requires unchanged
`controller::cacheable`, baseline not important, Routine category and importance,
Info/Noise severity, and category/importance confidence each at least 0.85. Those
confidence thresholds protect the risk boundary. Severity confidence remains
required, finite and within [0,1], but is not thresholded after a reviewed v2 rule
has pinned complete shape, protected and operation literals, prefix grammar and
source scope. Info and Noise are equivalent non-escalating outcomes only within
this reviewed contract. Drain, semantic shadow promotion, generic cacheability,
controller policy and persistent-cache behavior are unchanged.

Two focused adversarial tests cover Info and Noise at severity confidence 0.0,
0.1, 0.84, 0.85 and 1.0; exact category/importance threshold acceptance; missing,
NaN, infinite and out-of-range confidences; low category/importance confidence;
every non-routine category/importance and escalating/unknown severity; analysis
errors, baseline-important, truncated and unsafe evidence; and changed source,
prefix, shape, operation and protected literals. Unsafe completions invalidate all
pending tickets before any publication, including when evidence changes after
preparation. Tests cover both batch orders, multiple safe siblings, delayed tickets,
no speculative reuse, rescore bypass and subsequent independent safe retry.
Existing TTL/capacity/expiry/stale-owner, protected semantics and shadow tests pass.

The synthetic example and runtime replay now deliberately inject severity confidence
0.0 for routine seeds. Whole-line results remain **300/1/299** and logger-envelope
results remain **200/3/197** sequentially and **200/10/190** at batch size 8
(retained/classifications/reuses). Every event keeps its evidence. Controller policy
still produces 198 Review decisions for low severity confidence and two security
Notify decisions; the replay stores zero persistent verdicts. These are deterministic
mechanical checks, not model accuracy, calibration or production acceptance evidence.

All Cargo commands used cached dependencies with `CARGO_NET_OFFLINE=true` and
`--locked` where applicable. Dependencies and lockfile are unchanged. No private
aggregate reports/artifacts, real credentials, provider APIs or Kubernetes were
accessed. No external network, push, PR or deployment was used. No Kubernetes
command or tooling installation was run.

| Check | Result |
| --- | --- |
| `cargo fmt --all --check`; `git diff --check` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| Focused `cargo test --locked --all-features --lib reviewed` | 18 passed. |
| Socket-free `cargo test --locked --all-features` with the 17 previously documented loopback fixtures explicitly skipped | 97 library + 15 CLI tests passed; main and doctests passed (zero tests). No unrestricted full Rust pass claimed. |
| `cargo build --locked --release --examples --bin jevernetes` | Passed. |
| All three release examples | Passed: reviewed 300/1/299 and 200/3/197; shadow 304 classifications/zero reuse; Drain 1,000/2/998. |
| Release offline CLI smoke | Passed: two synthetic events retained with expected baseline judgments. |
| Python unittest discovery | 113 tests ran; `test_dashboard.HttpTests` class setup failed at denied loopback bind. **Not a passing full suite.** |
| Socket-free Python discovery excluding the four `test_dashboard.HttpTests` cases | 113 passed. |
| Python compileall; five JS helper suites; app/search JS syntax | Passed. |
| Controller/deployment and tracked-release static checks | Passed; no deployment or Kubernetes command. |
| Python build/security tooling | build, setuptools, Bandit and pip-audit unavailable; no installation or network fetch attempted. |

The initial Python exclusion used the wrong class name, so discovery attempted a
synthetic loopback listener; the sandbox rejected the bind with `Operation not
permitted`. The corrected socket-free run passed. Rust loopback fixtures were
excluded without attempting sockets. Full loopback-dependent validation remains
for an authorized environment; historical full-suite results below are not results
for this changed snapshot. Browser integration, live providers and production
acceptance were not evaluated.

# Reviewed logger envelopes and subset scopes (2026-09-27)

Started at exact local HEAD `3542ac4074c3e7b9e0f419b2b9e4df0cb6059e89`.
Reviewed artifact version 2 now requires an explicit canonical prefix identity,
including the reused strict Drain logger-clock grammar; whole-line JSON uses an
empty identity. Only one bounded prefix and a complete JSON object suffix qualify.
Reviewed nonempty source scopes match exact subsets and replace volatile sources
only in the session cache key. Compatible overlapping scopes are rejected unless
prefix/shape/literal constraints prove they cannot match the same event. Version 1
artifacts fail closed and require explicit recompilation/review.

Five new tests cover envelope grammar and malformed/ambiguous extraction, clock
range/Unicode/format changes, source subsets and overlap in either rule order,
sensitive/escaped credential fields, and the 198-routine/two-security replay. Existing
shape/protected-value, publication/TTL/capacity/rescore/restart and shadow quarantine
regressions remain passing. All source/text evidence remains unchanged. Parser-extracted
timestamps conservatively fall back because their bytes cannot satisfy a prefix
identity in `Event.text`. No provider, collector, policy, SQLite or shadow activation
semantics changed.

The wholly synthetic two-replica replay has 200 retained events, 159 exact 42-byte
prefixes and one normalized prefix. At batch size 1 it uses three injected
classifications and 197 reviewed reuses; at batch size 8 it uses ten classifications
and 190 reuses. Both security-shape records retain their complete evidence and pod
identity in two Notify/outbox intents. Whole-line replay remains 300 retained events,
one classification and 299 reuses. These are mechanical tests, not production
accuracy or security-equivalence claims.

All Cargo commands used `CARGO_NET_OFFLINE=true`; dependencies/lockfile are unchanged.
No live Kubernetes/provider APIs, credentials, private reports, external network,
push, PR or deployment were accessed. No Kubernetes command was run.

| Check | Result |
| --- | --- |
| Format and diff checks | Passed. |
| Strict Clippy, all targets/features | Passed. |
| Full Rust all-feature suite | 95 library tests passed; 17 existing loopback fixtures failed at bind with PermissionDenied. **Not a passing full suite.** |
| Socket-free all-feature suite (only those 17 named fixtures skipped) | 95 library + 15 CLI tests passed; main and doctest targets passed (zero tests). |
| Reviewed tests | All 16 passed, including five new envelope/scope tests. |
| Locked release build and offline CLI smoke | Passed; two smoke events retained, zero provider requests. |
| All three Rust examples | Passed: reviewed whole-line 300/1/299 and envelope 200/3/197 (retained/classifications/reuses); shadow 304 classifications, zero reuse; Drain 1,000/2/998. |
| Python unittest discovery | 113 tests ran; dashboard HTTP class setup failed on denied loopback bind. Other executed tests passed. **Not a passing full suite.** |
| Python compileall; five JS helper suites; app/search syntax | Passed. |
| Optional browser pipe integration | Did not pass: exited 13 without diagnostics; no browser pass claimed. |
| Controller/deployment and tracked-release static checks | Passed, including v2 canonical clock grammar, stable subset fixture and existing optional read-only mount. |
| TruffleHog 3.97.1 tracked-source scan, updates and verification disabled | Exit 0; zero verified/unverified findings. Sandbox process-cleanup permission errors remain; **not an unqualified scanner pass**. |
| Python build/security tooling | build, setuptools, Bandit and pip-audit unavailable; no installation/network fetch attempted. |

The user-reported 107-library/15-CLI unrestricted host pass applies to the parent.
The changed snapshot still needs loopback-dependent checks in an authorized host
before claiming a complete host matrix. Historical results below describe earlier
snapshots; the v2 prefix/subset contract supersedes their full-source v1 contract.

# Reviewed compiled-rule activation (2026-09-27)

Implemented the second stage on top of reviewed parent `36b7570`: default-off
`--template-rules PATH`, strict bounded versioned artifacts, computed byte digests,
full-source/complete-shape deterministic scalar normalization, protected literals,
and bounded session-only verdict publication/reuse in files, snapshots and the
continuous controller. Exact defaults, Drain, provider adapters and SQLite schema 2
are unchanged. The optional reviewed-rule mount patch is outside the base.

**LLMs learn candidates in shadow; a separately reviewed artifact activates reuse.**
No proposal/report import, automatic promotion or confidence-as-approval exists.
Embeddings and fine-tuning are unnecessary and never authorization. The human/offline
compilation and review boundary is documented in
[the artifact contract](docs/provider-template-learning.md#reviewed-artifact-activation).

The exact sample-17 regression now permanently rejects pending replay as incomplete.
Unsafe/ineligible evidence invalidates its proven source/shape family before return,
including private and malformed continuation evidence whose nonserialized original
shape hash survives parsing. Entirely unparseable new evidence with no provable family
never matches; unrelated families are not merged by source alone. Shadow contract is
now `template-shadow-v2`; proposals remain advisory regardless of replay result.

New tests cover strict unknown/missing/duplicate fields, bounded file/rule/string/path/
source counts, unsupported versions, expiry, protected names and close variants at
any depth, overlapping/ambiguous rules, computed digests, regular-file/projected-link
policy, scalar-only normalization, nested arrays and empty containers, literal/type/
source changes, same-batch independent misses and unsafe siblings, failed/uncertain/
non-cacheable retry, TTL/capacity/expiry, rescore, stale/restarted tickets, changed
artifact/provider/model identity and separate reports. CLI regressions prove invalid
artifacts fail before credentials/input/state/cluster access and reviewed activation
does not require a template role. Controller checks preserve policy transactions,
notification intents, recurrence and zero semantic writes to the durable verdict cache.

Synthetic results (zero provider or Kubernetes calls): 300 routine occurrences retain
all 300 events, require **one classification and 299 reuses** at batch size 1; batch
size 8 requires eight independent warm-up classifications. Nine status/outcome/error/
authorization/security/fraud/new-field/type/source variants classify independently and
produce nine controller Notify/outbox intents. Failed verdicts retry, rescore bypasses
reuse, controller TTL caps apply, and repeated security observations advance existing
incident recurrence. Ordinary offline baseline judgments have no synthetic confidence
and cannot seed reuse. The standalone example uses a fixed synthetic review time.

All Cargo operations used cached dependencies with `CARGO_NET_OFFLINE=true`.
`Cargo.lock` is unchanged. Work was confined to this worktree; no credentials, live
providers, cluster API, network escalation or private Pingdex reports were accessed.
Kustomize was local rendering with `KUBECONFIG=/dev/null`. No push, PR or deployment.

| Check | Result |
| --- | --- |
| `cargo fmt --all --check`, `git diff --check` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| `cargo test --locked --all-features` | **90 library tests passed, 17 existing loopback fixtures blocked at bind with OS code 1 / PermissionDenied. Not a passing full suite.** Same fixture names as the parent section below. |
| Socket-free all-feature suite (only those 17 fixture names skipped) | Passed: 90 library tests, 15 CLI tests, main target and doctests (0). |
| Reviewed-rule / shadow tests | All 11 reviewed-rule tests and all shadow regressions passed. |
| `cargo build --locked --release` | Passed. |
| Release CLI offline synthetic smoke | Passed: 2 retained events, 1 important, 1 uncertain, zero provider requests. |
| All three Rust examples | Reviewed: 300 retained / 1 classification / 299 reuses; shadow: 304 retained / 304 classifications / 0 reuses; unchanged Drain: 1,000 retained / 2 classifications / 998 reuses. |
| `python3 -m unittest discover -s tests -v` | 113 tests ran; existing dashboard HTTP setup failed on denied loopback bind. Other executed tests passed. **Not a passing full suite.** |
| `python3 -m compileall -q jevernetes` | Passed. |
| Five JavaScript helper suites and app/search syntax checks | Passed. |
| `python3 tools/check_controller_artifacts.py` | Passed, including base default-off and optional read-only reviewed-rule mount assertions. |
| `KUBECONFIG=/dev/null kubectl kustomize deploy/base` | Passed; local rendering only. |
| `python3 tools/check_release.py` | Passed, including newly staged synthetic example and rule source. |
| `cargo tree --locked` | Passed offline, no dependency changes. |
| TruffleHog 3.97.1 source-snapshot filesystem scan (`--no-verification --no-update --fail --fail-on-scan-errors`) | Exit 0, zero verified/unverified findings. Scanner reported a sandbox process-cleanup permission error; **not an unqualified scanner pass**. |
| Dependency advisory/Python security tools | cargo-audit, Bandit and pip-audit unavailable locally. No installation, advisory fetch or network audit attempted. |

Limits: rule files load once at startup; restart is required to load changes and always
starts an empty active verdict cache. No durable semantic restoration or hot reload.
Projected symlinks to regular files follow the existing credential policy; trusted
private directories/mounts remain the operator's responsibility. Review labels are
metadata, not cryptographic attestation. Event retention/queue bounds and controller
crash/partial-coverage guarantees remain unchanged. These synthetic checks establish
mechanical reuse and notification behavior, not real model accuracy or production
security equivalence. The user reports that the parent host matrix passed; this child
snapshot still needs its socket-dependent checks in an authorized host environment.

The historical stage-one and earlier results below describe their respective snapshots;
the stage-one absence of active reuse is superseded by this section.

# Provider adapters and semantic shadow learning (2026-09-27)

Implemented the explicitly permitted conservative **shadow-only** stage: TypeSafe/Jev,
OpenAI Responses and Anthropic Messages risk contracts; independent OpenAI/Anthropic
proposal roles; bounded typed proposals and deterministic registry replay; shared
files/snapshot/controller plumbing; report provenance/role usage and optional Secret
file projections. Exact remains the default and Drain is unchanged. Automatic
promotion, active semantic reuse and durable candidate restoration are **not implemented**.
No semantic classification savings or active-template controller validation is claimed.
See [the next activation step and limitations](docs/provider-template-learning.md).

Validation used cached dependencies (`CARGO_NET_OFFLINE=true`); Cargo.lock and dependency
versions are unchanged. No live provider, Kubernetes, credentials, private reports or
external services were accessed. Synthetic HTTP tests attempted only local loopback
listeners, which this sandbox disallowed. No network escalation was requested.

| Check | Result |
| --- | --- |
| `cargo fmt --all --check` | Passed. |
| `cargo check --locked --all-targets` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| `cargo test --locked --all-features` | 76 library tests passed; 17 fixtures failed solely at loopback bind with OS code 1 / PermissionDenied. **Not a passing full suite.** |
| `cargo test --locked --test cli` | All 13 passed, including validation before input/state access and offline semantic behavior. |
| `cargo test --locked --doc` | Passed (0 doctests). |
| `cargo test --locked --lib semantic` | 9 passed (8 new semantic tests plus an existing Drain semantic-mutation test). |
| `cargo test --locked --lib provider::tests` | All 5 provider contract/credential/identity/usage tests passed. |
| `cargo build --locked --release` | Passed. |
| Release CLI synthetic offline smoke | Passed: 2 retained events, 1 important, 1 uncertain, zero provider attempts. |
| `cargo run --locked --example semantic_shadow` | 304 retained events, 304 injected classifications, 296 shadow matches, 4 independently classified security variants; 0 avoided classifications, 0 network calls. |
| `cargo run --locked --example drain_reduction` | Existing 1,000 retained / 2 classifications / 998 reuses preserved. |
| `python3 -m unittest discover -s tests -v` | 113 tests ran; dashboard HTTP class setup failed at denied loopback bind. Other executed tests passed. **Not a passing full suite.** |
| `python3 -m compileall -q jevernetes` | Passed. |
| Five JavaScript helper suites and app/search syntax checks | Passed. |
| `python3 tools/check_controller_artifacts.py` | Passed, including optional OpenAI/Anthropic projections and exact/off-learning base defaults. |
| `KUBECONFIG=/dev/null kubectl kustomize deploy/base` | Passed; local rendering only, no cluster configuration or API access. |
| `python3 tools/check_release.py` | Passed. |
| `cargo tree --locked` | Passed offline; no dependency changes. |
| TruffleHog 3.97.1 filesystem scan, verification/update checks disabled | Exit 0, zero verified/unverified secrets in source snapshot; scanner reported a sandbox process-cleanup permission error. Not an unqualified scanner pass. |
| `cargo audit --no-fetch --stale --deny warnings` | Unavailable: cargo-audit is not installed. No installation/advisory download attempted. Bandit and pip-audit are also unavailable locally. |
| `git diff --check` | Passed. |

New synthetic tests cover distinct request schemas and sensitive auth headers, refusal,
incomplete/max-token/tool responses, malformed or extra judgment fields, confidence and
usage validation (including cached Anthropic input), credential bounds/aliases/projected
symlinks, provider/model/version cache invalidation, cancellation before requests and
request-size limits without sockets. Existing timeout/cancellation tests now enumerate
all three providers; new HTTP fixtures cover new adapters, retry exhaustion, redirect
destinations and response-size limits, but those transport tests remain sandbox-blocked.

Shadow tests cover minimum independent support, routine matches without reuse,
protected/unobserved paths, opaque-ID limits, status/outcome/auth/error/new-field changes,
security/fraud/failed/private/truncated/multiline/invalid/duplicate-key evidence,
same-batch pending evidence, replay collisions, stale generation tickets after TTL,
capacity/restart bounds and budget/cancellation behavior. The shared runtime/controller
regression retains all 304 events, classifies every occurrence, records four Notify/outbox
intents for security/fraud, and checks role usage in the final report. It uses injected
provider envelopes, no sockets and no credentials. Active-template reuse is deliberately
absent; its future tests must accompany a reviewed activation contract.

The 17 Rust loopback failures are the 15 existing fixtures listed in the earlier
validation section plus:

- `jev::provider_transport_tests::adapters_use_real_http_contract_and_reject_redirects_and_oversize`
- `jev::provider_transport_tests::all_adapters_bound_retries_and_refuse_redirect_destinations`

Run the full Rust/Python suites in an authorized environment permitting local synthetic
listeners, and dependency advisory checks with locally available tooling/database,
before treating this as a fully validated release. No API/model availability, real
provider accuracy/cost, cluster deployment or production readiness is established.
This snapshot is committed locally only; no push, PR or deployment is part of this work.

# Rust validation

## Logger clock identity (2026-09-27)

Strict logger clock prefixes remaining in `Event.text` now share template identity
while retaining any date still in that text, level and clock format. Parser-extracted
timestamps remain event metadata outside template identity; extraction is syntactic
and also strips timestamp-shaped invalid dates. This is an existing Parser boundary,
not a logger-clock regression. Quoted/structured payloads remain literal, including
ID-looking words inside user content. No dependency, persistence, policy or
notification implementation changed. See [the grammar and limits](docs/drain-template-mining.md).

Six additional synthetic regressions cover clock-only reuse, malformed/ambiguous
prefixes, literal structured payloads (escapes, duplicate/reordered keys, oversized
and malformed inputs), semantic changes, batch warm-up and controller notification
evidence. A 26-event runtime fixture retains every event and separately classifies
security/fraud observations after routine reuse. With batch size 1 it performs 4
classifications in 4 requests and reuses 22 events; with batch size 8 it performs
10 classifications in 2 requests and reuses 16. These are injected verdicts with
zero provider attempts and zero measured cost, not a billing or accuracy benchmark.

A follow-up Parser-to-Drain regression uses `Parser::feed`/`flush` to verify that
valid dated prefixes and a timestamp-shaped invalid calendar date are stripped
from text, retained verbatim as timestamp metadata, and excluded from template
identity. Direct grammar tests are explicitly scoped to already-built `Event.text`.

Validation used cached dependencies with `CARGO_NET_OFFLINE=true`. The timestamp
boundary review reran every check below except the unchanged reduction demo:

| Check | Result |
| --- | --- |
| `cargo fmt --all --check` | Passed. |
| `cargo clippy --locked --all-targets --all-features -- -D warnings` | Passed. |
| `cargo test --locked --all-features drain` | 20 passed. |
| `cargo test --locked --all-features` | 61 library tests passed; the same 15 loopback bind fixtures listed below failed with `Operation not permitted`. Sandbox-limited, not a passing full suite. |
| `cargo test --locked --all-features --test cli` | 11 passed. |
| `cargo test --locked --all-features --doc` | Passed (0 doctests). |
| `cargo build --locked --release` | Passed. |
| `git diff --check` | Passed. |
| `cargo run --locked --example drain_reduction` | 1,000 retained events, 2 synthetic classifications, 998 reuses, 0 fallbacks, 0 network calls. |

Host loopback works as reported by the user; this run was confined to the sandbox,
where listener binds were denied with OS code 1 (`PermissionDenied`). These results
do not indicate a host loopback failure or establish a passing full suite on the host.

Private report replay uses an untracked `/tmp` helper that reads the original files
in place and prints only aggregate counts. It compares the baseline miner with the
new miner at batch sizes 1 and 8, replaying recorded judgments without synthesizing
confidence or weakening uncertain/security/fraud exclusions. Template reduction
alone does not establish classification savings. No private evidence or sample
fixtures are included in this repository.

## Prior Drain baseline validation (2026-09-27)

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
