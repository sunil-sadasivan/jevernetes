# Remote inspection

```sh
jevernetes remote --context example --namespace monitoring --watch --json
jevernetes remote --context example --namespace monitoring --pod controller-example --json
jevernetes remote --namespace monitoring --incident <64-hex-incident-id>
jevernetes dashboard --controller-context example --controller-namespace monitoring
```

The controller must enable `--inspect-port 9091`. The workstation reads its configured Kubernetes context, selects exactly one running, non-terminating pod and opens client-go SPDY port-forwarding. Discovery uses `app=jevernetes` by default, requests at most two pods, and rejects pagination/multiple/empty results. Exact `--pod` uses GET and conflicts with `--selector`. Namespace is mandatory.

The inspecting identity needs pod get/list as applicable and create on `pods/portforward`; it does not need pod log, exec, attach, Secret or workload-write access. The controller service account receives no port-forward permission from this feature. Kubeconfig authentication plugins may execute local programs, so use operator-approved configuration.

Every sample has a 20-second bound and a fresh forward, closed after success, failure or cancellation. Go uses an ephemeral `127.0.0.1` listener internally. No user-supplied URL is accepted. Port is 1–65535; interval 2–3600 seconds. Watch failures mark prior output stale. JSON watch output is JSONL; `--output` atomically saves the latest successful envelope. SIGINT/SIGTERM exits 130.

Inspection endpoints are GET-only `/v1/status`, `/v1/incidents` and `/v1/incidents/ID`. ID is exactly 64 hexadecimal characters. Query strings, encoded path aliases and unknown paths fail. Status contains schema, sample time, readiness, partial-coverage marker, Go process counters, incident/outbox counts and up to 20 recent incidents. Detail returns the last enqueued notification and delivery status/attempts. Persisted notification evidence is metadata-only schema 2.

Requests have a 1 KiB application-level header/URI limit and two-second server read/write bounds; the standard HTTP parser may allocate its small fixed header allowance before that check. A single request is processed at a time; busy/corrupt/oversize reads return fixed 503 errors. Responses are capped at 256 KiB and notification JSON at 128 KiB. Readers never claim outbox work or change ingestion readiness.

The dashboard requires context and namespace together. Targets are fixed at startup; the browser selects only one validated incident ID. Sampling is in-process, serialized, cached for the configured interval and throttled to one uncached attempt per two seconds per resource class. Failed samples clear detail and label counters stale. Polling pauses when hidden/collapsed; late responses after cancellation are ignored. No browser-provided executable, URL, port or configuration path is accepted.
