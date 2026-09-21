# Contributing

Use Python 3.11 or newer. Runtime dependencies are limited to the Python standard library; Kubernetes collection additionally requires a configured `kubectl`. Node.js is used only for the small browser-logic tests.

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

Build and inspect release artifacts in a virtual environment:

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt
.venv/bin/python -m build
python3 tools/check_release.py --dist dist
```

Keep collection read-only, model outputs advisory, context fetches bounded, and review overrides reversible. For behavioral changes, add regression tests that exercise observable outcomes. Contributions are made under the project's Apache 2.0 license.

## Security automation

Dependabot checks GitHub Actions and Python development/build requirements weekly. The Security workflow runs on pushes, pull requests, a weekly schedule, and manual dispatch:

- TruffleHog scans Git history/diffs with full checkout history and fails on unsuppressed findings or scan errors. Credential verification is disabled, so potential secrets are not sent to external verification endpoints. A single inline ignore applies to the explicitly synthetic PostgreSQL password-redaction fixture; no detector or test file is excluded.
- Bandit fails on medium/high Python findings with at least medium confidence. Reviewed low findings are recorded in SECURITY_REVIEW.md.
- pip-audit checks the development/build tooling and its resolved dependencies; the application has no Python runtime dependencies.
- CodeQL runs security-extended analysis for Python and JavaScript, publishing findings in GitHub code scanning.

Actions are pinned by commit. TruffleHog's scanner image version is also pinned; update its `version` input alongside its action pin. Only CodeQL's upload job receives `security-events: write`. Pull requests use `pull_request`, never `pull_request_target`; checkout credentials are not persisted. Do not broadly suppress detectors to make scans pass.
