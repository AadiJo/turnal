# Turnal telemetry policy

Status: local-only client foundation; network collection is disabled.

Turnal's telemetry design is pseudonymous installation analytics, not anonymous
people analytics. The client aggregates an allowlisted set of integer counters by
UTC date. It never reads Turnal's event log, hidden Git repository, SQLite index,
or workspace configuration to build telemetry.

No stable or development build currently sends telemetry. Network collection
must remain disabled until the consent, durable-acceptance, deletion,
metric-semantics, and authenticity gates in this document have passing evidence
and an assigned maintainer.

## Ownership

| Area | Accountable role |
| --- | --- |
| Schema and metric changes | Turnal CLI maintainer |
| Consent copy and privacy requests | Turnal project maintainer |
| Collector deployment, access, and incidents | Turnal infrastructure maintainer |
| Deletion rehearsals and completion checks | Turnal privacy/operations maintainer |
| PostHog access | Small, explicitly named maintainer group |

The roles must be assigned to named people before network collection is enabled.
Every schema change must update this inventory, the typed metric registry, golden
payload fixtures, collector validation, dashboard definitions, and this policy's
change log in the same release.

## Consent

The initial stable model is explicit opt-in. A disclosure may be shown on an
eligible interactive invocation, but that invocation must not create metric
counters, queue payloads, or send network requests. Collection starts only after
the person running Turnal explicitly enables it.

These process-level overrides always disable both recording and sending:

- `TURNAL_NO_ANALYTICS=1`
- `DO_NOT_TRACK=1`
- a recognized CI environment
- a development build (`0.0.0` or channel `dev`)

Disabling analytics must delete unsent aggregates. Resetting analytics must also
rotate the random installation identifier. Workspace configuration, environment
configuration paths, and command flags must never enable telemetry, select an
endpoint, or supply an installation identifier.

## Data inventory

Allowed fields are closed in schema version 1. Any unlisted field is forbidden.

| Allowed | Purpose |
| --- | --- |
| `schema_version` | Decode the fixed contract without silently changing semantics. |
| `batch_id` | Random UUID used only for idempotent collector acceptance. |
| `anonymous_id` | Cryptographically random installation UUID; never fingerprinted. |
| `date` | UTC calendar date; exact invocation time and within-day order are discarded. |
| `build.version` | Released Turnal semantic version. |
| `build.channel` | Allowlisted release channel (`stable` or `nightly`). |
| `build.install_source` | Allowlisted distribution source. |
| `build.os` and `build.arch` | Go runtime target, limited to allowlisted values. |
| `metrics[].key` | A value from the compile-time metric registry. |
| `metrics[].count` | Positive bounded integer aggregate. |

The collector adds one backend-only boolean, `collector_canary`, solely to mark
synthetic downstream reconciliation events. Client payloads cannot set it, and
all product dashboards exclude it.

The following data is forbidden in telemetry files and request bodies:

- names or identifiers for people, accounts, organizations, machines, devices,
  repositories, workspaces, sessions, turns, agents, or models;
- hostname, username, email, Git identity, npm identity, advertising identifiers,
  MAC addresses, disk identifiers, or any derived machine fingerprint;
- repository remotes, branch names, refs, commit hashes, paths, file names,
  file contents, diffs, prompts, transcripts, tool names, tool input/output,
  command arguments, flags, stdin, stdout, or stderr;
- raw errors, stack traces, error hashes, locale, timezone, exact OS build,
  device specifications, IP-derived geolocation, source IP storage, request
  headers, or user-agent retention.

The application collector may use connection metadata ephemerally for abuse
controls, but it must not persist that metadata in application-controlled logs
or analytics.

## Identity and local storage

The installation identifier is generated with a cryptographically secure random
UUID and stored only in the operating system's global user configuration area:

```text
config directory / turnal / telemetry.json      preference, notice state, UUID (0600)
cache directory  / turnal / telemetry /         disposable daily aggregates (0700/0600)
workspace        / .turnal /                     never telemetry preference or identity
```

State survives ordinary upgrades, so metrics describe installations rather than
people. Reinstalls can over-count and shared machines can under-count. Reports
must use the word "installation" and, while opt-in is used, the prefix
"consenting."

Local aggregates are retained for at most 14 UTC days, 128 batch files, or 512
KiB, whichever bound is reached first. The oldest telemetry is discarded first.
Malformed data is quarantined or removed and can never block a Turnal command.
Current-day counters use atomic replacement without forcing every approximate
increment to stable storage; a machine-level storage failure can lose the newest
counter but cannot expose a partially written JSON file. Consent state and
rotated sendable batches use durable writes. Best-effort recording budgets 5 ms
for the state read and 15 ms for the aggregate lock, dropping the metric on
contention rather than delaying the hook beyond its 25 ms telemetry budget.

## Metric semantics

Schema version 1 stores daily counters and intentionally cannot establish order
between events on the same UTC date. Activation and retention queries must use
date-level milestones. Dashboards must sum `count`; counting aggregate rows gives
installation-days, not command volume.

Changing a metric's meaning requires a new metric key or schema version. npm and
release downloads are distribution signals only and must never be used as a
runtime activation denominator.

## Retention and processors

Before network collection is enabled, the public policy must identify PostHog as
a processor and disclose the exact enabled fields, purposes, withdrawal behavior,
retention, and deletion contact. Raw analytical events are limited to 90 days.
A collector durable outbox should retain delivered payloads only as long as
needed for delivery, targeted at seven days or less. Retention for separately
materialized aggregates must be documented before those aggregates exist.

PostHog events must be personless (`$process_person_profile: false`) with GeoIP
disabled. Identify, alias, group identify, autocapture, and session replay are
prohibited.

## Network enablement gates

Collection remains disabled until all of the following are true:

1. Consent and lawful basis: the disclosure and explicit opt-in flow are reviewed,
   published, and tested end to end.
2. Durable acceptance: the collector atomically stores a unique `batch_id` and
   canonical payload before returning HTTP 202; crash and replay tests prove no
   acknowledged loss or duplicate inflation.
3. Deletion: staging proves personless events can be located and deleted by
   installation ID across PostHog, the outbox, exports, caches, and derivatives.
4. Metric semantics: seeded dashboards exactly reconcile numeric counts and
   date-level D7/D30 fixtures without relying on same-day order.
5. Authenticity: policy and dashboards label the public-client dataset
   directional and pseudonymous, with anomaly quarantine and abuse budgets.

An empty, release-time endpoint is the client kill switch. The collector must
also have a documented reject-all mode. It must never return 202 while silently
discarding a batch.

## Deletion

The analytics status command must eventually expose the current installation UUID
so a request can reference it. Reset creates a never-reused UUID. While deletion
is in progress the old identifier is placed on a temporary denylist.

The pre-launch rehearsal must verify asynchronous completion for PostHog events,
collector outbox rows, exports, cached query results, and derived aggregates. The
runbook must state which data cannot be deleted and the exact date on which it
expires. See [ADR 0004](decisions/0004-personless-event-deletion.md).

## Directional-data limitation

A distributed open-source CLI cannot keep a collector credential secret. Schema
validation, rate limits, and deduplication do not prove that a request came from a
genuine Turnal binary or a unique person. Telemetry may guide product decisions
alongside other evidence, but it is not tamper-proof usage data and must not be
the sole basis for a business-critical decision.

## Change log

- 2026-07-10: froze schema version 1, explicit opt-in, UTC-day semantics,
  network-disabled client foundation, 90-day raw retention maximum, durable
  acceptance boundary, and deletion rehearsal requirement.
