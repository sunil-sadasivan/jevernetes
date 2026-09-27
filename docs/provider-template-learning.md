# Provider adapters and semantic shadow learning

This release implements **shadow learning only**. It can collect and validate bounded
structured-template proposals in files, Kubernetes snapshots/follow, and the continuous
controller. It never activates a proposal or reuses its judgment. Every occurrence in
semantic mode receives ordinary risk classification and continues through retention,
recurrence, policy and notification. Exact remains the default; Drain is unchanged.
Embeddings and fine-tuning are neither required nor implemented, and are never authority
for reuse. This is not an accuracy or security-equivalence claim.

## Independent providers

`--risk-provider typesafe|openai|anthropic` defaults to TypeSafe (`jev` is an alias).
TypeSafe retains `/v1/systemone`, the choice contract and `jev-latest` default.
OpenAI uses `/v1/responses`, `text.format` with `strict: true` JSON schema and
`store: false`. Anthropic uses `/v1/messages`, `anthropic-version: 2023-06-01`,
`max_tokens`, and `output_config.format` JSON schema. These are distinct adapters,
not OpenAI-compatible Anthropic requests. Model support for structured output varies;
choose a supported, preferably pinned model revision. There is no silent fallback.
No real provider calls or online documentation verification were performed for this change.

OpenAI/Anthropic risk selection requires explicit `--model`, `--input-price`, and
`--output-price`. Both adapters use the same internal Judgment JSON schema and strict
local enum/confidence/count validation. Unknown fields, malformed output, refusal,
tool calls, incomplete Responses and Messages stopping for anything except `end_turn`
fail closed. TypeSafe keeps its existing decoder and typed Judgment semantics.
Provider, model, prompt, contract and schema participate in durable identity; changing
any of them invalidates reuse. Session caches are bound to a provider contract;
Drain state belongs to a single immutable provider lane and is rebuilt on restart.

Credentials are discovered only for explicitly enabled providers:

| Provider | Environment priority | File fallback |
| --- | --- | --- |
| TypeSafe/Jev | `TYPESAFE_API_KEY`, `TYPESAFEAI_API_KEY` | `TYPESAFE_API_KEY_FILE` |
| OpenAI | `OPENAI_API_KEY` | `OPENAI_API_KEY_FILE` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_API_KEY_FILE` |

Use file inputs for Kubernetes. Files must be regular (mounted Secret symlinks work),
UTF-8, nonempty after trimming, at most 16 KiB. Reads are capped at 16 KiB plus one
byte; environment values have the same bound. Control characters within keys are
rejected. Keep mounted files and their parent directories trusted and private.
Keys are never accepted as CLI flags or included in prompts, identities, reports,
Debug output or error messages. Environment values precede files. Missing or invalid
selected credentials fail before input collection or controller state creation.
Learning off never discovers or reads a template credential. `--offline` uses local
rules and rejects an enabled template provider.

All adapters share one serial transport: verified TLS, fixed endpoints, no redirects
or environment proxies, 10-second connect and 30-second total request timeouts,
2 MiB serialized request and 1 MiB response caps. At most three attempts are allowed;
429/500/502/503/504 retry with bounded jitter/backoff/Retry-After (maximum 30 seconds).
Transport errors and response bodies never become diagnostics. Cancellation accounts
for an in-flight attempt as unmetered. Output limits are 16,384 tokens for risk JSON
and 2,048 tokens for proposals. Proposals additionally have an 8 KiB decoded JSON cap.

Rates are operator-supplied USD per million tokens. TypeSafe retains legacy defaults
0.042 input / 0 output for compatibility, not verified current pricing. No price is
invented for new providers. OpenAI input usage already includes cached tokens.
Anthropic input, cache-creation and cache-read tokens are summed and charged at the
operator's single input rate: a conservative estimate when the configured rate covers
all those token classes, **not exact tiered billing**. No prompt caching is requested.
Missing/invalid usage is visible; counters never invent token counts. In-flight work,
retries and missing usage can exceed estimates; neither role's threshold is a billing
ceiling. Budget and usage counters reset on restart.

## Selecting shadow learning

Select `--grouping-strategy semantic` (alias `learned`) and
`--template-provider openai|anthropic`. This strategy is always shadow; there is no
promotion flag. Template TypeSafe is deliberately unsupported because proposals do
not fit its preserved choice endpoint. `--template-provider off` is the default.
Online semantic mode requires a template provider; ordinary `--offline` semantic mode
only runs local rules, without candidates or synthetic confidence.

Both roles may use the same provider/model or different ones. Set
`--template-model`, `--template-input-price`, and `--template-output-price` explicitly.
For example, after supplying mounted credential files and agreement-specific rates:

```sh
jevernetes files synthetic.jsonl --risk-provider typesafe \
  --grouping-strategy semantic --template-provider openai \
  --template-model "$TEMPLATE_MODEL" \
  --template-input-price "$TEMPLATE_INPUT_RATE" \
  --template-output-price "$TEMPLATE_OUTPUT_RATE" \
  --template-min-support 4 --template-max-requests 10 \
  --output .runs/shadow.json
```

The same flags work on `k8s` and `controller`. These examples are configuration only;
no cluster or provider access is needed for the tests/demo. The template role has its
own `--template-max-requests` (default 10, range 1–1000 before retries) and
`--template-max-cost` (default $0.05 estimated) budget, independent of risk budgets.
At most one request is in flight across the two roles. A proposal request can delay
the lane, so existing queue/drop metrics still matter. Risk classification and the
controller transaction for the current event precede proposing its template.
Template failure or budget exhaustion never overwrites a risk judgment.

## Deterministic registry and validation

Partitions are SHA-256 identities of the complete source plus observed scalar paths
and types. Model names and descriptions cannot choose runtime identity. Only
single-line, complete, non-sensitive JSON objects of at most 2,048 bytes, depth 4,
32 scalar paths and 128 bytes per path qualify. Arrays, empty objects, unusual field
names, invalid JSON, multiline/private/redacted/truncated evidence fall back to normal
classification. The parser conservatively marks changed JSON serialization sensitive;
therefore whitespace/reordered/duplicate-key JSON can be excluded even when benign.
This limitation is deliberate in the first shadow implementation.

`TemplateProposal` has a bounded identifier name, version 1, observed required,
normalization and protected paths, cacheability recommendation, finite confidence,
and an explanation **enum**. No free-form explanation, action, tool or destination is
accepted. Schema outputs are untrusted until strict local decoding and replay checks.
All proposed paths must exist in the observed structure. Every non-normalized path
remains literal even if the model omitted it from required/protected lists.
Outcome/status/error/auth/security/fraud/category/importance/severity/level/permission/
role/success/failure/result-like paths are protected by default. Protected, required
or explicitly protected paths cannot normalize. Only observed string fields named
`request_id`, `trace_id`, `span_id`, or `correlation_id`, containing canonical UUIDs
or 16/32/64 hex digits, can pass normalization validation. Numeric telemetry,
user-controlled text and identities are never normalized.

`--template-min-support` defaults to 4 (range 2–16). Support uses independently
classified distinct occurrences, not reused judgments or repeated observation IDs.
Only high-confidence (all three at least 0.85), baseline-clean routine judgments
with routine category and info/noise severity qualify. Security/fraud, failures,
unknown or uncertain judgments quarantine that source/shape partition. All stored
samples must replay with identical non-normalized fields. Later literal changes
invalidate the candidate even if risk classification calls them routine. New fields
create a separate partition and require fresh evidence. Samples collected while a
proposal is pending participate in replay; monotonic generation tickets reject stale
completions after expiry/recreation. No same-batch or later event borrows a verdict.

The registry holds at most `--template-capacity` partitions (default 64, range 1–256),
16 samples of at most 2 KiB each per partition, and one proposal per partition.
`--template-ttl` defaults to 300 seconds (range 1–3600), non-sliding from creation.
Capacity misses classify normally; expired partitions are pruned on observations,
completions and report snapshots. The gauge may remain stale while the lane is idle.
Rejection reason labels are a fixed enum, never model or event text.

## Reports, controller state and next activation step

Reports add `risk_contract` and `template_learning` with schema/version, role-specific
provider/model provenance, proposal-schema and prompt fingerprint, bounded candidate
metadata, hashed evidence IDs, replay results, and template usage. `provider_usage` separates risk/template usage;
summary `total_api_requests` and `total_estimated_cost_usd` include both roles. The
legacy `usage` and summary `api_requests` fields retain risk-only meaning. No duplicate sample
payloads are persisted by the registry. Existing report event retention/counters remain
unchanged. `semantic_*` and `template_*` metrics are available in JSON and numeric
controller Prometheus fields; rejection-reason counts are in JSON. `semantic_shadow_matches`
counts **already classified** observations matching a candidate, not avoided work.
Promotions, active templates and semantic classifications avoided are always zero.

Candidates are process-local and saved only in the final report (`--output` for a
controller). Reports are never imported as rules. No SQLite schema migration or active
rule persistence is introduced. Restart loses candidates and repeats learning; existing
incidents, outbox, delivery retries, health and shutdown semantics remain intact.
Semantic mode bypasses durable exact verdict reads/writes to keep evidence independent;
every occurrence still enters policy/novelty/incident/outbox processing. Template
failure does not suppress security/fraud notifications. Queue drops and retention
limits still apply; reuse must never mean deleting raw occurrences.

Automatic activation, one-classification reuse for varying structured events, durable
candidate restoration and active-template controller tests are deferred. A next step
must define a reviewed compiled-rule contract, validate restart provenance/TTL,
collision and protected-mutation checks across all evidence, publish only completed
verdicts with generation tickets, and test active reuse plus normal security/fraud
notifications. A replay pass or model confidence alone is insufficient authorization.
This release deliberately claims **zero semantic classification savings**.

Run the wholly synthetic, socket-free structural example:

```sh
CARGO_NET_OFFLINE=true cargo run --locked --example semantic_shadow
CARGO_NET_OFFLINE=true cargo test --locked --lib semantic
```

It retains 304 events, uses 304 injected classifications, observes 296 shadow matches
following four support samples, and classifies four security variants independently.
The candidate is quarantined by the security evidence. Actual avoided classifications
and network calls are both zero. The fixture uses invented operation text and opaque
IDs; no live application text, logs, credentials, embeddings or provider calls.
For demonstrated actual reuse under existing conservative rules, the separate
`drain_reduction` example retains 1,000 events with 2 injected classifications and
998 reuses. Neither example measures provider accuracy, latency or real cost.
