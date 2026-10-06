# Jevernetes

Jevernetes is a Go CLI and read-only Kubernetes log controller. It frames and redacts bounded log events, groups equivalent evidence, obtains typed operational-risk judgments, and retains advisory incidents in SQLite. A local dashboard inspects reports and continuously monitors one configured controller.

```sh
go build -o bin/jevernetes ./cmd/jevernetes
printf 'ERROR synthetic failure\nINFO ready\n' | bin/jevernetes files - --offline --json
bin/jevernetes dashboard --report report.json
```

Go 1.25+ is required to build. The product is one native executable; `cmd/jev` provides the same entry point for installations that need that name. No interpreter, paid dependency, or external service is required for offline analysis. Linux and macOS are supported.

- Files, stdin, concatenated gzip, Kubernetes snapshots and live collection.
- Exact or conservative Drain-style grouping; bounded group evidence and a 350ms microbatch deadline.
- TypeSafe, OpenAI and Anthropic adapters with strict response validation, fixed hosts and cumulative request/cost reservations.
- Adaptive audits, reviewed JSON rules, shadow template proposals and escalation-only template reviews.
- Durable policy, idempotent outbox, webhook/stdout notifications, health/readiness and Prometheus metrics.
- Loopback inspection and remote status/watch/incident through client-go port-forwarding.
- Embedded read-only dashboard with fixed controller targets, stale-state reporting and text-safe evidence rendering.

This release has explicit compatibility limits. It does **not** claim complete behavioral parity with every previous command. See [migration](docs/migration.md) for unsupported commands, stricter grouping, schema changes and learning restrictions. [VALIDATION.md](VALIDATION.md) records the exact checks and environment limitations.

Read [usage](docs/usage.md), [controller operations](docs/controller.md), [remote inspection](docs/remote-controller.md), [architecture](docs/architecture.md) and [security](SECURITY.md). The checked-in deployment is a template, not an instruction to deploy. No cluster or provider is contacted by tests.

Licensed under the [MIT License](LICENSE), copyright 2026 Sunil Sadasivan.
