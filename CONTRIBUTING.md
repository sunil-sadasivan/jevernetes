# Contributing

Use Go 1.25+ and Node 20+ for development. Product execution needs only the compiled binary. Run `tools/check.sh` and `tools/release.sh` before proposing changes. Unit tests use synthetic evidence, fake Kubernetes clients, in-memory HTTP transports and temporary SQLite files. Never use cluster credentials, live providers, private reports or real notifications in tests.

Keep producer/analysis ownership explicit. Carry `context.Context` across I/O, bound every queue and evidence collection, and join goroutines before returning. Preserve original events when assigning group judgments. A cached judgment must never escape its scope, template/version, generation, safety gate or TTL. Provider schemas reject unknown properties, duplicate keys, missing fields and invalid confidence.

Add focused tests for changed behavior. Do not add an active learning rule merely because a provider suggested it. Reviewed artifacts require independent validation; unknown evidence remains unknown. Persistent controller state contains hashes, counters and typed decisions, never message bodies or readable source metadata.

`tools/check-release.mjs` inspects the intended source tree and optionally compares an archive byte-for-byte. Its diagnostics report filenames and counts, never suspected secret values. `.cache`, local reports, build output and state databases are ignored. Preserve the MIT license and existing media assets.
