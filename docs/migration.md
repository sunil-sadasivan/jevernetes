# Staged coexistence and rollback

The retirement sequence in [the base migration plan](migration-base.md) remains binding. Rust and Python stay supported until replacements pass equivalent workflow, security and data acceptance tests. This branch does not retire them. `tests/parity.json` is executable with `python3 tools/check_parity.py --run` and checks that base runtime/test assets still exist.

Use the Rust `jevernetes` / native `--tui` for the existing terminal and exact/semantic search. Use `jevernetes-legacy` or `python3 -m jevernetes` for dashboard live collection, investigation APIs, context, reviews, acknowledgements, expected rules, report browsing and prompt export. Their active acceptance suites and packaging are retained. Go releases use `jevernetes-go` / `jev-go`; the container is a separate Go controller product. Do not replace an investigation installation with the controller image.

Keep schema-1 reports and `.review-rules.json` in place for the Python companion. Human expected-event rules are not Go reviewed-template rules and must never be silently converted into provider suppression rules. The companion retains literal scope, disabled rules, acknowledgement keys and reversible original judgments. Go reports and controller SQLite state are separate artifacts. Back up private state while the controller is stopped before an upgrade; never run old and new writers concurrently. Preserve the backup for rollback; do not point older executables at a newer schema. No automatic destructive downgrade is provided.

Before retiring a runtime: port dashboard collection/search/review/context; preserve Host/Origin/CSRF, CSP, text rendering, fixed command/URL and read-only cluster boundaries; provide report and rule migration; run the complete acceptance inventory; remove the old runtime only in a separately reviewed change.

Live provider, production cluster and actual container-engine execution are not established by hermetic tests. Validation records must distinguish simulated HTTP/SPDY/storage contracts from those environments.

## Report compatibility and human review import

The Go read-only dashboard directly accepts schema-1 and schema-2 reports (maximum 64 MiB / 100,000 retained events), preserving original event IDs and evidence. Its query is literal case-insensitive text, not semantic search. `/api/report?offset=0&limit=50&q=literal` returns byte-bounded pages, total retained/matched counts, a next offset, explicit unavailable-event counts and coverage status. Summary metadata is bounded at 32 KiB after JSON encoding. Oversized imported events are explicitly marked with their original index; use the original report in the companion for full evidence. Coverage without an explicit schema-2 completeness assertion is unverified. Page completeness is separate from collection completeness. Full coverage, groups and learning metadata remain in the original report, rather than being duplicated in each page. The UI searches all retained events and exposes next/first-page navigation.

Human review state stays in the supported companion's version-1 format. For a new private destination directory, run:

```sh
mkdir -m 700 investigation-copy
python3 -m jevernetes.migrate backup/.review-rules.json investigation-copy/.review-rules.json
```

This is an explicit import into the coexisting investigation runtime, not into Go's provider-template rules. It preserves literal scopes, enabled/disabled states, rule IDs, acknowledgement report keys/event IDs, duplicates and reversible projections. Keep report filenames unchanged when copying schema-1 reports alongside it: acknowledgement keys identify those filenames. Source files are never modified. An identical rerun does nothing; a differing destination, malformed input, conflicting duplicate identity, symlink or special-file input fails without replacement. Import uses private temporary files, fsync and atomic no-clobber publication. Interrupted pre-publication staging can be ignored; rerun the command. A destination published before an interruption is complete and recognized on rerun. Maximum input is 16 MiB. ReviewStore's restored tests and migration tests exercise scope isolation and restoring original judgments after disabling/removing projections.

## Acceptance status for this staged change

| Workflow | Status | Executable coverage |
| --- | --- | --- |
| Existing Rust CLI, TUI, exact/semantic search | Coexistence; base implementation restored | 38 Rust library tests, 7 CLI tests, PTY search/Ctrl-C/SIGTERM |
| Python live dashboard, investigation API, search/context/review/expected rules | Coexistence; active packaged companion | Full Python suite and installed-wheel suite; five legacy browser suites |
| Prompt/export assets | Exact base assets | `tests/test_prompt_ui.cjs` |
| Go intake, plain evidence output, provider attribution, controller stop lifecycle | Equivalent bounded contracts | Delayed online intake/duration tests, three supervised-state restart cycles, fake provider HTTP |
| Report/rule continuity | Schema-1 view plus coexistence importer | Bounded paging/escaped-byte tests; idempotent no-clobber review import and reversible projection tests |
| Kubernetes and remote inspection | Equivalent wire contracts | Actual client-go HTTP snapshots/reconnect and SPDY port-forward against local fakes |
| State migrations, outbox, packaging, RBAC | Hermetic equivalent contracts | SQLite crash/recovery/upgrade tests; native manifest-volume smoke; private archive checks |
| Live provider accuracy/billing, actual clusters/container runtime, long production soak | Unverified | No live execution authorized or performed |

The merged change must remain staged coexistence. Full Go-native investigation parity is still deferred; removing either existing runtime would fail the retirement gate. Provider budgets are process bounds, so a deployment supervisor can renew them by restarting the process. That policy must be chosen deliberately. Runtime files/state remain separate, enabling rollback by restoring a quiescent backup and the previous executable without translating human review rules into model-template rules.

TypeSafe questions now name their exact `groups[index]` and evidence ID, retain descriptive importance/severity/category criteria, and carry redacted source metadata alongside opaque group identity. The controller risk-contract namespace changes to `go-v2-attributed-criteria`, preventing reuse of judgments cached under the prior request semantics. All three adapters are covered by real local HTTP serialization/decoding tests; provider model quality and billing remain unverified.

## Local Kubernetes credential helpers

Go collection and remote/dashboard inspection use a context-aware exec-credential transport for local kubeconfigs. Helpers receive the request's cancellation and the owning command's shutdown, with a separate 30-second execution ceiling. The process group is killed on cancellation and the command/output readers are joined; inspection HTTP shutdown also joins handlers. This applies to first authentication, expiry refresh, 401 refresh and SPDY upgrades. An HTTP timeout alone does not provide this guarantee. Helpers remain trusted local executable code, not a sandbox; deliberately detached processes outside their process group are outside this contract.

The transport accepts `client.authentication.k8s.io/v1` and `v1beta1`, preserves configured environment overrides, interactive modes, plugin allow/deny policy, explicit credential precedence, cluster information and per-cluster extension/audience data. Caches are per client, with no cross-client/global credential reuse. Tokens remain opaque; the API server validates their audience. Certificate/key pairs are parsed together, certificate rotation closes tracked HTTP connections, and TLS callbacks only use already-acquired credentials. 401 refresh prepares the next request without replaying the current one. Unknown or duplicate JSON fields, wrong kind/version, empty credentials, malformed pairs, expired credentials, control characters in tokens and output over 1 MiB fail closed. Noninteractive helper stderr and detailed parsing/command failures are suppressed. Interactive helpers keep terminal prompts, foreground ownership is restored after success/cancellation, and Ctrl-C returns cancellation. PTY regression coverage requires Python 3, already a repository development prerequisite.

Local kubeconfig/CA/certificate/key/token paths retain the strict regular-file contract. The fixed explicit in-cluster service-account credentials remain client-go owned. The controller's deployment-only projection opt-in is documented in [deployment operations](../deploy/README.md); it does not apply to local kubeconfigs. Go/Rust/Python coexistence remains staged and runtime retirement remains disabled.
