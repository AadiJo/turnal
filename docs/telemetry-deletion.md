# Telemetry deletion rehearsal and runbook

Network collection cannot be enabled until this runbook completes against the
actual staging PostHog project. A random installation UUID is pseudonymous and
must be handled as deletion-request data.

PostHog's current bulk-person deletion endpoint accepts `distinct_ids` and can
queue deletion of their events even when no person profile is retained. Event
deletion is asynchronous. The admin tool therefore separates request, event
verification, and final completion.

## Required access

- Persistent collector database snapshot and an operator with write access.
- A PostHog personal API key scoped to person deletion and query access.
- `POSTHOG_PROJECT_ID`, `POSTHOG_PERSONAL_API_KEY`, and the correct
  `POSTHOG_APP_HOST` (`https://us.posthog.com` or `https://eu.posthog.com`).
- Access to every configured batch export destination and any manually retained
  dashboard extract or query cache.

Never paste the installation ID, API key, request body, or query response into
chat, tickets, or retained application logs.

## Staging rehearsal

1. Seed a dedicated installation ID through the staging collector and verify its
   `turnal_daily_active` and `turnal_metric` events are queryable.
2. Stop the test client or run `turnal analytics reset` so the old ID is never
   reused.
3. Begin deletion:

   ```sh
   turnal-telemetry-admin delete "$INSTALLATION_ID"
   ```

   This atomically deny-lists the ID, removes collector outbox and daily-volume
   rows, records a pending audit row, and requests PostHog bulk deletion by
   distinct ID. If the vendor call fails, the denylist remains active.
4. Remove rows for the ID from every batch-export destination. Invalidate any
   manually materialized extract or cache. The committed dashboard blueprint is
   query-time only and creates no repository-side aggregate copy.
5. Poll the asynchronous event deletion until it reports zero:

   ```sh
   turnal-telemetry-admin verify "$INSTALLATION_ID"
   ```

   Do not interpret `persons_found: 0` as failure: personless events intentionally
   do not require a person profile. The event-count query is the completion check.
6. Inspect the PostHog project for the distinct ID and confirm no event export,
   saved raw result, or separately retained derivative remains. Record the
   destinations checked, operator, UTC time, and evidence location in the private
   operations record.
7. Only after those checks, attest the derived-copy verification and complete:

   ```sh
   TURNAL_DERIVED_DELETION_VERIFIED=true \
     turnal-telemetry-admin complete "$INSTALLATION_ID"
   ```

8. Attempt a replay of the deleted batch and prove the collector returns 410
   without recreating data. Confirm the reset client uses a different UUID.

## Scope and retention

- Undelivered collector payloads and daily abuse counters are deleted at request
  start.
- Delivered payload bytes are removed immediately after PostHog acknowledgement;
  deduplication hashes and delivered metadata expire after seven days.
- Quarantined payloads expire after seven days.
- Daily abuse counters expire after fourteen days.
- Completed deletion audit and denylist rows expire after ninety days. Pending
  deletion rows do not expire.
- Raw PostHog event retention is capped at ninety days.
- External batch-export copies follow the destination's documented retention and
  must be part of every request rehearsal.

If any copy cannot be located, deleted, or bounded truthfully by retention, keep
the client endpoint empty and the collector kill switch off.
