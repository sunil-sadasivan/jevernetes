# Adaptive audits and budgets

`--drain-adaptive` requires online Drain grouping. New and expired tickets are assessed immediately at microbatch close. Confirmed routine verdicts are audited at a deterministic interval that grows from 32 observations to `--drain-normal-interval` (32–4096, default 1024). Important verdicts retain a 32-observation audit interval. Failed/uncertain/unsafe observations invalidate reusable state. `--drain-require-review ID` vetoes a template identity; at most 256 IDs are allowed.

`--drain-budgeted` requires adaptive mode. All provider clients already enforce pessimistic cumulative reservations, maximum batches and attempts; the flag does not enable a different priority scheduler. Reservations are never refunded. Missing token accounting and reservation underestimates stop future admission. Risk and template clients have separate explicit budgets. There is no shared monetary pool or automatic funding.

Checkpoints contain only opaque template/version/generation IDs and counters. They are diagnostic exports, not automatically restored safety authority. Probabilistic normality, burst scheduling, fixed random sampling, automatic restart learning and feedback models are not implemented. Unsupported compatibility flags are rejected. See [migration](migration.md) for the full list.
