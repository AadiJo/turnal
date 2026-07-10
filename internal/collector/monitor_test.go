package collector

import (
	"testing"
	"time"
)

func TestMonitorTracksOnlyAggregateOperationalCounters(t *testing.T) {
	monitor := NewMonitor()
	for range 100 {
		monitor.RecordRequest()
	}
	monitor.RecordServerError()
	monitor.RecordServerError()
	monitor.RecordSchemaRejected([]string{"0.4.2"})
	monitor.RecordSchemaRejected([]string{"0.4.2"})
	monitor.RecordSchemaRejected([]string{"0.4.2"})
	monitor.RecordAcceptance(AcceptResult{Accepted: 4, Duplicates: 2, Quarantined: 1})
	snapshot := monitor.Snapshot()
	if snapshot.Requests != 100 || snapshot.ServerErrors != 2 || snapshot.AcceptedBatches != 4 || snapshot.DuplicateBatches != 2 || snapshot.SchemaRejectedByVersion["0.4.2"] != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestAlertEvaluatorUsesPublishedThresholds(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	alerts := EvaluateAlerts(AlertInput{
		Now: now,
		Monitor: MonitorSnapshot{
			Requests:         100,
			ServerErrors:     2,
			SchemaRejected:   3,
			BatchConflicts:   1,
			LastCanaryFailed: true,
		},
		Outbox:                OutboxStats{OldestPending: now.Add(-16 * time.Minute)},
		DailyAccepted:         51,
		PreviousDailyBaseline: 10,
	})
	want := map[string]bool{
		"collector_5xx_rate":    true,
		"schema_rejection_rate": true,
		"batch_id_conflict":     true,
		"outbox_delivery_slo":   true,
		"canary_reconciliation": true,
		"accepted_volume_spike": true,
	}
	for _, alert := range alerts {
		delete(want, alert.Code)
	}
	if len(want) != 0 {
		t.Fatalf("missing alerts: %#v (got %#v)", want, alerts)
	}
}
