# Jevernetes

Read-only log triage and investigation with a native Go CLI, TUI, dashboard and durable controller. Rust and Python remain available for rollback; no existing runtime or user data is retired.

- **Rust (existing CLI/TUI):** `cargo build --locked --release`; run `target/release/jevernetes files app.log --offline --tui`. Exact and semantic search remain in the native terminal. See [usage](docs/usage-rust.md).
- **Python (investigation):** `python3 -m pip install .`; run `jevernetes-legacy --help` or `python3 -m jevernetes --help`. Dashboard live collection, report browsing, context, search, review/acknowledgements, expected rules and prompt export remain supported. See [legacy usage](docs/legacy-usage.md) and [dashboard](docs/legacy-dashboard.md).
- **Go (CLI/TUI/dashboard/controller):** `go build -o bin/jevernetes-go ./cmd/jevernetes`; run `bin/jevernetes-go --help`. `tools/release.sh` produces `jevernetes-go` / `jev-go`, leaving existing commands untouched. See [Go usage](docs/usage.md), [controller](docs/controller.md) and [deployment](deploy/README.md).

The Go dashboard reads existing schema-1/schema-2 reports and version-1 `.review-rules.json` directly. Use `--reports-dir` to select their directory. Go reviewed-template artifacts remain a separate contract from human reviews. The Go container runs only the Go controller; it does not replace an investigation installation.

See [migration and rollback](docs/migration.md), the [executable parity inventory](tests/parity.json), and [validation](VALIDATION.md) for tested boundaries. All external provider and cluster operations require explicit operator configuration; acceptance tests use synthetic inputs and local fakes.

```sh
go build -o bin/jevernetes-go ./cmd/jevernetes
bin/jevernetes-go files app.log --offline --tui
bin/jevernetes-go k8s -f --offline --tui
bin/jevernetes-go dashboard --reports-dir .runs
```

The Go terminal has clickable tabs, multiline details, frozen exact/Jev searches, occurrence inspection and independent search budgets. The dashboard uses the established investigation interface for uploads, live collection, filters, confidence/grouping, semantic search, bounded context, bulk reviews, expected rules, themes and prompt export. Controller monitoring is linked at `/controller/`; `--inspection-only` retains the compact read-only dashboard.
