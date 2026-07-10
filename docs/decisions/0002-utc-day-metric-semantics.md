# ADR 0002: Aggregate telemetry by UTC date

- Status: accepted
- Date: 2026-07-10
- Owner: Turnal CLI maintainer

## Decision

Schema version 1 records only allowlisted integer counters grouped by UTC calendar
date. It discards invocation timestamps, duration, and within-day ordering. The
canonical serialized date is `YYYY-MM-DD`, and a future collector will normalize
the analytical timestamp to noon UTC for that date.

Activation and retention are date-level milestones. Same-day events form an
unordered set. Volume and success-rate formulas must sum the numeric `count`
property instead of counting aggregate rows.

## Rationale

Daily aggregation answers adoption and retention questions while revealing less
behavioral detail and making accidental collection of command timing impossible.

## Consequences

Turnal cannot report session duration, command latency distributions, time of day,
or a proven same-day funnel sequence. A semantic change requires a new key or a
new schema version and updated fixtures.
