# Contributing

Use Go 1.27.2+, Python 3.11+ and Node 20+ for development. Python and Node run development fixtures only; the application is one Go executable. Run `tools/check.sh` and `tools/release.sh` before proposing changes. The executable workflow inventory is `python3 tools/check_parity.py --run`. Tests use synthetic evidence, fake Kubernetes clients, in-memory HTTP transports and temporary SQLite files. Never use cluster credentials, live providers, private reports or real notifications in tests.

Keep producer/analysis ownership explicit. Carry `context.Context` across I/O, bound every queue and evidence collection, and join goroutines before returning. Preserve original events when assigning group judgments. A cached judgment must never escape its scope, template/version, generation, safety gate or TTL. Provider schemas reject unknown properties, duplicate keys, missing fields and invalid confidence.

Add focused tests for changed behavior. Do not add an active learning rule merely because a provider suggested it. Reviewed artifacts require independent validation; unknown evidence remains unknown. Persistent controller state contains hashes, counters and typed decisions, never message bodies or readable source metadata.

`tools/check-release.mjs` inspects source/package privacy and compares source archives byte-for-byte. Diagnostics report filenames and counts, never suspected secret values. Runtime/build output and state databases stay ignored. The release gate rejects Python/Rust implementation and packaging files; historical source remains in Git. Preserve the MIT license and existing media assets.

All six browser acceptance suites target the Go embedded assets. The native executable is exercised over loopback HTTP and through a PTY. Preserve literal human-review scopes, reversible original judgments and frozen search references. Full browser rendering is an additional manual check, not implied by Node logic tests.
