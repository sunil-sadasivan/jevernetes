# Architecture

`cmd/jevernetes` and `cmd/jev` invoke the same Cobra command tree. Packages under `internal/` provide the complete runtime.

| Package | Responsibility |
| --- | --- |
| `event` | Incremental byte framing, private-key suppression, JSON/text redaction, multiline events, deterministic IDs, offline rules |
| `analysis` | Single-owner bounded grouping state; scope/version/generation tickets; microbatch dossiers and fanout; adaptive audits; reviewed rules; provider learning |
| `provider` | Fixed-host adapters, strict JSON schemas, cumulative reservations, token/cost accounting, sanitized transport errors |
| `report` | Bounded event/coverage retention, schema-2 snapshots, private atomic replacement |
| `kube` | Official client-go pod discovery, log collection, reconnect state, exact selection and port-forward lifecycle |
| `controller` | Pure-Go SQLite, migrations, writer lock, deterministic policy, novelty, verdicts, outbox and sinks |
| `inspect` | Separate health/metrics and loopback inspection protocols; strict remote validation |
| `dashboard` | Embedded static UI, fixed-target sampling/cache and same-origin read-only APIs |
| `cli` | Validation before inputs/credentials, cancellation, producer ownership, shutdown/reporting |

A bounded producer channel feeds one analysis owner. Files/snapshots wait for capacity; live streams drop excess events with counters. Batches close at capacity or after 350ms. Provider I/O blocks that owner, so downstream latency includes earlier batches and provider latency. No occurrence-count threshold delays a new singleton judgment.

Controller transitions and notification intents share a transaction. The single delivery loop records an attempt before sending, retaining stable idempotency keys across crashes. Inspection uses a nonblocking store lock and cannot fail ingestion merely because a reader contends. Metrics and report snapshots use independent locks. Shutdown cancels producers, closes streams, joins workers and writes the final report.

Established libraries are pinned in `go.mod`/`go.sum`: client-go, Prometheus client_golang, modernc SQLite, Cobra/pflag and x/sys file locking. The [axiomhq/drain3 API](https://github.com/axiomhq/drain3) was evaluated: its similarity-based generalization and immutable trained matcher do not supply protected-field, exact scope or verdict-generation guards. The bounded conservative implementation keeps those application invariants explicit. It generalizes a fixed grammar, not arbitrary similar strings.

Kubernetes transport timestamps are removed from each physical line before multiline framing; the first physical line supplies the event timestamp. Chunk boundaries do not change framing. Live idle flushes still bound latency; an orphan continuation is marked parse-uncertain. Invalid UTF-8 and missing transport timestamps also mark parse uncertainty, which prevents reusable grouping and requires controller review.

Completeness is derived from cumulative report metrics, not retained coverage samples. Every `coverage_*` signal, truncation, parse uncertainty, queue drop, retention eviction, state eviction, event/byte limit and interrupted collection prevents `complete_within_window=true`. Kubernetes and live reports always remain incomplete. Coverage records are capped at 500 with an omitted-record counter. Prometheus and inspection expose collection gaps and cursor/capacity signals alongside ingestion counters.

Controller schema 4 keeps row counts and eviction totals transactionally with insert/delete triggers. Eviction and novelty/incident/outbox writes commit together. An all-pending outbox applies backpressure instead of deleting delivery intent; it does not block the independent delivery worker. A global delivery sequence survives incident eviction. See [controller operations](controller.md) for exact bounds and consequences.
