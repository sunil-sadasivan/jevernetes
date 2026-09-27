# Jevernetes

**Jevernetes uses Rust to collect and analyze Kubernetes logs.** The `jevernetes` binary provides bounded streaming ingestion, local offline rules, typed [Jev](https://docs.typesafe.ai) analysis, JSON reports, and a durable probabilistic monitoring controller. Collection is read-only and judgments are advisory.

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
```

Kubernetes access uses the Rust Kubernetes client with your trusted kubeconfig, or service-account configuration with `--in-cluster`. No `kubectl` or Python is needed for the Rust runtime. Offline file analysis makes no network requests. Online analysis requires `TYPESAFE_API_KEY`, `TYPESAFEAI_API_KEY`, or `TYPESAFE_API_KEY_FILE`; omit `--offline` to enable it. Never put credentials into command-line arguments or git.

Live mode reports queue pressure and coverage gaps on stderr, batches analysis, and saves a final report on SIGINT/SIGTERM or a configured duration/budget stop. Reports contain a bounded retained window, cumulative counters, and usage estimates. Kubernetes reports conservatively report restricted history; exit code 2 means partial coverage, not a crash.

## In-cluster controller

`jevernetes controller --namespace example --state /var/lib/jevernetes/state.db`
continuously follows logs, reuses successful judgments from SQLite, applies a deterministic
security/fraud confidence policy, and commits notifications to a durable outbox. JSONL
stdout and HTTPS webhook sinks are available. Review is the default for uncertain evidence;
there is no automatic remediation. One replica owns one persistent volume.

See [controller operation and exact limits](docs/controller.md), the [Kustomize base](deploy/base),
and the [probabilistic control-plane thesis and roadmap](docs/probabilistic-control-plane.md).
Templates contain placeholder images and Secret references; no deployment is implied.

In-repository Drain template mining is opt-in with `--grouping-strategy drain`; exact
reuse remains the default. It reduces remote representatives while retaining every
accepted event for reports and controller policy. Run the deterministic offline demo:

```sh
cargo run --locked --example drain_reduction
cargo test --locked --all-features drain
```

The sequential synthetic fixture varies only opaque request IDs and reduces 1,000
events to 2 classifications with 998 reuses. Drain honors the configured batch size;
classification savings differ from request savings. IPs and numeric telemetry stay
literal. The fixture retains all 1,000 events and makes no network calls. See [Drain design and limits](docs/drain-template-mining.md)
for conservative variable fields, bounded capacity and process-local restart behavior.

## Compatibility

The **Python companion is legacy** and remains available for the dashboard, interactive TUI, semantic search, review overrides, context fetching, and investigation-prompt export:

```sh
python3 -m jevernetes dashboard
python3 -m jevernetes k8s -f --offline --tui
```

These commands still use the legacy Python collector and require Python 3.11+ and `kubectl`. Installing the Python package provides `jevernetes-legacy`, so it cannot overwrite the Rust command. Rust report schema 2 is not yet an input contract for the legacy UI. Existing UI screenshots and demos show the legacy companion.

See [Rust usage](docs/usage.md), [architecture](docs/architecture.md), [exact parity and removal plan](docs/migration.md), and the [legacy dashboard guide](docs/legacy-dashboard.md).

## Data and security

Online mode sends redacted log text and source metadata to the explicitly selected provider over verified HTTPS (TypeSafe by default). Redaction is best effort, including streamed private-key suppression. Use offline mode when evidence must stay local. Logs are untrusted data; neither model answers nor log content can invoke tools or mutate Kubernetes resources. Keep reports, kubeconfigs, real logs, and credentials outside git.

[Security policy](SECURITY.md) · [Security review scope](SECURITY_REVIEW.md) · [Contributing](CONTRIBUTING.md) · [Validation](VALIDATION.md)

Licensed under Apache 2.0. Inspired by [Log Sentinel](https://github.com/dabit3/jev-experiments/tree/main/log-sentinel).

## Provider choice and structured shadow learning

Rust supports explicit `--risk-provider typesafe|openai|anthropic`, with TypeSafe/Jev
remaining the default. OpenAI/Anthropic require an explicit model and both operator
prices. Credentials come only from environment or bounded `*_API_KEY_FILE` reads;
prefer mounted files in Kubernetes. There is no provider fallback.

`--grouping-strategy semantic --template-provider openai|anthropic` opts into
**shadow-only** structured template proposals. The template role has an independent
model, rates and budget. Local validation checks observed paths, protected fields,
minimum support and deterministic replay, but never promotes or reuses proposals.
Without a reviewed artifact every event still classifies and reaches controller policy; shadow savings
are zero in this release. Exact defaults and Drain behavior are preserved.
See [provider contracts, bounds, setup and activation limitations](docs/provider-template-learning.md).

```sh
CARGO_NET_OFFLINE=true cargo run --locked --example semantic_shadow
```

Separately reviewed compiled rules can activate deterministic semantic reuse with
`--grouping-strategy semantic --template-rules PATH` (default off). LLM proposals stay
shadow-only and cannot self-promote. Active reuse needs only the risk provider, retains
every occurrence, and uses bounded session-only verdicts. See the
[reviewed artifact contract](docs/provider-template-learning.md#reviewed-artifact-activation)
and run `cargo run --locked --example reviewed_reduction` for a socket-free synthetic demo.
