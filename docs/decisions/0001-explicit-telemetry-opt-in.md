# ADR 0001: Require explicit telemetry opt-in

- Status: accepted for the network-disabled client foundation
- Date: 2026-07-10
- Owner: Turnal project maintainer

## Decision

Turnal telemetry is disabled until a person explicitly enables it. A disclosure
does not constitute consent and must not record counters or queue payloads during
the invocation on which it is first shown. CI, `DO_NOT_TRACK`,
`TURNAL_NO_ANALYTICS`, and development builds override an enabled preference.

A future default-on or notice-first experiment is a separate product and privacy
decision requiring a documented lawful basis, updated disclosure, a restricted
cohort, and new approval. It is not compatible with this ADR.

## Rationale

Turnal is local-first and processes sensitive coding-agent material. Explicit
opt-in keeps collection aligned with user expectations and lets the project prove
the field inventory, deletion workflow, and reliability boundary before accepting
data.

## Consequences

Reach metrics describe consenting installations and will be biased toward people
who opt in. The project accepts that limitation and will not extrapolate opt-in
counts to all installations or divide them by package downloads.
