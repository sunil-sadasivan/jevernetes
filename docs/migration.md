# Staged coexistence and rollback

The retirement sequence in [the base migration plan](migration-base.md) remains binding. Rust and Python stay supported until replacements pass equivalent workflow, security and data acceptance tests. This branch does not retire them. `tests/parity.json` is executable with `python3 tools/check_parity.py --run` and checks that base runtime/test assets still exist.

Use the Rust `jevernetes` / native `--tui` for the existing terminal and exact/semantic search. Use `jevernetes-legacy` or `python3 -m jevernetes` for dashboard live collection, investigation APIs, context, reviews, acknowledgements, expected rules, report browsing and prompt export. Their active acceptance suites and packaging are retained. Go releases use `jevernetes-go` / `jev-go`; the container is a separate Go controller product. Do not replace an investigation installation with the controller image.

Keep schema-1 reports and `.review-rules.json` in place for the Python companion. Human expected-event rules are not Go reviewed-template rules and must never be silently converted into provider suppression rules. The companion retains literal scope, disabled rules, acknowledgement keys and reversible original judgments. Go reports and controller SQLite state are separate artifacts. Back up private state while the controller is stopped before an upgrade; never run old and new writers concurrently. Preserve the backup for rollback; do not point older executables at a newer schema. No automatic destructive downgrade is provided.

Before retiring a runtime: port dashboard collection/search/review/context; preserve Host/Origin/CSRF, CSP, text rendering, fixed command/URL and read-only cluster boundaries; provide report and rule migration; run the complete acceptance inventory; remove the old runtime only in a separately reviewed change.

Live provider, production cluster and actual container-engine execution are not established by hermetic tests. Validation records must distinguish simulated HTTP/SPDY/storage contracts from those environments.
