# Final projected-input and exec-auth blockers — 2026-10-11

Started in the clean implementation worktree on `fix/go-full-parity-20261011` at exactly `f79e7f2ef2ada0bdcb4d71589fcb5fb19f24fc20`. Read `CONTRIBUTING.md`; no applicable `AGENTS.md` was present. All fixtures and credentials are synthetic/local. No push, deployment, GitHub mutation, production access or live provider/cluster contact occurred. Go/Rust/Python coexistence remains staged; runtime retirement remains disabled.

## Regression-first evidence

[Committed failure excerpts](docs/validation/final-blockers-before-f79e7f2.txt) preserve the commands and failures before each corresponding production change. The complete logs are ignored under `.cache/final-blockers-*-before.log`.

- The atomic-writer fixture reproduced rejection of every deployed input: policy, webhook URL, TypeSafe/OpenAI/Anthropic keys, and optional reviewed rules. The fixture discovers paths in both actual manifests. Initially its reader adapter called the existing strict `Read`; the retained test calls the new projection reader and continues asserting that ordinary `Read` rejects the same leaves.
- Initial exec auth and 401 refresh each exceeded the one-second cancellation bound for both request cancellation and owner-lifecycle cancellation. The before harness constructed the original collector with an in-memory 401 transport and killed/joined helpers after failures. The retained harness injects the same in-memory transport at the new transport-config boundary; separate real CLI, HTTP-client, SPDY and dashboard-handler regressions verify production routing.
- An adjacent inspection shutdown regression failed before its production change: `Serve` returned as soon as its listener closed, before the request handler joined. It uses `net.Pipe`, so the failure does not depend on socket-bind privileges.

## Fixes and audit

**Projected deployment inputs:** Both the base manifest and argument-replacing reviewed-rules overlay explicitly pass controller-only `--projected-inputs`. Only the six fixed configuration paths gain projection support; other paths retain ordinary no-alias semantics even with this opt-in. The reader pins the root and generation using directory descriptors, interprets only `leaf -> ..data/leaf` plus a timestamp-shaped generation name, and opens the generation/regular leaf with `openat` and `O_NOFOLLOW`. Same-descriptor type, identity, single-link, size and cancellation checks remain. Arbitrary/absolute/traversing links, symlinked generations/leaves, hardlinks, FIFOs, directories, over-limit content, unsafe generation permissions and cancellation fail closed. Tests race atomic generation replacement (including escape attempts) and leaf replacement. The manifest fixture checks read-only mounts, mode-2777 roots, timestamp generations and mode-0440 files. The root mode reflects Kubernetes' [emptyDir-backed volume setup](https://github.com/kubernetes/kubernetes/blob/v1.35.3/pkg/volume/emptydir/empty_dir.go); generation pinning follows its [atomic-writer layout](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/volume/util/atomic_writer.go).

Deployment mount ancestors and read-only roots remain trusted; this is not a general-purpose symlink resolver. Root/current-user directory ownership is checked, but root mode 0777 is valid because the mount supplies write protection. Generation removal can fail startup closed rather than retry through a different path. Policy/rules/provider/webhook values remain startup snapshots; pod restart is required to apply changes. Optional provider credentials are read only when selected; the optional rules overlay opts in independently. The unused `webhook-token` projection is not a Go authentication-header input and remains outside the allowlist. Ordinary inputs, reports and local Kubernetes files retain their prior protections. No in-cluster service-account path was read or changed.

**Exec authentication:** Both ordinary Kubernetes HTTP clients and SPDY upgrades bypass client-go's context-free exec runner. The runner has request/owner cancellation and a separate 30-second cap, kills the helper process group and joins the command and output readers. Context-aware serialization permits cancelled waiters to leave promptly. TLS callbacks return cached certificates only; helper execution occurs where a request context exists. Initial/expiry/401 refresh, no-replay 401 semantics, v1/v1beta1 wire versions, environment overrides, explicit auth precedence, plugin policy, certificate/key parsing, token+certificate combinations, per-client caching and cluster extension/audience data are covered. Tokens remain opaque and server-validated; no global cache crosses cluster/client scope. Strict parsing rejects duplicate/unknown fields, incorrect kind/version, empty or malformed credentials, expired output, token control characters and output above 1 MiB. Noninteractive stderr and detailed credential/command errors are suppressed. Interactive success, deadline and Ctrl-C are tested through a local PTY; foreground terminal ownership is restored and helper PIDs are gone. Helpers remain trusted local code; intentionally detached processes outside their process group are not sandboxed by this mechanism.

The CLI tests exercise real child processes for `k8s --duration 1`, `k8s` SIGTERM and `remote` SIGTERM, then assert the helper PID no longer exists. A dashboard handler using the real remote-client builder cancels/join auth without opening a listener. Concurrent ordinary HTTP and SPDY callers also join. Inspection server shutdown now cancels and joins handlers before returning, including on a listener error. No provider/cluster request can leave these hanging-auth fixtures: helpers block before transport I/O. The pinned `x/term` module becomes a direct dependency; no dependency version/checksum changes.

## Exact validation

Darwin arm64; Go 1.27.1, Python 3.14.7, Rust 1.94.0, Node 24.19.0. Commands use:

```sh
export PATH=/opt/homebrew/opt/node@24/bin:$PATH
export GOCACHE="$PWD/.cache/build" GOPROXY=off GOSUMDB=off CARGO_NET_OFFLINE=true PIP_NO_INDEX=1
```

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/kube ./internal/safefile ./internal/cli ./internal/inspect -run 'TestExec\|TestProjected\|TestCLIExecAuthShutdown\|TestInspectionShutdownJoinsHandlers'` | Passed; `.cache/final-blockers-focused.log` |
| `go test -race -count=1 ./internal/kube ./internal/safefile ./internal/cli ./internal/inspect -run 'TestExec\|TestProjected\|TestCLIExecAuthShutdown\|TestInspectionShutdownJoinsHandlers\|TestLocalConfigMergeAndSafeTokenRotation\|TestKubeFIFOFilesBounded\|TestFIFOOpenIsBounded\|TestCancellation'` | Passed; `.cache/final-blockers-focused-race.log` |
| `tools/check.sh` | Passed the complete gate with local-listener permissions: full Go tests/race, vet, cross-builds, UI, Python, source/privacy and parity inventory checks. `.cache/final-blockers-check-local-listeners.log` |
| `go test -race ./...` (inside `tools/check.sh`) | Passed every package with local-listener permissions, with no race reports. `.cache/final-blockers-check-local-listeners.log` |
| `tools/release.sh` | Passed both executable names, private final reports, signal smoke, state-volume/migration/reopen checks and source/archive byte/privacy checks; `.cache/final-blockers-release.log` |
| `go vet ./...`; `tools/check-cross.sh` | Passed, including Linux amd64/386/arm builds; binaries not executed on Linux. `.cache/final-blockers-vet-cross.log` |
| `cargo fmt --check`; `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed; `.cache/final-blockers-rust.log` |
| `cargo test --locked --offline --all-features` | Passed all 38 library and 7 CLI tests with local-listener permissions; binary/doc-test targets also pass. `.cache/final-blockers-rust-local-listeners.log` |
| `cargo build --locked --offline`; `python3 tests/rust/tui_pty.py target/debug/jevernetes` | Passed build, search, Ctrl-C and SIGTERM, with restored terminals/private reports; `.cache/final-blockers-rust.log` |
| `python3 -m unittest discover -s tests -q` | All 124 tests passed inside the complete check gate with local-listener permissions. `.cache/final-blockers-check-local-listeners.log` |
| `python3 tools/check_parity.py --run` | Passed all 17 executable suites/14 workflows with local-listener permissions; retirement disabled. `.cache/final-blockers-parity-local-listeners.log` |
| `for file in tests/*_ui.cjs; do node "$file" \|\| exit; done`; syntax checks for both dashboard JavaScript trees | Six UI suites and syntax checks passed; `.cache/final-blockers-ui.log` |
| `python3 tools/check_release.py`; `python3 tools/check_parity.py`; `node tools/check-release.mjs` | Passed source/privacy checks and 14-workflow/17-suite coexistence inventory; `.cache/final-blockers-ui.log` and release log |
| `git diff --check`; `git diff --cached --check` | Passed; final diff independently inspected and final committed worktree clean |

Implementation iterations included correcting the fractional-duration test argument, moving synthetic transport injection to the new boundary, fixing internal-to-wire cluster encoding, accepting the real mode-2777 volume root, and restoring terminal foreground ownership with a serialized SIGTTOU-safe handoff. The inspection join fixture initially failed to drain its `net.Pipe` response and was corrected to model a consuming HTTP client. These were resolved implementation/fixture issues, not environment exceptions. The PTY fixture explicitly skips only if the repository's Python 3 development prerequisite is unavailable; it ran here. Existing socket/ownership tests were not weakened or removed. Nonfatal macOS minimum-version linker warnings remain.

## Environment limits and initial attempts

The initially restricted runs of `tools/check.sh`, `go test -race -count=1 ./...`, Rust tests, Python tests and executable parity failed at denied TCP/Unix socket binding (including the listener-dependent controller notification timeout). Their logs remain `.cache/final-blockers-{check,go-race,rust,python,parity}.log`; there were no race reports. Automatic approval subsequently permitted the requested local-only gates to run with loopback listeners, still using offline dependencies and synthetic fixtures. All complete gates then passed as recorded above; no test was skipped or weakened to achieve those results. No live integrations, real Kubernetes mounts, container/PVC runtime, hosted CI or production access were exercised. The projected-volume tests emulate Kubernetes layout/rotation locally, including exact deployed names and relevant modes; Linux validation is cross-compilation only. The helper lifecycle tests and interactive PTYs ran natively on Darwin. Filesystem ancestors/read-only mounts and trusted local executable policy remain operator trust boundaries. These changes do not authorize runtime retirement. Historical entries below do not describe this pass.

---

# Eviction, stack context and safe file opens — 2026-10-11

Started in the clean worktree at exact HEAD `8875d1845ef0a19189613151ec8a47fe0a9d94a2`. Repository guidance in `CONTRIBUTING.md` was read; no applicable `AGENTS.md` was present. All evidence uses synthetic local data and fixtures. No push, GitHub mutation, deployment, production access, live provider or cluster contact occurred. Go/Rust/Python coexistence and disabled runtime retirement are preserved.

## Regression-first evidence and changes

[Committed before evidence](docs/validation/p2-before-8875d18.txt) records the commands and failing excerpts (trailing whitespace removed). The full initial log is `.cache/p2-before.log`. Before any production edit, the eviction tests reproduced stale durable hits and fanout for rollback, recovered wall, multiple evictions and provider failure. Python source/caret, Go and .NET fixtures split; the Python source case produced `[3,2,1,1]` instead of `[5,1,1]` with two trailing normal lines. All six FIFO subprocess cases exceeded their two-second bounds: file input, reviewed rules, policy, dashboard report, provider credential and SIGTERM/final reporting.

The initial command was:

```sh
go test -count=1 ./internal/controller ./internal/event ./internal/cli -run 'TestEvictionRetainsDurableLifetime|TestTimestampedStackContext|TestFIFOOpenIsBounded'
```

- **Durable lifetime:** Store retains process-local lifetime metadata independently of template eviction, under its existing mutex, with a hard bound of 10,000 records. Repeated lookups retain the earliest elapsed deadline and smallest configured expiry. Unknown families miss at saturation; new provider results without a registry slot are not persisted. Records are forgotten only after the corresponding row is deleted or found absent. Expiration/rollback deletes the durable row before provider dispatch, so failed refresh cannot resurrect it. The Grouper retains a wall high-water mark independent of partitions and forces refresh while wall time is behind it. A further regression exposed rollback with half a second of elapsed lifetime left after the initial Store fix; its failure is also committed before the high-water fix. Coverage includes the exact wall 1000/1059/1020, elapsed 0/59/60 sequence, recovery, eight evictions, valid remaining lifetime, actual provider errors, unknown responses, saturation and freeing a slot. An older test that explicitly expected resurrection after invalidation now asserts rejection followed by successful fresh publication. Existing TTL, latency, provider-schema and local-metadata serialization regressions pass. SQLite schema and persisted expiry calculation are unchanged; no local lifetime metadata is serialized.
- **Stack context:** A Python frame permits one following indented source line, followed by recognized caret lines. Explicit stack context allows .NET detail markers; Go panic/goroutine context allows bounded frame grammars. Context resets at flush/new normal events. Indentation alone does not merge timestamped normal events; known normal log prefixes also terminate a pending Python source expectation. Tests cover files and Kubernetes, every two-chunk split, byte-at-a-time input, header timestamp/ranges, normal indented lines, redaction, invalid UTF-8, truncation, partial/orphan uncertainty and context reset. The exact six-line fixture yields `[5,1]` in Go and retained Python; Java, Go and .NET fixtures are also covered.
- **File opening:** Shared `internal/safefile` opens read-only with `O_NONBLOCK|O_NOFOLLOW|O_CLOEXEC`, checks the same descriptor for regular type, single link and identity, and registers cancellation immediately after obtaining it. Preliminary lstat rejects known devices/special files without opening them. Regular-file reads keep `O_NONBLOCK` (regular files ignore it); no unsafe flag change after concurrent close is needed. Close is idempotent and joins the cancellation callback. Inputs, gzip inputs, rules, policy, webhook URL files, provider key files and dashboard reports use this path, with existing byte limits and caller context. FIFO, leaf symlink, hardlink, directory and device rejection plus descriptor cancellation are exercised. The Unix socket fixture remains present but cannot bind in this sandbox. Actual child SIGTERM exits 130 and writes equivalent valid stdout/private saved reports with coverage gaps; rejected FIFO input without a signal exits 2 with a final report. Invalid configuration fails before collection/report initialization.

## Adjacent file-path audit

The audit also found blocking reads inside client-go for user-supplied kubeconfig, token, CA, client certificate and key files. Before changing Kubernetes loading, `go test -count=1 ./internal/kube -run TestKubeFIFOFilesBounded` failed all five two-second subprocess bounds (`.cache/p2-kube-before.log`, excerpt committed). Local loading now reads bounded verified descriptors, merges named maps with first-file precedence, resolves origin-relative paths and passes certificate bytes to client-go. Configuration input is capped at 32 files of 1 MiB each; TLS files are capped at 1 MiB and tokens at 16 KiB. A context-aware token wrapper safely rereads regular token files on requests; synthetic transport tests verify precedence, relative paths, token rotation, immutable request headers and fail-closed FIFO replacement without network traffic.

The local loader does not perform client-go's optional kubeconfig migration writes or implicit in-cluster fallback: explicit `--in-cluster` remains the supported in-cluster path. Local certificate/key bytes are snapshots loaded at startup; local TLS file rotation requires restart. Local token rotation remains supported. User-supplied leaf symlinks and hardlinks are rejected intentionally. Fixed Kubernetes service-account projection paths for explicit in-cluster mode remain client-go owned, including their symlink/rotation behavior; they are not user-supplied input paths and were not accessed. Executable authentication plugins remain governed by client-go's existing trust model and were not invoked. State/volume descriptor paths already use nonblocking, no-follow regular-file/owner checks. Report output uses private atomic replacement; its directory open is for synchronization, not user input ingestion. Retained Rust/Python production code is unchanged.

## Exact validation

Darwin arm64; Go 1.27.1, Python 3.14.7, Rust 1.94.0, Node 24.19.0. Commands used:

```sh
export PATH=/opt/homebrew/opt/node@24/bin:$PATH
export GOCACHE="$PWD/.cache/build" GOPROXY=off GOSUMDB=off CARGO_NET_OFFLINE=true PIP_NO_INDEX=1
```

The focused selection below was run normally (`.cache/p2-focused.log`) and with `-race` after the final provider-error/report assertions (`.cache/p2-focused-race.log`); both passed. Analysis has no matching tests in this selection and was tested fully separately.

```sh
go test -race -count=1 ./internal/controller ./internal/analysis ./internal/event ./internal/cli ./internal/provider ./internal/kube ./internal/safefile -run 'TestEvictionRetainsDurableLifetime|TestStoreLifetimeCapacity|TestDurableMonotonicLifetime|TestTimestamped|TestPythonSource|TestFIFOOpenIsBounded|TestKubeFIFOFilesBounded|TestLocalConfigMerge|TestCancellation|TestCredential'
```

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/controller ./internal/analysis ./internal/event` | Passed complete suites on final source; `.cache/p2-core-final.log` |
| `go test -race -count=1 ./internal/event -run TestPythonExactTimestampedFixture` | Passed exact `[5,1]` fixture for both sources; `.cache/p2-exact-race.log` |
| `go test -race -count=1 ./internal/controller ./internal/analysis ./internal/event ./internal/cli ./internal/provider ./internal/kube ./internal/safefile` | Controller/analysis/event full suites passed (47.222s/2.299s/12.257s); no race reports. Full command **failed** at socket-bound CLI/provider/Kubernetes fixtures and safefile Unix-socket creation; `.cache/p2-race.log` |
| `tools/check.sh` | **Failed** at `go test ./...` because TCP/Unix socket binding is denied. CLI notification test times out waiting for its listener-based controller. Formatting, tidy and module-diff checks passed; later stages did not run. `.cache/p2-check.log` |
| `tools/release.sh` | Passed both executable names, classification, private reports, SIGINT/SIGTERM final reports, state-volume/migration/reopen checks, binary/source archives and source-byte/privacy checks; `.cache/p2-release.log` |
| `go vet ./...` and `tools/check-cross.sh` | Passed; Linux amd64, 386, arm cross-builds, not Linux execution; `.cache/p2-vet-cross.log` |
| `python3 -m unittest tests.test_analyzer.ParsingTests -v` | 10 tests passed including retained-Python `[5,1]` parity; `.cache/p2-python-parsing.log` |
| `python3 -m unittest discover -s tests -q` | **Failed**: 120 tests ran, one dashboard class setup error due to denied socket binding; `.cache/p2-python.log` |
| `cargo fmt --check`; `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed; `.cache/p2-rust.log` |
| `cargo test --locked --offline --all-features` | **Failed**: 24 library tests passed, 14 failed at denied listener setup; CLI phase not reached. `.cache/p2-rust.log` |
| `cargo build --locked --offline`; `python3 tests/rust/tui_pty.py target/debug/jevernetes` | Passed build, search, Ctrl-C and SIGTERM with terminal restoration/private reports; `.cache/p2-rust.log` |
| `python3 tools/check_parity.py --run` | **Failed** on its first suite (`go test ./internal/event ./internal/cli`) at the same CLI listener restriction; later inventory suites did not run via this command. `.cache/p2-parity.log` |
| `for file in tests/*_ui.cjs; do node "$file" || exit; done` | Six UI suites passed |
| `for file in internal/dashboard/web/*.js jevernetes/web/*.js; do node --check "$file" || exit; done` | Both trees passed syntax checks |
| `python3 tools/check_release.py`; `python3 tools/check_parity.py`; `node tools/check-release.mjs` | Passed source/privacy checks and 14-workflow/17-suite coexistence inventory |
| `git diff --check`; `git diff --cached --check` | Passed after removing trailing whitespace from the evidence excerpt |

An intermediate build caught a missing context import after threading cancellation into remote configuration loading; it was fixed before the successful focused/release/vet/cross runs. A report assertion initially compared pretty-printed versus compact JSON bytes; it now compares decoded values and checks private mode. These were implementation/test iterations, not environment restrictions. Nonfatal macOS deployment-target linker warnings remain. Logs and build outputs are ignored. No test was disabled or weakened to bypass binding restrictions, and no permissions escalation was available.

## Remaining limits

Full Go/Python/Rust and executable parity gates need a runner permitting local TCP and Unix socket binding. The Unix-socket rejection fixture could not create its socket; the other special-file and cancellation assertions ran. No live integration, hosted CI, production soak, privileged ownership capability or container/PVC runtime was exercised. Monotonic authority remains process-local: a restart cannot recover an elapsed-clock history that was never persisted. Saturation deliberately reduces durable hit rate; rollback forces refresh until the wall catches its high-water mark. No persistent expiry or configured TTL is extended by a cache hit. Ordinary filesystem access permissions govern read-only input ownership; filesystem ancestors/mounts and explicit in-cluster projected credentials remain trusted. Nonblocking opening does not promise deadlines for a malfunctioning filesystem's regular-file I/O. Earlier entries below are historical and do not describe this pass.

---

# Final parity hardening — 2026-10-11

Started in the clean current worktree at exactly `0fe8eaf35966b45664a3a6bf70d55bf088785ea7`. All evidence is synthetic/local. No push, PR mutation, deployment, production access or live provider/cluster contact occurred.

## Reproductions and fixes

Before production edits, the initial executable regressions failed with this command (`.cache/hardening-before.log`):

```sh
GOCACHE="$PWD/.cache/build" GOPROXY=off GOSUMDB=off go test -count=1 ./internal/controller ./internal/event -run 'TestDurableMonotonicLifetime|TestStackContinuationParity|TestTimestampedContinuationSafety'
```

Durable reuse survived backwards wall steps despite advancing monotonic time, delayed durable hits fanned out, and a fresh provider result outlived its elapsed deadline. Timestamped file stacks split into four events and lost combined safety flags. The final regressions additionally anchor wall time at 1000 with genuine monotonic components, cover expiry at 1060/load at 1059/rollback to 1020/wall 1049 after 30 elapsed seconds, and test recovered wall time, fractional remaining lifetime, forward steps, shorter/longer TTLs, delayed publication, and subsecond rollback during provider work. The test-only clock helper alters the reflected monotonic field and verifies both readings; unsupported Go time representations fail explicitly.

`Store.Lookup` now receives a full `time.Time` and attaches a monotonic deadline for precisely the remaining persisted lifetime. Wall bounds and elapsed deadlines are checked independently. Observe/Publish compare wall-only values to detect rollback, retain the original deadline, and force durable refresh after expiry or rejected publication so a row cannot reload with renewed authority. Provider delay consumes the original lifetime; already-invalid fresh results are not persisted. Local observation/deadline metadata is excluded from JSON and rejected by the strict provider schema. SQLite schema and stored JSON remain unchanged.

After timestamp removal, explicit stack markers join file and Kubernetes events. Timestamped normal lines, including indented normal lines, remain separate. Unstamped continuation framing is retained. Go tests check every input split, header timestamps, `[3,1]` line counts, orphan uncertainty, redaction, invalid UTF-8, oversized continuations and partial completion. The retained Python parser has matching timestamped/unstamped file/Kubernetes fixture assertions.

## Exact validation evidence

Darwin arm64; Go 1.27.1, Python 3.14.7, Rust 1.94.0. Shell gates used Node 24 from `/opt/homebrew/opt/node@24/bin`, with the following environment (Go dependency cache remained read-only; build output was local):

```sh
export PATH=/opt/homebrew/opt/node@24/bin:$PATH
export GOCACHE="$PWD/.cache/build" GOPROXY=off GOSUMDB=off CARGO_NET_OFFLINE=true PIP_NO_INDEX=1
```

| Exact command | Result |
| --- | --- |
| `go test -count=1 ./internal/controller ./internal/analysis ./internal/event` | Passed all affected packages; `.cache/hardening-affected.log` |
| `go test -race -count=1 ./internal/controller ./internal/analysis ./internal/event` | Passed all affected packages; no race report; `.cache/hardening-affected-race.log` |
| `go test -count=1 ./internal/controller ./internal/analysis ./internal/event -run 'TestDurableMonotonicLifetime\|TestStackContinuationParity\|TestTimestampedContinuationSafety\|TestEngineDurableTTL\|TestDurableLifetime'` | Passed; analysis had no matching tests in this focused command (its full suite passed separately); `.cache/hardening-after.log` |
| `python3 -m unittest tests.test_analyzer.ParsingTests -v` | Nine tests passed, including the Python parity fixture |
| `tools/check.sh` | **Failed** at `go test ./...`: sandbox denied local socket binding in CLI, inspection, Kubernetes and provider tests. CLI notification test also timed out waiting for delivery from its listener-based controller. Formatting/tidy/dependency-diff checks passed. Later script stages did not execute; separate results below do not convert this gate to a pass. `.cache/hardening-check.log` |
| `tools/release.sh` | Passed both executable names, classification/private-report/signals, SQLite migration/reopen/state-volume smoke and source/archive byte/privacy checks; `.cache/hardening-release.log` |
| `go vet ./...` | Passed; `.cache/hardening-vet.log` |
| `tools/check-cross.sh` | Passed Linux amd64, 386 and arm cross-builds; not executed on Linux; `.cache/hardening-cross.log` |
| `for file in tests/*_ui.cjs; do node "$file" \|\| exit; done` | Six UI suites passed |
| `for file in internal/dashboard/web/*.js jevernetes/web/*.js; do node --check "$file" \|\| exit; done` | Passed |
| `python3 -m unittest discover -s tests -q` | **Failed**: 119 tests ran; one class-setup error in dashboard HTTP tests due to socket-bind permission denial; `.cache/hardening-python.log` |
| `python3 tools/check_release.py`; `python3 tools/check_parity.py`; `node tools/check-release.mjs` | Passed source/privacy checks and 14-workflow/17-suite inventory; inventory execution with `--run` was not performed |
| `cargo fmt --check`; `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed |
| `cargo test --locked --offline --all-features` | **Failed**: library tests 24 passed, 14 failed at local listener setup with permission denial; CLI test phase was not reached |
| `cargo build --locked --offline` | Passed; `.cache/hardening-rust-build.log` |
| `python3 tests/rust/tui_pty.py target/debug/jevernetes` | Search, Ctrl-C and SIGTERM passed, with terminal restoration and private reports; `.cache/hardening-tui.log` |
| `git diff --check`; `git diff --cached --check` | Passed |

The broader targeted Go commands that also included `./internal/provider` failed at its HTTP listener fixtures, including under race; the three affected packages passed in both runs. Nonfatal macOS linker warnings and denied writes to the shared Go module stat cache appeared during some builds; build commands listed as passed exited zero. Logs/build artifacts remain ignored under `.cache/`, `dist/` and `target/`. No test or gate was weakened to bypass sandbox restrictions.

## Remaining limits

Full Go, Python and Rust gates require rerunning where local loopback binding is permitted; this session cannot claim those gates passed. No hosted CI, live integrations, production soak or container/PVC runtime was exercised. Existing foreign-owner capability skips remain. Monotonic lifetime is intentionally process-local: a rollback across restart that remains between durable creation and expiry cannot be distinguished from elapsed wall time. Runtime retirement remains disabled. Earlier entries below are historical and do not describe this pass.

---

# Blind-review P2 implementation — 2026-10-11

Started on clean `fix/go-full-parity-20261011` at exactly `c89497263cdca871fa705f53e5b7ef35d0b78115`. Changes and commits are confined to this worktree. All provider, teacher, cluster, stream and notification fixtures are synthetic/local. No live providers/clusters, production access, push, deployment or GitHub mutation occurred. Rust/Python coexistence is retained and retirement remains disabled.

## Reproductions and fixes

Before the production fixes, the following command failed all seven default-mode sample-limit outcomes, the near-expiry boundary/past-boundary/shorter-TTL/longer-TTL cases, rollback after a durable hit, and both generated and saved teacher-veto paths. The only production plumbing present for this reproduction was optional injectable clocks, still using the original cache behavior. Evidence: `.cache/blind-before.log`. An initial restricted-cache attempt could not write the shared Go build cache; the successful reproduction and validation had local cache/socket permission and kept dependency networking disabled.

```sh
GOPROXY=off GOSUMDB=off go test -count=1 ./internal/controller ./internal/analysis -run 'TestEngine(LimitedDefault|DurableTTL|LearningVeto)|TestLearningSaved'
```

The same command now passes (`.cache/blind-after.log`). Stateful regression coverage includes:

- `TestEngineLimitedDefaultReassessmentAcrossRestart`: persist routine A, close/reopen SQLite, submit nine distinct normalized request IDs with batch size 16 and default cache mode, then request A in the same engine and after another restart. Routine, safe-important, unknown, uncertain, invalid, provider-error and wrong-count responses all leave zero durable rows and cannot resurrect A. Unsafe evidence now deletes its identified durable family before dispatch, independently of rescore/audit mode; it cannot publish even a valid important result.
- `TestEngineLearningVetoAcrossRestart`: adaptive classic Drain, stable review ID, teacher threshold two, reversed sample order and repeated batches over two SQLite/engine restarts. Vetoed unknown results retain `review_required`, produce no teacher calls/escalations/reuse/fanout metrics and publish no durable rows. `TestLearningSavedEscalationRespectsVeto` first generates a real saved escalation, applies the Grouper veto and repeats matching batches; saved teacher state cannot overwrite either the primary result or the independently reviewed unknown fanout.
- `TestEngineDurableTTLAcrossRestart`: controllable clocks, original 60-second lifetime, durable hit after restart at t=59, successful reuse within the remaining second, then provider dispatch exactly at t=60 and just after. Current shorter TTL clamps to original creation plus the shorter duration; a longer current TTL cannot extend stored expiry. Tests also exercise restart before creation, rollback after a hit (including rollback within the original lifetime), and provider latency reaching the expiry boundary.
- `Store.Lookup` carries creation and the smaller of stored expiry/current configured lifetime through local-only judgment metadata. `CachedJudge` also stamps newly stored judgments with the same original window. Engine/Grouper publication checks the current clock and caps expiry, without renewing it on hits. Observed in-process clock rollback forces durable refresh; teacher entries are cleared on rollback. `TestLearningLifetimeDoesNotSlideOrSurviveRollback` covers saved teacher reuse at t=59, expiry at t=60 and backward clock movement.
- `TestUnsafeDossierInvalidatesBeforeDispatch` checks each identified sensitive, truncated, evidence-limited, sample-limited and security dossier against real SQLite, including deletion inside the provider callback and no publication afterward. `TestLearningLimitedBatchPreservesIndependentReview` covers both saved and newly generated teacher paths for a nine-sample batch. `TestDurableLifetimeMetadataStaysLocal` checks effective bounds, JSON exclusion and rejection of provider-injected lifetime fields.

## Neighboring-path audit

Inspected identity construction and scope, complete tickets/generations, Observe/Publish/Invalidate, batching and fanout, all durable cache modes, provider response validation, review IDs, reviewed artifacts, teacher candidate state and reports, CLI provider contracts, Store lookup/write/schema migration and checkpoint/report serialization. Existing regressions continue to cover safe audit replacement versus failed/unsafe audits, explicit rescore and veto, stale-generation durable publication and learning, reversed discovery/eviction order, source/pod scope, baseline, masking/strategy, protected literals, provider contract and reviewed rule version.

Ineligible events still receive no reusable family ticket; no family identity is inferred from unsafe evidence to broaden reuse. Identified unsafe dossiers invalidate their existing family. Teacher escalation remains process-local and never becomes a durable verdict or an active reviewed rule. The new learning gate preserves human veto, stale-generation rejection and limited-evidence independent review in both saved-state reads and generated escalation fanout. Review/report and cache identity namespaces are unchanged.

SQLite schema and stored judgment JSON are unchanged: existing v2 family rows retain their original timestamps, and local lifetime fields cannot be supplied through strict provider JSON. Old v1 sample-specific rows remain unreachable as before. Store rollback checks still reject times before persisted creation; Grouper additionally detects backward movement it observes in the running process. Across restart, a wall-clock rollback that remains between persisted creation and expiry cannot be distinguished from ordinary elapsed time; no claim of a persistent monotonic clock is made. Expiry is exclusive, and SQLite's existing whole-second precision remains conservatively bounded.

## Executed offline validation

Environment: Darwin arm64, Go 1.27.1, Node 24.19.0, Python 3.14.7 and Rust 1.94.0. Commands ran from this worktree with the following settings (Cargo commands also explicitly use `--offline`):

```sh
export PATH=/opt/homebrew/opt/node@24/bin:$PATH
export GOPROXY=off GOSUMDB=off CARGO_NET_OFFLINE=true PIP_NO_INDEX=1
```

| Exact command | Result |
| --- | --- |
| `gofmt -w internal/analysis/engine.go internal/analysis/group.go internal/analysis/learning.go internal/analysis/learning_test.go internal/controller/judge.go internal/controller/store.go internal/controller/blind_review_test.go internal/event/event.go`; `gofmt -l cmd internal examples` | Passed; no unformatted files |
| `go test -count=1 ./internal/controller ./internal/analysis ./internal/event ./internal/provider` | All affected packages passed; `.cache/blind-targeted.log` |
| `go test -race -count=1 ./internal/controller ./internal/analysis ./internal/event ./internal/provider` | All affected packages passed, no race report; `.cache/blind-race.log` |
| Focused reproduction command above | Passed after fixes; `.cache/blind-after.log` |
| `tools/check.sh` | Passed: tidy/no dependency diff, all Go tests/race/vet, Linux amd64/386/arm cross-builds, six JS suites across both trees, 122 Python tests, source/privacy checks and 14-workflow/17-suite parity inventory with retirement disabled; `.cache/blind-check.log` |
| `tools/release.sh` | Passed: both Go executable names, classification/private-report/signals, SQLite migration/reopen/state-volume smoke, binary/source archives and source-byte/privacy checks; `.cache/blind-release.log` |
| `cargo fmt --check` | Passed |
| `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed |
| `cargo test --locked --offline --all-features` | 38 library and 7 CLI tests passed |
| `cargo build --locked --offline --release` | Passed |
| `python3 tests/rust/tui_pty.py target/release/jevernetes` | Search, Ctrl-C and SIGTERM passed, with terminal restoration and private reports |
| `cargo package --list --allow-dirty --offline` | Passed; coexistence sources retained; `.cache/blind-rust-package.txt` |
| `.venv/bin/python -m build --no-isolation --outdir dist/python` | Wheel and sdist built; `.cache/blind-python-build.log` |
| `python3 tools/check_release.py --dist dist/python` | Both companion archives passed; `.cache/blind-python-archives.log` |
| `.venv/bin/python tools/check_python_package.py dist/python/*.whl` | Installed entry point/assets and all 122 tests passed; `.cache/blind-python-installed.log` |
| `for file in internal/dashboard/web/*.js jevernetes/web/*.js; do node --check "$file"; done` | Both trees passed; `.cache/blind-js-syntax.log` |
| `go test -count=1 -v ./internal/controller -run 'TestPrivateStateDirectory/foreign_owner\|TestPrepareVolumeRejectsConflicts/foreign-owner\|TestWALUnsafeSHMRejectedBeforeInitialization/foreign-owner'` | Confirmed the three existing ownership-capability skips; `.cache/blind-privileged.log` |
| `git diff --check` | Passed |

Rust command output is retained in `.cache/blind-rust.log`. Generated artifacts/logs stay ignored under `.cache/`, `dist/` and `target/`. Existing macOS minimum-deployment-target linker warnings were nonfatal. No required runnable gate was skipped.

## Unverified boundaries

No live model accuracy/billing, live Kubernetes/provider/notification integration, production soak, hosted CI, container/PVC execution or privileged foreign-owner checks were performed. Linux targets were cross-built, not executed. The three named ownership subtests require capabilities unavailable to UID 501 and retain their existing skips. These offline results do not authorize runtime retirement. Earlier validation entries below are historical and do not describe production access during this implementation pass.

---

# Stable audit families and review identities — 2026-10-11

This narrow follow-up starts from the verified clean branch `fix/go-full-parity-20261011` at exactly `3558887ff88c4f88deba61e83d2451e962e73f53`. Work and validation use local synthetic providers, SQLite databases, streams, loopback servers and Git fixtures. No production access, live provider/cluster calls, pushes, deployments or GitHub changes occurred. Staged Rust/Python/Go coexistence and all prior fixes remain in place.

## Identity dependency audit and changes

Before editing, the audit traced `TemplateID`, `VersionID`, full `Ticket` equality/`Compatible`, `Current`, `Publish`, `Invalidate`, batch grouping/fanout, reviewed-rule matching, teacher learning/refinement, assessment/report/checkpoint output, review flags, cache keys/provider contracts, SQLite lookup/deletion/publication, metrics and their tests.

- Review-facing template identity now hashes semantic source scope, version and baseline importance. Version identity hashes the normalization namespace, strategy/masking, template kind and canonical template. Reviewed templates already include rule ID/version; their full source scope is retained. Discovery order and eviction do not enter either identity.
- Generation remains a separate process-local counter in full ticket equality. Stale tickets cannot publish or invalidate a replacement. Engine marks already-evicted dossiers as durable-cache bypass before dispatch, because CachedJudge runs before Engine's final publication check. Learning also preserves stale results as unknown, including saved escalations and same-batch teacher reviews.
- Durable cache namespace `go-group-cache-v2` has one entry per semantic family/provider contract, independent of literal samples. Audit, veto and rescore deletion happens before dispatch and before the evidence-safety gate; limited-sample audits invalidate too. Only validated reusable replacements can be stored, and review-veto/stale-ticket results cannot populate the cache.
- Checkpoint schema/output fields remain diagnostic only: stable opaque template/version IDs plus separate generation/counters. No checkpoint is restored as safety authority. Metrics remain aggregate counters, with no identity labels; veto regression tests assert zero reuse/fanout counters.
- Host examples for remote/dashboard/controller and related Go demo/template-learning examples use `jevernetes-go`. Documentation explicitly distinguishes the Go image's internal `/usr/local/bin/jevernetes`; Rust and Python command examples and container commands are preserved.

## Reproduced counterexamples and regression evidence

Before production-code edits, `go test -count=1 ./internal/controller -run 'TestEngine(DurableReassessment|ReviewIdentity)'` failed all six audit-outcome subtests and both veto-capacity subtests against the starting implementation (`.cache/family-before.log`). Sample A and sample B differ only in a normalized request ID: B's audit left A's old routine verdict reusable after SQLite close/reopen, including when B's safe replacement should have changed it. Reversed discovery order bypassed the recorded veto and allowed fanout.

Passing regression coverage now includes:

- `TestEngineDurableReassessmentAcrossRestart`: Engine → CachedJudge → real SQLite close/reopen; ordinary persisted hits; sample-A seed/sample-B audit/sample-A restart; safe replacement and unknown, uncertain, invalid, provider-error and wrong-response-count outcomes; repeated vetoes with no durable publication.
- `TestEngineReviewIdentityAcrossRestartAndEviction`: reversed discovery order after restart, capacities 1 and 8, eviction/recreation, repeated multi-sample batches, stable template/version IDs, no vetoed fanout/reuse and zero durable rows after each vetoed batch.
- `TestEngineLimitedAuditInvalidatesDurableFamily`: nine distinct normalized request IDs exceed the sample cap; the audit deletes the prior family entry and a reopened engine must call the provider.
- `TestEngineStaleGenerationCannotSeedDurableCache`: A/other/B in one capacity-1 batch; the evicted generation's safe response cannot seed SQLite when the current generation returns unknown; reopen still requires a provider call.
- `TestEngineDurableSemanticIdentityIsolation`: SQLite restarts prove intended normalized-sample and Drain pod reuse, while source scope, baseline, protected template literals, masking/strategy, provider contract, exact-source pod identity, reviewed-rule version and reviewed full-source pod identity remain isolated.
- `TestSemanticIdentityKeepsGenerationSeparate`: stable IDs across recreation, unequal tickets/generations, stale publication/invalidation rejection and checkpoint identity/counter separation.
- `TestLearningCannotOverrideStaleGeneration`: both saved and newly generated learning escalations preserve stale results as unknown while escalating current results. Both variants failed before the learning guard (`.cache/family-learning-before.log`) and passed with an uncached race run afterward.

## Executed validation

Environment: Darwin arm64, Go 1.27.1, Node 24.19.0 with `/opt/homebrew/opt/node@24/bin` first in `PATH`, Python 3.14.7, Rust 1.94.0. Go gates use `GOPROXY=off GOSUMDB=off`; Cargo uses `--offline`; Python build/install uses local dependencies with `PIP_NO_INDEX=1`. Logs and build artifacts remain ignored under `.cache/family-*`, `dist/` and `target/`.

| Check | Result |
| --- | --- |
| `gofmt` on all changed Go files; `gofmt -l cmd internal examples` | Passed; no unformatted files |
| `go test -count=1 ./internal/analysis ./internal/controller` | Passed |
| `go test -race -count=1 ./internal/analysis ./internal/controller ./internal/provider ./internal/cli ./internal/kube ./internal/report` | All six packages passed; no race report |
| `go test -race -count=1 ./internal/analysis -run 'TestLearningCannotOverrideStaleGeneration\|TestSemanticIdentityKeepsGenerationSeparate'` | Passed |
| `tools/check.sh` with Node 24, rerun after the final code change | Passed: tidy/no dependency diff, all Go packages, race, vet, Linux amd64/386/arm cross-builds, six JS suites, 122 Python tests, privacy/source checks and the 14-workflow/17-suite coexistence inventory |
| `tools/release.sh` with Node 24 | Passed: both Go names, offline classification/private reports/signals, SQLite migration/reopen and state-volume smoke, source/archive byte/privacy checks |
| `cargo fmt --check` | Passed |
| `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed |
| `cargo test --locked --offline --all-features` | 38 library and 7 CLI tests passed |
| `cargo build --locked --offline --release` | Passed |
| `python3 tests/rust/tui_pty.py target/release/jevernetes` | Search, Ctrl-C and SIGTERM passed; terminal restored and private reports saved |
| `cargo package --list --allow-dirty --offline` | Passed; coexistence sources retained |
| `PIP_NO_INDEX=1 .venv/bin/python -m build --no-isolation --outdir dist/python` | Wheel and sdist built |
| `python3 tools/check_release.py --dist dist/python` | Both companion archives passed |
| `.venv/bin/python tools/check_python_package.py dist/python/*.whl` | Installed entry point/assets and 122 Python tests passed |
| `node --check` for every `internal/dashboard/web/*.js` and `jevernetes/web/*.js` | Passed |
| `git diff --check` | Passed |

One restricted test attempt was blocked by writes to the shared Go cache; successful runs had local cache/socket permission and used only synthetic fixtures. Existing macOS deployment-target linker warnings were nonfatal.

## Remaining limitations

Opaque IDs from the prior generation-dependent scheme cannot be translated: existing operator vetoes must be reissued using stable IDs from a new Go report. Semantic template/rule-version changes intentionally get new identities. Old v1 sample-specific cache rows are unreachable by this runtime and remain subject to normal bounded capacity eviction; this change does not rewrite the SQLite schema or migrate those verdicts.

No live provider accuracy/billing, production Kubernetes/SPDY compatibility, container/PVC execution, hosted CI or production soak was validated. Linux targets were cross-built, not executed. The three existing ownership subtests remain skipped as UID 501: `TestPrivateStateDirectory/foreign_owner`, `TestPrepareVolumeRejectsConflicts/foreign-owner`, and `TestWALUnsafeSHMRejectedBeforeInitialization/foreign-owner`. Checkpoints and transport cursors remain process-local/diagnostic as previously documented. These local checks do not authorize retirement of the retained runtimes. Earlier validation below is historical evidence, not a claim of production access during this follow-up.

---

# Independent review corrections — 2026-10-11

This follow-up starts from clean branch `fix/go-full-parity-20261011` at `fba6e375033d228374224b3b8b3fe8688f7796a1`. Work is confined to this worktree; tests use local synthetic providers, streams, Git repositories and SQLite state. No live providers/clusters, production access, pushes, deployments or remote GitHub changes are involved. Staged coexistence and the runtime-retirement prohibition remain in force.

## Regression evidence

- `TestEngineDurableReassessmentAcrossRestart`: Engine → CachedJudge → local provider fixture, real SQLite close/reopen, ordinary persisted hits, occurrence-triggered `stable_audit`, updated safe cache, invalid/unknown/failed audit invalidation, and repeated review veto across restarts with no durable publication or fanout.
- `tests/test_ci_parity.py`: a local `file://` shallow clone fails the fixed-base parity check; full history passes; removing a retained companion asset still fails. The test also requires explicit `fetch-depth: 0` on the Go CI checkout. This repository/CI-only test stays in the full source archive, outside the companion-only sdist.
- `TestLiveTerminatedHeaderTransportFailureThroughReuseGates`: a newline-terminated Kubernetes header followed by pipe transport failure is truncated and parse-uncertain through live framing, Engine, durable cache and policy review. Clean EOF behavior remains covered by existing tests.
- `TestDuplicateJSONPreservesRedactedEvidence`, `TestDuplicateJSONBoundsAndContinuation`, and `TestDuplicateJSONThroughReuseAndPolicyGates`: conflicting and escaped duplicate keys, nested secret-shaped values, continuation propagation, bounded evidence, repeated redaction, retained baseline signals, no grouping/durable reuse and policy review despite confident routine judgments.
- `TestMaxEventsDrainsOnlineAnalysisBeforeAndDuringProviderCall` and `TestMaxEventsStopsQuietLiveProducerAndDrainsQueue`: an open stdin writer and a quiet live producer stop after exactly N admissions while blocked/queued online work finishes. A following header may be needed to finalize the Nth multiline event; no N+1 finalized event or EOF is needed. Queue drops do not count as admissions.
- `TestControllerDeliversNotificationOnlyJSONOnStdout`: at least one actual notification is written and acknowledged in SQLite before collection ends; every stdout record decodes strictly as a notification, and plain evidence, judgments and summary appear on stderr.

## Follow-up validation

All gates below passed sequentially on Darwin arm64 with Go 1.27.1, Node 24.19.0 (`/opt/homebrew/opt/node@24/bin` prepended to `PATH`), Python 3.14.7 and Rust 1.94.0. Go dependency access was disabled with `GOPROXY=off GOSUMDB=off`; Cargo used `--offline`, and Python builds/installations used local dependencies. Local build outputs and `review-*` logs remain ignored under `.cache/`, `dist/` and `target/`.

| Check | Result |
| --- | --- |
| `gofmt` on modified Go files; formatting gate in `tools/check.sh` | Passed |
| `go test -count=1 ./internal/event ./internal/controller ./internal/kube ./internal/cli ./internal/analysis ./internal/provider` | All six packages passed |
| Same six packages with `go test -race -count=1` | Passed; no race report |
| `tools/check.sh` with Node 24 | Passed: tidy/no dependency diff, all Go packages, race, vet, Linux amd64/386/arm cross-builds, six JS suites, 122 Python tests, parity inventory and source/privacy checks |
| `tools/release.sh` with Node 24 | Passed: both binary names, offline classification, private reports, SIGINT/SIGTERM, state migration/reopen smoke and source archive checks |
| `PIP_NO_INDEX=1 .venv/bin/python -m build --no-isolation --outdir dist/python` | Wheel and sdist built |
| `python3 tools/check_release.py --dist dist/python` | Both companion archives passed |
| `.venv/bin/python tools/check_python_package.py dist/python/*.whl` | Installed entry point/assets and 122 tests passed (including the repository-level CI fixture) |
| `cargo fmt --check` | Passed |
| `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed |
| `cargo test --locked --offline --all-features` | 38 library and 7 CLI tests passed |
| `cargo build --locked --offline --release` | Passed |
| `python3 tests/rust/tui_pty.py target/release/jevernetes` | Search, Ctrl-C and SIGTERM passed; terminal restored and private report saved |
| `cargo package --list --allow-dirty --offline` | Passed; coexistence sources retained |
| `git diff --check` | Passed |

The initial restricted test attempt could not write the shared Go cache or bind local test sockets. The successful runs had local cache/socket permission and still used only hermetic fixtures. Existing macOS deployment-target linker warnings remained nonfatal. No production workaround or live-service substitute was used.

## Limits

These tests establish local control flow, conservative evidence handling, cache behavior and packaging/coexistence contracts. They do not establish live model accuracy/billing, production Kubernetes compatibility, container/PVC execution or a production soak. The CI history test is hermetic; no hosted workflow is run. Non-root ownership tests retain their existing privilege-dependent skips. The earlier validation below is the pre-review record, including its then-current runtime versions and test counts.

---

# Full-parity staged-coexistence validation — 2026-10-11

This run started at `629918de491be1fdbf45119ec89e06fb247c1a28`, compared with main at `3a08dcb1c3611c0014b000074006701ad767bfa2`, on the isolated `fix/go-full-parity-20261011` branch. No live providers, clusters, production systems, pushes, deployments or PR actions were used. Tests use synthetic evidence and local fakes. Build dependencies came from local caches.

## Current recommendation

**CONDITIONAL PASS for staged coexistence.** The existing Rust terminal/search runtime and Python investigation runtime remain supported, packaged and tested. This is not approval to remove them or to claim a complete Go-native investigation product. `tests/parity.json` and `python3 tools/check_parity.py --run` form the executable acceptance inventory; its retirement gate requires the base runtime/test assets to remain active. The detailed status matrix and rollback/import instructions are in [migration](docs/migration.md).

The blockers are addressed by restoring the working investigation implementations and their original tests; separating Go release names; accepting schema-1 reports; importing human review rules without replacement; separating intake cancellation from admitted analysis; exiting exhausted controllers nonzero under supervision; emitting plain evidence and preserving notification JSON; byte-bounded report pagination with explicit completeness; header/end-based Go multiline framing; descriptive TypeSafe criteria with per-group attribution; and real local HTTP/SPDY/package/state integration tests.

## Executed checks and results

Environment: Darwin arm64, Go 1.27.1, Rust 1.94.0, Python 3.14.7, Node 20.10.0. Linux amd64, 386 and arm binaries were cross-compiled, not executed. macOS linker deployment-target warnings were emitted; no linker failure occurred.

| Check | Result |
| --- | --- |
| `gofmt -l cmd internal examples` | No output |
| `go mod tidy` and `git diff --exit-code -- go.mod go.sum` | Exit 0; no dependency changes |
| `go test ./...` (also captured with `-json`) | 9 packages passed; 128 top-level tests passed, 220 passing test/subtest records; 0 failures; 3 subtests skipped |
| `go test -race ./...` | 9 packages passed; no race report |
| `go vet ./...` | Exit 0 |
| `tools/check.sh` | Exit 0: formatting/tidy, Go/race/vet, three Linux cross-builds, Python, all six JS suites, inventory and source/security checks |
| `python3 tools/check_parity.py --run` | 14 workflows, 17 distinct executable suite commands; exit 0 |
| Python source acceptance | 121 tests passed |
| JavaScript acceptance | All six suites passed: context, Go dashboard, grouping, prompt, review and search |
| `cargo fmt --check` | Exit 0 |
| `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Exit 0 |
| `cargo test --locked --offline --all-features` | 38 library tests and 7 CLI tests passed; no failures |
| `cargo build --release --locked --offline` | Exit 0 |
| `python3 tests/rust/tui_pty.py target/release/jevernetes` | Search, Ctrl-C and SIGTERM scenarios passed; terminal restored and private report saved in each |
| `tools/release.sh` | Exit 0: Go executable/alias, offline classification, private reports, signals, state-volume migration/reopen smoke, source archive byte/privacy checks |
| Python wheel and sdist build | Built with cached build/setuptools/wheel dependencies; both archives passed privacy checks |
| `tools/check_python_package.py` against built wheel | Entry point and packaged web assets passed; all 121 Python tests passed from a temporary directory using installed site-packages |
| `cargo package --list --allow-dirty --offline` | Exit 0; restored Rust sources/tests and coexistence documentation included |
| `git diff --check` | Exit 0 |

The three skipped non-root checks are `TestPrivateStateDirectory/foreign_owner`, `TestPrepareVolumeRejectsConflicts/foreign-owner`, and `TestWALUnsafeSHMRejectedBeforeInitialization/foreign-owner`. They require ownership-changing privileges. Other symlink/hardlink/FIFO/mode, crashed WAL, hot-journal, schema migration and private-volume cases ran.

The initial restricted sandbox denied Python loopback binding; the same hermetic suite passed with local socket permission. One Go validation attempt overlapped the Rust release build and raced a removed temporary directory under `target/`; final gates were serialized after the Rust build. There was no production workaround or skipped test to hide either environment issue.

## Focused acceptance evidence

- `internal/cli/lifecycle_test.go`: caps before/during blocked provider analysis preserve admitted judgments; duration expiry drains in-flight work; three supervised process cycles reopen the same SQLite state and terminate on provider budget exhaustion with hold and inspection enabled; plain evidence and notification stdout remain distinct.
- `internal/dashboard/report_test.go`: 301 large events, both report schemas, escaped JSON response limits, complete pagination without duplicates/loss, literal query across all retained events, hostile query rejection, explicit oversized-event markers and conservative legacy completeness.
- `tests/test_migration.py`: unchanged source bytes, private destination, identical rerun without rewriting, conflicting/malformed/duplicate inputs, interrupted publication, scope/disabled-state/acknowledgement preservation and reversal to original judgments.
- `internal/provider/http_contract_test.go`: actual serialized HTTP requests and typed decoding for all three fixed-host adapters through local-only injected transports; TypeSafe descriptions, group indexes, evidence IDs, source attribution and metered usage. This does not measure model accuracy.
- `internal/kube/http_contract_test.go`: actual client-go HTTP discovery pagination, regular/init/ephemeral/previous snapshots, per-open identity checks, transport timestamps, reconnect wire query and replay suppression.
- `internal/inspect/spdy_test.go`: real SPDY handshake, error/data streams, loopback port forward, HTTP inspection handler/client, credential separation and joined cancellation against a fake Kubernetes endpoint.
- Existing controller suites and native release smoke exercise versioned SQLite upgrades, preserved committed WAL data, interrupted migration/conflict refusal, outbox/delivery/eviction/backpressure and manifest-aligned private-volume preparation.

## Limits and remaining verification

No actual container image build/run, Kubernetes mount/fsGroup upgrade, live provider semantic/billing validation, production cluster/SPDY proxy compatibility or long-duration production soak was performed. Container guarantees here are manifest/security assertions and host filesystem/process simulations of the exact preparation command. Cross-build success does not establish Linux execution. The three privileged ownership subtests remain unverified locally. Live source cursors remain process-local and collection coverage is explicitly partial. A supervised restart renews process provider budgets; it is not a daily spending cap. Idle Go singleton events wait for a following header or stream end to avoid splitting delayed traces. Human reviews continue in the supported companion and are not silently translated into Go model-template rules.

Logs and local binaries/archives are in ignored `.cache/`, `target/` and `dist/`. They are not shipped as source or committed. The historical record below predates this staged change: its runtime-retirement statements, old file counts and prior validation environments are historical only, not current claims.

---

# Production-hardening validation

## SHM secure-opening correction (2026-10-06)

This continuation started in a clean worktree at exactly `81212ad894767790d8875a48bc0f87ed1aaec1f2`. Before changing the opener, `go test ./internal/controller -run '^TestWALUnsafeSHMRejectedBeforeInitialization$' -count=1 -v` failed against real crashed WAL produced by the pinned driver: the writable SHM hardlink was accepted and its external target changed from 118,784 to 32,768 bytes; a mode-0400 FIFO blocked until the five-second subprocess deadline. The mode-0600 FIFO returned an error on this Darwin arm64 host without blocking. Those outcomes are platform-specific observations, not an assertion that all FIFO modes block everywhere. The before/after logs are retained in ignored `.cache/shm-before.log` and `.cache/shm-after.log`.

Source inspection used only the cached `modernc.org/sqlite v1.46.1` generated Unix implementations for Darwin arm64 and Linux amd64/386/arm. `_unixShmMap` reaches `_unixOpenSharedMemory`, which calls `_robust_open` directly with `O_RDWR|O_CREAT|O_NOFOLLOW`, followed by an `O_RDONLY|O_NOFOLLOW` fallback without `O_NONBLOCK`. `_unixLockSharedMemory` takes SQLite's DMS lock and truncates the SHM descriptor to three bytes before rebuilding its index. On Linux amd64 these operations occur near lines 23704/23707 and 23597 of `lib/sqlite_linux_amd64.go`; the corresponding paths were inspected in all three Linux target files and the native Darwin file. In contrast, `_sqlite3WalOpen` and rollback-journal creation/hot-journal recovery reach `_sqlite3OsOpen`, which dispatches `FxOpen` through the selected VFS. No adjacent WAL/journal bypass fix is supported by that source evidence or included here.

`Open` now checks existing SHM while holding the controller writer lock, before registering or initializing SQLite. Relative to its retained private-directory descriptor, no-follow metadata rejects special files, then a no-follow/nonblocking/read-only `openat` and `privateStat` enforce regular type, current ownership, private mode and a single link. Validation does not create, chmod, truncate or unlink SHM. Missing SHM remains SQLite's responsibility: the driver uses the verified database's private mode. Proactive creation would leave unused sidecars on normal DELETE databases and would not eliminate root/same-identity replacement. The restrictive directory/ancestor contract and default SQLite locking, synchronization and recovery remain intact. The documentation explicitly retains the race between validation (including absence) and SQLite's later path-based SHM open; the eventual SHM descriptor is not verified by this preflight.

Focused tests reject hardlink, symlink, public-mode, directory, writable FIFO and read-only FIFO leaves without changing external bytes, database/WAL bytes or SHM identity/mode/size. Foreign-owner coverage exists but requires root and is skipped on this non-root host. The WAL fixture commits 2,000 rows and exits without closing SQLite; an immutable read confirms those rows are absent from the main database, proving that recovery depends on the real WAL. Preparation is tested with legacy, empty-private-destination, existing-private and missing-SHM layouts. Database/WAL/SHM/lock bytes survive repeated preparation unchanged, repaired modes are private, all committed row keys/timestamps/decisions survive recovery and a second reopen, `PRAGMA integrity_check` returns `ok`, journal mode returns to DELETE, and WAL/SHM are removed by SQLite. Normal DELETE opens never create SHM. A host rename models the direct subPath mount; no actual mount/runtime behavior is claimed.

The same-image UID/GID-65532 init container, fsGroup, subPath migration, preparer implementation and their existing tests are preserved. Native state smoke additionally rejects an unsafe SHM hardlink even on a DELETE database, preserves the external target and state bytes, confirms no unused SHM, and retains the manifest-driven preparation/migration checks. Its child environments are empty and listener validation stops execution before cluster/provider setup.

Final offline gates, using `.cache/env.sh` (`GOPROXY=off`, `GOSUMDB=off`, local toolchain):

- `gofmt` on cmd/internal/examples, `go mod verify`, `go test ./...`, `go test -race ./...`, `go vet ./...`: pass. The final `tools/check.sh` passes, including tidy/module-diff checks, all six UI suites and diff checks. Logs: ignored `.cache/shm-check.log`.
- Linux amd64/386/arm cross-builds through `tools/check-cross.sh`: pass; `file` confirms stripped static ELF executables for all three architectures. Linux binaries were compiled, not executed. Native tests ran on Darwin arm64.
- `node --check tools/smoke.mjs` and `tools/release.sh`: pass, including native CLI/report/signal/state smoke with empty child environments. A redundant successful reopen initially invalidated the smoke fixture's earlier database-byte snapshot; removing that extra reopen preserves the original migration assertion, and the final release run passes. Log: ignored `.cache/shm-release.log`.
- Source/archive byte comparison and privacy checks: pass for 123 intended files (115 text), with zero credential/personal-path signatures, retired-language references or private/build paths. The source archive was refreshed after this validation record was finalized. Binaries, archives and logs remain ignored.
- Final staged and unstaged diff checks pass. Dependencies, deployment manifest, volume preparer and its pre-existing tests are unchanged.

The implementation sandbox could not create the linked-worktree `index.lock`, so the host performed the final staging and amendment only after the complete diff and all gates above were independently verified. This validation section is incorporated in the same single hardening commit; both the implementation and review worktrees were clean at handoff.

No Chrome, network resources, Kubernetes API, providers, credentials, deployment, push or remote Git operation was used. No container build or live fsGroup/subPath validation was attempted. Existing native macOS minimum-version linker warnings remain non-fatal. This section supersedes earlier gate counts and historical environment claims for this continuation.

## Deployment-volume correction (2026-10-06)

This narrowly scoped continuation starts at `55cfd565c756bddcd4e438c095f008c4a8ecc2e7`. The supplied untracked `TestDeploymentStateCompatibility` was run first with `.cache/env.sh` and failed with `fsGroup/direct-mount deployment cannot open state: controller state operation failed`. An additional untracked volume-preparation helper was present on inspection and was completed along with the regression. `secure_open.go`, dependencies, collection and provider behavior are unchanged.

Direct inspection of the state opener, Dockerfile entrypoint and cached Kubernetes API source confirmed the conflict: fsGroup changes volume group access before exposure to the pod, while the state opener requires a directly mounted, current-user-owned private directory beneath protected ancestors. The manifest retains fsGroup and adds a UID/GID-65532 init container using the same image, explicitly executing the binary's offline `prepare-state-volume` command. The main container mounts only `private` through `subPath` at `/var/lib/jevernetes`, with the state path unchanged. Both containers retain non-root execution, RuntimeDefault seccomp, read-only root filesystems, no privilege escalation and all capabilities dropped.

Preparation restores directory/file modes after fsGroup, handles fresh and existing volumes, and migrates known legacy SQLite artifacts only into an absent/empty destination. It uses no-follow descriptor checks, advisory locks, per-file/directory synchronization, recovery-files-first moves and a final directory rename. Conflicts, orphan/unknown artifacts, unsafe links/types/ownership, held locks and interrupted staging fail closed. Interrupted staging requires documented operator recovery, never automatic merging. Deployment `Recreate` and a dedicated quiescent local volume remain required; advisory locks and ReadWriteOnce are not fencing against unrelated writers.

Focused offline regressions pass for manifest state path/subPath/PVC wiring, image identity and init command/security, group-accessible direct-mount rejection, repeated clean initialization, legacy migration to absent/empty destinations, mode repair, conflict preservation, links/FIFO/locks, and interruption after each of the five artifact moves. A real hot rollback journal is migrated byte-for-byte before SQLite reopens it; all 2,000 committed rows survive and `PRAGMA integrity_check` returns `ok`. Foreign-owner coverage is present but skipped on this non-root host. Host filesystem renames model the main container's direct mount view; no real mount or container runtime test is claimed.

Final offline gates using `.cache/env.sh`:

- Formatting (`gofmt` on cmd/internal/examples), `go mod verify`, `go test ./...`, `go test -race ./...`, `go vet ./...`, all six UI suites and `git diff --check`: pass. The tests/race/vet/UI/cross-build gates ran through `tools/check.sh`, which passed along with tidy/module-diff and source/privacy checks.
- `tools/check-cross.sh` within that gate: Linux amd64/386/arm static binaries all build; `file` confirms the three stripped static ELF architectures. They were not executed on Linux.
- `tools/release.sh`: pass, including native CLI/report/signal smoke and the new native state smoke. The state smoke executes the manifest's preparation arguments with an empty child environment, preserves legacy database bytes, checks repeatable initialization and modes, then reopens SQLite before intentionally failing listener validation, without reaching cluster/provider configuration.
- `node --check` for the changed tool scripts: pass. Base Kustomize rendering and an overlay replacing both images with one synthetic immutable digest pass using the local renderer with an empty environment; no cluster or credentials are accessed.
- Final source archive byte comparison and privacy validation: pass for 122 intended files (114 text), including both new controller files. Zero retired-language references, credential/personal-path signatures or private/build paths. Generated archives, binaries, rendered manifests and logs remain ignored.

No Chrome, network resources, Kubernetes API, provider, credentials, deployment, push or remote Git operation was used. No container image build or live fsGroup/subPath validation was attempted. The existing macOS minimum-version linker warnings remain non-fatal. This section supersedes earlier gate counts and historical Chrome/production-read statements for this continuation.

Follow-up implemented on 2026-10-06 in `fix/go-production-hardening`, based exactly on PR #23 commit `a3d42e08fe2cd1b6ada362d9bebb6e1d08c9ce2e`. The independent-review continuation started from clean commit `19bc6059ca592499032a7c871533d47fe2bbf246` and is included in the amended single task commit. This section supersedes the historical base validation below for changed controller, collector and coverage behavior. Repository documentation/source were inspected directly. No live cluster, provider, credentials, webhook, deployment or remote Git operation was used for this follow-up.

## Independent-review reproduction and fixes

Each of the six defects was reproduced before its implementation fix, using local fixtures and the offline module cache. No network resources, Kubernetes, real providers, deployment, credentials or remote Git were used. The focused failures were:

| Defect | Before fix | Passing regression / final behavior |
| --- | --- | --- |
| Transient validation | Worker returned nil after one log open, with `identity_changed` | `TestValidationUnavailablePreservesCursor`: GET error, missing runtime ID, or missing UID followed by recovery preserves the fractional cursor and parser line position. Validation retries from 100 ms to a five-second cap; log opens require a successful fresh validation. Confirmed mismatch/NotFound retires the stream. |
| Whole-second wire replay | Fractional replay counted older same-second lines and produced three out-of-order gaps | `TestFractionalWireCursorRepeatedReconnect` encodes real `PodLogOptions` with the client-go parameter codec and checks two reconnects, timestamp multiplicity and exact occurrence count. `TestReplayCatchupEndsAtSavedMultiplicity` checks conservative out-of-order handling after catchup. |
| FIFO journal / directory | Hot-journal FIFO child exceeded its three-second deadline; unsafe directories were accepted | `TestHotJournalFIFORejectedWithoutBlocking`, `TestPrivateStateDirectory`: special files are rejected before Unix VFS delegation, with a private current-user directory, protected canonical ancestry and retained directory descriptor. Mode, symlink and writable-ancestor checks pass. Foreign-owner test is present but skipped on this non-root host. Existing descriptor replacement/lock tests pass. |
| Kubernetes partial physical line | `Finish(false)` left event flags clear, allowing reuse and policy bypass | `TestTransportPartialThroughReuseGates`: initial and continuation fragments set both safety flags, limit the provider dossier, bypass grouping/durable reuse and require policy review even with a high-confidence local routine fixture. Clean file EOF remains eligible. |
| 32-bit allocation | Both linux/386 and linux/arm failed compilation at the `uint64` argument to `Xcalloc` | Uses the dependency's platform-sized `types.Size_t`. `tools/check-cross.sh`, included in `tools/check.sh`, builds static amd64/386/arm binaries with offline dependency resolution. |
| Consumer EOF | EOF and wrapped EOF became reconnect gaps and eventually a context deadline | `TestConsumerEOFDoesNotReconnect`: structural consumer errors propagate unchanged; transport termination has a separate sentinel. |

Additional migration/connection evidence replaces the old relabeled-current-schema tests. `testdata/schema{1,2,3}.sql` contains pristine historical creation DDL from local commits `41e4a58`, `b9eeb6c` and `a3d42e0`, with synthetic rows added separately. Tests assert that no schema-4 artifacts exist before opening, then verify version 4, counts, insert/delete triggers and rollback, global sequence advancement, verdict invalidation/preservation, legacy notification detail lookup, preserved IDs/attempts/status, and evidence removal.

`TestInterruptedAndReplacedConnectionsRestorePragmas` first reproduced lost busy timeout, secure deletion, page limit, journal size limit and temporary-store settings after interrupt/replacement. Connection URI PRAGMAs now initialize every physical connection. A canceled recursive query and an explicitly discarded connection both permit subsequent policy transactions with all configured PRAGMAs restored. `TestHotJournalRecoveryRetainsCommittedState` abruptly exits a child with an uncommitted transaction and a real hot rollback journal, then verifies all 2,000 committed rows and SQLite integrity after recovery.

## Implemented contracts and regression evidence

- **Bounded continuous state:** schema 4 adds transactional row counts/eviction totals. All four tables are populated to 10,000 young rows in regression tests, then ingestion and verdict writes continue at constant table size. Composite time/key indexes provide deterministic eviction without per-event full-table counts. Cache updates do not evict other rows. Only terminal outbox entries are eligible for eviction; an all-pending outbox rolls back the event transaction and retries while delivery continues. Tests verify pending retention, retry recovery and collection cancellation during backpressure. A global sequence/new notification identity namespace prevents ID reuse after incident eviction, while inspection can still locate legacy notifications.
- **Timestamp framing:** Kubernetes transport timestamps are stripped per physical line, before continuation matching. Every two-chunk boundary of a timestamped stack trace is tested; first-line event time and continuation count/text remain correct. Oversized empty lines remain visible as truncated events. Invalid UTF-8, missing Kubernetes timestamps and orphan continuations mark parse uncertainty. Uncertain parsing cannot publish reusable routine safety or bypass controller review.
- **Truthful cumulative coverage:** completeness derives from cumulative loss counters, independently of retained event/coverage samples. Table-driven tests cover byte/event caps, queue drops, omitted/unavailable streams, identity changes, disconnects, cursor uncertainty/overflow, collection interruption, report/state eviction and future `coverage_*` signals. Oversized input produces an incomplete CLI report. Normal file completion remains distinguishable from interrupted collection.
- **Fresh identity:** every log open (including reconnect and snapshot) performs a fresh pod GET and compares pod UID, container kind/name, runtime ID and restart count. Fake-client tests cover replacement, restart, runtime ID changes/missing IDs, removal and termination. Previous-instance attribution is restart count minus one with the prior runtime ID.
- **Raw replay:** a process-local cursor stores up to 4,096 SHA-256 physical-line digests with multiplicities at the latest transport timestamp. Hashing includes discarded oversized bytes and happens before redaction/framing. Tests cover equal-timestamp multiplicity, different raw lines that redact identically, later identical occurrences, reconnect `SinceTime` requests, oversized replay, different discarded suffixes and partial lines. Reconnect replay skips older timestamps within the floored wire second and consumes saved latest-timestamp multiplicities. After catchup, out-of-order lines are retained with coverage gaps. Overflow and untimestamped/partial lines also retain unprovable occurrences and emit coverage gaps. Every reconnect, including clean EOF, is a gap.
- **SQLite opening:** a retained private current-user state-directory descriptor and protected canonical ancestry exclude other identities from racing pre-open special-file checks. No-follow opens verify private regular files, current ownership and single-link identity through descriptors. The pinned modernc Unix VFS verifies SQLite's actual opened descriptor against the retained verified state descriptor before database reads. Tests replace the path with a new file or symlink between verification and SQLite open; neither replacement is used or modified. Additional tests cover public modes, hardlinks, FIFOs, lock-file symlinks, URI-significant filenames, locking and migrations. Native ABI structures use bounded byte copies; vet and race/check-pointer gates pass.
- **Adjacent fixes:** controller lifetime event count defaults to unbounded (`--max-events 0`), with bounded memory/disk/queues and unchanged explicit caps/provider budgets. Stream slots remain occupied until canceled readers finish. Collector consumer errors propagate instead of becoming reconnects or ignored snapshot gaps. Backpressure waits honor collection deadlines even when delivery/inspection remain running.

## Gates and results

Offline Go 1.27.1 darwin/arm64 and Node 20.10.0. The pre-existing module cache was copied into ignored `.cache/go/pkg/mod`; builds use writable `.cache/build`, `GOPROXY=off`, `GOSUMDB=off` and `GOTOOLCHAIN=local`. No dependency versions changed; `modernc.org/libc` is now a direct dependency because of the VFS adapter.

| Gate | Result |
| --- | --- |
| `go mod verify` | Pass; all modules verified |
| `test -z "$(gofmt -l cmd internal examples/replay)"` | Pass |
| `go test ./...` | Pass; includes all independent-review regressions and pristine migration fixtures |
| `go test -race ./...` | Pass; no race/check-pointer reports |
| `go vet ./...` | Pass; no diagnostics |
| `for f in tests/*_ui.cjs; do node "$f"; done` | All six suites pass |
| `node --check` for browser JS, tool MJS and test MJS | Pass |
| `tools/check.sh` | Pass (tidy/module diff, formatting, Go/race/vet, all three offline cross-builds, UI, source/privacy, diff checks) |
| `tools/release.sh` | Pass; native binaries, CLI/risk/report/SIGINT/SIGTERM smoke, source/binary archives |
| `node tools/check-release.mjs --archive dist/jevernetes-source.tar.gz` | Pass; source/privacy/RBAC/container checks and archive bytes match intended source |
| Retired-language filename/text scan over intended source | Pass; zero matches, values suppressed |
| `tools/check-cross.sh` (Linux amd64/386/arm, `CGO_ENABLED=0`, offline) | Pass; `file` identifies stripped, statically linked x86-64, Intel 80386 and ARM EABI5 ELFs; compiled, not run on Linux |
| Native controller state smoke | Pass; empty child environment, mode-0600 state creation/migration, intentional invalid listener stops before cluster configuration |
| `git diff --check`, full diff and status inspection | Pass; only task source/tests/docs/module metadata changed |
| Optional `node tests/test_demo_browser.mjs` | **Previously environment-blocked; not repeated in this continuation:** exits 13 with no test assertions reached. A separate isolated-profile launch confirms Chrome terminates with `SIGABRT` before its pipe session starts. No browser integration pass is claimed. |

The source scanner includes 120 intended files (112 text), with zero retired-language references, credential/personal-path signatures or private/build paths. The native alias is byte-identical. macOS test linking can emit the pre-existing minimum-version warning; tests and native execution pass. The earlier read-only shared cache emitted a harmless local stat-cache write warning during one release; final gates use the writable copy.

The amended task includes controller, collector, event-safety, coverage and regression-test changes, historical SQL fixtures, the offline cross-build gate, module metadata and documentation. Final source/archive scans cover the complete intended worktree, including new fixture/test files; generated binaries and validation logs remain ignored.

## Remaining limits

Kubernetes log GET has no instance precondition; replacement between validation GET and log GET cannot be eliminated. Discovery is polling, reconnect cursors are process-local, timestamp regressions/overflow are conservative, and live/history coverage is always incomplete. Idle flushes can separate delayed continuations; orphan continuations mark uncertainty. Capacity eviction shortens novelty/incident history and can cause repeated reviews. An unavailable sink can fill the pending outbox, inducing visible queue loss; delivery remains one due attempt per second and eight attempts maximum. Explicit provider budgets still stop online collection. SQLite enforces a private current-user directory with protected canonical ancestry and uses the pinned Linux/macOS Unix VFS ABI. It is not a boundary against root or another process with the same OS identity: such a process can rename the directory or swap a sidecar after the pre-open check but before SQLite's path-based open, including inserting a blocking FIFO. The main-file descriptor check rejects replacement before database reads but cannot eliminate same-user sidecar races portably. Schema 4 requires a backup before upgrade if rollback is needed. Unsupported legacy commands remain explicit; no unrelated parity work was added.

No live-service, container-runtime or Linux-runtime validation was performed. The optional Chrome integration suite remains blocked as described above. The historical production-read claims below belong only to the base commit and were not repeated here.

---

# Historical Go rewrite validation (base commit)

Validated on 2026-10-06. This is a working native Go implementation with explicitly limited parity, not a claim that every prior workflow has been reproduced. The primary CLI, bounded analysis, controller, collection, provider adapters, report writer and continuous remote dashboard are implemented. Unsupported or intentionally different behavior is detailed below and in [migration](docs/migration.md).

## Scope and provenance

- The merge candidate was rebuilt on `feat/go-rewrite-main` directly from current `origin/main` at `3a08dcb1c3611c0014b000074006701ad767bfa2`. The verified Go tree from `f822a6efb6d3885a2226318982a9c10ded717322` was applied as one intentional replacement, without merging or replaying the stacked feature-branch commits.
- The remote-dashboard reference was inspected read-only at `7ce66bc92501ef81dc483d4c9d478301cac8fcfd` for exact pod selection, inspection envelopes, status/watch/incident, held-controller lifecycle and continuous dashboard behavior.
- `LICENSE` is byte-identical to the current MIT license on `origin/main`. Obsolete license metadata was removed; README/packaging use MIT.
- Existing static browser assets and all eight media files are retained. The active dashboard is separately embedded from `internal/dashboard/web`.
- No provider requests, credentials, real webhooks, deployment, branch deletion or history rewriting were used. One explicitly authorized production validation used only Kubernetes GET/LIST/log reads; temporary mode-0600 reports were deleted immediately after aggregate validation. Module downloads were initially blocked; the host supplied the isolated cache. All subsequent dependency/build commands used `GOPROXY=off`.
- Build/test caches and generated archives are ignored. Source checks cover surviving tracked files plus intended new files, excluding deleted paths and ignored output.

## Architecture and contract mapping

| Area | Go implementation | Verification / compatibility |
| --- | --- | --- |
| Executables | `cmd/jevernetes`, `cmd/jev`, shared `internal/cli` | Same entry point; release aliases are byte-identical; Cobra validation before I/O |
| Framing and events | `internal/event` | 64 KiB lines, 16,000-byte UTF-8 events, multiline/timestamp framing, deterministic IDs, redaction including source sensitivity and discarded private-key bytes |
| Grouping | `internal/analysis/group.go` | Exact source or conservative Kubernetes template scope; fixed normalization grammar; protected names including separator variants; bounded capacity and monotonic generation |
| Group risk | `internal/analysis/engine.go` | One dossier per compatible ticket; first singleton at microbatch close; at most eight 2,048-byte samples; same-ticket fanout; unchanged original event text/IDs |
| Fail-closed reuse | Event eligibility, judgment gates and ticket checks | Sensitive, truncated, security-shaped, multiline, oversized, uncertain, failed, low-confidence and security/fraud evidence/verdicts do not seed reuse |
| Adaptive scheduling | Grouper audit counters | Deterministic bounded occurrence audits, TTL and explicit review veto; no probabilistic/burst-priority model |
| Provider budget/accounting | `internal/provider` | Non-refundable cumulative reservations, batch/attempt caps, separate risk/template clients, accounting on invalid responses, breaker on missing/underestimated usage |
| Provider protocols | Fixed-host TypeSafe/OpenAI/Anthropic adapters | Strict typed choices/structured JSON; model refusal/incomplete/tool-output rejection; duplicate/unknown/missing field checks; offline injected transport fixtures |
| Reviewed rules | `internal/analysis/rules.go` | Versioned artifact, exact shape/literals, expiry/review metadata, protected-path checks, trailing/duplicate JSON rejection; active reuse in semantic mode |
| Template learning | `internal/analysis/learning.go`, provider template schemas | Shadow-only proposals with local replay; classic reviews only escalate; field generalization requires separate reviewed artifact |
| Files/stdin/gzip | `internal/cli/run.go`, `stdin.go` | Decompressed byte caps, global event cap, partial coverage, blocking-input cancellation and native signal smoke tests |
| Kubernetes collection | `internal/kube` | Official client-go reads; regular/init/ephemeral/previous snapshots; bounded live concurrency, five-second discovery, reconnect cursor and backpressure/drop accounting |
| Reports | `internal/report` | Schema 2, `runtime: go`, original events/judgments, bounded retention/coverage, private atomic replace and sync |
| Durable state | `internal/controller/store.go`, `migrate.go` | Pure-Go SQLite, one writer lock, schema 1/2→3 transaction, old verdict invalidation, metadata-only notification conversion preserving delivery identities/status |
| Policy/outbox | `internal/controller` | Atomic novelty/incident/outbox transitions, cooldown and escalation, at-least-once idempotency, bounded retry/dead state and final-attempt crash recovery |
| Persistent verdicts | `internal/controller/judge.go` | Contract/evidence hashes, exact template/scope/version, TTL, rollback-clock rejection, rescore and unsafe-evidence bypass |
| Health/metrics | `internal/inspect/server.go` | Separate live/ready endpoints, Prometheus registry, process counters, stopped readiness |
| Inspection | `internal/inspect` | Loopback binding; GET-only fixed paths; strict IDs/schema; 1 KiB application request limit, 256 KiB response limit; bounded concurrency and nonblocking store reads |
| Remote CLI | `internal/cli/remote.go`, client-go SPDY forwarding | Exactly one running pod, fresh tunnel per bounded sample, status/watch/incident, atomic latest output, stale failure markers and cancellation |
| Dashboard | `internal/dashboard` | Embedded read-only UI; exact Host/Origin checks, fixed controller target, serialized cached sampling, fixed URLs/IDs, safe text rendering, stale/visibility/cancellation handling |
| Packaging/automation | Go module, Dockerfile, shell/Node checks, CI/dependabot | Native archives, Go build/check workflows, retained JS security analysis, no product interpreter/backend dependency |

## Library choices

| Library | Pinned version | Purpose |
| --- | --- | --- |
| `k8s.io/client-go`, API, apimachinery | `v0.35.3` | Official pod/log clients, fake clients and port-forward machinery |
| `github.com/prometheus/client_golang` | `v1.23.2` | Metrics registry and HTTP exposition |
| `modernc.org/sqlite` | `v1.46.1` | Pure-Go SQLite; release builds use `CGO_ENABLED=0` |
| `github.com/spf13/cobra` / pflag | `v1.10.2` / `v1.0.9` | CLI and option parsing |
| `golang.org/x/sys` | `v0.42.0` | Unix file locking and cancellable stdin polling |
| `golang.org/x/net` | `v0.47.0` | Connection-limited listeners plus dependency transport support |

All direct/transitive module versions and checksums are in `go.mod`/`go.sum`; `go mod tidy` and `go mod verify` pass offline. The module declares Go 1.25.0. Local verification used Go 1.27.1 on darwin/arm64 and Node 20.10.0.

The [axiomhq/drain3 primitive](https://github.com/axiomhq/drain3) was evaluated rather than incorporated blindly. Its generic similarity merging and trained matcher do not provide protected-field/scope/version/generation/privacy authority. The application therefore retains a bounded conservative Drain-style grammar with explicit ticket ownership. This is narrower than generic Drain equivalence and is documented as such; no unused dependency was added.

## Tests and results

Final Go run: **75 top-level tests, 78 passing test records including three provider subtests, zero failures** across nine tested packages. Two executable packages and the synthetic replay example build without separate unit test files.

| Package | Top-level tests | Focus |
| --- | ---: | --- |
| `analysis` | 16 | Singleton deadline, dossiers/fanout, scope/protected fields, sensitive/truncated isolation, stale generations, TTL/adaptive audits, failed/security judgments, opaque checkpoints, veto, reviewed rules, semantic reuse, shadow replay and escalation-only review |
| `cli` | 9 | Schema-2 offline output, validation order, gzip/global cap, partial coverage, private output, context cancellation, OS pipe cancellation, duration grammar, alias and sanitized unknown-command errors |
| `controller` | 11 | Policy gates, atomic novelty/outbox/cooldown, metadata privacy, retries/dead state, locking/future schema, migrations, TTL/clock rollback, inspection contention, cancellation, webhook URL checks and crash recovery |
| `dashboard` | 4 | Host/Origin/method checks, fixed routes/cache, throttle/stale clearing, CSP/static serving |
| `event` | 8 | Multiline/timestamp framing and IDs, text/JSON/source redaction, private-key chunks, byte/UTF-8 bounds, backpressure/cancellation, console safety and offline rules |
| `inspect` | 6 | HTTP limits/routes/IDs, readiness/metrics, exact forwarding lifecycle, response/schema bounds, HTML rejection and typed notification metadata |
| `kube` | 5 | Fake-client exact/ambiguous/terminating/paginated selection, read-only actions, snapshot kinds/previous instances, live idle flush/cancellation and config validation |
| `provider` | 13 | Three adapter fixtures, strict JSON, refusal/extra-field checks, bounded dossiers, invalid-answer accounting, budget breakers/reservations/caps, credential injection, redirect policy, response bounds and template schemas |
| `report` | 3 | Schema/retention, private atomic replacement, bounded coverage |

Commands run successfully against the completed source:

```sh
# Dependency and build caches reside under the ignored worktree cache.
export GOPATH="$PWD/.cache/go" GOCACHE="$PWD/.cache/build" GOPROXY=off
gofmt -w cmd internal examples/replay
go mod tidy
go mod verify
go test ./...
go test -json ./... > .cache/tests.json
go test -race ./...
go vet ./...
tools/release.sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o .cache/jevernetes-linux-amd64 ./cmd/jevernetes
go run ./examples/replay
for file in tests/*_ui.cjs; do node "$file"; done
for file in internal/dashboard/web/*.js jevernetes/web/*.js; do node --check "$file"; done
node tools/check-release.mjs
git diff --check
```

- `go test -race ./...`: passes; no race reports.
- `go vet ./...`: passes with no diagnostics.
- Module checksum verification: `all modules verified`.
- Native release: stripped darwin/arm64 executable; `jev` alias identical.
- Linux release cross-build: stripped, statically linked x86-64 ELF. It was built and inspected, not run on Linux in this session.
- Native smoke: both executable names classify a synthetic warning about disabling authentication on a public admin endpoint as important/degraded while leaving a benign unchanged reload uncertain/info, in both input orders. It verifies per-event/group results, identical stdout and mode-0600 saved reports, zero provider attempts/cost, stale-output replacement, and empty child environments. This proves warning-based offline triage for one synthetic pair, not broad semantic security accuracy. Actual SIGINT and SIGTERM each exit 130 and emit a valid final report while stdin remains open.
- Synthetic replay: 1,000 events, one classification, one provider fixture batch and 999 reuses. This is an exact synthetic fixture, not a production accuracy/cost result.
- Six Node UI helper suites pass, including the active dashboard’s text-injection fixture, sequential polling, fixed routes, detail, stale state and visibility cancellation. Five browser JavaScript files pass syntax checks.
- Source and binary archives are produced. The source archive is checked against the intended file list and compared byte-for-byte with working-tree files. No cache, report, database, credential or build directory enters it.
- MIT license matches `origin/main` byte-for-byte. Existing media is preserved without modification.

### Bounded production read

- The rebuilt native binary connected to the production workload using the configured Kubernetes context and read-only client-go collection.
- A 20-second live-follow window used live lines only, excluded previous containers, capped concurrency at eight streams and events at 500, and made no provider calls. The workload emitted no new lines in that exact window; shutdown and the intentionally incomplete bounded-coverage result behaved as designed.
- A follow-up recent-history snapshot was capped at 25 lines per stream, a 10-minute window, 1 MiB per stream and 500 total events. It ingested and classified 11 events, retained all 11, evicted none, made zero provider requests, incurred zero estimated provider cost, wrote a mode-0600 report and emitted no stderr.
- All 11 production events were uncertain under offline rules; no production risk-quality conclusion is claimed. The synthetic native smoke above is the deterministic proof that the executable can surface an explicitly risky warning. Raw production reports and captures were deleted immediately after aggregate checks.

## Failures found and resolved

The initial module cache was absent and later incomplete; offline builds proceeded after host cache population, without enabling live service access. A newly written UI mock had a syntax error and was corrected before final UI checks.

A socket-based HTTP test could not bind inside the sandbox. It now exercises the same request handler, schema validation, exact pod selection and tunnel cleanup using an in-memory HTTP transport; no production assertions were removed to obtain a pass.

The executable-level signal smoke test found that closing a standard stdin descriptor did not reliably unblock a read on macOS. Context-aware descriptor polling fixed the issue. An OS-pipe regression test and both native signal smoke tests now pass. Other review fixes include required response fields, cumulative reservations, explicit batch caps, protected field-name separator handling, source sensitivity propagation, exact last-notification lookup, review-veto enforcement on same-batch fanout, final-attempt outbox crash recovery and dashboard-specific response timeouts.

Post-PR validation found that one macOS host could reject a freshly linked local release at first execution even though static signature verification passed. The release script now finalizes the locally built executable's ad-hoc signature before first execution and creates the `jev` alias as a hard link, preserving byte identity and the validated executable inode. Two consecutive clean release runs, both executable names and the signal/risk smoke pass with this path. Linux release behavior is unchanged.

The hosted credential scanner initially reported two unverified synthetic user-info URL fixtures and no verified credential. The fixtures are now assembled at runtime, preserving redaction and webhook-rejection coverage without storing credential-shaped URLs in source. The workflow also supplies explicit base/head ranges for pull requests and feature-branch pushes, so an unreachable pre-force-push object cannot turn range resolution into a failed or skipped scan; scheduled/manual full-history scans fail only on verified findings.

The first hosted CodeQL review identified a partially anchored private-path expression and a check/read filesystem race in the release scanner. Private-file detection now uses explicit basename/extension comparisons, and every scanned source file is opened once with no-follow semantics, verified as regular through the same descriptor and read from that descriptor. The scanner rejects a synthetic symlink probe, its full source/privacy check passes, and the fix removes the check/use window from archive and policy-file comparisons as well.

The local linker prints a macOS minimum-version warning for some test binaries. Tests pass; this warning does not occur as a build error. Release builds are pure Go.

## Environment-limited checks

- A Docker build was attempted with an isolated empty Docker configuration, `--network=none` and `--pull=false`. Connection to the local daemon socket was denied by the sandbox before building. No image was built or published. Dockerfile/context/RBAC static checks and the pure-Go Linux cross-build pass; they do not substitute for a container runtime test.
- Local TCP binding is prohibited. Real listener/browser/SPDY end-to-end tests were not run. In-memory handler/transport tests and fake forwarding abstractions pass. The bounded production read validates Kubernetes discovery and log collection only; no live model, controller tunnel or deployment claim follows from it.
- CI workflow execution, CodeQL and remote dependency/advisory scans were not run locally. CI definitions were updated; no CI result is implied.

## Exact source and privacy scans

The requested case-insensitive retired-language/artifact pattern is represented here with hexadecimal escapes so the validation document itself does not introduce a forbidden token:

```text
\x72\x75\x73\x74|\x63\x61\x72\x67\x6f|\.\x72\x73\b
```

The pattern was also executed with `rg -ni` over every surviving tracked text file and every intended new text file; exit status 1 confirms no matches. Binary image/video assets are preserved and excluded from text matching. Result: **106 source files, 98 text files; zero matching filenames or text references; zero implementation/build artifacts of the retired language; no exceptions, including in `go.sum` or license material**.

`tools/check-release.mjs` also checks known credential signatures, personal filesystem path signatures, private/generated paths, symlinks, interpreter files, controller RBAC and the container contract. Final result: **zero credential/personal-path signatures and zero private/build paths in intended source**. These are bounded static checks, not a guarantee that arbitrary secrets are detectable. Diagnostics suppress matched values.

The final diff/status review covers all intended additions, replacements and deletions. `.cache`, `dist`, generated binaries and audit reports remain ignored. No unrelated media/assets were removed. The branch is an ordinary single replacement commit directly atop the recorded `origin/main` base; exact merge simulation is conflict-free by construction. The current source tree contains no retired implementation artifacts or textual references, while pre-existing historical commits on `main` remain unchanged.

## Explicit limitations / non-parity

1. No full prior dashboard scan/live-tail/search/context/review-write/TUI backend. Unsupported commands/routes fail explicitly; the active dashboard is a report viewer and fixed-controller monitor.
2. No probabilistic normality/burst-priority scheduler, fixed random drift sampling, uncertain-verdict cache, shared risk/template monetary pool or automatic checkpoint restoration. Unsupported sampling/cost compatibility options are rejected.
3. Classic normalization is conservative; path shape and logger-clock grammars are not ported. Reviewed artifacts with logger-clock prefixes are rejected. Automatic provider-proposed free-text masking remains disabled pending an independently reviewed artifact.
4. Kubernetes discovery polls every five seconds rather than maintaining watch streams. Live cursors are process-local, history remains partial, and no exactly-once or complete-coverage assertion is made.
5. Remote forwarding temporarily opens a loopback TCP listener. Inspection metrics use the Go counter subset and may reject older richer payloads. Notification schema 2 intentionally omits raw evidence/readable sources; consumers need migration.
6. Report/event identity hashes and metric inventories change. Retention defaults to 2,000 in all modes. Cost projection fields are not fabricated. Model/provider prices and token limits remain external assumptions, especially the TypeSafe output bound.
7. Webhook authentication-header configuration is not ported. HTTPS URL configuration, redirects rejection, idempotency and durable retries are implemented.
8. SQLite is local-filesystem-only; Unix file locking limits builds to Linux/macOS. Provider, controller-tunnel and container behavior has no live validation in this session; the production validation was limited to read-only Kubernetes discovery and log collection with offline judgments.

These limitations are substantive. The successful offline checks establish the implemented contracts, not complete parity with all functionality at either reference commit.
