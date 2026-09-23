# Contributing

The default runtime is Rust. Use Rust 1.94 for the verified toolchain:

```sh
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --all-features
cargo build --release --locked
```

Keep `Cargo.lock` tracked and build output in ignored `target/`. See docs/architecture.md and docs/migration.md for boundaries and deferred parity. The legacy companion still needs Python 3.11+ and `kubectl`; Node.js is used for browser-logic tests. Preserve its regression checks while migrating workflows:

```sh
python3 -m unittest discover -s tests -v
node --check jevernetes/web/app.js
node --check jevernetes/web/search.js
node tests/test_context_ui.cjs
node tests/test_prompt_ui.cjs
node tests/test_review_ui.cjs
node tests/test_grouping_ui.cjs
node tests/test_search_ui.cjs
```

All tests use synthetic inputs or mocks. Do not add real application logs, personal data, access keys, cluster names or kubeconfig files to fixtures, issues, screenshots, or pull requests. Keep runtime reports and review rules out of version control. Security reports belong in the private reporting channel described in SECURITY.md.

To regenerate the shareable search demo, run `node tools/record-search-demo.mjs` with Node 22+, Chrome and ffmpeg installed. It records the actual dashboard against synthetic API responses, blocks external browser requests, and exports a GIF, MP4 and preview image under `docs/images/`. The recording labels its logs, results and usage as illustrative.

Browser recorders use Chrome's private debugging pipes and pass dynamic values as structured function arguments. Run `node tests/test_demo_browser.mjs` with Chrome installed to check that caption/selector text cannot become executable code.

Run `python3 tools/record-terminal-search.py` to export the terminal GIF, MP4 and preview. It drives the production terminal renderer and input handlers with synthetic data, blocks provider requests, and asserts grouping, instance inspection and cache reuse. It requires ffmpeg with drawtext and a monospace font; set `DEMO_FONT` outside macOS.

Build and inspect release artifacts in a virtual environment:

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt
.venv/bin/python -m build
python3 tools/check_release.py --dist dist
```

Keep collection read-only, model outputs advisory, context fetches bounded, and review overrides reversible. For behavioral changes, add regression tests that exercise observable outcomes. Contributions are made under the project's Apache 2.0 license.

## Security automation

Dependabot checks Cargo, GitHub Actions and Python development/build requirements weekly. The Security workflow runs on pushes, pull requests, a weekly schedule, and manual dispatch:

- TruffleHog scans Git history/diffs with full checkout history and fails on unsuppressed findings or scan errors. Credential verification is disabled, so potential secrets are not sent to external verification endpoints. A single inline ignore applies to the explicitly synthetic PostgreSQL password-redaction fixture; no detector or test file is excluded.
- Bandit fails on medium/high Python findings with at least medium confidence. Reviewed low findings are recorded in SECURITY_REVIEW.md.
- pip-audit checks the development/build tooling and its resolved dependencies; the application has no Python runtime dependencies.
- CodeQL runs security-extended analysis for Python and JavaScript, publishing findings in GitHub code scanning.

Actions are pinned by commit. TruffleHog's scanner image version is also pinned; update its `version` input alongside its action pin. Only CodeQL's upload job receives `security-events: write`. Pull requests use `pull_request`, never `pull_request_target`; checkout credentials are not persisted. Do not broadly suppress detectors to make scans pass.

## Controller changes

Read [controller semantics](docs/controller.md) before changing keys, policy or outbox state.
Bump the decision contract for semantic changes not captured by prompt hashing; add an explicit
migration before changing SQLite schema 1. Never reuse failed/unknown/truncated judgments as
routine, refresh TTL on a cache hit, hold state locks across I/O, or drop pending/dead outbox rows
to satisfy capacity. Tests inject timestamps and jitter for deterministic replay/retry checks.
Run the synthetic controller smoke and `python3 tools/check_controller_artifacts.py`; if
installed, `kubectl kustomize deploy/base` performs offline rendering without cluster access.
Container image selection/build and cluster validation are separate operator tasks.
