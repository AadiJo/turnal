package telemetry

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDisabledFlushDoesNotRotateAttemptOrUseNetwork(t *testing.T) {
	aggregates, state, build := testAggregateStore(t)
	stateStore := testStateStore(t)
	stateStore.Path = filepath.Join(t.TempDir(), "config", "turnal", "telemetry.json")
	if _, err := stateStore.SetPreference(PreferenceOn); err != nil {
		t.Fatal(err)
	}
	loaded, err := stateStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	state = loaded
	if _, err := aggregates.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
		t.Fatal(err)
	}
	flusher := Flusher{
		Aggregates: aggregates,
		State:      stateStore,
		Sender:     NewSender(build.Version),
		Build:      build,
		LookupEnv:  mapEnv(nil),
	}
	result, err := flusher.Flush(context.Background(), true)
	if err != nil || result.Status != FlushDisabled {
		t.Fatalf("Flush() = %#v, %v", result, err)
	}
	loaded, err = stateStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastFlushAttemptAt != "" {
		t.Fatalf("disabled flush wrote attempt %q", loaded.LastFlushAttemptAt)
	}
	current, err := filepath.Glob(filepath.Join(aggregates.currentDir(), "*.json"))
	if err != nil || len(current) != 1 {
		t.Fatalf("disabled flush rotated current aggregate: %v, %v", current, err)
	}
}

func TestFlushAcceptedRemovesBatchAndMarksSuccess(t *testing.T) {
	flusher, transport := enabledTestFlusher(t, http.StatusAccepted)
	result, err := flusher.Flush(context.Background(), true)
	if err != nil || result.Status != FlushAccepted || result.BatchCount != 1 {
		t.Fatalf("Flush() = %#v, %v", result, err)
	}
	if transport.request == nil {
		t.Fatal("collector was not called")
	}
	batches, err := flusher.Aggregates.ListBatches(0)
	if err != nil || len(batches) != 0 {
		t.Fatalf("accepted batches = %#v, %v", batches, err)
	}
	state, err := flusher.State.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastFlushSuccessAt == "" || state.LastFlushAttemptAt != state.LastFlushSuccessAt {
		t.Fatalf("flush timestamps = %#v", state)
	}
}

func TestFlushRetryAndRejectionLifecycle(t *testing.T) {
	for name, test := range map[string]struct {
		status         int
		wantStatus     FlushStatus
		wantReady      int
		wantQuarantine int
	}{
		"retry":     {status: http.StatusTooManyRequests, wantStatus: FlushRetryable, wantReady: 1},
		"rejection": {status: http.StatusBadRequest, wantStatus: FlushQuarantined, wantQuarantine: 1},
	} {
		t.Run(name, func(t *testing.T) {
			flusher, _ := enabledTestFlusher(t, test.status)
			result, err := flusher.Flush(context.Background(), true)
			if err != nil || result.Status != test.wantStatus {
				t.Fatalf("Flush() = %#v, %v", result, err)
			}
			ready, err := flusher.Aggregates.ListBatches(0)
			if err != nil || len(ready) != test.wantReady {
				t.Fatalf("ready batches = %#v, %v", ready, err)
			}
			quarantined, err := filepath.Glob(filepath.Join(flusher.Aggregates.quarantineDir(), "*.json"))
			if err != nil || len(quarantined) != test.wantQuarantine {
				t.Fatalf("quarantined batches = %v, %v", quarantined, err)
			}
		})
	}
}

func TestDetachedSchedulerDoesNothingWhileNetworkDisabled(t *testing.T) {
	called := false
	scheduler := DetachedScheduler{
		Sender: NewSender("0.4.2"),
		Executable: func() (string, error) {
			called = true
			return "/turnal", nil
		},
		Start: func(*exec.Cmd) error {
			called = true
			return nil
		},
	}
	scheduled, err := scheduler.Schedule()
	if err != nil || scheduled || called {
		t.Fatalf("Schedule() = %v, %v, called=%v", scheduled, err, called)
	}
}

func TestDetachedSchedulerRequiresRolloutEligibleInstallation(t *testing.T) {
	oldRollout := collectorRolloutPercent
	collectorRolloutPercent = "100"
	t.Cleanup(func() { collectorRolloutPercent = oldRollout })
	called := false
	scheduler := DetachedScheduler{
		Sender: Sender{endpoint: CollectorURL, version: "0.4.2"},
		Executable: func() (string, error) {
			called = true
			return "/turnal", nil
		},
	}
	scheduled, err := scheduler.Schedule()
	if err != nil || scheduled || called {
		t.Fatalf("Schedule() = %v, %v, called=%v", scheduled, err, called)
	}
}

func enabledTestFlusher(t *testing.T, status int) (Flusher, *captureTransport) {
	t.Helper()
	oldRollout := collectorRolloutPercent
	collectorRolloutPercent = "100"
	t.Cleanup(func() { collectorRolloutPercent = oldRollout })
	aggregates, _, build := testAggregateStore(t)
	stateStore := testStateStore(t)
	stateStore.Path = filepath.Join(t.TempDir(), "config", "turnal", "telemetry.json")
	state, err := stateStore.SetPreference(PreferenceOn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aggregates.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
		t.Fatal(err)
	}
	transport := &captureTransport{status: status, header: make(http.Header)}
	sender := Sender{
		endpoint: CollectorURL,
		version:  build.Version,
		client:   &http.Client{Transport: transport, Timeout: NetworkTimeout},
	}
	return Flusher{
		Aggregates: aggregates,
		State:      stateStore,
		Sender:     sender,
		Build:      build,
		LookupEnv:  mapEnv(nil),
		Now:        func() time.Time { return time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC) },
	}, transport
}
