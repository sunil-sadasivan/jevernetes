# Sole Go runtime and canonical executable — 2026-10-14

At the explicit request to replace Python and Rust, Go is now the only product runtime. The previously required coexistence policy is superseded; earlier entries below remain historical records. Revision `e310a2b32d206a6061ec573d1bc32d7eb50335e9` preserves the last coexistence tree in Git, with all hosted checks passing and no open branch CodeQL, repository dependency or secret alerts at that review.

Removed the Python package, Rust sources, their packaging manifests and runtime-specific build/test jobs. Native CLI, TUI, dashboard and controller code remain. `make build` (or `go build -o ./jevernetes ./cmd/jevernetes`) produces `./jevernetes`; releases provide `jevernetes` and `jev`. Removed obsolete help placeholders advertising retired runtimes. Schema-1/2 reports and version-1 human review rules open directly in Go; migration and rollback instructions preserve filenames, private backups and separate model-template rules.

The six browser acceptance suites now load Go's embedded assets directly, with their assertions unchanged. The executable inventory covers 14 workflows and 21 native suites. Retirement regression checks reject reintroduced Python/Rust runtime/package paths and changed browser fixtures; source/archive checks reject old implementations, private state and credential signatures. Python/Node remain development-only test tools. Go vulnerability/race checks, secrets scanning and CodeQL for Go, JavaScript and remaining Python fixtures stay enabled.

Local Darwin arm64 verification: the full `tools/check.sh` gate passed (normal/race/vet, vendored SPDY, Linux amd64/386/arm cross-builds, browser contracts, native HTTP and PTY, retirement and privacy checks). `make build` produced the canonical executable. `tools/release.sh` passed alias/offline/signal/state/HTTP/PTY checks and source archive byte comparisons; the binary archive contains only `jevernetes`, `jev`, `LICENSE` and `README.md`. CLI tests passed again after removing the help placeholders. Logs are ignored under `.runs/native-only-check.log` and `.runs/native-only-release.log`. Final CI verifies the pushed revision.

Workflow equivalence retains the documented framing/default/identity differences. Live provider accuracy/billing, production clusters, actual container-engine execution and a long production soak remain unverified. No real provider/cluster, deployment or production evidence was used.

---

# Public PR security follow-up — 2026-10-14

Reviewed the public PR at `9cc8d93`, branch-specific CodeQL alerts, repository dependency/secret alerts, repository notifications and scheduled security runs. GitHub reported no open Dependabot or secret-scanning alerts; the October 12 scheduled full-history security run passed. The PR did report two high-severity Go path-injection alerts (#8/#9) and one JavaScript filesystem-race warning (#7). These counts are observations at review time, not a guarantee of absence of vulnerabilities.

An independent `govulncheck` v1.9.0 scan found **13 reachable advisories** in the old Go 1.27.1 standard library and `golang.org/x/net`/`golang.org/x/text`: GO-2026-6617, 6613, 6612, 6611, 6610, 6609, 6608, 6607, 6605, 6603, 5970, 5026 and 4918. These include HTTP transport/parser denial-of-service and framing issues. Reachable symbols do not prove every advisory is exploitable in the application's configured protocols. The initial log is `.runs/security-govulncheck.log`; the official advisory database includes [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617).

The module now requires **Go 1.27.2**, with `x/net` 0.60.0, `x/text` 0.42.0, `x/sys` 0.48.0 and `x/term` 0.46.0. The repeated source scan reports **No vulnerabilities found** (`.runs/patched-govulncheck.log`). CI runs the pinned vulnerability checker on Linux. The Go setup action is pinned to its official immutable commit; Docker builds require the operator-selected patched toolchain rather than silently downloading one. Offline cross-builds and nested-module checks use the selected project toolchain.

Browser upload names remain validated display metadata but no longer become disk paths; uploads use generated private directories and fixed filenames, preserving gzip decoding and original source labels. Report IDs are resolved against actual regular directory entries; only a directory-derived filename reaches the filesystem reader. This also resolves the scanner flow that persisted through the previous basename guard. Traversal and source-name preservation are tested. The test-only state smoke now checks metadata and contents through one no-follow file descriptor. Existing Host/Origin/CSRF, private-file and read-only cluster boundaries remain in place; no scanner alert was manually dismissed.

Hosted Linux race testing also exposed the one-second intake deadline expiring during cold collector initialization. Duration now starts after setup. The regression deliberately spends longer than the configured duration initializing the fake collector, then still requires the quiet pending event, partial coverage, joined stream and private final report. Affected package race tests and the complete pre-dependency-upgrade gate passed; the patched Go normal/race/vet suites and patched release/executable HTTP/PTY/archive checks passed. The complete patched gate passed (`.runs/patched-final-check.log`). Hosted Linux Go/release/race/vulnerability, Rust, Python and secrets gates passed at `a1f6b7c`; CodeQL cleared the upload and test-script findings but retained the report-lookup flow. The final directory-entry lookup revision has focused race/HTTP coverage and is rechecked by hosted CI.

Logs are ignored under `.runs/`. No credentials were printed, no live provider/cluster or deployment was accessed, and no production evidence was used. Retained Rust/Python assets and runtime retirement policy are unchanged.

---

# Native Go investigation parity — 2026-10-13

This pass started from `d1435dd` on the staged-coexistence branch and completes native investigation workflows in an isolated worktree. The previously published PR head (`629918d`) did not have TUI or investigation-dashboard parity. The new Go TUI and dashboard use the native collector/provider lifecycle; neither starts Python, Rust or kubectl. The 60 retained runtime/package/test assets remain byte-for-byte and mode-identical to the accepted retained base. Runtime retirement remains disabled.

## Completed workflows

- Native terminal: clickable/filter tabs, grouping, multiline details and occurrence IDs/timestamps, retained details across live eviction, Unicode query editing, frozen exact and TypeSafe semantic searches, occurrence-aware cache, separate search budgets, cancellation and final report/terminal cleanup.
- Native investigation dashboard: established embedded browser interface, report history and schema-1/2 imports, uploads, snapshot/live collection, filtering/confidence/grouping, exact/semantic search, context lookup, reversible bulk reviews and version-1 expected rules, prompt/export and themes. The compact controller inspector remains available at `/controller/` and `--inspection-only`.
- Compatibility: legacy scheduling flags and namespace aliases, shared human expected rules before provider dispatch, pinned source context/identity, source counts and live stream/rate counters, bounded pending evidence, independent search accounting and conservative partial coverage. See `docs/usage.md` for intentional differences in framing, defaults, event IDs and output layout.
- Interactive Kubernetes authentication: the TUI suspends raw mode, mouse capture and alternate-screen rendering under a shared terminal lease. The helper receives the cooked terminal; UI input/rendering cannot consume login input or race foreground ownership. Success, failure, timeout and Ctrl-C all restore ownership and the saved terminal state, both with and without the TUI.

## Verification

Local Darwin arm64, Go 1.27.1 and Rust 1.94.0; synthetic evidence, local fixture listeners, in-memory transports and temporary private state. Sandbox escalation enabled the local listener/PTY tests. No tests were excluded or weakened. Nonfatal macOS linker minimum-version warnings remain.

| Check | Result |
| --- | --- |
| `tools/check.sh` | Passed: all Go normal/race tests and vet, module tidy, vendored SPDY checks, Linux amd64/386/arm cross-builds, six retained browser suites and five suites against the Go embedded frontend, native executable PTY and HTTP tests, 128 Python tests, retained-asset mutation tests and source/privacy checks. Log: `.runs/native-complete-check.log`. |
| `python3 tools/check_parity.py --run` | Passed all 26 executable suites across 14 workflows; all 60 retained assets verified by bytes/type/mode. Log: `.runs/native-parity-run.log`. |
| `tools/release.sh` | Passed both binary aliases, offline/signal/state smoke, native terminal/HTTP smoke, source archive byte comparison and privacy/container manifest checks. Log: `.runs/native-release.log`. |
| Native interactive auth PTYs | Passed all eight cases (success/error/timeout/Ctrl-C, with/without TUI), including cooked helper input, foreground ownership, complete terminal attributes and joined helper processes. Included in the full normal/race gate; focused log: `.runs/native-terminal-handoff.log`. |
| Retained Rust | All 38 library and 7 CLI tests passed; fmt, clippy with warnings denied, build and all three PTYs passed. |
| Native HTTP smoke | Actual binary: uploads, report history, embedded assets, controller route, origin/CSRF checks, exact search, reversible reviews and mode-0600 persistence passed. Runs in check/release gates. |
| Browser observation | Chrome rendered a synthetic Go report in the established interface, including grouped occurrences. This was an initial render check, not a complete browser click-through; the computer-use service later became unavailable. Deterministic browser logic and HTTP acceptance remain covered above. |

Hermetic workflow parity is implemented; live provider accuracy/billing, production cluster behavior, actual container-engine execution and a long production soak remain unverified. No live cluster/provider calls or deployment were performed. Hosted CI must validate the final pushed commit before merging. These limits do not disappear when local checks pass.

---

# Discovery redirects and shutdown draining — 2026-10-11

Started at exact clean HEAD `eb5a98e3042cd29e7c334318fdd441e3ce4b526d` in the staged-coexistence worktree. Only this worktree was changed. No push, PR change, deployment or live service access occurred. Earlier entries below describe previous changes.

## Regression-first evidence

Before production edits, the new focused tests reproduced all three findings:

- In-memory discovery transports observed a second authenticated request after both cross-host and same-host redirects, for bearer credentials and a credential-attaching wrapper.
- With four admitted events, a delayed provider and plain output, cancellation saved only one event to the report. The controller variant also persisted only one `seen` row. Both variants now retain all four across the active batch and queue.
- A live fake Kubernetes stream containing one timestamped quiet ERROR saved zero events on duration expiry and cancellation. Both now retain the event with truncated/parse-uncertain evidence and a conservative judgment.

## Changes and bounds

`HTTPClientFor` installs a reject-all redirect policy at the shared discovery/log HTTP client boundary; SPDY uses the same policy. Auth wrappers, TLS, OIDC refresh and exec credential behavior are otherwise unchanged.

Plain evidence output is optional to persistence/report draining. A failed write stops fresh intake, suppresses subsequent evidence writes and is surfaced after admitted work and the saved report complete. Each evidence write has a one-second bound and process cancellation; cancelled processing skips optional evidence output. Final report output retains its existing independent one-second grace period. Tests also cover blocked output, failing writers, cancellation during a write and exit 130.

Live stream finalization has a distinct nonblocking admission callback, sharing the normal admission lock, exact event cap and bounded queue/drop accounting. Cancellation closes and joins readers, drains only the bounded already-read chunks and conservatively finishes pending parser evidence. Readers check cancellation before opening/reading again. Additional tests cancel inside a Read, verify the reader is closed/joined without another read, and saturate final admission at both the queue bound and event cap.

## Verification

Validation uses the ignored worktree Go build cache, Node 24 and the existing Python virtual environment, with dependency network access disabled. Local fixture listeners required sandbox escalation; no tests were excluded or weakened. Focused regressions passed, including 30 repetitions of cancellation inside a Read. Unfiltered `go test -race ./internal/kube ./internal/cli -count=1` passed without race reports. The cancellation-during-write exit regression was added afterward and passes separately; the full repository gate includes it under race detection.

| Check | Result |
| --- | --- |
| Focused regressions and touched-package race tests | Passed; no race reports. |
| `tools/check.sh` | Passed: all Go tests/race/vet, tidy with no module diff, Linux amd64/386/arm cross-builds, six UI suites, 124 Python tests, retained-inventory and source/privacy checks. |
| `python3 tools/check_parity.py --run` | Passed: 14 workflows, 18 executable suites, 60 retained assets verified byte-for-byte and by mode; retirement disabled. Rust reported 38 unit and 7 contract tests passing. |
| `tools/release.sh` | Passed: native build/alias, SIGINT/SIGTERM report smoke, private state/migration/reopen smoke, source archive byte comparison and privacy checks. |
| Retained contribution checks | `cargo fmt --check`, offline clippy with warnings denied, offline build and all three terminal PTY cases passed. |
| Formatting and diff inspection | Touched Go files formatted; `git diff --check` clean; module and retained runtime files unchanged. |

Logs are ignored under `.cache/p2-*.log`.

Finalization remains subject to the configured queue and event cap: evidence that cannot be admitted is accounted as queue loss or event-limit coverage. Private persistence failures retain their existing fatal semantics. Arbitrary library writers must implement their own context ownership. No live cluster/provider validation is claimed. Coexistence, retained runtime assets, migration and cache identity are unchanged; retirement remains disabled.

---

# PR 23 compatibility and render lifecycle fixes — 2026-10-11

This pass started clean at exactly `7916bcb970384e96da5975e0bd47addeac484d5d` on `fix/go-full-parity-20261011`. It fixes the three P2 findings from the subsequent blind review. Earlier entries below are historical; their host passes do not validate this change. Coexistence and `retirement_allowed=false` are unchanged. No push, deployment, remote changes, or live cluster/issuer/provider calls occurred.

## Regression-first evidence and behavior

[Pre-fix failures](docs/validation/p2-before-7916bcb.txt) were recorded before production edits. The new tests reproduced a post-only issuer rejecting Basic with HTTP 400, scheme-less HTTP/HTTPS SPDY requests never reaching the endpoint, swallowed help/usage/short-write errors, and twelve blocked-pipe child processes requiring termination after cancellation or SIGTERM.

- OIDC discovery now selects advertised Basic or form-body client authentication before token dispatch. Basic is preferred when both are advertised; unsupported advertised lists fail without a token request. Missing metadata retains Basic-first compatibility, with one fallback only for HTTP 401 or a complete HTTP 400 JSON `invalid_client` response. Network failures, invalid grants and server errors are not replayed. Credential escaping, private atomic rotation, bounded cancellation, verified TLS, redirect/downgrade rejection and body limits remain in place.
- SPDY uses `rest.DefaultServerUrlFor` for the upgrade URL, matching REST's scheme-less server and TLS defaults while retaining explicit URL prefixes. Local in-memory HTTP/TLS tests cover scheme-less HTTP, scheme-less HTTPS, explicit HTTPS, path prefixes and explicit SNI.
- Cobra writes use the existing cancellable output ownership path and retain discarded writer errors for `Execute` to report. The help command returns usage errors instead of invoking Cobra's process-exit helper. Successful text and exit codes are checked against raw Cobra rendering. Failed/short writers return 1 and cancelled rendering returns 130. Application writes retain their own context, including the final-report grace window; custom blocking library writers still own their own cancellation.

## Verification and remaining host boundary

Native Darwin arm64, Go 1.27.1, Rust 1.94.0, Python 3.14.7 and Node 24.19.0. Commands used offline dependency settings (`GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, `CARGO_NET_OFFLINE=true`, `PIP_NO_INDEX=1`) and an ignored worktree Go build cache. Logs and machine-readable test events are in `.cache/p2/`.

| Check | Result |
| --- | --- |
| New focused regression tests | Passed before broad verification; seven initial top-level tests / 53 results including subtests. The later wrapper-contract test is included below. |
| Exact new regression selection, `-race -count=2` | Passed: eight top-level tests per run, 16 executions / 108 results including subtests; all kube/cli/output packages passed. `regressions-race.json` |
| Unfiltered kube/cli/output normal and race tests | Attempted; existing controller and Kubernetes wire listener fixtures fail because this sandbox denies TCP bind. Output passed. `focused.json`, `focused-race.json` |
| Fresh affected-package normal supplement | Passed: 85 top-level tests / 250 results including subtests in kube/cli/output, with the six named TCP listener tests excluded. `nonlistener-focused.json` |
| Whole repository fresh race supplement | Passed: 210 top-level tests / 555 results including subtests across 11 test packages; three command/example packages have no tests. Nine named listener-dependent tests were excluded at invocation, without modifying assertions. `nonlistener-race.json` |
| `tools/check.sh` | Attempted; formatting, module tidy/diff and initial Go tests ran, then the gate stopped at existing listener failures. This is not a full host pass. `check.log` |
| `python3 tools/check_parity.py --run` | Attempted; stopped in the first Go suite on the same controller listener restrictions. The executable inventory still requires a host run. `parity-run.log` |
| Static parity | Passed: 14 workflows, 18 executable suites, all 60 retained assets byte/type/mode verified, retirement disabled. |
| `tools/release.sh` | Passed: native build and aliases, SIGINT/SIGTERM final-report smoke, state preparation/migration/reopen, source archive byte comparison and privacy checks. `release.log` |
| `node tools/check-release.mjs` | Passed source/privacy/container checks. |
| Vet, module verification, Linux cross-builds | Passed `go vet ./...`, `go mod verify`, amd64/386/arm pure-Go builds. Module files unchanged. `remaining.json` |
| Retained checks | Rust fmt/clippy/build and all three Rust PTYs passed. Six UI suites, Python release/package checks and retained mutation regression passed. Full Rust tests: 24 passed, 14 listener-dependent failures. Full Python discovery: 120 tests ran, one HTTP class setup failed on denied bind (four tests could not run). `remaining.json` and associated logs |

The race supplement excluded `TestControllerDeliversNotificationOnlyJSONOnStdout`, `TestControllerBudgetStopsEvenWithHoldAndInspection`, `TestKubernetesWireSnapshotAndReconnectContracts`, `TestSPDYStalledUpgradeCancellation`, `TestSPDYAuthenticatedTLSAndProxy`, `TestSPDYRejectsUntrustedTLS`, `TestRealSPDYInspectionAndCancellationContract`, `TestProviderHTTPContracts`, and `TestRejectSpecialFilesAndAliases`. These are invocation exclusions, not removed tests. The safe-file parent assertions separately passed under race detection with only the `socket` subtest excluded (`safefile-race.json`). This environment does not permit escalating to an unrestricted host; no host, live-service, container-runtime or remote-CI result is implied.

Retained Rust/Python runtime, package and test assets are unchanged from the starting commit and match the accepted retained base `3338ea130d7daaca9d4916985654c047af296612`; the previously accepted migration module is preserved. `go.mod` and `go.sum` are unchanged. The final report records the local commit, final diff/status checks and verification counts. A separate blind review follows; this entry makes no merge-readiness claim.

---

# OIDC rotation and output ownership — 2026-10-11

Started clean on `fix/go-full-parity-20261011` at exactly `d6161ff0e29ed0e84047be8897974883441e2ede`. Read `CONTRIBUTING.md` and the relevant architecture, migration, usage and controller documentation; no applicable `AGENTS.md` was found. Independent host-level acceptance passed. HEAD remains `d6161ff0`; this fix pass is **unstaged and uncommitted**. No push, deployment, live issuer/provider/cluster access, retained runtime changes or runtime retirement occurred. Earlier validation entries below describe earlier changes, not this worktree.

## Regression-first evidence

[Pre-fix failure excerpts](docs/validation/ownership-before-d6161ff0.txt) were saved before production edits; only the three new regression files and evidence were then untracked. Full log: `.cache/ownership-before-final.log`.

- An expired synthetic OIDC configuration refreshed successfully through a local HTTP server over `net.Pipe`, but the replacement refresh token was absent from the kubeconfig.
- Stalled discovery and token endpoints both remained blocked after request cancellation. Fixtures released and joined the old requests only after recording each failure.
- Child processes writing to open, non-draining OS pipes failed to exit after cancellation, both for `StdoutSink` and plain CLI evidence. The parent killed and joined each failed child. A failing plain writer was also incorrectly reported as successful (exit 0).

An initial fixture incorrectly closed its readiness channel twice when upstream OAuth retried authentication; that test bug was fixed before the final recorded baseline run. No production edit preceded those baseline failures.

## Implementation and adjacent audit

The shared HTTP/SPDY transport bypasses context-free upstream OIDC refresh. Cached tokens retain the ten-second expiry margin and avoid issuer contact. Refresh inherits request/command cancellation, has a fifteen-second ceiling (including lock wait), five-second dial/TLS/header bounds, bounded bodies, verified issuer TLS/CA handling, and no redirect or HTTPS downgrade. Only explicit HTTP 401 permits the client-authentication fallback; network errors are never replayed. Detailed issuer responses/credentials are not exposed in errors.

Local loading attaches a selected-user, single-file refresh destination. Multiple loading paths fail closed for OIDC. Refresh acquires the client-go `.lock` convention, rereads credentials to coordinate separate Go clients, and validates a private owned regular file, single link, private owned directory and protected ancestry. No-follow/nonblocking descriptor opens reject aliases and special files. The latest configuration is reread after issuer I/O so unrelated changes survive, and changed credential/provider settings fail closed. A 0600 temporary file (explicit even with a restrictive umask), file sync, atomic directory-relative rename and directory sync complete before using the new token. A final reread rejects observed noncooperating changes. The standard serialized kubeconfig remains readable by retained clients; a fresh standard client-go transport test consumes its cached token.

Notification/plain/remote/dashboard descriptor output uses synchronous nonblocking writes, short cancellation polls and error propagation. Ownership gates are scoped by descriptor file identity, so a full stdout pipe does not monopolize an independent stderr pipe; aliases serialize and gates are removed after their last owner/waiter. Descriptor flags are restored, short writes are errors, and there are no detached write goroutines. JSON/plain final output and terminal error diagnostics get a one-second independent grace window after workers join. Private report writing precedes final stdout. Protocol selection remains unchanged: controller notification JSON is stdout, evidence is stderr, and controller JSON/stdout-sink conflict is rejected.

Coverage additionally exercises inherited stdout descriptors, delivery-worker join without draining the pipe, pending delivery after cancellation, blocked final JSON, descriptor restoration and independent progress, closed/short writers, OIDC request deadlines and command SIGTERM, SPDY lifecycle cancellation, issuer header timeout, verified embedded/relative-file CA and untrusted TLS, unsafe CA/destination paths, destination replacement during refresh, body limits, concurrent clients/unrelated edits, fresh retained-client consumption, and restrictive umask persistence. Existing cached OIDC HTTP/SPDY, exec-helper, signal/report and library tests remain active.

## Verification

Native Darwin arm64; Go 1.27.1, Rust 1.94.0, Python 3.14.7, Node 24.19.0 (`/opt/homebrew/opt/node@24/bin` first on PATH). All commands use `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, `CARGO_NET_OFFLINE=true`, `PIP_NO_INDEX=1`, and ignored `.cache/build` for GOCACHE. Earlier builder-sandbox gate ledgers: `.cache/ownership-gates.json` and `.cache/ownership-final-gates.json`. Independent host reruns use the completed production implementation; the exact new focused regression selection also passed twice under race detection for kube/controller/cli/output. Nothing has been staged or committed.

The independent host passes below supersede the earlier builder-sandbox TCP/Unix listener failures in Go, Rust and Python. Those attempts remain historical evidence, not outstanding acceptance blockers. `tools/check.sh` completed the full Go/race, Python, UI and static checks; `python3 tools/check_parity.py --run` additionally completed every executable parity suite, including the listener-dependent Rust tests.

| Gate | Result / evidence |
| --- | --- |
| New regression tests, focused race | Exact new focused selection passed twice on the host with `-race -count=2` for kube/controller/cli/output; `.cache/host-ownership-focused-race.log`. Earlier passes: `.cache/ownership-focused.log`, `.cache/ownership-focused-race.log`, `.cache/ownership-repeat-race.log` |
| `tools/check.sh` | Complete independent host pass: full Go tests/race including listener suites, formatting, tidy/module diff, vet, Linux amd64/386/arm cross-builds, six UI suites, all 124 Python tests, retained-asset regression, parity/static and source/privacy checks; `.cache/host-ownership-check.log`. Supersedes `.cache/ownership-final-check.log` |
| Affected race packages: kube/controller/cli/output | All passed in the unfiltered host `go test -race ./...`; `.cache/host-ownership-check.log`. Supersedes the listener-blocked standalone attempt in `.cache/ownership-final-affected-race.log` |
| Full `go test -race ./...` | Every package passed on the host, including CLI, inspection, Kubernetes, provider TCP and safe-file Unix listener fixtures; no race reports. `.cache/host-ownership-check.log`. Supersedes `.cache/ownership-all-race.log` and `.cache/ownership-final-nonlistener-all-race.log` |
| Nonlistener affected packages plus inspection, fresh race | Passed with exactly seven named listener tests excluded at invocation, without editing tests. `.cache/ownership-nonlistener-race.log` |
| Fresh nonlistener whole-repository race | Every Go package passed with `-race -count=1 -timeout=120s`, excluding only the named listener suites; `.cache/ownership-final-nonlistener-all-race-complete.log`. The safe-file parent assertions were separately rerun successfully with only its socket subtest unselected; `.cache/ownership-final-safefile-nonlistener.log` |
| `tools/release.sh` | Complete independent host pass: native build/signing, both executable aliases, SIGINT/SIGTERM final-report and private-report smoke, native state migration/reopen smoke, source archive byte/privacy checks; `.cache/host-ownership-release.log`. Earlier pass: `.cache/ownership-final-release.log` |
| `python3 tools/check_parity.py --run` | Complete independent host pass: 14 workflows / 18 executable suites / 60 retained assets byte/type/mode verified / retirement disabled; `.cache/host-ownership-parity.log`. Supersedes `.cache/ownership-final-parity-run.log` |
| `go vet ./...`, `go mod verify`, module diff | Passed; `.cache/ownership-final-{vet,modules,module-diff}.log`; no module version/checksum changes |
| Linux pure-Go amd64/386/arm cross-builds and extra arm64 build | Passed; `.cache/ownership-final-cross.log`, `.cache/ownership-final-cross-arm64.log`; binaries were not executed |
| Rust fmt / clippy / build (locked, offline) | Passed; `.cache/ownership-rust-{fmt,clippy,build}.log` |
| `cargo test --locked --offline --all-features` | All 38 library and 7 CLI tests, plus binary/doc targets, passed on the host, including all 14 listener tests; `.cache/host-ownership-parity.log`. Supersedes `.cache/ownership-rust-test.log` |
| Rust nonlistener supplement and PTYs | 24 library + 7 CLI tests and binary/doc targets passed with those 14 named listener tests excluded; `.cache/ownership-rust-nonlistener.log`. All three PTYs passed; `.cache/ownership-rust-pty.log` |
| Python full discovery | All 124 tests passed in both host check and executable parity, including the four dashboard HTTP tests; `.cache/host-ownership-check.log`, `.cache/host-ownership-parity.log`. Supersedes `.cache/ownership-python.log` |
| Python nonlistener supplement | All 120 tests passed, excluding only `test_dashboard.HttpTests`; `.cache/ownership-python-nonlistener.log`. An initial temporary runner lacked the repository on `sys.path`; corrected by running discovery from stdin at the repository root; retained files were unchanged |
| Six UI suites and both JS trees' syntax checks | All six UI suites passed again in both host check and parity; `.cache/host-ownership-check.log`, `.cache/host-ownership-parity.log`. Earlier UI and syntax passes: `.cache/ownership-test_*_ui.log`, `.cache/ownership-syntax-*.log` |
| Static parity, retained mutation regression, Python package/release and source/privacy integrity | Passed again in host check/parity: 14 workflows, 18 suites, 60 retained assets byte/type/mode verified, retirement disabled; valid baseline and 45 rejected mutations. `.cache/host-ownership-check.log`, `.cache/host-ownership-parity.log`; earlier evidence: `.cache/ownership-{parity-static,retained-tests,python-release,source-integrity}.log` |
| Retained runtime implementation/package/test/license diff against exact starting HEAD; module diff; staged diff; `git diff --check` | Passed: retained runtime and `go.mod`/`go.sum` unchanged; `git diff --cached --exit-code` and `git diff --cached --check` passed with an empty index diff. Diff checks reconfirmed during this validation update; earlier evidence: `.cache/ownership-retained-diff.log`, `.cache/ownership-diff-check.log`, `.cache/ownership-final-module-diff.log`. Nothing staged or committed |

The earlier builder-sandbox affected-package Go supplement excluded `TestControllerDeliversNotificationOnlyJSONOnStdout`, `TestControllerBudgetStopsEvenWithHoldAndInspection`, `TestKubernetesWireSnapshotAndReconnectContracts`, `TestSPDYStalledUpgradeCancellation`, `TestSPDYAuthenticatedTLSAndProxy`, `TestSPDYRejectsUntrustedTLS`, and `TestRealSPDYInspectionAndCancellationContract`. The fresh whole-repository supplement additionally excluded `TestProviderHTTPContracts` and the `TestRejectSpecialFilesAndAliases/socket` subtest. Its safe-file parent assertions ran separately using `-run '^(TestRejectSpecialFilesAndAliases|TestCancellationClosesDescriptorAndJoins)$/^$'` (both parent tests passed). All of these listener gates subsequently passed in the unfiltered host suites; the supplemental selections are historical evidence only. New issuer tests use real HTTP/TLS over in-memory connections and child/OS pipes, so they also run under the builder restrictions. Nonfatal macOS linker minimum-version warnings and a denied optional module stat-cache write occurred in earlier runs; builds and release completed offline.

## Residual limits

Complete check, executable parity and unfiltered Go race acceptance passed independently on the host; no builder-sandbox listener failure remains an outstanding blocker. No live issuer, cluster, provider, external proxy or deployment was exercised. Storage durability assumes a functioning local filesystem; ordinary regular-file operations cannot promise deadlines on a stalled filesystem. The lock coordinates cooperating writers; observed noncooperating edits are preserved or rejected, but same-user/root replacement racing the final check/rename is outside the ownership boundary. Retained clients can consume the new kubeconfig, but their refresh implementations are unchanged and must not independently refresh the same rotating token concurrently without compatible locking. Issuer rotation and filesystem persistence cannot form a distributed transaction: a lost response after server consumption, invalid response or storage failure can require reauthentication. Arbitrary library-supplied `io.Writer` implementations remain trusted synchronous code and must supply their own cancellation if they block. Successful stdout delivery preserves JSON bytes; interrupted output may be partial. Passing the executable inventory does not establish complete product parity or authorize runtime retirement; retirement remains disabled. HEAD is still `d6161ff0`, and this fix pass remains unstaged and uncommitted, with nothing pushed or deployed and no retained runtime changes.

---

# Endpoint identity, target SNI and OIDC compatibility — 2026-10-11

Started clean on `fix/go-full-parity-20261011` at exactly `aa75b6ce58f56e888ec8c936f8f11c9f7d8bdaf0`. Read `CONTRIBUTING.md`; no applicable `AGENTS.md` was found. These changes are implemented and the complete acceptance gates passed in independent host-level verification, superseding the earlier builder-sandbox listener failures. Staging and the intended local commit have not yet occurred; HEAD remains `aa75b6ce` and the worktree is dirty. No push, deployment, GitHub mutation, live provider/cluster contact or runtime retirement occurred. Historical entries below are not results for this change.

## Regression-first evidence

[Failure evidence](docs/validation/compatibility-before-aa75b6ce.txt) records the final baseline run before any production edits. At that point only the new regression test file was untracked. The full ignored log is `.cache/compat-before-final.log`.

- Production `kube.New` collectors using different resolved endpoints emitted identical event sources and drain scopes, both with a shared named context and with empty default/in-cluster-style contexts. Synthetic snapshot regular/init/ephemeral/previous events and live events reproduce the collision. Reopening SQLite between batches caused the two clusters to share a single provider judgment (`calls=1`, expected 2). A separately seeded exact pre-fix durable key was still reachable.
- Actual TLS ClientHello/handshake tests required target SNI, then read the upgrade HTTP request. Direct, HTTP CONNECT, HTTPS CONNECT (TLS inside TLS) and SOCKS5 all failed when verification was disabled and ServerName was unset. Verified TLS, explicit ServerName and IP-host cases served as controls. The final matrix has 16 cases, plus stalled-negotiation cancellation for all four routes.
- A synthetic HTTPS kubeconfig with a populated OIDC auth provider failed initialization with `no Auth Provider found for name "oidc"`. The retained test loads a private kubeconfig through production `LoadContext`, forwards an unexpired synthetic cached JWT through ordinary HTTP and the SPDY transport, and asserts an issuer-transport tripwire observed zero refresh/discovery calls. It does not claim signature validation by a Kubernetes API server. Unsupported providers and mixed OIDC/exec configurations are also rejected.

An initial listener fixture was denied TCP bind permission. The retained TLS fixtures use real cryptographic TLS over `net.Pipe`; CONNECT/SOCKS protocol negotiation and nested HTTPS-proxy TLS still execute. Initial test setup also needed a mode-0700 SQLite parent and an HTTPS kubeconfig so client-go retained authentication. Both were corrected before the recorded baseline failures and before production edits. No existing listener tests were weakened or edited.

## Fixes and adjacent audit

Every production Collector is constructed through `kube.New`. Its immutable `cluster_id` is a versioned hash derived with client-go's resolved API URL semantics: scheme, lowercased hostname, effective port and path prefix (ignoring trailing slashes). Userinfo, query and fragment never enter the hash or source metadata. Existing context, namespace, pod/UID, container ID/kind, previous flag and restart attribution remain intact. Snapshot/live events and discovery/stream gaps carry the identity. Drain scope retains it while permitting same-cluster replica reuse. Different hosts, schemes, ports and prefixes isolate reuse; equivalent endpoint spellings and repeated collection remain stable. The added scope component makes pre-change Kubernetes durable keys unreachable without deleting old state or changing file-source caches. The regression explicitly seeds an old key and verifies a fresh assessment. Endpoint identity is not a Kubernetes cluster UID: aliases can partition reuse, and replacement behind an unchanged endpoint retains the same routing identity.

The SPDY upgrade transport clones target TLS config and derives missing ServerName from the resolved API hostname independently of InsecureSkipVerify. Explicit ServerName and Go's IP/no-SNI behavior remain authoritative. Proxy TLS uses its original config, preserving its own hostname inference and existing explicit-name behavior; custom dial, CONNECT/SOCKS negotiation and cancellation socket ownership are unchanged. Verified TLS runs in the new local matrix. Existing network-listener mTLS/SPDY end-to-end tests passed in the independent host-level runs.

The shared Kubernetes transport imports standard client-go OIDC registration. Dependency versions/checksums remain unchanged. Both executables reach that package; ordinary collector/remote HTTP and SPDY use the same auth configuration boundary. The custom exec runner and its lifecycle, schema, credential precedence, rotation, concurrency and PTY tests remain active. OIDC intentionally retains upstream cache/refresh/persistence/CA-file behavior: its process-wide cache is keyed by cluster address, issuer and client ID. Upstream issuer refresh does not inherit the custom exec runner's cancellation guarantee. Only cached-token behavior is validated here; no live refresh or issuer security claim follows. Docs distinguish these auth contracts and explain template-ID/veto changes.

## Verification

Native Darwin arm64, Go 1.27.1, Rust 1.94.0, Python 3.14.7, Node 24.19.0. All runs used offline dependency settings: `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, `CARGO_NET_OFFLINE=true`, `PIP_NO_INDEX=1`; Go build cache is under ignored `.cache/build`.

The independent host-level passes below supersede the earlier builder-sandbox failures caused by denied local TCP/Unix listener binds. Those earlier attempts remain recorded in `.cache/compat-check.log`, `.cache/compat-all-race.log`, `.cache/compat-parity-run.log`, `.cache/compat-rust-test.log` and `.cache/compat-python.log`; they are not outstanding acceptance failures.

| Check | Result / evidence |
| --- | --- |
| Focused new tests, then gofmt | Passed before broader gates; `.cache/compat-focused.log` |
| Focused `go test -race -count=1` including new regressions, existing exec/upgrade-socket and durable identity tests | Passed; `.cache/compat-focused-race.log` |
| Final new regression suite `go test -race -count=2 ./internal/kube -run 'TestCollectorEndpoint\|TestSPDYTargetSNI\|TestOIDCCachedTokenCompatibility\|TestUnsupportedAuthCompatibility'` | Passed twice; `.cache/compat-regression-final.log` |
| `tools/check.sh` | Complete host-level gate passed: full Go tests/race including listener suites, vet, tidy/module diff and formatting, Linux amd64/386/arm cross-builds, six UI suites, all 124 Python tests, retained-asset regression, 60 retained assets verified with retirement disabled, and source/privacy checks; `.cache/independent-compat-check.log` |
| `go test -race ./...` (inside `tools/check.sh`) | Passed every package, including CLI, inspection and Kubernetes listener tests, with no race reports; `.cache/independent-compat-check.log` |
| Focused affected race packages: kube, analysis, controller, cli, inspect | Passed host-level command; `.cache/independent-compat-race.log`. Analysis reported no matching tests in this focused selection; its full race suite passed in `.cache/independent-compat-check.log` |
| `go vet ./...`; `go mod tidy`; module diff; `go mod verify` | Passed, no module changes; `.cache/compat-vet.log`, `.cache/compat-modules.log` |
| `tools/release.sh` | Passed independent host-level release, including native smoke for both executable names, signal/private-report checks, native state migration/reopen smoke and source/archive byte/privacy checks; `.cache/independent-compat-release.log` |
| `tools/check-cross.sh`; additional `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath` | Passed pure-Go Linux amd64/386/arm/arm64 builds; `.cache/compat-cross.log`, `.cache/compat-cross-arm64.log`; cross-built binaries were not executed |
| `python3 tools/check_parity.py --run` | Complete host-level pass: 14 workflows / 18 executable suites, all 60 retained assets byte/type/mode verified, retirement disabled; `.cache/independent-compat-parity.log` |
| Static parity and retained-asset mutation regression | Passed: 14 workflows / 18 suites inventoried, all 60 retained assets match byte/type/mode, retirement disabled; valid baseline and 45 rejected mutations; `.cache/compat-parity-static.log`, `.cache/compat-retained-tests.log` |
| Rust fmt / clippy (`--locked --offline --all-targets --all-features -- -D warnings`) / build | Passed; `.cache/compat-rust-{fmt,clippy,build}.log` |
| `cargo test --locked --offline --all-features` | Passed all 38 library and 7 CLI tests, plus binary/doc-test targets, in host-level executable parity; `.cache/independent-compat-parity.log` |
| Rust PTY suite | Passed search, Ctrl-C and SIGTERM, terminal restoration/private reports; `.cache/compat-rust-pty.log` |
| `python3 -m unittest discover -s tests -q` | All 124 tests passed in both host-level complete check and executable parity; `.cache/independent-compat-check.log`, `.cache/independent-compat-parity.log` |
| Six UI suites and both JavaScript trees' syntax checks | Passed; `.cache/compat-ui.log` |
| Retained implementation/package/tests/license diff against baseline | Passed: no changes to `src`, `jevernetes`, Cargo/Python manifests/locks, tests or LICENSE |
| `git diff --check`; `git diff --exit-code -- go.mod go.sum` | Passed; module files unchanged. Nothing has been staged or committed yet |

## Commit and operational limits

The complete acceptance gates passed independently on the host. Staging and the intended local commit have not yet occurred. No new commit exists; HEAD remains `aa75b6ce58f56e888ec8c936f8f11c9f7d8bdaf0` (its existing parent is `3338ea130d7daaca9d4916985654c047af296612`). The intended parent for the requested new commit is `aa75b6ce58f56e888ec8c936f8f11c9f7d8bdaf0`. The worktree contains the implementation, tests and docs and is **not clean**. These validation results do not claim a completed commit.

Host-level verification exercised local listeners, including the existing mTLS/SPDY end-to-end suites, as well as the new in-memory TLS fixtures. No live provider/cluster, deployment, container runtime, external proxy, hosted CI or production soak was used. Nonfatal macOS minimum-version linker warnings remain. Rust/Python coexistence and every retained asset are preserved; runtime retirement remains disabled. No push or deployment occurred.

---

# Upgrade sockets, interactive terminals and retained assets — 2026-10-11

Started in the exact existing worktree on `fix/go-full-parity-20261011`, clean at `3338ea130d7daaca9d4916985654c047af296612`. Read `CONTRIBUTING.md`; no applicable `AGENTS.md` was present. All endpoints, credentials, helper processes and evidence are synthetic/local. No push, deployment, GitHub mutation, live provider or cluster contact occurred. Rust/Python coexistence is preserved and runtime retirement remains disabled.

## Regression-first evidence

[Committed failure excerpts](docs/validation/lifecycle-before-3338ea13.txt) record failures before production edits. The permitted loopback run is `.cache/lifecycle-before-local.log`; the original restricted run is `.cache/lifecycle-before.log`.

- A fake endpoint accepts the port-forward POST and withholds response headers until explicitly released after the assertion. Both `Open` and an owning inspection `Serve` exceeded the one-second cancellation bound. The same failure reproduced during HTTP proxy CONNECT negotiation. The retained fixture additionally covers the real dashboard → inspection client → SPDY forwarder path, with a fake pod client. None of these success assertions releases the endpoint.
- A real interactive helper enters raw terminal mode, then succeeds, fails, blocks until forced timeout, or receives terminal Ctrl-C. All four initial cases left changed termios fields after helper join, despite foreground ownership being restored. The final fixture compares every saved `x/term.State` field, verifies foreground process ownership and checks the helper PID is gone. A zero-time readiness observation settles Darwin's transient PENDIN bookkeeping before comparison; no termios flags are masked. Darwin's canonical-mode transition and pending-input handling are documented in [Apple's tty implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/tty.c).
- A disposable local Git repository reproduces the old parity gate accepting 32 mutations across nine retained Rust/Python/test/package fixtures, including empty files, executable-mode changes and symlink replacements. The final regression also checks ordinary permission changes: all 45 mutations fail, while the original fixture passes. It lives in `tools/test_retained_parity.py` and runs in both the complete check and parity inventory.

## Fixes and adjacent lifecycle audit

The SPDY transport registers cancellation against the raw connection immediately after dialing, before proxy negotiation, TLS, request writes or response reads. It uses client-go's upgrade transport hook and keeps client-go's HTTP/SPDY response validation, framing, streams, pings and auth wrappers. HTTP/HTTPS CONNECT and SOCKS5 negotiation use the same cancellable socket; configured dial hooks, proxy callbacks, proxy credentials, verified TLS/SNI, exec tokens and client certificates remain active. Proxy callbacks receive the authenticated upgrade request, and TLS configuration remains discoverable through transport wrappers. Successful upgrades detach and join the handshake callback before response-body cleanup cancels the HTTP context. Forwarder completion closes any remaining socket on every path, including failed response validation and errors that bypass `NewConnection`. Tunnel cancellation watchers are stopped and joined before tunnel completion.

Additional regressions cover cancellation and deadlines during blocked request writes, direct/proxy TLS, SOCKS5, CONNECT and incomplete error bodies; socket closure at the peer; callback close/cancel races; authenticated mTLS over direct, HTTP-proxy and HTTPS-proxy paths; rejection of untrusted TLS; and survival of a successful SPDY connection after handshake-body cleanup. Existing real SPDY inspection/stream cancellation and HTTP exec-auth/refresh tests remain active.

Interactive exec auth saves termios inside the serialized foreground section, then restores foreground ownership and terminal attributes after `cmd.Run` has joined the helper and output readers, while SIGTTOU protection and serialization are still held. Restoration errors fail closed. Process-group cancellation and terminal prompts remain intact.

The parity gate compares raw Git blob bytes, regular-file type and exact permission modes for all 60 retained assets against the pinned accepted coexistence tree `3338ea130d7daaca9d4916985654c047af296612`. It does not trust the index or `core.fileMode`. The historical workflow reference remains `3a08dcb1c3611c0014b000074006701ad767bfa2`; `retained_base` separately freezes existing package/test adaptations and the migration companion. This avoids reverting the already-adapted `Cargo.toml`, `MANIFEST.in` and `tests/test_analyzer.py`. Only the evolving parity inventory itself is excluded; new acceptance suites can be added. The existing retained tree is unchanged. Documentation now states these lifecycle and retention guarantees explicitly.

## Verification

Native Darwin arm64, Go 1.27.1, Rust 1.94.0, Python 3.14.7 and Node 24.19.0. Dependencies remain offline (`GOPROXY=off`, `GOSUMDB=off`, `CARGO_NET_OFFLINE=true`, `PIP_NO_INDEX=1`), with `GOCACHE` in the ignored worktree cache and Node 24 on PATH. Local TCP/Unix listener permission was granted for the requested hermetic suites.

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/kube ./internal/inspect ./internal/cli -run 'TestSPDY\|TestUpgradeSocket\|TestExec\|TestRealSPDY\|TestCLIExecAuthShutdown\|TestInspectionShutdownJoinsHandlers'` | Passed; `.cache/lifecycle-focused.log` |
| Same focused command with `-race -count=1` | Passed, no race reports; `.cache/lifecycle-focused-race.log` |
| `tools/check.sh` | Complete gate passed with local-listener permission: full Go tests/race, `go vet ./...`, module tidy/diff, Linux amd64/386/arm cross-builds, six UI suites, Python, retained parity and source/privacy checks. Final pass: `.cache/lifecycle-check.log`; earlier full pass: `.cache/lifecycle-check-initial.log` |
| `tools/release.sh` | Passed local build/signing, both executable names, signal/private-report and state migration/reopen smoke, source/archive byte and privacy checks; `.cache/lifecycle-release.log` |
| `python3 tools/check_parity.py --run` | Passed 14 workflows / 18 suites; 60 retained assets byte/type/mode verified, retirement disabled; `.cache/lifecycle-parity.log` |
| `python3 -m unittest discover -s tests -q` | All 124 tests passed in complete check and parity runs |
| `python3 -m unittest discover -s tools -p test_retained_parity.py -q` | Passed the valid baseline and 45 rejection assertions; included in complete check and parity runs |
| `cargo fmt --check`; `cargo clippy --locked --offline --all-targets --all-features -- -D warnings` | Passed |
| `cargo test --locked --offline --all-features` | Passed all 38 library and 7 CLI tests, plus binary/doc-test targets, with local-listener permission; also rerun by parity |
| `cargo build --locked --offline`; `python3 tests/rust/tui_pty.py target/debug/jevernetes` | Passed build and all three PTYs (search, Ctrl-C, SIGTERM), with restored terminals and private reports |
| Six `tests/*_ui.cjs` suites; `node --check` for both dashboard JavaScript trees | Passed; UI suites included in complete check, syntax checks run separately |
| `git diff --exit-code 3338ea13 -- src jevernetes Cargo.toml Cargo.lock pyproject.toml MANIFEST.in requirements-dev.txt tests ':!tests/parity.json'` | Passed: retained implementation/package/test tree unchanged |
| `git diff --check`; `git diff --cached --check` | Passed; final source/privacy checks cover all 209 intended files; local commit and clean status verified after completion |

## Resolved fixture/environment issues and operational limits

The restricted first run could not bind its local endpoint; the permitted run reproduced the actual upgrade failures. A request-write fixture originally signaled readiness before dialing, allowing cancellation before socket ownership; it now signals at the blocked write. A one-second successful-handshake test timeout collided with the race detector's normal child-process exit delay; its success timeout is five seconds, while all stalled-cancellation assertions remain one second. Initial termios comparison exposed Darwin's transient pending-input bit after canonical-mode restoration; the fixture now observes readiness before comparing the complete state. These were resolved fixture issues, with no disabled tests or masked terminal fields. Nonfatal macOS minimum-version linker warnings remain.

These checks exercise local protocol endpoints and PTYs, not live providers/clusters, deployed proxies, container/PVC runtime, hosted CI or a production soak. Linux amd64/386/arm validation is cross-compilation, not execution. Custom dial/proxy hooks and credential helpers remain trusted local code: hooks must honor contexts, and intentionally detached descendants outside a helper's process group are not sandboxed. No full Go investigation-parity or retirement claim follows from these results. Historical entries below describe earlier passes only.

---

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
