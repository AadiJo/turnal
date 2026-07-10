# Turnal analytics definitions and verification

The PostHog dashboard blueprint is
[`analytics/posthog/dashboard.json`](../analytics/posthog/dashboard.json). It is
not published automatically: production publication requires a dedicated PostHog
project, named access owners, configured IP discard, a 90-day maximum raw event
retention, and a successful deletion rehearsal.

## Canonical KPIs

| Role | Metric | Definition |
| --- | --- | --- |
| Primary outcome | Weekly recording-active installations | Distinct consenting installation IDs with at least one `turn.recorded.*` metric on the inclusive seven-date UTC window. |
| Activation driver | Activated installations | A successful `workspace.initialized` date plus an adapter configuration or successful wrapped run on the same or a later UTC date. Same-day milestones are unordered. |
| Retention | D7 / D30 recording retention | Activated installations with a completed recorded turn exactly 7 or 30 UTC dates after activation, divided by cohorts old enough to observe the complete window. |
| Depth | Inspection and recovery adoption | Distinct installations with successful allowlisted inspection or recovery commands among weekly recording-active installations. |
| Quality guardrail | Command success rate | Sum of numeric success counts divided by summed success and failure counts for each canonical command family. |
| Trust guardrail | Small-sample suppression | Do not display or act on a slice with fewer than 20 distinct installations. |
| Reliability guardrail | Collector health | Durable acceptance, retry, quarantine, oldest-outbox age, rejection, duplicate, and canary reconciliation rates. |

Every visible reach label says "installation," and opt-in releases say
"consenting installation." Distribution downloads remain a separate top-of-funnel
signal and are never an activation denominator.

## Synthetic reconciliation fixture

Generate the clearly labeled synthetic 30-installation, 35-date fixture:

```sh
go run ./cmd/turnal-analytics-fixtures > /tmp/turnal-analytics-fixture.json
go test ./internal/analytics
```

The committed test expectation is:

| Check | Expected |
| --- | ---: |
| Installations / activated | 30 / 30 |
| Weekly recording-active at 2026-07-05 | 22 |
| D7 retained / eligible | 21 / 30 |
| D30 retained / eligible | 18 / 30 |
| Weekly search-active / recording-active | 12 / 22 |
| Status success / failure command volume | 90 / 30 |
| Status success rate | 75% |

The fixture intentionally has 73 aggregate rows while status command volume is
120. This catches dashboards that count `turnal_metric` rows instead of summing
the numeric `count` property. Reversing aggregate order does not change activation
or retention; reversing metric order makes the canonical schema invalid rather
than inventing within-day sequence.

## Dashboard QA before publication

1. Import the synthetic aggregates through a staging collector and wait for all
   canary batches to become queryable.
2. Reconcile every value above, including distinct installation denominators and
   numeric count totals.
3. Confirm global version, channel, install-source, OS, and architecture filters
   update every applicable widget from the same event-date definition.
4. Confirm rare slices disappear below 20 installations and rare platforms roll
   into `other`.
5. Inspect every query for UTC date logic, numeric count sums, and the absence of
   same-day ordered funnels.
6. Confirm dashboard titles and exports say the dataset is directional,
   pseudonymous, and limited to consenting installations.

Until a real staging PostHog project is available, the repository validation is
`Ready to share` for calculation logic and `Needs revision` for publication: live
source freshness, access, project retention, query syntax against the selected
PostHog deployment, and staged deletion are external release gates rather than
facts this fixture can prove.
