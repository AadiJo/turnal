package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestDeletionWorkflowRequiresExternalAndDerivedVerification(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeDeletionProvider{remaining: 2}
	workflow := DeletionWorkflow{Store: store, PostHog: provider, Now: func() time.Time { return now }}
	started, err := workflow.Begin(context.Background(), batch.AnonymousID)
	if err != nil || started.Collector.OutboxRows != 1 || !started.EventsQueued {
		t.Fatalf("Begin() = %#v, %v", started, err)
	}
	denied, err := store.InstallationDenied(context.Background(), batch.AnonymousID)
	if err != nil || !denied {
		t.Fatalf("denylist = %v, %v", denied, err)
	}
	if err := workflow.Complete(context.Background(), batch.AnonymousID, false); err == nil {
		t.Fatal("completion without derived-copy verification succeeded")
	}
	if err := workflow.Complete(context.Background(), batch.AnonymousID, true); err == nil {
		t.Fatal("completion while PostHog events remain succeeded")
	}
	provider.remaining = 0
	if err := workflow.Complete(context.Background(), batch.AnonymousID, true); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionWorkflowLeavesDenylistPendingWhenProviderFails(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	workflow := DeletionWorkflow{Store: store, PostHog: &fakeDeletionProvider{err: errors.New("API unavailable")}, Now: func() time.Time { return now }}
	if _, err := workflow.Begin(context.Background(), batch.AnonymousID); err == nil {
		t.Fatal("provider failure was ignored")
	}
	denied, err := store.InstallationDenied(context.Background(), batch.AnonymousID)
	if err != nil || !denied {
		t.Fatalf("denylist after failure = %v, %v", denied, err)
	}
}

type fakeDeletionProvider struct {
	remaining uint64
	err       error
}

func (provider *fakeDeletionProvider) Request(context.Context, telemetry.UUID) (BulkDeletionResponse, error) {
	if provider.err != nil {
		return BulkDeletionResponse{}, provider.err
	}
	return BulkDeletionResponse{EventsQueuedForDeletion: true, RecordingsQueuedForDeletion: true}, nil
}

func (provider *fakeDeletionProvider) CountEvents(context.Context, telemetry.UUID) (uint64, error) {
	return provider.remaining, provider.err
}
