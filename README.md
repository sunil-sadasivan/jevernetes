# Jevernetes

Read-only log triage and investigation in one Go executable: CLI, TUI, dashboard and durable controller. No Python, Rust or kubectl runtime is required.

Build with Go 1.27.2 or newer:

```sh
go build -o ./jevernetes ./cmd/jevernetes
./jevernetes files examples/mixed.log --offline --tui
./jevernetes k8s -f --offline --tui
./jevernetes dashboard --reports-dir .runs
```

`make build` builds the same local executable. `tools/release.sh` produces `dist/bin/jevernetes`, its `jev` alias, and `dist/jevernetes-binary.tar.gz`. The container uses the same `jevernetes` command.

The terminal supports clickable tabs, multiline details, exact and semantic search, grouped occurrences and independent search budgets. The dashboard supports uploads, live collection, filters, confidence/grouping, semantic search, bounded context, bulk reviews, expected rules, themes and prompt export. Controller monitoring is available at `/controller/`; `--inspection-only` serves the compact read-only dashboard.

Existing schema-1/schema-2 reports and version-1 `.review-rules.json` open directly in Go. Keep their filenames and select their directory with `--reports-dir`. Model-reviewed template rules remain separate from human reviews.

See [usage](docs/usage.md), [migration and rollback](docs/migration.md), [controller](docs/controller.md), [deployment](deploy/README.md), [workflow acceptance](tests/parity.json), and [validation](VALIDATION.md). External provider and cluster operations require operator configuration; automated tests use synthetic inputs and local fakes.
