package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestWorkerDeliveryRetryAndQuarantineLifecycle(t *testing.T) {
	for name, delivery := range map[string]DeliveryResult{
		"delivered":   {Disposition: DeliveryDelivered, Code: "accepted"},
		"retryable":   {Disposition: DeliveryRetryable, Code: "posthog_retryable"},
		"quarantined": {Disposition: DeliveryRejected, Code: "posthog_rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
			now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
			if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
				t.Fatal(err)
			}
			worker := Worker{Store: store, Delivery: fixedDelivery{result: delivery}, Now: func() time.Time { return now }}
			result, err := worker.ProcessOnce(context.Background())
			if err != nil || result.Claimed != 1 {
				t.Fatalf("ProcessOnce() = %#v, %v", result, err)
			}
			stats, err := store.Stats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch delivery.Disposition {
			case DeliveryDelivered:
				if result.Delivered != 1 || stats.Delivered != 1 {
					t.Fatalf("delivered result=%#v stats=%#v", result, stats)
				}
			case DeliveryRetryable:
				if result.Retryable != 1 || stats.Retryable != 1 {
					t.Fatalf("retry result=%#v stats=%#v", result, stats)
				}
			case DeliveryRejected:
				if result.Quarantined != 1 || stats.Quarantined != 1 {
					t.Fatalf("quarantine result=%#v stats=%#v", result, stats)
				}
			}
		})
	}
}

func TestWorkerTreatsAmbiguousErrorAsRetryable(t *testing.T) {
	store := testStore(t)
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.Accept(context.Background(), []telemetry.DailyAggregate{batch}, AcceptOptions{Now: now}); err != nil {
		t.Fatal(err)
	}
	worker := Worker{
		Store:    store,
		Delivery: fixedDelivery{err: errors.New("connection reset after send")},
		Now:      func() time.Time { return now },
	}
	result, err := worker.ProcessOnce(context.Background())
	if err != nil || result.Retryable != 1 {
		t.Fatalf("ProcessOnce() = %#v, %v", result, err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Retryable != 1 {
		t.Fatalf("stats = %#v, %v", stats, err)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if retryDelay(1) != time.Minute || retryDelay(2) != 2*time.Minute || retryDelay(100) != MaxRetryDelay {
		t.Fatalf("retry delays = %s %s %s", retryDelay(1), retryDelay(2), retryDelay(100))
	}
}

type fixedDelivery struct {
	result DeliveryResult
	err    error
}

func (delivery fixedDelivery) Deliver(context.Context, telemetry.DailyAggregate) (DeliveryResult, error) {
	return delivery.result, delivery.err
}
