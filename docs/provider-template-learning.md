# Provider adapters and semantic shadow learning

LLMs learn candidates in **shadow only**. Separately operator-reviewed compiled
artifacts can activate deterministic judgment reuse with `--template-rules PATH`.
The flag defaults off and requires semantic grouping. No proposal, report, confidence
score, embedding or fine-tuning result can authorize activation. Exact remains the
default; Drain and all provider adapters keep their existing behavior.

Every raw occurrence remains in the normal bounded retention, recurrence, policy and
notification pipeline. Reuse replaces only the risk judgment and records its original
representative ID. This is not a provider accuracy or security-equivalence claim.

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
`--template-provider openai|anthropic`. This provider role is always shadow; there is no
promotion flag. Reviewed artifacts use the separate `--template-rules` option. Template TypeSafe is deliberately unsupported because proposals do
not fit its preserved choice endpoint. `--template-provider off` is the default.
Online semantic mode requires a template provider **or** reviewed rules. Active rules
do not require a template provider/key. `--offline` only runs local baseline judgments;
these remain unscored/uncertain and cannot seed active reuse. Socket-free tests and the
reviewed reduction example inject deterministic synthetic judgments without credentials.

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
unknown or uncertain judgments quarantine that source/shape partition. Ineligible
evidence also quarantines its proven family before returning. A nonserialized hash of
the complete original source/shape is retained before redaction and continuation; this
allows private/redacted/malformed multiline evidence to invalidate its own family.
An entirely unparseable new record with no proven shape cannot be assigned to a family
and never matches a rule; unrelated families are not merged by source alone. All stored
samples must replay with identical non-normalized fields. Later literal changes
invalidate the candidate even if risk classification calls them routine. New fields
create a separate partition and require fresh evidence. Samples collected while a
proposal is pending participate in replay; a 17th independent sample permanently rejects
that candidate/ticket as replay-incomplete, even if the sample would match; monotonic generation tickets reject stale
completions after expiry/recreation. No same-batch or later event borrows a verdict.

The registry holds at most `--template-capacity` partitions (default 64, range 1–256),
16 samples of at most 2 KiB each per partition, and one proposal per partition.
`--template-ttl` defaults to 300 seconds (range 1–3600), non-sliding from creation.
Capacity misses classify normally; expired partitions are pruned on observations,
completions and report snapshots. The gauge may remain stale while the lane is idle.
Rejection reason labels are a fixed enum, never model or event text.

## Reports and controller state

Reports add `risk_contract` and `template_learning` with schema/version, role-specific
provider/model provenance, proposal-schema and prompt fingerprint, bounded candidate
metadata, hashed evidence IDs, replay results, and template usage. `provider_usage` separates risk/template usage;
summary `total_api_requests` and `total_estimated_cost_usd` include both roles. The
legacy `usage` and summary `api_requests` fields retain risk-only meaning. No duplicate sample
payloads are persisted by the registry. Existing report event retention/counters remain
unchanged. `semantic_*` and `template_*` metrics are available in JSON and numeric
controller Prometheus fields; rejection-reason counts are in JSON. `semantic_shadow_matches`
counts **already classified** observations matching a candidate, not avoided work.
Promotions are always zero. Active-template and avoided-classification metrics count
only separately reviewed rules; with no artifact both remain zero.

Candidates are process-local and saved only in the final report (`--output` for a
controller). Reports are never imported as rules. No SQLite schema migration or durable active
verdict persistence is introduced. Restart loses candidates and repeats learning; existing
incidents, outbox, delivery retries, health and shutdown semantics remain intact.
Semantic mode bypasses durable exact verdict reads/writes; every occurrence still
enters policy/novelty/incident/outbox processing. Active verdicts publish only after
the batch classifications and controller transactions finish. Template
failure does not suppress security/fraud notifications. Queue drops and retention
limits still apply; reuse must never mean deleting raw occurrences.

## Reviewed artifact activation

An operator must deliberately compile a new artifact offline from the documented
schema, inspect source scope and every complete path/type, approve each scalar to
normalize, fix required/protected literals, assign review identifiers, version and
expiry, and place the result in a trusted private directory. A replay pass is evidence
for that review, never approval. There is no proposal import, report-to-rule converter,
auto-promotion, hot reload or model-controlled file path. Review identifiers are bounded
non-secret identifiers, not signatures or proof that a human review actually occurred;
the operator-selected file and its access controls are the trust boundary.

The complete synthetic example is
[`examples/reviewed-rules.synthetic.json`](../examples/reviewed-rules.synthetic.json).
It contains invented evidence only and is not a production rule. Its explicit expiry
is intentional. Use `cargo run --locked --example reviewed_reduction` for deterministic
replay at a fixed synthetic time: 300 retained events, one injected classification,
299 reuses and zero network calls. A prefilled batch of eight requires eight independent
classifications before any verdict publishes.

```
jevernetes files - --grouping-strategy semantic --template-rules .runs/reviewed.json
```

The same option works for snapshot/follow and controller modes, with independently
selected TypeSafe, OpenAI or Anthropic risk credentials as above. `--template-provider`
is optional; adding it collects advisory candidates alongside the reviewed matcher.

The strict version-1 artifact has mandatory `artifact_version`, `schema_version` and
`rules`. Every rule requires `id`, positive `version`, Unix-seconds `expires_at`, exact
`source_scope`, complete `shape`, `required_literals`, `normalize_paths`,
`protected_literals` and `review`. Review requires `reviewer`, `review_id`, `compiler`
and Unix-seconds `reviewed_at`. Unknown/missing fields and duplicate JSON keys at any
depth are rejected. A claimed digest field is rejected. The runtime computes SHA-256
from the actual file bytes; even whitespace/review changes invalidate identity on reload.
Provider/model, actual risk prompt/schema/contract, artifact digest/versions, rule
ID/version, complete source, complete path/type shape and every non-normalized value
participate in the fingerprint.

| Resource | Hard bound / semantics |
| --- | --- |
| File | 64 KiB, capped read plus one-byte probe; regular files only |
| Rules | 1–64; duplicate IDs (even different versions) and potentially overlapping rules rejected |
| IDs/review labels/source keys | 1–64 ASCII identifier bytes |
| Source scope | 1–16 exact scalar fields; full equality, no wildcard/subset/workload expansion |
| Shape | 1–128 nodes, depth ≤8, root object; includes every object, array, index and empty container |
| Paths | ≤128 bytes, restricted JSON pointers with nonempty ASCII alphanumeric/underscore/hyphen segments; escape sequences unsupported |
| Literals/normalization | 1–32 required literals, 1–32 normalize paths, 0–32 protected literals; no overlap or duplicate paths |
| Strings | ≤256 bytes per scalar; no control characters or recognized redaction/secret values |
| Expiry | Reviewed time positive and not in the future, expiry strictly future, validity ≤366 days |
| Matched event | ≤2 KiB, single complete non-sensitive JSON object; no truncation/redaction/private/multiline/parse ambiguity |
| Session cache | `--template-capacity` 1–256 (default 64) fingerprints; full cache falls back to classification |
| Verdict TTL | `--template-ttl` 1–3600 seconds, default 300; controller also caps by `--verdict-ttl` |

Credential loaders already allow Kubernetes projected symlinks to regular files; the
rule loader follows the same policy and rejects directories/devices/non-regular targets.
Keep files and parent directories private and stable against untrusted replacement;
existing permissions are not changed. All rule decoding/validation and bounded loading
finish **before** input, credentials, state creation or Kubernetes access. A malformed
artifact aborts startup. A mismatched/expired event or rule falls back to classification.
Rules load once; restart to load changes. The digest binds the loaded snapshot, and
restarts always start with an empty verdict cache.

Normalization replaces only explicitly approved scalar values with deterministic
markers. Complete type shape remains part of identity; arrays, object keys, ordering
of arrays, empty containers and all non-normalized values remain exact. Outcome,
status, code, error, auth/authorization, security, fraud, category, importance, severity,
level, result, success, denied/deny, permission, role and failure-like names (including
case/separator variants and ancestors; access/allow/permit/privilege/denial and
recognized credential names are also blocked) can never normalize. They remain literal even
if omitted from `protected_literals`. Required and protected literals additionally
restrict which values can match a rule. Unknown/new/missing keys, changed types,
source or structure always miss. No similarity, embeddings or fine-tuning participates.

Only a completed, independently classified, baseline-clean, cacheable routine judgment
(category routine; severity info/noise; all three finite confidences ≥0.85) can seed a
fingerprint. Failed, important, uncertain, security/fraud and non-cacheable judgments
cannot seed it. Any unsafe sibling in the same batch invalidates pending publication
for that fingerprint regardless of completion order. A later independent classification
may retry. Generation tickets reject stale completions after TTL/recreation; cache hits
do not extend TTL. `--rescore` bypasses all reviewed reads. Rule expiry is checked on
every lookup and publication. Capacity, TTL and restart losses increase classification
work; they never imply routine judgments.

Reports separate advisory `template_learning` from `reviewed_template_rules`, whose
mode is `reviewed-session-only`, with artifact digest/version, reviewed metadata, rule
constraints, risk contract and bounds. `reviewed_*` metrics expose misses, fallbacks,
publications, capacity misses, stale tickets, expirations and entries. Semantic avoided
classifications count actual hits; automatic promotions remain zero. Numeric metrics
are exposed by the existing controller health service. Expiry gauges refresh on work
and final report (they can be stale while idle).

SQLite schema, durable incidents, novelty, recurrence, outbox, delivery, health,
shutdown, queue/drop bounds and read-only Kubernetes collection are unchanged.
Reviewed verdicts are session-only across the continuous controller's entire analysis
lane; restart requires reclassification. Every event still enters the existing policy
transaction, including variants and reused routine observations. Retention bounds and
existing crash/partial-coverage limitations still apply.

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
