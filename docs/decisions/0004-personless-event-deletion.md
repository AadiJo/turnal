# ADR 0004: Rehearse personless-event deletion before launch

- Status: required before network enablement
- Date: 2026-07-10
- Owner: Turnal privacy/operations maintainer

## Decision

Before accepting stable telemetry, staging must prove that all data for a random
installation UUID can be located and deleted even though PostHog person profiles
are disabled. The rehearsal covers analytical events, collector outbox records,
exports, cached query results, and derived aggregates and verifies asynchronous
completion.

During deletion the identifier is denied at the collector. Client reset disables
analytics, deletes unsent data, and generates a UUID that has never been used.
The runbook must identify data that cannot be deleted and its bounded expiry.

## Rationale

Exposing an identifier is not a deletion capability. Personless ingestion changes
the operational path for locating data, so the real vendor and derived-data flow
must be tested before the policy promises deletion.

## Consequences

The raw analytical retention maximum is 90 days. Network rollout stops if any
copy cannot be found, deleted, or truthfully bounded by retention. A named person
must own requests and completion verification before launch.
