# Compatibility and removal plan

Rust `jevernetes` is the default for ingestion, classification and reports. The Python `jevernetes/` package is a separate, explicitly legacy companion retained to avoid breaking investigation workflows. It is not imported or invoked by Rust. Its existing modules and test layout remain in place to preserve imports and `python3 -m jevernetes`; the installable Python distribution/entry point is now `jevernetes-legacy`. Uninstall any older Python-installed `jevernetes` command before installing the Rust binary to avoid PATH ambiguity.

| Behavior | Rust status | Legacy/deferred gap |
| --- | --- | --- |
| Text/JSONL/gzip/stdin; bounded decompression | Implemented | Limits measured in UTF-8 bytes; no directory/glob expansion beyond shell behavior |
| Timestamps, continuation stacks, redaction, stable IDs | Implemented | IDs intentionally differ from Python and are stable only for identical parsed occurrence inputs; live quiet flush may split delayed traces |
| Typed importance/severity/category + confidence | Implemented, with `fraud` | No semantic-search relevance API in Rust |
| Strict validation, safe errors, HTTP budgets/usage | Implemented | One provider lane; no `--workers`; default rates are legacy assumptions |
| Exact source grouping, successful judgment reuse | Implemented | Bounded session cache in files/k8s; explicit controller mode adds versioned SQLite TTL reuse; large snapshots may reclassify evicted groups |
| Offline rules | Implemented | No saved expected/acknowledged overrides applied by Rust |
| Kubernetes current/previous regular/init/ephemeral snapshots | Implemented through kube-rs | Sequential reads; conservative partial coverage even on successful API responses |
| Long-running discovery/follow/reconnect | Implemented through paginated watch/log API | Bounded active targets; omitted targets reconsidered on updates/5-minute resync; no durable cursor, exactly-once or replica coordination |
| Queue/batch/retention bounds and shutdown | Implemented | Live dropped payloads are counters only; no pending-row UI; reports saved at shutdown only |
| JSON report and private atomic writes | Implemented, schema 2 | Legacy UI accepts its own schema 1, not a supported Rust import contract; coverage history is bounded observations |
| Dashboard/theme/filters/clipboard prompts | Legacy Python only | Requires a Rust service/API bridge and browser contract tests before migration |
| TUI/interactive tabs and exact/semantic search | Legacy Python only | Needs a Rust terminal/event API adapter and frozen-window search parity |
| Review rules, bulk acknowledgment, expected rules | Legacy Python only | Needs versioned rule persistence, atomic bulk validation, reversible baseline projection and source-scope tests |
| Context windows and extra Kubernetes history | Legacy Python only | Needs bounded context retrieval, instance/restart checks and context UI parity |

Unsupported flags fail explicitly in Rust. No `--tui`, dashboard, review, search or context workflow is silently routed through Python. The retained Python collector/classifier/parser are dependencies of those workflows, not the primary runtime. Existing synthetic demos are legacy demonstrations and remain useful without implying Rust UI parity.

## Concrete retirement sequence

1. Add a versioned Rust service/report adapter for the existing dashboard and TUI. Validate pending/unknown/dropped states, full retained-window queries, source identity and schema migration using the existing browser/Python fixtures. Switch both UIs to consume Rust collection; then remove Python `events.py`, `kubernetes.py`, `live.py`, collection portions of `cli.py`, and duplicate grouping/analysis hot paths once no imports remain.
2. Port relevance search and its frozen evidence windows, occurrence metadata, TTL cache, separate budget/usage, cancellation and unevaluated-evidence behavior. Port reversible reviews and private atomic rules storage without changing literal source scoping. Port bounded context fetch with replacement/restart checks and investigation prompt evidence limits. Use the existing search/review/context tests as acceptance fixtures, plus adapter tests against Rust.
3. Port/replace terminal rendering and the loopback HTTP/API implementation, preserving browser Host/Origin/token/CSP controls, safe text rendering, private files and all keyboard/search interactions. Run UI and signal/cleanup regression suites. Provide a documented report/rule migration path, then remove the remaining Python package, Python packaging/dependencies and obsolete CI jobs in a separately reviewed change.

The second stacked deliverable adds [durable probabilistic controller state and notification](controller.md) and the [control-plane product/opportunity design](probabilistic-control-plane.md). These additions do not close the legacy UI/workflow gaps.
