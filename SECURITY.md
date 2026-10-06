# Security model

Jevernetes is advisory and read-only. Kubernetes access is limited to pod discovery/log GETs and explicitly requested inspection port-forwarding. It never executes model actions or performs workload mutation, exec, attach or Secret reads. Kubeconfig authentication plugins can execute local programs; configuration must be operator-approved.

Logs, source fields, model answers and remote responses are unverified data. Evidence is bounded, stripped of terminal control sequences, and best-effort redacted before storage. Private-key markers are scanned even in discarded oversized line bytes. Reports remain sensitive and should be stored privately. Event IDs and opaque metadata reduce exposure but do not promise anonymization.

Provider hosts are fixed to the three supported adapters. HTTP redirects are rejected; proxy environment variables are not inherited by provider/webhook/inspection clients. Requests/responses, evidence, retries, queues and retained state are bounded. Credentials are loaded only for the selected online provider, never included in reports or diagnostic text. Errors use fixed messages rather than provider/transport bodies.

Grouping is fail-closed: sensitive/truncated/security-shaped evidence and uncertain/failed/security/fraud judgments cannot seed reusable safety decisions. Scope, template version and generation must match exactly. Provider template proposals are shadow-only. Automatic free-text normalization requires a separate reviewed artifact. No model-supplied regex, URL, executable or action is accepted.

Controller state contains hashes/counters and typed notification/verdict metadata, not raw log bodies or readable source labels. SQLite uses private files, one writer, bounded tables and durable transactions. Migration retains delivery identity while removing evidence from active database pages; external backups and filesystem snapshots are outside that guarantee. At-least-once notifications require consumer deduplication.

Inspection binds only to numeric loopback. Remote selection rejects ambiguity and terminating/paginated results. URLs are constructed internally, IDs are strict, response schemas and sizes are checked, and forwards close after each sample. Health/metrics are a separate listener intended for operator-controlled networks.

The dashboard is read-only and loopback-only. Exact Host and Origin checks, fetch-site checks, a restrictive content security policy, fixed target configuration, request throttling and text-node rendering protect the local boundary. Unknown write/legacy API routes fail explicitly. It exposes no arbitrary browser-selected URLs or paths.

Tests use synthetic data and fake clients only. Report a suspected issue privately to the repository maintainer; do not include credentials, private logs or report bodies in public issues. No live-service security assessment is implied by local checks.
