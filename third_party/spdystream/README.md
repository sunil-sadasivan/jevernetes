# Bounded SPDY receive decoder

This is the Go source and tests from `github.com/moby/spdystream v0.5.0`,
copied from the pinned module without a network fetch. LICENSE and NOTICE are
retained. The upstream module content checksum is
`h1:7r0J1Si3QO/kjRitvSLVVFUjxMEb/YLj6S9FF62JBCU=`.
The root module replaces that dependency with this directory so the
client-go and application stream engines use the same hardened decoder. No
transport, authentication, negotiation, proxy, TLS, write encoding, or worker
lifecycle code is changed.

Local production changes are in `spdy/read.go` and `spdy/types.go`. Receive
failures poison the decoder; callers cannot resume at an attacker-chosen offset.
The existing stream owner closes the raw connection and joins its workers.

| Input | Check before allocation or payload decoding |
| --- | --- |
| All control frames | SPDY version 3, known type, permitted flags, isolated payload reader |
| SYN_STREAM | 10-byte prefix; at most 64 KiB compressed headers |
| SYN_REPLY, HEADERS | 4-byte prefix; at most 64 KiB compressed headers |
| RST_STREAM, GOAWAY, WINDOW_UPDATE | Exactly 8 payload bytes |
| PING | Exactly 4 payload bytes |
| SETTINGS | Exactly `4 + 8 * count` bytes; at most 1,024 entries |
| DATA | Nonzero stream ID, FIN-only flags, at most 1 MiB per frame |
| Decompressed headers | At most 256 names and 256 total NUL-separated values; 4 KiB per name, 16 KiB per value block, 64 KiB aggregate including count/length words |

The header parser checks each count and length before allocating, splitting,
or canonicalizing strings. The aggregate decompressed reader budget is renewed
per frame, while the zlib dictionary remains shared across frames as SPDY
requires. Go's inflater has a fixed-size history/output buffer; no unbounded
read-all or draining of compressed output is used. Header parsing can consume
at most 64 KiB per frame, in addition to the inflater's fixed buffering.
Raw payload isolation prevents short frames from borrowing bytes from the next
frame; exact fixed-frame lengths reject trailing bytes. These are per-frame
receive limits, not a lifetime byte cap on an otherwise healthy tunnel.

`spdy/bounds_test.go` covers allocation witnesses, every control envelope,
compressed counts/lengths/expansion, aggregate header limits, and shared zlib
context. One upstream malformed-zlib fixture now sets version 3 rather than
version 2 to reach the zlib validation under the stricter envelope check.
`internal/kube/frame_bounds_test.go` exercises rejection after a real HTTP 101,
buffered upgrade bytes, pending stream acknowledgement, and worker joins.
The existing client-go port-forward, TLS/proxy/authentication, and cancellation
suites remain active. `tools/check.sh` also runs this nested module's tests,
race tests, formatting, and vet checks.

These limits intentionally reject unusually large individual SPDY frames or
metadata, even when the 24-bit wire length can represent them. Normal client-go
port-forward traffic uses much smaller metadata and chunked DATA. Rust and
Python remain staged alongside Go; retirement is not authorized.
