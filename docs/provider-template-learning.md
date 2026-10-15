# Provider template learning

Risk classification (`--risk-provider`) and template review (`--template-provider`) use independent fixed-host clients. Templates support OpenAI and Anthropic only. Explicit template model and input/output prices are required. Offline mode reads no template credentials and makes no requests.

```sh
jevernetes files synthetic.jsonl --grouping-strategy semantic \
  --template-provider openai --template-model MODEL \
  --template-input-price 0 --template-output-price 0
```

The example shows syntax only; zero rates are not a billing assertion. Template admission is bounded by `--template-max-requests` (10), `--template-max-cost` ($0.05), candidate capacity (64), TTL (300 seconds), and at most two reviews per batch. Evidence is limited to eight safe 2,048-byte samples. Review thresholds are the configured first sample threshold (default 4), then 32, 256 and 2048 occurrences.

Semantic mode classifies events independently while proposing typed shadow candidates. Name/version, required/normalized/protected paths, cacheability, confidence and explanation are validated. Local replay checks exact JSON shape and every required path in every sample. Normalization proposals are restricted to opaque allowlisted IDs. A successful shadow replay never authorizes reuse; a separately reviewed artifact is required.

Classic Drain reviews return a fixed schema of suggested free-text fields, importance, category, severity, confidence and explanation. Reviews can raise a non-important judgment only with confidence at least 0.75, a concrete risk category/severity and incident evidence beyond volume. They cannot lower an existing important judgment. Escalations are scoped to the same template/version and bounded review-state TTL. Suggested field masking is recorded for review, not automatically installed.

Reports expose bounded decisions using opaque template/version IDs. Samples and readable scope do not enter learning metadata. Provider usage is recorded separately in `provider_usage.template`, and total API/cost summary fields include both clients. Unknown fields, duplicate JSON keys, refusals, incomplete messages, multiple text outputs, missing confidence and invalid enums fail closed.

Reviewed artifacts use artifact version 2/schema version 1. Exact scope, JSON shape, required/protected literals, expiry and review metadata are mandatory; normalization paths must be unprotected strings. Logger-clock prefix grammar is currently rejected. `examples/reviewed-rules.synthetic.json` is an illustrative synthetic artifact, not a deployment policy.
