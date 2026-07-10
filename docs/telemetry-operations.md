# Telemetry operating model

The collector logs stable aggregate counters and alert codes only. It does not log
request bodies, headers, source addresses, installation IDs, PostHog responses,
or raw errors. External proxies must be configured to meet the same boundary.

## Alerts

| Code | Trigger | First response |
| --- | --- | --- |
| `collector_5xx_rate` | More than 1% of at least 100 requests fail server-side. | Enable the 410 kill switch if durable acceptance is at risk; inspect storage health without dumping payloads. |
| `schema_rejection_rate` | More than 2% of at least 100 requests are schema-rejected; counters are split by released version when decodable. | Stop rollout for the affected version and compare its golden fixture. |
| `batch_id_conflict` | A known batch ID arrives with different canonical content. | Stop rollout and investigate client/storage integrity. No conflicting payload is committed. |
| `outbox_delivery_slo` | Oldest accepted/retryable batch is older than 15 minutes. | Pause rollout, inspect PostHog and worker health, and protect outbox capacity. |
| `canary_reconciliation` | A synthetic canary is absent, duplicated, rejected, or cannot be queried. | Treat downstream visibility as unknown; do not claim delivery health. |
| `accepted_volume_spike` | Daily accepted volume exceeds five times the prior baseline without a release/distribution explanation. | Quarantine anomalous traffic and review abuse budgets; authenticity remains unprovable. |

The in-process evaluator emits only code and severity. Production alerting should
scrape or forward these stable counters without adding high-cardinality labels.

## Synthetic canary

When the collector has both a project token and query-scoped personal API key, it
sends one hourly `turnal_daily_active` event marked `collector_canary: true`, waits
for ingestion, and queries the deterministic insert ID. Exactly one event must be
visible. Product dashboards apply the global predicate
`coalesce(properties.collector_canary, false) = false`.

Canary IDs are newly random, never tied to client installations, and expire under
the same 90-day raw retention. A 2xx response with malformed JSON does not count
as delivery.

## Incident priorities

1. Protect consent and prevent new exposure: kill collection or blank the next
   release endpoint.
2. Preserve the durable acceptance contract: do not delete accepted pending data
   or acknowledge discarded input.
3. Bound the affected schema versions, UTC dates, and stable alert counters. Do
   not inspect raw payloads unless the named incident owner approves it.
4. Reconcile outbox, PostHog visibility, exports, and deletion scope.
5. Publish user-facing impact when trust, collection scope, retention, or deletion
   promises were affected.

## Access and ownership

- Schema owner: approves every key/property change with privacy tests.
- Collector owner: deploys, monitors, rotates tokens, and manages incidents.
- Deletion owner: runs and records staging/production deletion verification.
- Data access group: a minimal named set; dashboards are preferred over raw data.

Quarterly, review the membership, API key scopes, processor settings, export
destinations, alert delivery, and retention jobs.
