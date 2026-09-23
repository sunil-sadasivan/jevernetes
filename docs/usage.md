# Rust CLI usage

`jevernetes` is the default runtime. Build with `cargo build --release --locked`, or install with `cargo install --path . --locked`. CI verifies Rust 1.94; the declared minimum compiler is 1.89. Run `jevernetes --help` and subcommand `--help` for the full option list.

```sh
jevernetes files synthetic.txt --offline --json
jevernetes files synthetic.txt.gz - --offline --output .runs/report.json
jevernetes k8s --namespace example --selector app=api --offline --since 30m
jevernetes k8s -f --namespace example --tail 0 --offline --duration 60
jevernetes k8s -f --in-cluster --offline --max-streams 64 --retain-events 2000
```

`kubernetes` aliases `k8s`; `--live` aliases `--follow` / `-f`. Scope defaults to all namespaces. An explicit `--context` selects a kubeconfig context; `--in-cluster` selects the mounted service account and conflicts with `--context`. Without either, kube-rs infers configuration. The process never modifies cluster resources. Its identity needs `get/list/watch` on pods and `get` on pods/log in the intended scope. A namespaced Role is sufficient with `--namespace`. No Secret, mutation, exec, or attach permissions are needed. Trusted kubeconfig authentication plugins can still execute local programs.

Snapshot collection includes regular, init, ephemeral and available previous container instances. `--no-previous` excludes previous instances. Live discovery follows running instances and detects pod UID/restart changes. `--since` accepts nonnegative integer seconds or `s/m/h/d` suffixes; default 1h. `--tail` defaults to 500; use 0 for new logs. Snapshot reads are sequential and bounded by `--max-bytes` (32 MiB default per stream) and 30 seconds including reads. Live streams have no total lifetime byte limit, but every line, event, queue and retained window is bounded.

Files support plain text, JSONL, gzip (including concatenated members), and `-` for stdin. `--max-file-bytes` caps decompressed input at 32 MiB by default, including blank and oversized lines. Missing/unreadable files create coverage gaps while other inputs continue. `--max-events` is a global snapshot/file event cap (default 100,000). Oversized lines (64 KiB), oversized events (16,000 UTF-8 bytes), and incomplete byte-limit fragments are marked truncated. Continuations group stack traces; a live pending event flushes after 500 ms of quiet. IDs are deterministic for the same source, timestamp, line position and redacted text within a collection; they are not legacy IDs or persistent cross-run Kubernetes occurrence IDs.

## Analysis and bounds

Without `--offline`, the key is read from `TYPESAFE_API_KEY`, then `TYPESAFEAI_API_KEY`, then `TYPESAFE_API_KEY_FILE`. The endpoint is fixed to `https://api.typesafe.ai/v1/systemone`. Each representative has importance, severity and category choice questions, each requiring numeric confidence in [0,1]. Category includes `fraud`, separately from `security`. Low-confidence routine answers (<0.7) and truncated online events become uncertain. Invalid/incomplete answers, failed requests and exhausted budgets become unknown. Offline rules mark failure/warning/HTTP-5xx keywords important and everything else uncertain; they do not produce AI confidence.

Exact redacted message/source groups reuse successful judgments for five minutes in a bounded session cache. Source includes pod UID, restart count and previous/current identity. Numbers, IDs, stack traces and all source fields remain significant. Truncated and failed judgments are never cached; `--no-grouping` disables reuse. No persistent cache or review overrides are applied by Rust.

One async analysis lane batches up to `--batch-size` events (default 8, maximum 64), with a 350 ms fill deadline. `--max-batches` defaults to 500. A batch can make up to three HTTP attempts; 429/500/502/503/504 use bounded backoff, jitter and Retry-After. Requests have a 30-second total timeout and 10-second connection timeout. Redirects are rejected, responses capped at 1 MiB, and errors exclude response bodies and transport details.

`--max-cost` defaults to an estimated $0.25. `--input-price` and `--output-price` are dollars per million tokens; defaults 0.042 and 0 retain the legacy assumptions, **not newly verified prices**. Set rates for your agreement. Usage from invalid verdicts and HTTP error responses still counts; missing usage is visible and never invented. Thresholds are estimates, not billing caps. Live sessions stop scheduling when the batch/cost threshold is reached; files/snapshots retain unknown events beyond the budget.

`--queue-size` defaults to 1,024. Files and snapshots wait for capacity. Live streams keep draining; full queues drop analysis events and increment cumulative `dropped` and queue high-water counters. Dropped payloads are not retained. `unfollowed_observations` counts omitted-target observations (not distinct currently omitted containers). Status is printed every ten seconds; detailed metrics appear in the final report. `--max-streams` defaults to 64. Omitted streams produce coverage gaps and are reconsidered on pod updates or a five-minute paginated resync. Narrow namespace/selector scope before increasing limits for large clusters. `--retain-events` defaults to 2,000 live events; evictions are counted. Coverage history retains 500 records plus cumulative gap/omission counts. Reconnect cursors retain up to 4,096 distinct fingerprints at the latest timestamp; overflow favors possible duplicates and increments `dedup_overflows`.

SIGINT/SIGTERM cancels discovery, requests and reconnect waits, joins producers, drains queued events (unrequested online work becomes unknown), then writes the final report. `--duration` stops Kubernetes collection after the specified seconds. Reports are written at shutdown, not continuously checkpointed. `--json` reserves stdout for the final JSON report; live progress remains on stderr. Human output is intentionally simple and has no pending-row UI.

## Reports and exit codes

`--output PATH` atomically replaces a JSON report using a private temporary file (0600 on Unix); newly created directories are 0700. Existing directories are not chmodded. Choose a private location such as ignored `.runs/`. Default output is a terminal summary; `--json` emits schema 2. It retains familiar `summary`, `events`, `coverage`, `important_groups`, and `usage` fields and adds cumulative runtime metrics. Live totals can exceed retained events and groups; coverage records are observations, not a complete stream inventory.

- `0`: no detected file/snapshot gaps or unknown events within the requested bounds. Important events do not change the exit code.
- `1`: fatal configuration or runtime failure.
- `2`: partial coverage, bounds reached, evictions, or unknown events. Clap also uses 2 for invalid CLI arguments.
- `130`: SIGINT/SIGTERM interruption after final reporting.

All Kubernetes reports conservatively remain partial because tail/since limits and server retention cannot establish complete history. Reconnection uses inclusive timestamps and occurrence counts, but rotation, identical bursts beyond the cursor bound, missing timestamps and process restarts prevent exactly-once guarantees. Inspect coverage and metrics rather than interpreting no important events as no incidents.

The legacy `--workers`, `--collect-workers`, `--discovery-interval`, `--top`, `--tui`, `--rules-file`, dashboard/search/context/review commands are not silently emulated. Use the [legacy reference](legacy-usage.md) for those workflows and the [migration table](migration.md) for planned replacements.
