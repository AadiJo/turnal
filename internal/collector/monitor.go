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
	Outbox                OutboxStats
	DailyAccepted         uint64
	PreviousDailyBaseline uint64
}

// DailyAcceptanceWindow derives the current UTC-day volume and the immediately
// preceding complete UTC-day baseline from a process-lifetime cumulative
// counter. It deliberately keeps no installation-level state.
type DailyAcceptanceWindow struct {
	day          time.Time
	dayStart     uint64
	lastObserved uint64
	previous     uint64
}

func (window *DailyAcceptanceWindow) Observe(now time.Time, cumulative uint64) (current, previous uint64) {
	day := now.UTC().Truncate(24 * time.Hour)
	if window.day.IsZero() {
		window.day = day
	}
	if !day.Equal(window.day) {
		if day.Equal(window.day.AddDate(0, 0, 1)) && window.lastObserved >= window.dayStart {
			window.previous = window.lastObserved - window.dayStart
		} else {
			window.previous = 0
		}
		window.day = day
		window.dayStart = window.lastObserved
	}
	if cumulative < window.lastObserved {
		// A reset can only happen if the caller swaps monitors. Start a fresh
		// window instead of underflowing or reporting a false spike.
		window.dayStart = 0
		window.previous = 0
	}
	window.lastObserved = cumulative
	return cumulative - window.dayStart, window.previous
}

func EvaluateAlerts(input AlertInput) []Alert {
	var alerts []Alert
	if input.Monitor.Requests >= 100 && float64(input.Monitor.ServerErrors)/float64(input.Monitor.Requests) > 0.01 {
		alerts = append(alerts, Alert{Code: "collector_5xx_rate", Severity: "critical"})
	}
	if input.Monitor.Requests >= 100 && float64(input.Monitor.SchemaRejected)/float64(input.Monitor.Requests) > 0.02 {
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
