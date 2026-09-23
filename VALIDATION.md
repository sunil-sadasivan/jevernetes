# Rust validation

Required checks (CI uses Rust 1.94):

```sh
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --all-features
cargo build --release --locked
python3 tests/rust/tui_pty.py target/release/jevernetes
printf 'ERROR synthetic failure\nINFO ready\n' | target/release/jevernetes files - --offline --json
```

Rust tests use synthetic inputs and loopback HTTP fixtures, never live Kubernetes or Jev credentials. They cover byte/event bounds, multiline/redaction, typed choices/confidence, safe HTTP failures/retries, usage, exact reuse/expiry, reconnect multiplicity and cursor bounds, queue drops, retention and CLI reports/permissions. See docs/architecture.md for benchmarks and authorized cluster validation still needed. Legacy validation below describes the retained companion only.

Native TUI coverage includes pending-to-final analysis updates, bounded immutable views, stable selection during eviction, tab and mouse navigation, Unicode editing/rendering, control-sequence suppression, frozen local/semantic search, group occurrences, conservative relevance, separate search budgets/metering, cache identity/expiry, and invalid terminal/stdin/JSON combinations. The PTY smoke drives the actual release binary through local search and multiline details, then verifies normal quit, Ctrl+C while editing, SIGTERM, terminal restoration, and private final reports. It uses Python only as the test driver; the Rust runtime has no Python dependency.

Verified locally for the native TUI: formatting, Clippy with warnings denied, 45 Rust tests, a locked release build, all three PTY scenarios on macOS, 117 legacy Python tests, and all five JavaScript helper suites plus syntax checks. Provider and Kubernetes tests use synthetic loopback fixtures; no live cluster or provider calls were made.

Verified for this migration: Rust formatting and Clippy with warnings denied, 26 Rust tests, release build, 117 legacy Python tests, all five JavaScript helper suites, and an offline release-binary smoke with six synthetic events. The smoke checked JSON equivalence, stable distinct occurrence IDs, multiline grouping, password/PEM suppression, zero provider requests, and mode 0600. Loopback HTTP tests exercise the real Kubernetes client against synthetic list/watch/get/log responses and the real Jev client against synthetic provider responses. No live cluster or provider calls were made. The optional legacy wheel/sdist build was not run locally because the `build` module was absent; the existing CI packaging job remains enabled.

# Legacy validation

Run the automated checks from the project directory:

```sh
python3 -m unittest discover -s tests -v
python3 -m compileall -q jevernetes
node --check jevernetes/web/app.js
node tests/test_context_ui.cjs
node tests/test_prompt_ui.cjs
node tests/test_review_ui.cjs
```

The Python suite contains 83 tests covering parsing, multiline grouping, redaction, bounded collection, model response validation, retry and token accounting, Kubernetes inventory errors, live-stream reconnection and cleanup, pending classifications, queue overflow, report persistence, and dashboard HTTP access controls. Kubernetes and provider behavior use synthetic fixtures or mocks; these tests do not establish connectivity to a real cluster.

Bulk review checks cover atomic validation and persistence, scoped acknowledgments, exact matching for short messages, legacy rules, and live-session changes. Browser checks with a synthetic local backend verified selection across filters, bulk acknowledgment and expected rules, single-event saves, and stable layout during delayed live polling. Pattern suggestion checks cover multiline structured messages and standalone braces.

Context and review regression checks cover time windows, redaction, pod replacement, previous-container selection, restart races, literal scope matching, acknowledgment isolation, rule persistence, restoration of original judgments, skipping AI for expected events, and authorization on the new endpoints. JavaScript helper checks cover source filtering, routine neighbors, file windows, instance separation and selected-event preservation. The theme defaults to dark and uses browser-local preference storage.

## Reproducible local checks

The included `examples/mixed.log` is synthetic. Analyze it without network requests:

```sh
python3 -m jevernetes files examples/mixed.log --offline
```

For a semantic analysis check, configure your own `TYPESAFE_API_KEY` and omit `--offline`. Results and token usage can vary between model versions and requests; this fixture is not an accuracy or cost benchmark.

## Kubernetes checks

Use a configured context with pod-list and pod-log read permissions. These commands use the current `kubectl` context:

```sh
kubectl config current-context
kubectl --request-timeout=8s get pods --all-namespaces
python3 -m jevernetes k8s --offline --since 5m --tail 100
python3 -m jevernetes k8s -f --offline --duration 30 --tail 0
```

Add `--namespace my-namespace` to the analyzer commands and `--namespace my-namespace` in place of `--all-namespaces` for `kubectl` when access is namespace-scoped. Live mode with `--tail 0` only receives new lines emitted after attachment, so a quiet workload may produce no events.

Inspect collection coverage alongside judgments. A timeout, denied permission, or unavailable container log should appear as a collection gap. Live inventory attempts have a 12-second deadline and retry every 15 seconds. Reports and real cluster logs are local runtime data and are not distributed with the project.

## Release security review

The initial release adds bounded decompression and credential-redaction regression cases. See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) for scanner versions, reviewed findings and scope.
