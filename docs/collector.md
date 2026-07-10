# Turnal telemetry collector

The collector is the durable acceptance boundary for Turnal telemetry. It accepts
only schema-v1 daily aggregates, commits each unique batch and canonical payload
to SQLite, then returns HTTP 202. PostHog forwarding is asynchronous and never
defines whether the client may delete its local batch.

Collection is disabled unless `TURNAL_COLLECTOR_ENABLED=true`. Keep it disabled
until every gate in [the telemetry policy](telemetry.md) has passing evidence.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `TURNAL_COLLECTOR_ADDR` | `:8080` | Listener address. |
| `TURNAL_COLLECTOR_DB` | `data/collector.db` | Durable SQLite database on persistent storage. |
| `TURNAL_COLLECTOR_ENABLED` | `false` | Reject-all kill switch; disabled returns 410. |
| `TURNAL_COLLECTOR_REQUIRE_HTTPS` | `true` | Reject batch requests that did not arrive over TLS. |
| `TURNAL_COLLECTOR_TRUST_PROXY_PROTO` | `false` | Trust `X-Forwarded-Proto: https` only behind a proxy that overwrites it. |
| `TURNAL_COLLECTOR_RATE_LIMIT` | `120` | Ephemeral requests per source address per minute. Source addresses are not persisted. |
| `TURNAL_COLLECTOR_DAILY_VOLUME_LIMIT` | `100000` | Per-installation/day count budget before quarantine. |
| `TURNAL_COLLECTOR_TLS_CERT` / `TURNAL_COLLECTOR_TLS_KEY` | unset | Optional direct TLS. Configure both or neither. |
| `POSTHOG_PROJECT_TOKEN` | unset | Separate project token. Forwarding pauses when absent. |
| `POSTHOG_HOST` | `https://us.i.posthog.com` | Allowlisted US or EU ingestion host. |
| `POSTHOG_PERSONAL_API_KEY` | unset | Query-scoped key for downstream canary reconciliation; also used separately by the deletion admin tool. |
| `POSTHOG_PROJECT_ID` | unset | Project queried by canary/deletion verification. |
| `POSTHOG_APP_HOST` | `https://us.posthog.com` | Allowlisted US or EU application API host. |

The production proxy and application must not log request bodies, headers,
User-Agent values, or source addresses. The Go server discards its per-connection
error log and emits only aggregate delivery counts and stable operational state.

## Deployment

Build the supplied image:

```sh
docker build -f deploy/collector/Dockerfile -t turnal-collector .
```

Mount `/app/data` on durable encrypted storage. Put the service behind a TLS
terminator that overwrites `X-Forwarded-Proto`, set
`TURNAL_COLLECTOR_TRUST_PROXY_PROTO=true`, and verify plain HTTP requests cannot
reach the origin directly. Alternatively, mount a certificate and key and use
direct TLS.

SQLite runs in WAL mode with `synchronous=FULL`. Back up the database with a
SQLite-aware snapshot mechanism; copying only the main file while the process is
running can omit WAL data. Run one collector process per database volume. Scale
only after moving the same transactional contract to a shared durable database.

## PostHog project controls

- Use a dedicated project with a small named access group.
- Enable project-level IP discard.
- Disable person profiles, autocapture, replay, and unrelated SDK features.
- Keep the project token only in the collector secret store.
- Configure raw event retention to no more than 90 days.
- Alert if any event other than `turnal_daily_active` or `turnal_metric`, or any
  property outside the documented schema, appears.

The collector emits `$process_person_profile: false`, `$geoip_disable: true`, and
a deterministic `$insert_id` for every translated event. Each aggregate date is
normalized to noon UTC; it never recreates an invocation timestamp.

When query credentials are configured, an hourly synthetic canary must reconcile
to exactly one queryable event. Canary events carry `collector_canary: true` and
are excluded by every product query.

## Acceptance and delivery states

```text
validated -> durably_accepted -> forwarding -> delivered
                   |                  |
                   |                  +-> retryable
                   +--------------------> quarantined
```

Replaying a `batch_id` with the same canonical payload returns its original
acceptance without incrementing volume. Reusing an ID with different content is
rejected. A forwarding lease that survives a crash becomes retryable. Delivered
payload bytes are removed immediately while the payload hash remains for replay
deduplication.

The client deletes local batches only after a bounded HTTP 202 response with the
closed `durably_accepted` acknowledgement shape and a batch total matching the
request. Malformed, oversized, trailing, or inconsistent acknowledgements remain
retryable.

## Local verification

```sh
go test -race ./internal/collector
go build ./cmd/turnal-collector
```

The suite covers request mutation, limits, durable crash points, concurrent
replay, anomaly quarantine, delivery retry, invalid acknowledgements, denylisting,
and deletion of collector-held data.
