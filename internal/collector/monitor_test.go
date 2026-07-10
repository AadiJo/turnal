package collector

import (
	"fmt"
	"testing"
	"time"
)

func TestSchemaRejectionVersionCountersAreBounded(t *testing.T) {
	monitor := NewMonitor()
	for index := 0; index < 256; index++ {
		monitor.RecordSchemaRejected([]string{fmt.Sprintf("1.0.%d", index)})
	}
	snapshot := monitor.Snapshot()
	if len(snapshot.SchemaRejectedByVersion) > maxSchemaVersionCounters+1 || snapshot.SchemaRejectedByVersion["other"] == 0 {
		t.Fatalf("version counters are not bounded: %d, %#v", len(snapshot.SchemaRejectedByVersion), snapshot.SchemaRejectedByVersion)
	}
}

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
			BatchConflicts:   1,
			LastCanaryFailed: true,
		},
		RateMonitor:           MonitorSnapshot{Requests: 100, ServerErrors: 2, SchemaRejected: 3},
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

func TestOperationalRateWindowUsesOnlyRollingFifteenMinutes(t *testing.T) {
	var window OperationalRateWindow
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	first := window.Observe(start, MonitorSnapshot{Requests: 60, ServerErrors: 2, SchemaRejected: 1})
	if first.Requests != 60 || first.ServerErrors != 2 {
		t.Fatalf("first rate window = %#v", first)
	}
	second := window.Observe(start.Add(10*time.Minute), MonitorSnapshot{Requests: 120, ServerErrors: 2, SchemaRejected: 4})
	if second.Requests != 120 || second.ServerErrors != 2 || second.SchemaRejected != 4 {
		t.Fatalf("combined rate window = %#v", second)
	}
	third := window.Observe(start.Add(16*time.Minute), MonitorSnapshot{Requests: 180, ServerErrors: 2, SchemaRejected: 4})
	if third.Requests != 120 || third.ServerErrors != 0 || third.SchemaRejected != 3 {
		t.Fatalf("expired rate window = %#v", third)
	}
	reset := window.Observe(start.Add(17*time.Minute), MonitorSnapshot{Requests: 3, ServerErrors: 1})
	if reset.Requests != 123 || reset.ServerErrors != 1 {
		t.Fatalf("counter reset window = %#v", reset)
	}
}
