package collector

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestStoreAcceptanceIsAtomicAndReplaySafe(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	result, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now})
	if err != nil || result.Accepted != 1 || result.Duplicates != 0 {
		t.Fatalf("first accept = %#v, %v", result, err)
	}
	result, err = store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now.Add(time.Minute)})
	if err != nil || result.Accepted != 0 || result.Duplicates != 1 {
		t.Fatalf("replay accept = %#v, %v", result, err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.DurablyAccepted != 1 {
		t.Fatalf("stats = %#v, %v", stats, err)
	}

	conflict := batch
	conflict.Metrics = []telemetry.MetricCount{{Key: telemetry.MetricInstallationActive, Count: 2}}
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{conflict}, AcceptOptions{Now: now}); !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestStoreAcceptanceVolumeCountsUniqueDurableBatchesByUTCDay(t *testing.T) {
	store := testStore(t)
	day := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	first := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	second := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{first}, AcceptOptions{Now: day}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{first, second}, AcceptOptions{Now: day.Add(8 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	volume, err := store.AcceptanceVolume(context.Background(), day.UTC())
	if err != nil || volume != 2 {
		t.Fatalf("acceptance volume = %d, %v", volume, err)
	}
	previous, err := store.AcceptanceVolume(context.Background(), day.AddDate(0, 0, -1))
	if err != nil || previous != 0 {
		t.Fatalf("previous acceptance volume = %d, %v", previous, err)
	}
}

func TestStoreCrashPointsRollbackOrReplayWithoutInflation(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricTurnRecordedCodex, 3)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	crash := errors.New("simulated crash")

	store.AcceptHooks.BeforeCommit = func() error { return crash }
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); !errors.Is(err, crash) {
		t.Fatalf("before-commit crash = %v", err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.DurablyAccepted != 0 {
		t.Fatalf("pre-commit stats = %#v, %v", stats, err)
	}

	store.AcceptHooks.BeforeCommit = nil
	store.AcceptHooks.AfterCommit = func() error { return crash }
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); !errors.Is(err, crash) {
		t.Fatalf("after-commit crash = %v", err)
	}
	stats, err = store.Stats(context.Background())
	if err != nil || stats.DurablyAccepted != 1 {
		t.Fatalf("post-commit stats = %#v, %v", stats, err)
	}
	store.AcceptHooks.AfterCommit = nil
	result, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now.Add(time.Minute)})
	if err != nil || result.Duplicates != 1 || result.Accepted != 0 {
		t.Fatalf("post-crash replay = %#v, %v", result, err)
	}
}

func TestStoreConcurrentReplayCreatesOneDurableBatch(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricCommandStatusSuccess, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var wait sync.WaitGroup
	results := make(chan AcceptResult, 20)
	errs := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now})
			results <- result
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	accepted := 0
	duplicates := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		accepted += result.Accepted
		duplicates += result.Duplicates
	}
	if accepted != 1 || duplicates != 19 {
		t.Fatalf("accepted=%d duplicates=%d", accepted, duplicates)
	}
}

func TestStoreQuarantinesVolumeAnomaly(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricTurnRecordedClaude, 5)
	result, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{
		Now:              time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		DailyVolumeLimit: 3,
	})
	if err != nil || result.Accepted != 1 || result.Quarantined != 1 {
		t.Fatalf("anomaly accept = %#v, %v", result, err)
	}
	items, err := store.Claim(context.Background(), 10, time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC), time.Minute)
	if err != nil || len(items) != 0 {
		t.Fatalf("quarantined claim = %#v, %v", items, err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Quarantined != 1 {
		t.Fatalf("quarantine stats = %#v, %v", stats, err)
	}
}

func TestStoreDeliveryLifecycleAndStaleClaimRecovery(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Claim(context.Background(), 1, now, time.Minute)
	if err != nil || len(items) != 1 || items[0].Attempts != 1 {
		t.Fatalf("first claim = %#v, %v", items, err)
	}
	items, err = store.Claim(context.Background(), 1, now.Add(2*time.Minute), time.Minute)
	if err != nil || len(items) != 1 || items[0].Attempts != 2 {
		t.Fatalf("stale claim = %#v, %v", items, err)
	}
	if err := store.MarkRetryable(context.Background(), batch.BatchID, now.Add(2*time.Minute), now.Add(5*time.Minute), "network"); err != nil {
		t.Fatal(err)
	}
	items, err = store.Claim(context.Background(), 1, now.Add(4*time.Minute), time.Minute)
	if err != nil || len(items) != 0 {
		t.Fatalf("early retry claim = %#v, %v", items, err)
	}
	items, err = store.Claim(context.Background(), 1, now.Add(5*time.Minute), time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("due retry claim = %#v, %v", items, err)
	}
	if err := store.MarkDelivered(context.Background(), batch.BatchID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Delivered != 1 || stats.DurablyAccepted != 0 || stats.Retryable != 0 {
		t.Fatalf("delivered stats = %#v, %v", stats, err)
	}
	result, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now.Add(6 * time.Minute)})
	if err != nil || result.Duplicates != 1 {
		t.Fatalf("delivered replay = %#v, %v", result, err)
	}
}

func TestStoreDeletionPurgesAndDeniesIdentifier(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	result, err := store.BeginDeletion(context.Background(), batch.AnonymousID, now.Add(time.Hour))
	if err != nil || result.OutboxRows != 1 || result.DailyVolumeRows != 1 {
		t.Fatalf("deletion = %#v, %v", result, err)
	}
	denied, err := store.InstallationDenied(context.Background(), batch.AnonymousID)
	if err != nil || !denied {
		t.Fatalf("denied = %v, %v", denied, err)
	}
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now.Add(2 * time.Hour)}); !errors.Is(err, ErrInstallationDenied) {
		t.Fatalf("accept after deletion = %v", err)
	}
	if err := store.CompleteDeletion(context.Background(), batch.AnonymousID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCannotCompleteDeletionThatWasNeverStarted(t *testing.T) {
	store := testStore(t)
	id, err := telemetry.ParseUUID("167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDeletion(context.Background(), id, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("unstarted deletion was completed")
	}
}

func TestStorePurgesOperationalDataAtDocumentedBounds(t *testing.T) {
	store := testStore(t)
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	delivered := testAggregate(t, "2026-01-01", telemetry.MetricInstallationActive, 1)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{delivered}, AcceptOptions{Now: start}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Claim(context.Background(), 1, start, time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim = %#v, %v", items, err)
	}
	if err := store.MarkDelivered(context.Background(), delivered.BatchID, start); err != nil {
		t.Fatal(err)
	}
	quarantined := testAggregate(t, "2026-01-02", telemetry.MetricTurnRecordedClaude, 2)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{quarantined}, AcceptOptions{Now: start.Add(24 * time.Hour), DailyVolumeLimit: 1}); err != nil {
		t.Fatal(err)
	}

	result, err := store.PurgeExpired(context.Background(), PurgeOptions{Now: start.Add(30 * 24 * time.Hour)})
	if err != nil || result.OutboxRows != 2 || result.VolumeRows != 2 {
		t.Fatalf("purge result = %#v, %v", result, err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Delivered != 0 || stats.Quarantined != 0 {
		t.Fatalf("post-purge stats = %#v, %v", stats, err)
	}
	if _, err := store.BeginDeletion(context.Background(), delivered.AnonymousID, start); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDeletion(context.Background(), delivered.AnonymousID, start); err != nil {
		t.Fatal(err)
	}
	result, err = store.PurgeExpired(context.Background(), PurgeOptions{Now: start.Add(100 * 24 * time.Hour)})
	if err != nil || result.DeletionRows != 2 {
		t.Fatalf("deletion audit purge = %#v, %v", result, err)
	}
	denied, err := store.InstallationDenied(context.Background(), delivered.AnonymousID)
	if err != nil || denied {
		t.Fatalf("expired denylist = %v, %v", denied, err)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testAggregate(t *testing.T, date string, key telemetry.MetricKey, count uint64) telemetry.DailyAggregate {
	t.Helper()
	id, err := telemetry.ParseUUID("167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	if err != nil {
		t.Fatal(err)
	}
	parsedDate, err := time.Parse(time.DateOnly, date)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := telemetry.NewDailyAggregate(id, parsedDate, telemetry.Build{
		Version:       "0.4.2",
		Channel:       telemetry.ChannelStable,
		InstallSource: telemetry.InstallSourceNPM,
		OS:            "linux",
		Arch:          "amd64",
	}, map[telemetry.MetricKey]uint64{key: count})
	if err != nil {
		t.Fatal(err)
	}
	return aggregate
}
