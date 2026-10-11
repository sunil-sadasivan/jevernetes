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
