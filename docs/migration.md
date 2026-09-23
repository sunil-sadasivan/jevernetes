# Compatibility and removal plan

Rust `jevernetes` is the default for ingestion, classification and reports. The Python `jevernetes/` package is a separate, explicitly legacy companion retained to avoid breaking investigation workflows. It is not imported or invoked by Rust. Its existing modules and test layout remain in place to preserve imports and `python3 -m jevernetes`; the installable Python distribution/entry point is now `jevernetes-legacy`. Uninstall any older Python-installed `jevernetes` command before installing the Rust binary to avoid PATH ambiguity.

| Behavior | Rust status | Legacy/deferred gap |
| --- | --- | --- |
| Text/JSONL/gzip/stdin; bounded decompression | Implemented | Limits measured in UTF-8 bytes; no directory/glob expansion beyond shell behavior |
| Timestamps, continuation stacks, redaction, stable IDs | Implemented | IDs intentionally differ from Python and are stable only for identical parsed occurrence inputs; live quiet flush may split delayed traces |
| Typed importance/severity/category + confidence | Implemented, with `fraud` | Relevance search also uses typed choices with conservative confidence handling |
| Strict validation, safe errors, HTTP budgets/usage | Implemented | One provider lane; no `--workers`; default rates are legacy assumptions |
| Exact source grouping, successful judgment reuse | Implemented | Bounded five-minute cache in files and live; no persistent cache; large snapshots may reclassify evicted groups |
| Offline rules | Implemented | No saved expected/acknowledged overrides applied by Rust |
| Kubernetes current/previous regular/init/ephemeral snapshots | Implemented through kube-rs | Sequential reads; conservative partial coverage even on successful API responses |
| Long-running discovery/follow/reconnect | Implemented through paginated watch/log API | Bounded active targets; omitted targets reconsidered on updates/5-minute resync; no durable cursor, exactly-once or replica coordination |
| Queue/batch/retention bounds and shutdown | Implemented; TUI shows the pending analysis batch | Queued/dropped payloads are counters only; reports saved at shutdown only |
| JSON report and private atomic writes | Implemented, schema 2 | Legacy UI accepts its own schema 1, not a supported Rust import contract; coverage history is bounded observations |
| Dashboard/theme/filters/clipboard prompts | Legacy Python only | Requires a Rust service/API bridge and browser contract tests before migration |
| TUI/interactive tabs, mouse/keyboard, multiline details | Native Rust `--tui` | No saved-report import; requires an interactive terminal; legacy TUI remains available |
| Exact/semantic search and grouped instances | Native Rust TUI; frozen windows, occurrence-aware TTL cache, separate usage/budgets, cancellation | No standalone API/browser adapter; results are session-only; local search uses Unicode lowercase substring matching |
| Review rules, bulk acknowledgment, expected rules | Legacy Python only | Needs versioned rule persistence, atomic bulk validation, reversible baseline projection and source-scope tests |
| Context windows and extra Kubernetes history | Legacy Python only | Needs bounded context retrieval, instance/restart checks and context UI parity |

Unsupported flags fail explicitly in Rust. `--tui` and its searches run entirely in Rust. Dashboard, review and extra-context workflows are not silently routed through Python. The retained Python collector/classifier/parser are dependencies of the legacy workflows, not the primary runtime. Existing synthetic demos still show the legacy UI.

## Concrete retirement sequence

1. The native Rust terminal now consumes a bounded runtime progress view. Add a versioned Rust service/report adapter for the existing dashboard. Validate pending/unknown/dropped states, retained-window queries, source identity and schema migration using the browser/Python fixtures. Switch the dashboard to Rust collection before removing any legacy collection dependencies.
2. Expose Rust relevance search through the dashboard adapter. Port reversible reviews and private atomic rules storage without changing literal source scoping. Port bounded context fetch with replacement/restart checks and investigation prompt evidence limits. Use existing search/review/context tests as acceptance fixtures, plus adapter tests against Rust.
3. Replace the loopback HTTP/API implementation, preserving browser Host/Origin/token/CSP controls, safe text rendering, private files and keyboard/search interactions. Keep the Rust terminal rendering and signal/cleanup regression suites. Provide a documented report/rule migration path, then remove the remaining Python package, Python packaging/dependencies and obsolete CI jobs in a separately reviewed change.

The next sequential deliverable may introduce durable probabilistic controller state; it must not claim these UI/workflow gaps have disappeared. No broader product-opportunity document is included here.
