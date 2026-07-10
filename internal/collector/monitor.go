package collector

import (
	"sync"
	"sync/atomic"
	"time"
)

type Monitor struct {
	requests          atomic.Uint64
	acceptedResponses atomic.Uint64
	serverErrors      atomic.Uint64
	rejectedRequests  atomic.Uint64
	schemaRejected    atomic.Uint64
	rateLimited       atomic.Uint64
	killSwitch        atomic.Uint64
	acceptedBatches   atomic.Uint64
	duplicateBatches  atomic.Uint64
	quarantined       atomic.Uint64
	conflicts         atomic.Uint64

	mu                    sync.Mutex
	schemaRejectedVersion map[string]uint64
	lastCanary            CanaryResult
	lastCanaryFailed      bool
}

type MonitorSnapshot struct {
	Requests                uint64
	AcceptedResponses       uint64
	ServerErrors            uint64
	RejectedRequests        uint64
	SchemaRejected          uint64
	SchemaRejectedByVersion map[string]uint64
	RateLimited             uint64
	KillSwitchResponses     uint64
	AcceptedBatches         uint64
	DuplicateBatches        uint64
	QuarantinedBatches      uint64
	BatchConflicts          uint64
	LastCanary              CanaryResult
	LastCanaryFailed        bool
}

func NewMonitor() *Monitor {
	return &Monitor{schemaRejectedVersion: make(map[string]uint64)}
}

func (monitor *Monitor) RecordRequest()     { monitor.requests.Add(1) }
func (monitor *Monitor) RecordServerError() { monitor.serverErrors.Add(1) }
func (monitor *Monitor) RecordRejected()    { monitor.rejectedRequests.Add(1) }
func (monitor *Monitor) RecordRateLimited() { monitor.rateLimited.Add(1) }
func (monitor *Monitor) RecordKillSwitch()  { monitor.killSwitch.Add(1) }
func (monitor *Monitor) RecordConflict()    { monitor.conflicts.Add(1) }

func (monitor *Monitor) RecordSchemaRejected(versions []string) {
	monitor.schemaRejected.Add(1)
	monitor.rejectedRequests.Add(1)
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if len(versions) == 0 {
		monitor.schemaRejectedVersion["unknown"]++
		return
	}
	seen := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		if version == "" {
			version = "unknown"
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		monitor.schemaRejectedVersion[version]++
	}
}

func (monitor *Monitor) RecordAcceptance(result AcceptResult) {
	monitor.acceptedResponses.Add(1)
	monitor.acceptedBatches.Add(uint64(result.Accepted))
	monitor.duplicateBatches.Add(uint64(result.Duplicates))
	monitor.quarantined.Add(uint64(result.Quarantined))
}

func (monitor *Monitor) RecordCanary(result CanaryResult, failed bool) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.lastCanary = result
	monitor.lastCanaryFailed = failed
}

func (monitor *Monitor) Snapshot() MonitorSnapshot {
	monitor.mu.Lock()
	versions := make(map[string]uint64, len(monitor.schemaRejectedVersion))
	for version, count := range monitor.schemaRejectedVersion {
		versions[version] = count
	}
	canary := monitor.lastCanary
	canaryFailed := monitor.lastCanaryFailed
	monitor.mu.Unlock()
	return MonitorSnapshot{
		Requests:                monitor.requests.Load(),
		AcceptedResponses:       monitor.acceptedResponses.Load(),
		ServerErrors:            monitor.serverErrors.Load(),
		RejectedRequests:        monitor.rejectedRequests.Load(),
		SchemaRejected:          monitor.schemaRejected.Load(),
		SchemaRejectedByVersion: versions,
		RateLimited:             monitor.rateLimited.Load(),
		KillSwitchResponses:     monitor.killSwitch.Load(),
		AcceptedBatches:         monitor.acceptedBatches.Load(),
		DuplicateBatches:        monitor.duplicateBatches.Load(),
		QuarantinedBatches:      monitor.quarantined.Load(),
		BatchConflicts:          monitor.conflicts.Load(),
		LastCanary:              canary,
		LastCanaryFailed:        canaryFailed,
	}
}

type Alert struct {
	Code     string
	Severity string
}

type AlertInput struct {
	Now                   time.Time
	Monitor               MonitorSnapshot
	RateMonitor           MonitorSnapshot
	Outbox                OutboxStats
	DailyAccepted         uint64
	PreviousDailyBaseline uint64
}

type operationalRateSample struct {
	at             time.Time
	requests       uint64
	serverErrors   uint64
	schemaRejected uint64
}

type OperationalRateWindow struct {
	last    MonitorSnapshot
	samples []operationalRateSample
}

func (window *OperationalRateWindow) Observe(now time.Time, cumulative MonitorSnapshot) MonitorSnapshot {
	now = now.UTC()
	sample := operationalRateSample{
		at:             now,
		requests:       counterDelta(window.last.Requests, cumulative.Requests),
		serverErrors:   counterDelta(window.last.ServerErrors, cumulative.ServerErrors),
		schemaRejected: counterDelta(window.last.SchemaRejected, cumulative.SchemaRejected),
	}
	window.last = cumulative
	window.samples = append(window.samples, sample)
	cutoff := now.Add(-15 * time.Minute)
	first := 0
	for first < len(window.samples) && window.samples[first].at.Before(cutoff) {
		first++
	}
	window.samples = window.samples[first:]
	var result MonitorSnapshot
	for _, current := range window.samples {
		result.Requests += current.requests
		result.ServerErrors += current.serverErrors
		result.SchemaRejected += current.schemaRejected
	}
	return result
}

func counterDelta(previous, current uint64) uint64 {
	if current < previous {
		return current
	}
	return current - previous
}

func EvaluateAlerts(input AlertInput) []Alert {
	var alerts []Alert
	if input.RateMonitor.Requests >= 100 && float64(input.RateMonitor.ServerErrors)/float64(input.RateMonitor.Requests) > 0.01 {
		alerts = append(alerts, Alert{Code: "collector_5xx_rate", Severity: "critical"})
	}
	if input.RateMonitor.Requests >= 100 && float64(input.RateMonitor.SchemaRejected)/float64(input.RateMonitor.Requests) > 0.02 {
		alerts = append(alerts, Alert{Code: "schema_rejection_rate", Severity: "warning"})
	}
	if input.Monitor.BatchConflicts > 0 {
		alerts = append(alerts, Alert{Code: "batch_id_conflict", Severity: "critical"})
	}
	if !input.Outbox.OldestPending.IsZero() && input.Now.UTC().Sub(input.Outbox.OldestPending) > 15*time.Minute {
		alerts = append(alerts, Alert{Code: "outbox_delivery_slo", Severity: "critical"})
	}
	if input.Monitor.LastCanaryFailed || (!input.Monitor.LastCanary.SentAt.IsZero() && !input.Monitor.LastCanary.Verified) {
		alerts = append(alerts, Alert{Code: "canary_reconciliation", Severity: "critical"})
	}
	if input.PreviousDailyBaseline > 0 && input.DailyAccepted > 5*input.PreviousDailyBaseline {
		alerts = append(alerts, Alert{Code: "accepted_volume_spike", Severity: "warning"})
	}
	return alerts
}
