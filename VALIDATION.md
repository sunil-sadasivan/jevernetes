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
