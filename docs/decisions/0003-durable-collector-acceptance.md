# ADR 0003: Make Turnal's outbox the acceptance boundary

- Status: required before network enablement
- Date: 2026-07-10
- Owner: Turnal infrastructure maintainer

## Decision

A future collector may return HTTP 202 only after one atomic transaction records
the unique `batch_id` and canonical validated payload in a durable outbox. Replay
of the same batch returns the original acceptance result without new analytical
work. PostHog forwarding is asynchronous and replaceable.

The client treats network errors, 408, 429, and 5xx responses as retryable across
later invocations. Other 4xx schema failures are quarantined. A PostHog HTTP
success is not evidence that events became queryable; synthetic canaries and
reconciliation monitor that downstream boundary.

## Rationale

Deleting a local batch after a non-durable proxy response can lose acknowledged
data. Retrying ambiguous requests without atomic deduplication can inflate counts.
The outbox and `batch_id` solve both failure modes at the Turnal-controlled
boundary.

## Consequences

Direct-to-PostHog ingestion is not a production option. Crash-point, replay, and
canary tests plus a reject-all kill switch are release gates. Until they pass,
the client endpoint remains empty and sending remains disabled.
