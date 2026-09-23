# Security policy

## Report vulnerabilities privately

Use [GitHub private vulnerability reporting](https://github.com/sunil-sadasivan/jevernetes/security/advisories/new). Include affected versions, reproduction steps using synthetic data, and the expected security boundary. Do not include real credentials, personal log data, kubeconfigs, or private cluster addresses in public issues.

Security fixes target the current release and main branch. This is an early-stage local tool, not a security monitoring system or a replacement for human incident review.

## Rust runtime scope

The default runtime is the Rust CLI. It has no HTTP dashboard listener and uses kube-rs API get/list/watch/log requests rather than kubectl. Required permissions are read-only pods get/list/watch and pods/log get. Kubeconfig authentication plugins remain trusted executable configuration. The Python loopback dashboard and all controls below remain applicable to the explicitly legacy companion.

Rust reports use schema 2 and private atomic writes. Queue drops, reconnects, bounded replay uncertainty and truncated collection are visible; no durable or exactly-once guarantee is made. See docs/architecture.md for Rust resource/trust boundaries and docs/migration.md for deferred workflows. The initial security review predates Rust and is not a Rust audit.

## Trust model

- The dashboard binds to `127.0.0.1` only. It validates Host and mutation Origin headers and requires a random per-process token for mutations. CSP and text-only rendering reduce browser injection risk. It is not a multi-user authenticated service: other processes/users with access to the same host can reach its loopback API. Do not expose it through a public proxy or tunnel.
- Kubernetes access uses the caller's trusted kubeconfig, PATH and credential plugins. The collector lists/gets pods and reads logs; it does not create, change or delete cluster resources. Use a read-only Kubernetes identity with access only to intended namespaces. Kubeconfig exec plugins are executable programs; use only configurations and binaries you trust.
- Pod/container arguments are passed as subprocess argument lists, never through a shell. Context fetches derive their target from an existing event, verify pod UID/restart identity when recorded, and reject changed instances.
- Log text is untrusted input. The application renders it as text, passes explicit untrusted-data instructions to Jev, and has no model-driven tool execution or remediation. Classifications can be incorrect, including under prompt injection. Expected-event rules are explicit literal matches scoped to selected sources; they can suppress real problems if written too broadly.

## Data handling

Jev mode sends redacted log text and source metadata to `https://api.typesafe.ai`. HTTPS certificate verification is enabled, redirects are rejected, and provider keys remain on the backend. There is no telemetry or automatic scan on startup. Offline mode and context fetching do not call Jev.

Redaction covers common credential fields, authorization values, JWT-shaped strings, URL passwords and PEM private-key blocks. It is best effort, not a data-loss-prevention guarantee. Personal data, unusual credential formats, and secrets lacking recognizable context may remain. Use offline mode when logs must not leave the machine, and inspect copied investigation prompts before sharing them with an agent. Copying a prompt itself does not make network requests.

Reports, review rules and cluster metadata are private runtime data. Default reports/rules live in ignored `.runs/`; report writes are atomic with mode 0600 and newly created report directories use mode 0700 on supported platforms. Choose a private reports directory. The application trusts locally stored reports and rule files; do not import untrusted report files or share their directory with untrusted writers. Existing directory permissions are not changed automatically.

## Resource and spending limits

Uploads are limited to 20 files and 10 MiB total; parsing defaults to at most 32 MiB of decompressed data per file or stdin. Individual log lines/events, queues, retained events, subprocess reads, context-fetch concurrency and model responses are bounded. Some limits are configurable for trusted inputs. The development HTTP server is not hardened against hostile local denial-of-service attacks.

AI limits use batches and an estimated cost threshold. Neither constitutes a strict billing cap: in-flight requests, retries and missing usage can increase actual charges. Context availability depends on Kubernetes log retention; missing or rotated logs are reported rather than inferred.

## Release checks

Source releases exclude local reports, review rules, environment files, kubeconfigs, keys, bytecode and build output. CI runs synthetic regression tests, JavaScript checks and package-content checks with read-only repository permissions and pinned GitHub Actions. No real cluster credentials are needed for tests. See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) for the initial review scope and limitations.

## Rust controller state and notification boundary

Controller mode adds a private SQLite database and a network notification destination. The
store contains redacted evidence in outbox payloads and sensitive operational judgments;
best-effort redaction is not anonymization. Restrict the directory/PVC and backups, use
volume encryption, and keep state out of git. New database/lock files use mode 0600 on Unix;
existing directory/file permissions remain the operator's responsibility. Cache identities
include full evidence/source, provider/model, prompt/taxonomy and the versioned decision
contract. Failed, unknown or truncated judgments cannot be reused as safe.

Only deterministic typed policy produces advisory notify/review intents. No remediation,
cluster write, executable model action, label/annotation copying or arbitrary callback is
implemented. Payload metadata is allowlisted; context labels, input paths and endpoints
are excluded. Configure webhook secrets solely via environment or mounted files. URL and
auth values are never logged; Authorization is marked sensitive; HTTPS is required,
redirects/proxies disabled, timeouts and request/response sizes bounded. Operators must
approve destinations and egress; this is not a general SSRF-filtering proxy. `--offline`
disables Jev but can still deliver notifications. Stdout is a development sink and must be
drained and access-controlled.

State failure is fatal/visible, notification failure is independently retried/dead-lettered.
The unauthenticated metrics/health endpoint exposes aggregate counters only; restrict
network access. One writer/replica owns a local persistent volume. See
[controller limits and maintenance](docs/controller.md) for retention, crash windows,
clock assumptions, safe backup/requeue practices and incomplete-coverage semantics.
