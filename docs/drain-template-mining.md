# Conservative Drain-style grouping

Use `--grouping-strategy drain --drain-masking strict|classic`. Capacity defaults to 256 and is bounded to 1–8192 entries. Least-recently observed entries are evicted; a new monotonic generation prevents old tickets from publishing into replacements. TTL is non-sliding, 300 seconds by default, at most seven days.

Strict masking generalizes only named request/trace/span/correlation/session IDs whose values are opaque hexadecimal/UUID strings. Classic additionally generalizes numeric/IP/clock tokens and unprotected JSON numeric/string values. Literal words, protected fields and positional HTTP status codes stay distinct. JSON arrays stay literal. Names containing status/code/level/error/auth/role/security/fraud/permission/success/failure/result/outcome/severity/payment/amount/balance/backup/bytes/count are protected. File grouping retains the complete source. Kubernetes template scope includes context, namespace, container kind/name, previous state and restart count; pod identities remain on original events.

Sensitive, truncated, security-shaped, multiline, >2,048-byte or >128-token events take independent analysis paths. Offline rules never authorize reuse. Unknown/uncertain/failed, low-confidence and security/fraud judgments cannot seed reuse. Group dossiers contain at most eight distinct samples of at most 2,048 bytes. Sample/evidence limitations prevent reusable fanout. Provider output applies only to the exact scope/template/version/generation ticket; stale tickets become unknown.

Every singleton is considered when its microbatch closes, at most 350ms after reaching the analysis lane, excluding queue/provider latency. Original events remain intact. Assessment metadata is opaque: template/version IDs, counts, sample counts, boundedness flags and reason. Failed or unsafe shared responses leave siblings unresolved rather than inferring safety.

This implementation is deliberately more conservative than arbitrary similarity clustering. Paths, padded logger prefixes and all previous normalization grammars are not emulated. The [migration table](migration.md) describes those limits.
