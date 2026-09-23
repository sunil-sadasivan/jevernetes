# A probabilistic control plane

This document records the product thesis, architectural direction and falsifiable hypotheses.
It distinguishes the implemented Rust security/fraud monitor from proposed products. It makes
no market-size estimate or claim that model confidence is objective measurement.

## The primitive

The loop is **observe messy state → typed probabilistic judgment → deterministic confidence
policy → act/escalate/abstain → record outcomes/recalibrate**. Observations include logs,
transactions, deployment evidence, tool requests or sensor histories. A judgment gives a
bounded taxonomy and confidence, with explicit missing evidence and failure states. Policy
uses thresholds, source scope, permissions, recurrence, costs and review requirements to
choose a permitted response. Outcome capture closes the learning loop; it must distinguish
what happened from what the model predicted.

A conventional controller reconciles observed state toward an explicit desired state.
Here, part of the observed state must first be inferred from ambiguous evidence. The
probabilistic step supplies typed evidence; deterministic code still owns invariants and
actuation. Uncertainty is part of the state machine, not an exception hidden in logs.
Abstention, insufficient coverage, stale judgments and human review are first-class results.

This is a candidate new control-plane primitive because durable observation identity,
versioned judgments, replayable policy, incident state, idempotent effects and outcome
feedback form a reusable lifecycle. A classifier wrapper ends after returning a label. A
useful control plane also explains which evidence was examined, why a decision was allowed,
what was delivered, what was withheld, and whether the response helped. The present controller
implements observation through durable advisory delivery; full outcomes and recalibration
are roadmap work. Calling that future loop complete today would be misleading.

Jev supplies typed choice judgments today. It does not replace compilers, authorization
checks, deterministic rules, enforcement engines, transaction ledgers or objective
measurements. A compiler error remains a compiler error. A payment balance must still
reconcile arithmetically. No model should override hard safety or authorization boundaries.

## Open-source wedge: security and fraud monitoring

Start with an installable, read-only Kubernetes monitor for important security/fraud evidence
in application logs. Teams can evaluate it beside existing systems, without granting cluster
writes or trusting automatic remediation. The initial experience should be: install in one
namespace, see honest coverage, receive a small number of evidence-backed advisories, review
false positives, and measure incremental value and cost. Fraud allegations require particular
care: logs can suggest anomalous transaction/account behavior, not establish intent or guilt.
Human review is the product boundary for consequential judgments.

This complements established sensors and policy engines. [Falco](https://falco.org/docs/)
provides runtime detections with rules over kernel and plugin events;
[Tetragon](https://tetragon.io/docs/overview/) provides eBPF security observability and runtime
enforcement; [Tracee](https://aquasecurity.github.io/tracee/latest/) provides runtime security
observability/detection. Those systems supply evidence and deterministic detection/enforcement
capabilities this log monitor does not have. [OPA](https://www.openpolicyagent.org/docs)
separates deterministic policy decisions from applications; inferred facts can be inputs to
such policy only with explicit provenance and constraints. These are product-composition
proposals, not claims that integrations are implemented.

Conventional observability agents remain responsible for collection, transport and objective
metrics; SIEM systems remain investigation, correlation and retention destinations. Reuse their
pipelines and incident workflows where possible. Do not require customers to replace a
working sensor fleet to try semantic interpretation of ambiguous application evidence.
The wedge succeeds only if it finds useful incremental evidence at tolerable false-positive,
privacy, latency and cost levels. It must be compared against simple rules and existing
analyst workflows, not merely against an unstructured language-model prompt.

## Ranked opportunity landscape

Ranking is a product judgment about initial learning velocity, measurable value, adoption
friction and expansion potential, not a revenue forecast. The platform ranks highest as the
long-term thesis; security ranks first as the practical entry point.

| Rank | Opportunity | Why pursue it | Proof and principal obstacle |
| --- | --- | --- | --- |
| 1 | General controller SDK/runtime | Common lifecycle for uncertain observations, deterministic policy, durable effects and outcome evaluation across applications | Two materially different domains must reuse the same contracts without a maze of special cases; premature abstraction is the risk |
| 2 | Security operations | Read-only open-source adoption, existing evidence and analyst workflows, visible alert burden | Show incremental confirmed findings and lower analyst work at fixed recall; crowded workflows and alert fatigue |
| 3 | Fraud/transaction risk | Decisions have measurable downstream outcomes and explicit review budgets | Obtain lawful labels and transaction context, preserve deterministic financial checks, handle delayed outcomes and unequal harms; high cost of false accusations |
| 4 | AI-agent governance | Tool requests and plans provide natural uncertain inputs before deterministic permission checks | Demonstrate fewer unsafe attempts with tolerable task-completion impact; adversarial inputs, latency and easily copied superficial guardrails |
| 5 | Change/release risk | Compare deployment evidence with expected behavior, advise hold/review/canary investigation | Prove signal beyond tests, SLOs and canary metrics; confounding between deployment and unrelated incidents |
| 6 | Data quality/integrity | Interpret semantic inconsistencies that schemas and arithmetic constraints miss | Find material defects beyond deterministic validation; establish ground truth and prevent invented errors |
| 7 | Identity/access decisions | Review anomalous access context and explain requests | Advisory review only initially; authorization must remain deterministic, with fairness, appeal and audit obligations |
| 8 | Edge/local autonomous systems | Local models and bounded policies can operate under connectivity constraints | Establish hardware, energy and safety performance with independent measurements; physical consequences and certification make this a later market |

Avoid expanding merely because all eight domains can call a model. Each needs a decision
owner, an observable outcome, an affordable review/action path and a defensible reason that
uncertain semantics add value beyond ordinary rules. Domain-specific limits may prohibit
actuation entirely. Edge safety is especially incompatible with treating a generic model
score as a safety certificate.

## Evaluation, counterfactuals and calibration

Build a versioned replay corpus from consented, redacted evidence and synthetic adversarial
cases. Split by time, source and incident, preventing duplicated templates from leaking across
training/tuning/evaluation. Track important-security/fraud precision and recall, false alerts
per source-day, review burden, time to actionable notification, incremental confirmed findings,
misses discovered later, abstention rate, coverage, subgroup/source drift and delivery loss.
Measure provider calls per new exact group, reuse/expiration rates, dollars per reviewed or
confirmed finding, p50/p95 latency, idle CPU, burst memory and database write amplification.
Report denominators and confidence intervals; partial analysis cannot certify absent risk.

Compare against keyword/rule baselines, existing SIEM detections and analyst triage. Evaluate
uncertainty using reliability diagrams, Brier/log scores where labels support them, and
selective risk versus coverage. Calibrate separately for categories and deployment populations;
a global confidence threshold need not transfer. Hold out model revisions, noisy sources,
prompt injections, truncation, provider failures, store outages and delivery failures. Change
thresholds only with explicit versioned policy and a reviewed tradeoff, never silently from a
few flattering anecdotes.

The outcome ledger should record: immutable evidence/contract IDs, time/source/coverage,
judgment scores, policy version and reason, candidate permitted actions, selected response,
notification acknowledgments, human assessment, later confirmed outcome, timestamps and label
provenance. Preserve reversals and reviewer disagreement. Do not infer truth from notification
success, silence, lack of analyst response, or a model explaining its own answer.

Counterfactual capture means recording what alternative policies *would* have done in shadow
mode, including withheld alerts and abstentions. It does not mean inventing outcomes for
unexecuted actions. Where ethical and authorized, use staged deployments, matched cohorts or
randomized review allocation to estimate intervention effects. Record selection bias, delayed
labels, censoring and confounders. Fraud outcomes such as chargebacks are delayed and imperfect;
security incident confirmation is likewise incomplete. Independent evidence and adjudication
are required before claiming causal impact.

Offline replay should rerun policy over fixed typed judgments first, then optionally rescore
with separately versioned providers. This separates policy changes from model changes and
makes regressions inspectable. Production can shadow a candidate contract while the approved
contract controls delivery. Human labels feed a reviewed calibration release; no automatic
feedback loop should turn recent user clicks into unreviewed enforcement.

## Architecture, defensibility and product boundary

Keep observation adapters, the typed judgment contract, confidence policy, state engine,
sinks and evaluation/outcomes independent. The current Rust implementation has a Jev client,
provider-independent policy and a sink trait; it is not yet a general provider plugin SDK.
The next adapter boundary should return the same bounded typed judgment plus provenance,
usage and explicit errors. Jev, other hosted providers and local models should be comparable
under the same corpus. Provider identity/model/version must invalidate reuse; changing a
provider must not change authorization semantics. Structured output alone does not ensure
calibration or correctness.

Potential moats are trustworthy operational integration, longitudinal outcome data obtained
with permission, reproducible evaluation/calibration, incident/workflow integration and
switching value from verified policies and audit history. Prompt text, a thin API wrapper,
a generic dashboard or access to one foundation model is unlikely to be durable advantage.
Data network effects are a hypothesis: tenant confidentiality and domain differences can
prevent pooling. Design tenant-local learning and portable exports before promising shared
intelligence. Open protocols and local execution can build trust; they also make competition
easier, so the product must earn value through reliability and measurable outcomes.

Keep a useful single-cluster runtime, core contracts, policy evaluation, offline replay formats,
local state and basic sinks open source. A proposed commercial boundary is managed fleet
coordination, durable multi-tenant/server storage, organization SSO/RBAC, policy approval and
rollout, long-retention outcome analytics, calibration services, enterprise connectors,
compliance exports, private networking, support and operating guarantees. Do not make safe
failure handling, exportability or basic security dependent on payment. Current code is Apache
2.0; “open core” here is a proposed product boundary, not a licensing change.

Enterprise adoption starts with a narrow shadow pilot, explicit data-processing/residency
terms, private deployment options, operator-approved egress and retention, named decision
owners, evaluation acceptance criteria and integration into existing case management. Expand
only after an audited incident/outcome trail and a repeatable installation/upgrade story.
Managed service revenue must cover inference, storage, review support and operations; measure
unit economics per useful decision rather than celebrating raw inference volume.

Risks include hallucination, poor calibration, prompt injection, sensitive-data disclosure,
biased or unrepresentative labels, automation bias, alarm fatigue, workload drift, provider
outages/pricing changes, alias changes, replay gaps, storage exhaustion and uncertain liability.
Mitigations require explicit abstention, least privilege, durable effects, measured coverage,
independent outcomes and human authority. They do not eliminate these risks. Distribution,
buyer ownership and willingness to pay may fail even if the technical architecture works.

## Concrete MVP and 12-month roadmap

**MVP now:** one read-only in-cluster replica; exact grouping and versioned TTL reuse;
security/fraud confidence gates; cooldown/recurrence policy; transactional notification
outbox; stdout/HTTPS delivery; local readiness and aggregate metrics; bounded behavior and
synthetic failure tests. [Operational limits](controller.md) explicitly describe non-durable
input queues, per-process cost budgets, finite table capacity, timestamp ambiguity and no
automatic remediation. This is an evaluable primitive, not validated detection efficacy.

**Months 0–3:** run consented shadow pilots with three design partners; publish a labeled
synthetic/releasable corpus and baseline results; measure resource/cost envelopes; add incident
export/archive, dead-letter tooling and a versioned reviewer/outcome ledger. Test volume-full,
process/power-loss recovery and target Kubernetes distributions in authorized environments.

**Months 4–6:** ship replay and calibration reports, contract/policy change review, stable
multiplicity-aware occurrence IDs, durable quotas across restarts and two prioritized incident
sinks. Introduce a second provider/local-model adapter and quantify portability. Start paid
pilots only when the agreed quality and operating criteria are met.

**Months 7–9:** extract the reusable controller SDK from a second domain, preferably transaction
risk review or agent-tool governance selected by partner evidence. Add source ownership/shards
or transactional server storage before multi-replica operation; add enterprise identity,
retention and private-network controls. Keep automatic high-consequence actions out of scope.

**Months 10–12:** deliver managed fleet policy/version rollout, outcome analytics, audited
exports and measured operating objectives. Validate repeatable onboarding, renewals/expansion,
support cost and inference unit economics. Decide whether a horizontal runtime is earning
adoption or whether the strongest business remains a focused security product. Dates are
planning targets, not commitments or evidence of completed capability.

## Recommendation and falsifiable company thesis

The strongest billion-dollar-company **thesis**, not a valuation prediction, is a
**provider-open runtime and managed control plane for decisions under uncertainty**, entered
through a useful open-source security/fraud monitor. Sell accountable decision operations:
which evidence was new, which interpretation was trusted, which policy authorized a response,
what actually happened and how quality changed. Security supplies a concrete first buyer and
feedback workflow; the horizontal runtime is earned through proven reuse. A standalone log
classifier is a weaker thesis because differentiated accuracy, distribution and durable value
are harder to defend without owning that lifecycle.

What must be uniquely true: the shared lifecycle materially lowers the cost of safely deploying
uncertain judgments; outcome-driven calibration beats easy baseline integrations; customers
trust the evidence/permission boundary; integrations and outcome history create durable value;
and provider openness improves economics or resilience without destroying quality. If these
are not true, the abstraction may be unnecessary infrastructure and the company thesis fails.

Pre-register measurable gates with partners. Proposed falsifiable milestones:

1. By month 3, three independent shadow pilots produce adjudicated datasets with coverage and
   negative examples. At least two show incremental confirmed findings beyond their existing
   baseline, within a pre-agreed false-alert budget. If labeling or incremental value cannot
   be established, do not infer success from alert volume.
2. By month 6, at least two partners pay for continued use and demonstrate a proposed target
   of 30% lower median triage effort at non-inferior measured recall on their held-out corpus.
   Also meet a partner-approved maximum cost per useful finding. These are targets, not results;
   revise the wedge if the benefit disappears under independent adjudication.
3. By month 9, a second domain uses the same judgment/policy/outbox/outcome contracts, and a
   second provider meets its quality/latency/cost acceptance criteria without policy rewrites.
   If domain-specific plumbing dominates, retain a focused product instead of forcing an SDK.
4. By month 12, at least three paying teams renew or expand after an independently reviewed
   outcome report; measured support and inference costs support a viable contract margin.
   Failure to sustain usage without founder-operated triage falsifies the managed-platform path.

No market-size arithmetic substitutes for these observations. The milestone that matters most
is repeatable, independently measured value with explicit uncertainty and accountable outcomes.
