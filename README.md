# Jevernetes

**Jevernetes uses Rust to collect and analyze Kubernetes logs.** The `jevernetes` binary provides bounded streaming ingestion, local offline rules, typed [Jev](https://docs.typesafe.ai) analysis, an interactive terminal UI, and JSON reports. Collection is read-only and judgments are advisory.

## Quick start

Install Rust 1.94 (the verified toolchain), then build from this repository:

```sh
cargo build --release --locked
./target/release/jevernetes files - --offline --json <<'LOGS'
2026-09-20T12:00:00Z ERROR dependency unavailable
  at synthetic_worker:12
2026-09-20T12:00:01Z INFO ready
LOGS
```

To install the default binary:

```sh
cargo install --path . --locked
jevernetes k8s --offline --namespace example --since 1h --output .runs/snapshot.json
jevernetes k8s -f --offline --tail 0 --max-streams 64 --output .runs/live.json
jevernetes k8s -f --tail 0 --tui
```

Kubernetes access uses the Rust Kubernetes client with your trusted kubeconfig, or service-account configuration with `--in-cluster`. No `kubectl` or Python is needed for the Rust runtime. Offline file analysis makes no network requests. Online analysis requires `TYPESAFE_API_KEY`, `TYPESAFEAI_API_KEY`, or `TYPESAFE_API_KEY_FILE`; omit `--offline` to enable it. Never put credentials into command-line arguments or git.

Live mode reports queue pressure and coverage gaps on stderr, batches analysis, and saves a final report on SIGINT/SIGTERM or a configured duration/budget stop. Reports contain a bounded retained window, cumulative counters, and usage estimates. Kubernetes reports conservatively report restricted history; exit code 2 means partial coverage, not a crash.

Use `--tui` for clickable Important, Routine, Needs Review, All, and Search tabs. Press `/` to ask Jev, `f` to find exact text, Enter for event details, and `q` to stop and exit. Completed collection stays open for browsing and saves its report on exit. Search freezes the retained window and has separate usage and budgets. Add `--offline` for local rules and exact search without API calls. See [terminal usage](docs/usage.md#interactive-terminal).

## Compatibility

The **Python companion is legacy** and remains available for the dashboard, review overrides, context fetching, investigation-prompt export, and its older terminal UI:

```sh
python3 -m jevernetes dashboard
python3 -m jevernetes k8s -f --offline --tui
```

These commands still use the legacy Python collector and require Python 3.11+ and `kubectl`. Installing the Python package provides `jevernetes-legacy`, so it cannot overwrite the Rust command. Rust report schema 2 is not yet an input contract for the legacy UI. Existing UI screenshots and demos show the legacy companion.

See [Rust usage](docs/usage.md), [architecture](docs/architecture.md), [exact parity and removal plan](docs/migration.md), and the [legacy dashboard guide](docs/legacy-dashboard.md).

## Data and security

Jev mode sends redacted log text and source metadata to TypeSafe over verified HTTPS. Redaction is best effort, including streamed private-key suppression. Use offline mode when evidence must stay local. Logs are untrusted data; neither model answers nor log content can invoke tools or mutate Kubernetes resources. Keep reports, kubeconfigs, real logs, and credentials outside git.

[Security policy](SECURITY.md) · [Security review scope](SECURITY_REVIEW.md) · [Contributing](CONTRIBUTING.md) · [Validation](VALIDATION.md)

Licensed under Apache 2.0. Inspired by [Log Sentinel](https://github.com/dabit3/jev-experiments/tree/main/log-sentinel).
