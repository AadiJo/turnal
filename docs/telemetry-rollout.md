# Telemetry rollout gates

Telemetry rollout is controlled at release time by two linker-backed values. A
repository, workspace config, environment variable at runtime, or command flag
cannot change either one.

| Release state | Endpoint | Rollout percent | Intended use |
| --- | --- | ---: | --- |
| Dark client | empty | 0 | Stable foundation and maintainer payload inspection. No process can send. |
| Nightly opt-in | allowlisted URL | 100 | One-week collector and deletion validation after all launch gates pass. |
| Stable opt-in canary | allowlisted URL | 10 | Deterministic 10% of explicitly opted-in installations. |
| Stable opt-in full | allowlisted URL | 100 | Only after two healthy canary weeks and gate sign-off. |
| Emergency build | empty | 0 | Release-time network kill switch. |

The npm binary build accepts only:

```sh
TURNAL_TELEMETRY_ENDPOINT=https://telemetry.turnal.dev/v1/batch
TURNAL_TELEMETRY_ROLLOUT_PERCENT=10
```

The build fails if the endpoint differs, the percentage is outside 0–100, or a
positive percentage is paired with an empty endpoint. Selection hashes only the
random installation UUID into a stable 0–99 bucket. It does not inspect machine,
workspace, account, or network attributes.

GitHub releases read these values from repository variables with the same names;
missing variables resolve to the dark empty-endpoint/zero-percent configuration.
Changing them is a release operation and requires the evidence below.

## Required evidence before nightly

- The disclosure is published at `https://turnal.dev/telemetry`, names PostHog,
  lists exact fields and retention, and has an assigned privacy-request owner.
- Client privacy, corruption, concurrency, output, offline, and cross-platform
  suites pass from the exact release commit.
- The collector runs on persistent encrypted storage with request/body/IP logging
  disabled, the reject-all kill switch tested, and durable crash/replay tests
  passing.
- A dedicated PostHog project has IP discard, least-privilege access, and raw
  retention of no more than 90 days.
- The 30-installation staging fixture exactly reconciles every committed KPI.
- The deletion runbook completes, including exports and derived copies.
- Synthetic canaries reconcile to exactly one queryable event and alert on zero
  or duplicate visibility.
- Named maintainers own schema, collector, deletion, and data access.

## Promotion and rollback

### Nightly explicit opt-in

Run for at least seven complete UTC dates. Proceed only when there is no forbidden
field, acknowledged loss, dedupe conflict, stale outbox, canary discrepancy,
unexpected property, or deletion gap. Schema rejection must be at most 2% by
released version and valid-batch durable acceptance at least 99.5%.

### Stable 10% canary

Run for at least fourteen complete UTC dates. Compare command output, exit codes,
hook latency, queue age, support feedback, and opt-in behavior with dark builds.
Proceed only when collector 5xx stays below 1% in every 15-minute window, no
foreground networking occurs, lock wait remains bounded at 25 ms, and deletion
is rehearsed again against canary data.

### Stable full rollout

Set the release percentage to 100 only after the evidence record has sign-off from
all four owners. Opt-in remains required; 100% means all consenting eligible
installations, not all Turnal installations.

Rollback immediately for any sensitive field, pre-consent write/send, hook output,
exit-code regression, foreground network delay, acknowledged loss, batch conflict,
outbox breach, canary mismatch, or unbounded deletion. Use one or more of:

1. Set `TURNAL_COLLECTOR_ENABLED=false` so the collector returns 410 and clients
   persist a network-suppression deadline and back off for 30 days.
2. Pause PostHog forwarding while preserving already accepted outbox data within
   its operational retention and capacity.
3. Publish an emergency build with an empty endpoint and zero rollout.
4. Revoke the PostHog project token if the processor boundary is compromised.

Never return 202 while dropping a batch.

## Thirty-day audit

After full rollout, delete unused properties, reconcile the dashboard to a fresh
fixture, review raw access, inspect exporter retention, rerun deletion, and publish
a transparency note with directional scope, opt-in coverage, field changes,
incidents, and retention changes. A notice-first experiment remains a separate
privacy/product decision and cannot be enabled through these rollout controls.
