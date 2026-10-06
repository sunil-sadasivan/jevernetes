# CLI usage

```sh
jevernetes files examples/mixed.log --offline --json
jevernetes files application.txt.gz - --offline --output report.json
jevernetes k8s --namespace demo --selector app=api --offline --since 30m
jevernetes k8s -f --namespace demo --offline --tail 0 --duration 60
jevernetes dashboard --report report.json
```

`kubernetes` aliases `k8s`; `--live` aliases `--follow`/`-f`. Build `cmd/jev` for the compatible executable name. `--help` is the authoritative flag list. Unknown options and unsupported compatibility options fail before inputs or credentials are opened.

Files support plain text, JSONL, stack-trace continuations, gzip including concatenated members, and `-` once for stdin. Missing files produce coverage gaps while subsequent inputs continue. `--max-file-bytes` limits decompressed input (default 32 MiB). Lines are bounded at 65,536 bytes and events at 16,000 UTF-8 bytes. Incomplete byte-limit fragments and oversized lines/events are marked truncated. Unterminated Kubernetes physical lines also mark the containing event truncated and parse-uncertain; normal file EOF does not. Secret-like fields, authorization strings, URL credentials and private-key blocks are redacted before event storage. Redaction is best effort; reports remain sensitive.

`--max-events` is global (default 100,000 for files/standalone Kubernetes; controller defaults to 0 for unbounded lifetime ingestion); `--retain-events` bounds retained report events (default 2,000 for every mode). Event totals remain cumulative. Reaching a bound or evicting retained events makes coverage partial. Live collection uses `--max-streams` (64), `--queue-size` (1,024), five-second pod polling and context-aware reconnects. Snapshot discovery is capped at 4,096 pods, with 200-pod pages and a 15-second request bound. Regular, init, ephemeral and previous container instances are included where status records permit; `--no-previous` excludes previous logs. Snapshot log requests have a 30-second bound. `--since` accepts integer seconds or s/m/h/d, default 1h; `--tail` defaults to 500. `--context` conflicts with `--in-cluster`.

Offline mode uses keyword/HTTP-5xx rules, never AI confidence or provider credentials. Online mode defaults to TypeSafe; `--risk-provider openai|anthropic` requires explicit `--model`, `--input-price` and `--output-price`. Prices are operator inputs, not verified billing rates. Keys use the selected `*_API_KEY` or `*_API_KEY_FILE`; TypeSafe also accepts `TYPESAFEAI_API_KEY`. Key files must be regular and at most 16 KiB. Never put keys in command arguments.

`--batch-size` is 1–64, default 8; fill deadline 350ms. `--max-batches` defaults to 500. `--drain-max-provider-attempts` caps all attempts, including retries. `--max-cost` defaults to $0.25 in cumulative conservative reservations; input tokens are bounded by serialized request bytes and output by the requested maximum. Missing usage or a reservation underestimate opens the breaker. Reservations are not refunded. Billing guarantees depend on provider adherence to token limits and configured prices; TypeSafe does not provide the same output-token cap. Error bodies and transport diagnostics are never printed.

Use exact grouping by default; `--no-grouping`/`--grouping-strategy off` disables reuse. [Drain grouping](drain-template-mining.md), [adaptive audits](adaptive-log-learning.md), and [template learning](provider-template-learning.md) are opt-in. Unsupported fixed-sampling and uncertain-cache compatibility flags fail explicitly.

SIGINT/SIGTERM cancels collection and provider requests, drains queued events conservatively, joins workers and attempts a final report. Blocking stdin is closed on cancellation. `--json` reserves stdout for JSON; progress and errors use stderr. Reports use schema 2 with `runtime: "go"`, `summary`, `events`, `coverage`, `important_groups`, `usage`, `provider_usage`, `metrics` and learning sections. `--output` uses a 0600 temporary file, sync and atomic rename; newly created directories use 0700.

Exit codes: 0 complete within the requested file window; 1 configuration/runtime failure; 2 partial coverage or CLI option parsing failure; 130 interruption. Important events alone do not change the code. Every Kubernetes report is partial. No exactly-once or complete-history claim is made.
