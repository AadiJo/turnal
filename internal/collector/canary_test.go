package collector

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCanaryReconcilerRequiresExactlyOneQueryableEvent(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, visible := range []uint64{0, 1, 2} {
		probe := &fakeCanaryProbe{visible: visible}
		reconciler := CanaryReconciler{
			Sender: probe,
			Query:  probe,
			Now:    func() time.Time { return now },
			Wait:   func(context.Context, time.Duration) error { return nil },
		}
		result, err := reconciler.RunOnce(context.Background())
		if visible == 1 {
			if err != nil || !result.Verified {
				t.Fatalf("visible=%d result=%#v err=%v", visible, result, err)
			}
		} else if err == nil || result.Verified {
			t.Fatalf("visible=%d result=%#v err=%v", visible, result, err)
		}
	}
}

func TestCanaryReconcilerPropagatesAmbiguousDelivery(t *testing.T) {
	probe := &fakeCanaryProbe{err: errors.New("ambiguous delivery")}
	result, err := (CanaryReconciler{Sender: probe, Query: probe}).RunOnce(context.Background())
	if err == nil || result.Verified {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type fakeCanaryProbe struct {
	visible uint64
	err     error
}

func (probe *fakeCanaryProbe) DeliverCanary(context.Context, time.Time) (string, DeliveryResult, error) {
	return "collector-canary:167e8e5d-84fc-46bd-a39c-b67d47658f8e", DeliveryResult{Disposition: DeliveryDelivered}, probe.err
}

func (probe *fakeCanaryProbe) CountCanary(context.Context, string) (uint64, error) {
	return probe.visible, probe.err
}
