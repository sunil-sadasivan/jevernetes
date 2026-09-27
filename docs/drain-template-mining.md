# Native Drain template-mining experiment

Drain uses an in-repository clean-room Rust implementation under the repository's
Apache-2.0 license. It has no mining dependency, vendored/copied third-party source,
external persistence service or runtime service dependency. `logdrain` and its
transitive `bincode`/`dashmap` dependencies have been removed.

## Select the classification strategy

`--grouping-strategy exact|off|drain` defaults to `exact`. `--no-grouping` remains an
alias for disabling session reuse and conflicts with an explicit strategy. The
controller accepts `exact` or `drain`, preserving its existing requirement for
grouping. Ordinary `--offline` continues to use local rules without mining or
inventing cacheable Jev confidence. The synthetic demo below exercises mining
separately and never loads credentials or opens sockets.

Every accepted Event still goes through reporting and, in controller mode, policy,
novelty, recurrence and durable notification intent. Drain changes only the choice
of remote representatives. Queue drops, retention eviction and controller capacity
limits remain explicit existing boundaries; Drain does not deduplicate evidence.

## Safety and identity

The minimal online core partitions before mining by the full serialized stable
source, complete local baseline (`important` and every signal), and a conservative
token shape. This
identity is compared exactly, without a shortened hash. It allows only these
space-separated `name=value` fields to vary:

| Field | Accepted variable |
| --- | --- |
| `request_id`, `request-id`, `trace_id`, `span_id`, `correlation_id` | Exactly 16, 32 or 64 ASCII hex digits, or canonical UUID syntax (8-4-4-4-12 hex digits) |

Field name, hex width and UUID/hex format stay distinct. Malformed IDs stay literal.

Everything else stays literal, including IP addresses, counts, durations, latencies,
bytes, attempts, status codes, bare numbers, words, paths, user names and field names.
In particular, `ip=127.0.0.1` cannot cover `ip=0.0.0.0`, and `duration_ms=1` cannot
cover `duration_ms=86400000`. Only single-line text with single spaces between tokens
is eligible. Control characters, irregular whitespace, literal `<*>`, missing stable
Kubernetes fields, redacted/private provenance, redaction markers, truncation and
oversized evidence take an ordinary classification miss. This conservative grammar
intentionally bypasses most JSON and stack traces. The Event `sensitive` flag
retains private/redaction provenance even when a later truncation removes a marker;
JSON reserialization changes can conservatively set it too.

Kubernetes reuse scope is `type`, `context`, `namespace`, `container`, `kind`.
The collector's context includes a hash of the API URL. Pod name, UID, restart
count and previous/current instance are excluded from template identity, allowing
replica reuse. **This is a namespace/container-kind/name boundary, not a workload
owner boundary**: different workloads using the same container name in that scope
can share a matching template. Full original source, including pod/UID/restart,
remains on Events and reports; notifications retain the existing source allowlist.
Files/other sources use every original source field (including file path).

This is an experimental cost optimization, not a semantic-equivalence or accuracy
guarantee. Only opaque IDs are allowlisted; applications that encode meaningful
state in these ID fields should use exact/off. Local baseline separation and literal
guards reduce risk but do not prove equivalence.
Security, fraud and uncertain verdicts are deliberately not reusable by Drain.

## Lifecycle and bounds

1. A new partition creates one template and classifies a bounded current
   observation together with its template.
2. A template generalization invalidates the old verdict and classifies again.
3. An unchanged match reuses only the most recent eligible, unexpired verdict.
   Each successful reuse counts exactly one avoided classification.
4. Failed/unknown answers, missing/nonfinite/out-of-range confidences, uncertain,
   security/fraud, sensitive or truncated evidence cannot seed reuse. An unsuccessful
   representative leaves no cached verdict; the next unchanged event is classified.
5. Inconsistent template state removes that partition and falls back.
   Capacity exhaustion refuses new partitions and classifies normally.

Each partition has a fixed token count and one cluster. Its exact literal/variable
shape ensures only allowed opaque-ID positions can differ. The first observation
supplies literal tokens; subsequent differing ID tokens become wildcards and
invalidate the cached verdict. Wildcards never revert. Cluster IDs are monotonic,
process-local and stable through generalization; reset partitions get fresh IDs.
Tickets snapshot both ID and template version, so late responses cannot populate a
changed or recreated template. Literal tokens and token counts are checked before
updates. No member labels or raw-event queues are retained by the miner.

`--drain-capacity` defaults to 256 and validates **1–1024 total partitions/templates**.
Each eligible input is at most **2,048 bytes and 128 tokens**, the full partition key
at most **8,192 bytes**, and each partition holds bounded tokens/template text and
one verdict and representative ID (ID at most 128 bytes). Memory is bounded by capacity
times these limits plus map/vector overhead, not a byte-perfect RSS cap. Capacity
misses classify normally without insertion. No automatic LRU rotation occurs;
invariant failure is the only eviction/reset path. Invalid adapter configuration
falls back; the CLI rejects invalid limits before input collection.

Drain honors `--batch-size`. Fallbacks and independent representatives share normal
requests. Every new/unclassified/changed-template observation in a batch is classified
independently; no speculative verdict is copied within that batch. Completions are
applied in observation order after the response, accepting only current tickets.
An eligible result can serve later batches. This intentionally spends extra
classifications during warm-up instead of coalescing pending observations.

Eligible remote text is at most twice 2,048 bytes plus fixed labels; it contains the
current template and current redacted observation. This representation never
replaces Event text, source, IDs, timestamps or exact report grouping. Ineligible
fallbacks use the ordinary bounded event representation. Budget/retry/error handling
stays in the existing single provider lane. `--max-batches` counts provider requests
before retries, not individual classifications: a request can classify up to
`--batch-size` representatives. Reuse avoids classifications but need not avoid an
entire request. Fewer representatives do not prove a live billing reduction.

TTL is non-sliding, 300 seconds for files/k8s and the configured `--verdict-ttl` for
controller mode. Model, provider and classification contract are fixed for a lane's
lifetime. `--rescore` bypasses every Drain verdict read. Exact persisted verdict
reads and writes are disabled in Drain mode: template-context prompts must not
contaminate the exact cache. Policy/novelty/incidents/outbox still commit normally.
Switching back to exact uses that mode's original contract and persistence.

**Templates and template verdicts are process-local.** Restart retrains and may
classify additional representatives; it cannot restore a stale template verdict.
There is no new persisted evidence dependency. Existing durable controller
incidents/outbox survive restart; the existing volatile pre-transaction ingestion
window and replay limitations remain unchanged.

## Metrics and reproducible checks

JSON `metrics` and controller Prometheus `jevernetes_` metrics expose:
`drain_templates_created`, `drain_templates_changed`, `drain_templates_matched`,
`drain_verdict_reuses`, `drain_classifications_avoided`, `drain_fallbacks`,
`drain_capacity_fallbacks`, `drain_evictions`, `drain_expirations`, and
`drain_active_templates` (gauge). Fallbacks count input/configuration/invariant
bypasses; capacity is a subset. A template match without an eligible verdict is
not a reuse. Provider batches/attempts/tokens/cost and report totals remain separate.
Counters reset on restart and have no per-source labels.

From the repository root, without credentials or cluster access:

```sh
cargo run --locked --example drain_reduction
cargo test --locked --all-features drain
cargo test --locked --all-features exact_reuses_identical_events_but_off_classifies_each_offline
cargo test --locked --all-features --test cli grouping_strategy_validation_and_offline_compatibility
cargo fmt --all --check
cargo clippy --locked --all-targets --all-features -- -D warnings
cargo test --locked --all-features
cargo build --locked --release
git diff --check
```

Measured deterministic demo: **1,000 raw events → 2 synthetic representatives**,
**998 successful reuses (99.8%)**, one created template, one generalization, zero
fallbacks, 1,000 retained events and
**zero network calls**. Only the opaque request ID varies; IP, telemetry and source metadata remain fixed. The demo completes each synthetic response before
observing the next event. A prefilled runtime batch of 8 instead classifies its
first 8 observations and reuses the remaining 992 in this fixture; request counts
depend on arrival/batching. This uses fixed fixture judgments, not live Jev responses
or an accuracy/cost benchmark. Focused runtime/controller tests exercise the same
adapter in the actual analysis lane with an injected in-memory test classifier,
including notification evidence and exact incident recurrence. See
[VALIDATION.md](../VALIDATION.md) for full results and host rerun commands.
