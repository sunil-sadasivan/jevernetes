# Drain-template experiment: blocked before integration

Review date: 2026-09-27. Branch: `feat/drain-template-mining`. Base:
`b9eeb6c2de48052833c79681b46cb9a0bbd49e3e` (`feat/probabilistic-controller`).

**This branch does not implement Drain mining yet.** Existing exact grouping,
controller persistence, policy, and notification behavior are unchanged. There is
no new CLI option, template metric, or offline cost-reduction fixture. No measured
reduction is claimed.

## Blocking dependency resolution

The published `logdrain` 0.3.2 archive and extracted source are present in the local
Cargo registry. Its manifest declares `Apache-2.0 OR MIT`, edition 2021, and MSRV
1.85; these are compatible with this repository's Rust 1.89 minimum and the local
Rust 1.94 toolchain. The source was inspected directly, including `src/miner.rs`,
`src/options.rs`, `src/cluster.rs`, `src/tokenize.rs`, `src/similarity.rs`, and
`src/mask.rs`, rather than assuming an API from the crate description.

A temporary dependency declaration was evaluated:

```toml
logdrain = { version = "=0.3.2", default-features = false }
```

`cargo generate-lockfile --offline` failed with:

```text
error: no matching package named `bincode` found
location searched: crates.io index
required by package `logdrain v0.3.2`
```

Neither `bincode` nor `dashmap` has a cached source/archive in this environment.
Attempting ordinary online resolution also failed:

```text
failed to download from `https://index.crates.io/config.json`
[6] Couldn't resolve host name (Could not resolve host: index.crates.io)
```

The sandbox does not allow approval escalation. The temporary manifest addition
was removed; **Cargo.toml and Cargo.lock have no changes**. Vendoring an incomplete
crate, replacing its dependencies, or substituting a homegrown normalization
algorithm would not establish a validated implementation and was not attempted.

### Expected lockfile impact, not a resolved dependency graph

The crate's mandatory dependencies are `bincode` 1.3, `dashmap` 6, `regex` 1,
`rustc-hash` 2, `serde` 1 with `derive` and `rc`, `smallvec` 1, and `thiserror` 2.
The base lockfile already contains all of those except `bincode` and `dashmap`.
Their transitive additions and selected versions cannot be confirmed until Cargo
resolves the graph. Inspect the actual lockfile diff before proceeding; do not
refresh unrelated locked packages. Redis and Kafka are optional upstream features
and must remain disabled. No runtime network service is needed for the core miner.

## API findings and integration constraints

The crate is a plausible native implementation, but its default configuration is
not sufficient to meet this experiment's safety requirements. This review does
not establish that it is fundamentally unsuitable; dependency resolution blocks
building and testing the necessary adapter.

| Inspected API/behavior | Required integration treatment |
| --- | --- |
| `Miner::builder().build_options()` and `Miner::from_options()` | Validate application limits before construction; make `off`, `exact`, and `drain` explicit strategies, with existing behavior the default. |
| `Miner::add()` returns cluster ID and `UpdateType::{Created, TemplateChanged, None}` | Classify creation and every changed template. Reuse only an unchanged, validated template version with a fresh cacheable verdict. |
| `cluster(id)`, `clusters()`, `Cluster::template()` and `tokens()` | Check returned identity and template state; missing or inconsistent state must take the ordinary classification path. |
| `match_only()` returns only the best cluster; equal-score candidates are not reported as ambiguous | The adapter needs a tested conservative ambiguity check or a partition design that proves uniqueness. Do not treat a best match as proof of an unambiguous match. |
| `max_clusters_per_leaf` only bounds each leaf; token-count shards and prefix branches can grow | Impose a global limit on templates, scope partitions, and allocated tree growth. A verdict LRU alone does not bound the miner. At capacity, fall back without inserting; any reset must invalidate all related verdicts. |
| `add_with_member()` retains deduplicated member labels without an application bound | Do not use event IDs, pod identities, or evidence as member labels. Keep bounded representative evidence separately. |
| `first_line_only(true)` clusters only the first line and captures the initial suffix | A suffix is not a current representative of later stack frames. Preserve the full redacted Event and partition by exact multiline suffix, or conservatively bypass mining for multiline events. Test before enabling first-line reuse. |
| Generalization replaces any differing token with a wildcard; numeric parametrization affects tree descent | A word change can erase security meaning. Baseline state alone is not a complete semantic guard. Define and test restrictions on reusable wildcard positions and security-sensitive observations. Numeric, IP, and request-ID variation needs explicit coverage. |
| Snapshot/file persistence and optional external persistence backends | Do not attach upstream persistence to the controller without atomic contract/version checks. For this experiment, process-local templates and verdicts can safely retrain after restart, at additional classification cost. |

Use stable Kubernetes scope such as context/cluster, namespace, container, and
container kind. Pod UID, pod name, restart count, and previous-instance flags must
remain on each Event, but must not inadvertently prevent reuse across replicas.
Include the entire deterministic baseline state in the reusable identity. File
inputs need their own explicit source-scope rule rather than sharing a Kubernetes
scope accidentally.

The current parser represents streamed private keys as redacted text; Event does
not carry `Line.private` as a separate field. A future adapter must retain or
conservatively recognize this provenance, including multiline/truncated cases,
before lookup. Template verdict eligibility must reject private, truncated,
failed, unknown, invalid-confidence, and otherwise uncacheable evidence. The
session exact cache's `Judgment::reusable()` is weaker than controller
`cacheable()` and is not sufficient for template reuse.

## Intended analysis-lane design, not implemented

Keep framing, redaction, truncation, and baseline computation before mining.
Maintain a bounded process-local template cache with a non-sliding verdict TTL
and explicit template-version identity. Controller `--rescore`, contract changes,
and TTL reductions must bypass/invalidate template verdicts as appropriate.

Creation requests should contain bounded representative evidence. Changed-template
requests should contain the current template plus bounded evidence of the change.
Use a separate classification representation: never replace `Event.text`, source,
IDs, or report grouping with the mined template. Snapshot template versions while
batching so a response for an earlier version cannot populate a later version.
Pending same-batch representatives with failed or unknown responses must not
become reusable verdicts.

Every raw Event still reaches the current policy, incident recurrence, audit,
metrics, report retention, and durable outbox paths. Existing controller incident
identity uses original source/text; changing that identity to a template would be
a separate behavior change. Retain original evidence on each notification and
test both identical-event recurrence and variable-event notification behavior.

Expose template creation, change, match, verdict reuse, avoided classification,
fallback, capacity, and any eviction/reset counters beside provider calls/tokens
and estimated cost. Count an avoided classification only after successful reuse;
matched templates and batched representatives are not themselves saved API calls.

## Resuming safely

1. Make the native crate's complete dependency closure and registry metadata
   available to Cargo, or run dependency resolution in an authorized environment
   with crates.io access. Fetching build dependencies is separate from runtime
   Jev/Kubernetes access, which remains unnecessary for validation.
2. Pin `logdrain` to 0.3.2 with external persistence features disabled, resolve the
   dependency addition while retaining existing locked versions, and review the
   actual lockfile diff and security audit before integration.
3. Implement the bounded adapter and analysis-lane integration with the safety
   cases above. If ambiguity, global bounds, or evidence safety cannot be proven
   with this API, stop and document that specific blocker; do not substitute weak
   normalization.
4. Add deterministic tests and an offline fixture using synthetic Kubernetes-like
   events and a synthetic classifier. Exercise number/IP/request-ID variation,
   source/baseline isolation, version changes, TTL, failed/private/truncated
   fallback, capacity, raw evidence counts, controller recurrence/notifications,
   restart retraining, and disabled-mode compatibility. Report raw events,
   classifier-owned representatives, simulated batches, successful reuses, and
   fallbacks separately. Do not describe simulated savings as live provider cost.
5. Run the full validation matrix in [VALIDATION.md](../VALIDATION.md), update
   usage/controller/architecture documentation for the actual implemented options,
   and record measured fixture counts before committing implementation.
