> This reference applies only to the retained legacy Python companion. See [Rust usage](usage.md) for the default CLI.

# Usage reference

[Back to Quick start](../README.md#quick-start)

Run the commands below from the repository checkout.

## Local dashboard

```sh
python3 -m jevernetes dashboard
# Or: python3 -m jevernetes dashboard --port 8792 --reports-dir .runs
```

Open **http://127.0.0.1:8792**. The dashboard loads existing JSON reports from `.runs` and selects the newest one. It includes:

- History, importance counts, searchable events and source/importance filters.
- Event details, model confidence, severity, category, original line ranges and the keyword baseline.
- A coverage view showing unavailable logs, truncation and collection gaps.
- New Kubernetes scans using the current context or a specified context, namespace, label selector and time window.
- File uploads for text, JSONL and gzip, with Jev or offline analysis.
- Background scan progress, explicit errors and downloadable JSON reports.

One analysis runs at a time. Keep the terminal running; stopping the server interrupts active analysis. Uploads are limited to 20 files and 10 MiB total, stored temporarily during analysis and removed afterward. Reports use the uploaded filenames rather than temporary paths, are saved with mode 0600, and persist in the reports directory. The dashboard lists up to 100 recent reports, at most 64 MiB per report; use the CLI for larger inputs/reports. No sample data is generated automatically.

The server binds only to `127.0.0.1`. Host validation and a per-session request token protect analysis requests, and log content is rendered as text. Provider credentials remain on the server. The Jev key must be in the environment when starting the dashboard; the browser never accepts or receives it. There is no remote hosting, login system, or automatic cluster scan on startup.

## Live Kubernetes analysis and cost

For a `tail -f` experience, click **Start live tail** in the sidebar. This defaults to new lines only (`--tail 0`), opens the **Live tail** tab, and shows all arriving events in order before analysis completes. Pending labels update in place when Jev finishes. Use autoscroll, text filtering, **Important only**, or **Pause display** to inspect the stream. Pausing the display does not stop collection, classification, or costs; **Stop live analysis** stops the session. The tail shows the latest 300 matching buffered events. Queue-overflow events appear as `dropped`, not as analyzed judgments.

```sh
python3 -m jevernetes k8s -f --tail 0
```

`-f`, `--follow`, and `--live` are equivalent. The terminal prints incoming redacted log text to stdout once, and counters/later classification updates to stderr. With `--tail 0`, streams start with lines emitted after each container is attached; set `--tail 100` to include recent history. `--json` continues to emit report snapshots, now including `tail_events` with arrival sequences and pending/final labels. Arrival order is the collector's observed order, not a globally synchronized cluster clock. Display-buffer gaps are reported in terminal output.

In the dashboard choose **New analysis → Kubernetes → Follow new logs continuously**. Results and the cost panel refresh every second. **Stop live analysis** stops collection, finishes/cancels in-flight work and saves a session report. You can browse older reports while a session runs and return with **Live session**.

```sh
python3 -m jevernetes k8s --live \
  --since 30s --tail 100 --max-batches 5000 --max-cost 0.25 \
  --output .runs/live-session.json

# Bounded local-only collection check
python3 -m jevernetes k8s --live --offline --duration 60 --since 30s
```

Live mode uses `kubectl logs --follow` for running normal, init and ephemeral containers. It rediscovers pods every 15 seconds and identifies container instances by pod UID, container name and restart count. Reconnects resume from the last timestamp and skip already-seen occurrences at that timestamp. This is best-effort continuity: rotations, terminated containers between discovery cycles and removed pods can still lose logs. Use the snapshot command without `--live` to inspect retained previous-container logs.

Events are grouped with a 500 ms idle flush for stack traces and sent in batches of up to 8 with a 350 ms linger. The display refresh adds up to a second, plus provider and queue latency. Stack continuations arriving after the idle flush can form separate events. This is live collection with periodic dashboard refresh, not a hard latency guarantee.

The compact status strip shows session status, estimated cost, open streams and queue depth. Expand **Session details** for input/output tokens, request attempts (including retries), unmetered attempts, pricing, events/second and reconnects. Dropped events, unfollowed containers, inventory failures and incomplete cost estimates also appear in the collapsed strip. Session counters cover all processed events; the browser displays the latest 1,000, and the saved report retains the latest 2,000 by default. Older event text is evicted, but its counts and cost remain. `--retain-events` changes retention. Reports are saved on stop; process crashes do not persist an unfinished session.

Cost uses the API's actual `usage.input_tokens` and `usage.output_tokens`, multiplied by the configured price. The default direct-TypeSafe rate, verified September 20, 2026, is **$0.042 per million input tokens and $0 for output**, from the [official model reference](https://docs.typesafe.ai/models). The [API reference](https://docs.typesafe.ai/api) documents the usage fields. `--input-price` and `--output-price` override USD per million tokens in live CLI mode. The result is a model-cost estimate, not an invoice; it excludes taxes and infrastructure. Old reports without recorded usage show no cost panel.

Requests lacking valid usage (including failed attempts) are explicitly counted as unmetered; their cost is unknown, not assumed free. A usage-bearing response with invalid classifications still contributes its reported tokens. `--max-cost` defaults to $0.25 and stops after the measured estimate reaches that threshold. **It is not a strict spending cap:** concurrently in-flight requests, delayed usage and unmetered failures can exceed it. The independent `--max-batches` budget bounds batches (up to 3 HTTP attempts each); its default remains 500. Offline mode issues no Jev requests.

Resource bounds: up to 128 stream subprocesses, a 2,000-event queue and 4 classification workers by default. `--max-streams` (at most 256), `--queue-size`, and `--workers` adjust these. When the queue fills, newly arriving events are dropped and counted visibly; when the stream cap is reached, uncollected running containers are counted. No claim of complete cluster coverage is made. A scan cannot run concurrently with a live session. Stopping can take up to the outstanding inventory/API timeout to clean up.

### If live tail has no streams

Live pod inventory has a 12-second timeout. Both the terminal and dashboard show the current attempt and the error category (DNS, TCP timeout, TLS, authentication or RBAC), with a retry every 15 seconds. An inventory failure is a coverage gap, not a collected source; the source count remains zero. The terminal suppresses identical status messages except for a 10-second heartbeat.

Check a **new** request in the same terminal where you launch the analyzer:

```sh
kubectl config current-context
kubectl --request-timeout=8s get --raw=/version
kubectl --request-timeout=8s get pods -A
```

If an existing log tail works but fresh requests time out, compare the context, kubeconfig, VPN/network path and provider control-plane firewall. Existing connections can behave differently from new connections after firewall changes. The analyzer does not modify firewall rules or kubeconfig. A forbidden response instead indicates missing pod-list or pod-log permissions; successful logs in one namespace do not necessarily imply permission to list all namespaces.

## Start with all Kubernetes logs

Run from this directory. Set `TYPESAFE_API_KEY` for Jev analysis; `TYPESAFEAI_API_KEY` and `TYPESAFE_API_KEY_FILE` (a file containing only the key) are also supported. Credentials stay in the process and are not written into reports.

```sh
python3 -m jevernetes kubernetes \
  --since 1h --tail 500 \
  --output .runs/kubernetes.json
```

Without `--context`, the current kubectl context is resolved once and pinned for the scan. All namespaces are included by default. Collection uses your existing kubectl authentication and read permissions. No cluster resources are modified or installed.

The scan enumerates every visible pod, including normal, init and ephemeral containers, and requests the previous instance when a container has restarted. Unavailable logs, pending containers, RBAC errors and timeouts appear in `coverage`. It reads up to 500 lines from the last hour, at most 2 MiB per stream by default. This is a snapshot of logs retained by Kubernetes, not every historical log: rotated/deleted pods and older container instances require an external log archive. The byte/tail caps and any detected omissions are reported.

```sh
# Larger cluster/window: raise the collection and AI budgets explicitly.
python3 -m jevernetes k8s --since 6h --tail 2000 \
  --max-events 100000 --max-batches 12500 --output .runs/cluster.json

# Narrow investigation
python3 -m jevernetes k8s -n my-namespace -l app=my-app --since 30m

# Local rules only: no calls to Jev
python3 -m jevernetes k8s --offline --output .runs/offline.json
```

## Analyze files

```sh
python3 -m jevernetes files examples/mixed.log
python3 -m jevernetes files /path/app.log /path/worker.log.gz \
  --output .runs/files.json
cat /path/app.log | python3 -m jevernetes files - --json
```

Plain text, JSONL, gzip and stdin work without configuration. Parsing is limited to 32 MiB of decompressed input per file or stdin; `--max-file-bytes` changes the CLI limit. The limit also covers blank and oversized lines, and partial coverage is reported. Common Java/JavaScript/Python stack trace continuations are grouped into one event; the original line range is preserved. Grouping is heuristic, not a parser for every log format. Each event includes redacted text, source, timestamp when recognized, line range, local baseline and Jev results. Repeated important messages are grouped by exact text and source; unrelated incidents are not merged solely because they share a category.

Optional install in a virtual environment provides the `jevernetes` command:

```sh
python3 -m venv .venv
.venv/bin/pip install .
.venv/bin/jevernetes --help
```

## Investigate events in context

Open a group in **Events**, choose an instance, then choose **View in context**. You can also open an event directly from **Live tail** or switch the Events display to **Instances**. The selected event stays pinned and highlighted while you inspect surrounding lines, including routine and unclassified events. Filter by namespace, pod, container or message text. The same source filters are available in the main events list and live tail.

The initial context is a frozen view of collected events. Kubernetes windows range from 30 seconds to 15 minutes on either side; files and events without timestamps use surrounding line numbers. **Fetch from Kubernetes** reads additional retained logs for the selected event's container using its recorded context. Other pods in the context view use already-collected events. Fetching does not send logs to Jev or add AI charges. It is bounded to 1 MiB and 2,000 parsed events, displaying up to the nearest 500 events. Pod replacement, unavailable prior instances, rotation, truncation and missing logs are reported. Uploaded files are not reread after analysis; their context comes from the retained report.

## Group repeated messages

By default, identical redacted messages from the exact same source share a Jev judgment. Parsed timestamps and line positions are kept on each occurrence; numbers, request IDs, stack traces, embedded timestamps and source metadata (including pod identity and restart count when available) must still match. Changed or truncated messages are analyzed separately.

Snapshots send one representative per group before applying the batch budget. Live streams share in-flight requests and cache successful judgments for five minutes, with at most `--retain-events` cached groups. Failed judgments are not cached for later batches. Reuse is limited to the current analysis/session, and local acknowledgments or expected rules never become cached Jev judgments. Uncheck **Reuse Jev judgments for repeated messages** in New analysis, or pass `--no-grouping`, to send every event independently.

The **Groups** display shows counts within the current filters. Open a group for a paginated list of all its collected instances, including occurrences with different review decisions or renewed judgments. Each retains its timestamp, text, source, context and individual review actions. Group checkboxes select every matching instance; the header checkbox selects all instances in the visible groups. Switch to **Instances** to select individual occurrences. Partial selections show a mixed checkbox, and new live arrivals are not added to an existing selection automatically. The event totals count occurrences; **judgments reused** counts occurrences that avoided sending another event to Jev, not saved HTTP requests or estimated token savings.

Live instance lists cover the retained window (2,000 events by default); evicted events are not archived. The dialog freezes that window while you inspect it. Session totals and reuse counters cover the whole session, so they can exceed the visible instance counts.

## Surface events by confidence

Events default to **Highest confidence** first. Use **Confidence** to show High (90–100%), Medium (70–<90%), Low (below 70%), or No AI confidence, and **Surface first** to switch to lowest confidence, most repeated, or collection order. Your confidence filter and ordering are remembered in this browser. All confidence levels remain visible by default; unscored events sort after scored events in either confidence order.

Confidence badges and row accents appear in Groups, Instances, and the live tail. Groups show a range across their matching instances; mixed levels are labeled explicitly. Highest-first uses the group's highest score, and lowest-first uses its lowest score. Confidence filtering happens per instance before grouping, so group counts and selection include only matching occurrences. Opening the group still shows all retained instances.

These levels use Jev's importance-judgment confidence, not severity or category confidence. Local reviews, offline rules, pending events and failed analyses have **No AI confidence**. Live tail stays in arrival order. Confidence is a model output, not an independently calibrated probability.

## Hand selected events to a coding agent

In **Important**, **Needs review**, or any Events filter, use the row checkboxes to select log events. **Select page** selects the visible page; selections remain available across pages and filters within the report. **Copy investigation prompt** creates a prompt you can paste into Codex or another coding agent. It includes only the selected events, recorded source metadata, timestamps, judgments, baseline signals and full retained event text. **Preview** lets you inspect it first, and manual copying is available if the browser blocks clipboard access.

The prompt asks the agent to investigate, correlate evidence, propose a minimal fix and test appropriate code changes. It treats logs as untrusted data and asks for explicit authorization before deployments or infrastructure changes. Copying is local and makes no AI requests. Prompts combine identical selected messages, preserving each selected occurrence’s ID, timestamp, line range and confidence. Include up to 50 distinct messages within a 256 KiB prompt limit; oversized selections produce an error rather than silently truncating evidence. Selections are snapshots of events at selection time and reset when switching reports. Review the prompt for personal or sensitive data before sharing it.

## Review expected events and inspect rules

From an event's detail dialog:

- **Acknowledge** removes that event from the important queue. It applies only to that event in that report or live session, and can be undone.
- **Mark as expected** opens an editable literal message pattern. Choose a stable substring without timestamps or request-specific values. The default Kubernetes scope is the same cluster, namespace and container across pod replacements; exact-pod scope is also available. File rules use the exact source path/name.

To review several events together, select their checkboxes and choose **Acknowledge selected** or **Mark as expected…**. Selections carry across filters, pages and display modes (up to 100,000 instances). Acknowledgment applies to every selected instance in one atomic update. Expected-rule previews combine identical messages into one rule per group, with up to 50 rules per operation. The expected-rule preview lets you edit each pattern before saving the batch; each rule retains its event's source scope. If any event is no longer available or any pattern is invalid, nothing in the batch is saved.

Patterns shorter than eight characters, including standalone `{` or `}`, match only the entire message, ignoring surrounding whitespace. Longer patterns match literal substrings. For multiline objects, suggestions prefer a message or event field; a brace alone cannot suppress a larger object. **Rules & reviews** shows each rule's matching behavior.

Future matching events remain visible as **Expected** and skip AI analysis. Existing retained matches leave the important queue, with their original judgment preserved for inspection. **Expected** and **Acknowledged** filters let you find reviewed events. These are local overrides, not model training. Disabling an expected rule restores existing judgments; events that skipped analysis become unknown and are not automatically sent to Jev retrospectively.

Open **Rules & reviews** in the sidebar to inspect or disable expected-event rules and inspect the exact built-in keyword baseline expressions. The baseline is read-only and independent of Jev semantic judgments. Expected rules take precedence over either analysis mode. Rules are shared with the terminal through `.runs/.review-rules.json`; `--rules-file PATH` selects another file for CLI analysis. A dashboard using `--reports-dir` stores rules in that directory. Rules are private runtime data, saved with mode 0600, excluded from version control and source distributions. Live counters apply review changes to retained events; already-evicted history cannot be reviewed retroactively.

## Judgments and budgets

Jev receives three typed Choice questions for each event: importance (`important`, `routine`, `uncertain`), severity, and category. Each answer has a model confidence. Low-confidence routine answers and truncated events become uncertain. API failures, missing/invalid answers and events beyond the AI budget become `unknown`; they never become routine by default. These confidence values are model outputs, not independently calibrated probabilities.

By default, up to 8 group representatives are batched into each request, with 4 concurrent requests and a 500-batch budget (up to 4,000 unique groups per snapshot). With grouping disabled, that budget covers individual events. In snapshot mode, all collected events remain in the report even when the budget runs out. `--max-batches`, `--batch-size`, `--workers`, and `--max-events` control cost and load. HTTP 429 and transient server errors use a shared cooldown, with at most 3 attempts per batch. Requests have a 30-second timeout. Re-running a snapshot reclassifies its groups; there is no persistent cross-session cache. Live mode has the retention and stop behavior described above.

`--offline` is explicitly a keyword baseline, not semantic analysis: matching events are important, unmatched events are uncertain. It does not claim ordinary-looking lines are safe.

## Output and exit codes

- Default: terminal summary and top 15 groups of important events (`--top N` to change).
- `--output PATH`: complete JSON report, atomically replaced with file mode 0600.
- `--json`: the same report to stdout; progress goes to stderr.
- `0`: scan completed within its selected limits; this does **not** mean nothing important was found.
- `1`: configuration, inventory or fatal execution failure.
- `2`: partial coverage or at least one unknown/unclassified event. The report is still saved.
- `130`: interrupted.

Inspect `summary`, `important_groups`, `coverage`, and `events`. `complete_within_window` means there were no detected collection gaps or unclassified events; it does not assert perfect detection or availability of all historical logs. An `uncertain` result needs human judgment.

## Data handling

Jev mode sends **redacted log text and source metadata to TypeSafe** over HTTPS. Redaction covers common credential fields, bearer/basic authorization, JWTs and URL passwords, including nested JSON fields. It is best effort: arbitrary personal data or secrets embedded in unfamiliar formats may remain. Use `--offline` when logs must stay local. Saved reports contain log evidence, context/namespace/pod names and file paths; keep them outside version control. `.runs/` and `reports/` are ignored and excluded from source distributions. Only synthetic log fixtures are included with this project. Terminal output strips control bytes.

The analyzer has no remediation tools, arbitrary command execution based on logs, or cluster write operations. Log text is treated as untrusted data in the prompts. Model classification is advisory and can miss issues or flag benign activity.

## Interactive terminal tabs

Add `--tui` for a full-screen terminal view with clickable **Important**, **Routine**, **Needs Review**, and **All** tabs:

```sh
python3 -m jevernetes k8s -f --tail 0 --offline --tui
python3 -m jevernetes k8s -f --tail 100 --max-cost 0.25 --tui
python3 -m jevernetes files examples/mixed.log --offline --tui
```

Click a tab, press **1–4**, or use **Tab / Shift+Tab** and **Left / Right**. Use **Up / Down**, **j / k**, the mouse wheel, **Page Up / Page Down**, or **Home / End** to move through instances. Click a row to select it, then **Enter** to inspect its full retained text; **Esc** returns to the list. **q** or **Ctrl+C** stops collection, finishes saving `--output` when configured, restores the terminal, and exits. A naturally stopped live session stays open for inspection until you quit.

Tab counts cover the retained window. **Needs Review** includes uncertain, unknown, and dropped events; pending events remain visible in **All**. Local expected/acknowledged events appear under **Routine** with their review label. Offline rules leave unmatched logs uncertain, so the Routine tab may be empty in offline mode. Each occurrence is shown separately, including events whose Jev judgment was reused.

This mode uses Python's standard-library curses support and requires an interactive terminal. Mouse support depends on the terminal; keyboard shortcuts remain available. `--tui` cannot be combined with `--json` or piped input/output. Omit it to keep the usual streaming output. Collection, redaction, grouping, budgets and saved-report formats are unchanged.

## Ask Jev and find log evidence

In the dashboard, open **Ask Jev / Find logs** on a live session or saved report. Results stay in the main log viewer, with one compact line per matching group: original date/time, relevance, source, message, and instance count. Hover or focus a row for **Inspect** and **View all instances**; Enter on a focused row opens its full evidence. Multiline messages stay on one display line, with their full text preserved in inspection. Try `find me requests against ip address 192.0.2.1` or `major issue with db`. Search uses a frozen window of collected logs in the selected namespace/pod/container filters, including Routine, reviewed, and pending events. Importance, confidence, and the ordinary text filter do not hide evidence from this search. Each submission captures the latest server-side retained window, so logs expiring while you type do not block the search. Result evidence stays frozen for inspection.

**Jev semantic search** is the default in both interfaces, including IP-address questions. It sends your redacted question and redacted group representatives with source and occurrence metadata to TypeSafe, even if the original collection used offline rules. The dashboard has one question box with no mode selector. The terminal also offers explicit local, case-insensitive text search with **f**; this needs no API key and inspects the entire retained window.

Semantic search returns typed **Match**, **Possible match**, or **Unrelated** decisions, with relevance confidence separate from the original importance confidence. Low-confidence and truncated evidence remains visible as possible matches. Failed checks remain visible as unevaluated evidence. Results link to the original evidence and all retained instances; they do not generate a diagnosis or fetch additional Kubernetes history.

Identical messages in the same source share one check. Repeating an unchanged question/window reuses successful decisions from an in-memory cache (five minutes, up to 2,048 groups). Changed questions or occurrence metadata trigger new checks. Keyword hints prioritize candidates when the window exceeds the chosen budget; they do not establish a match. The default budget is 16 batches of 8 new groups, with up to 4 requests in flight. Search stops scheduling at an estimated $0.01 or on missing usage. In-flight requests and retries can exceed the estimate. Search usage is shown separately from collection usage.

Progress shows examined, unexamined, and failed groups. A partial search or no matching evidence does not establish that the issue is absent. **Stop search** preserves results already returned while in-flight requests finish. One search can run per dashboard; search results and the cache are not persisted across restarts.

The same search is available in **`--tui`**. Click the **Search** tab (or press **5**), press **/** or **?** for Jev, or **f** for local text search. Type a question and press **Enter**. While editing, **Tab** switches Jev/Exact text, **Ctrl+B** cycles the batch budget, **Ctrl+U** clears the question, and **Esc** cancels editing. Arrow keys, Home/End, Delete and Backspace edit the question; Unicode input is supported.

Terminal search includes every collected importance level and freezes evidence when you submit. The Search tab shows group matches, relevance confidence, coverage and separate query cost. **Enter** inspects evidence, **i** lists every retained instance of the selected group, **x** stops the search, and **Esc** returns to the previous view. You can switch back to the other tabs while a search runs. **q** quits outside the editor; **Ctrl+C** quits anywhere, stopping collection and search. Search evidence remains available even if the live window later evicts those events. Cache and cost limits are the same as in the dashboard, with a separate in-memory cache per terminal session.
