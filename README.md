# Jevernetes

Read-only log triage and investigation. This branch stages the Go controller alongside the working Rust terminal and Python investigation companion. No existing runtime or user data is retired.

- **Rust (existing CLI/TUI):** `cargo build --locked --release`; run `target/release/jevernetes files app.log --offline --tui`. Exact and semantic search remain in the native terminal. See [usage](docs/usage-rust.md).
- **Python (investigation):** `python3 -m pip install .`; run `jevernetes-legacy --help` or `python3 -m jevernetes --help`. Dashboard live collection, report browsing, context, search, review/acknowledgements, expected rules and prompt export remain supported. See [legacy usage](docs/legacy-usage.md) and [dashboard](docs/legacy-dashboard.md).
- **Go (new CLI/controller):** `go build -o bin/jevernetes-go ./cmd/jevernetes`; run `bin/jevernetes-go --help`. `tools/release.sh` produces `jevernetes-go` / `jev-go`, leaving existing commands untouched. See [Go usage](docs/usage.md), [controller](docs/controller.md) and [deployment](deploy/README.md).

Keep existing schema-1 reports and `.review-rules.json` with the companion. Go reviewed-template artifacts are a separate contract, not a replacement for human reviews. The Go container runs only the Go controller; it does not replace an investigation installation.

See [migration and rollback](docs/migration.md), the [executable parity inventory](tests/parity.json), and [validation](VALIDATION.md) for tested boundaries. All external provider and cluster operations require explicit operator configuration; acceptance tests use synthetic inputs and local fakes.
