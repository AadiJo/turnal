package telemetry

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
)

func BenchmarkAggregateRecord(b *testing.B) {
	id, err := ParseUUID("167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	if err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := AggregateStore{CacheDir: filepath.Join(b.TempDir(), "telemetry"), Now: func() time.Time { return now }}
	state := State{
		Version:     StateVersion,
		Preference:  PreferenceOn,
		AnonymousID: &id,
		CreatedAt:   now.Format(time.RFC3339),
		UpdatedAt:   now.Format(time.RFC3339),
	}
	build := supportedTestBuild()
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for range b.N {
		started := time.Now()
		if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
			b.Fatal(err)
		}
		durations = append(durations, time.Since(started))
	}
	b.StopTimer()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if len(durations) > 0 {
		p95 := durations[(len(durations)-1)*95/100]
		b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns")
	}
}

func TestAggregateDoesNotWriteBeforeConsentOrUnderOverrides(t *testing.T) {
	store, state, build := testAggregateStore(t)
	for _, test := range []struct {
		name       string
		preference Preference
		env        map[string]string
		build      Build
	}{
		{name: "unset", preference: PreferenceUnset, build: build},
		{name: "off", preference: PreferenceOff, build: build},
		{name: "environment", preference: PreferenceOn, env: map[string]string{"DO_NOT_TRACK": "1"}, build: build},
		{name: "CI", preference: PreferenceOn, env: map[string]string{"CI": "true"}, build: build},
		{name: "development", preference: PreferenceOn, build: Build{Version: "0.0.0", Channel: "dev", InstallSource: InstallSourceSource, OS: "linux", Arch: "amd64"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state.Preference = test.preference
			result, err := store.Record(RecordOptions{State: state, Build: test.build, LookupEnv: mapEnv(test.env)}, MetricInstallationActive)
			if err != nil {
				t.Fatal(err)
			}
			if result.Recorded || result.Policy.Enabled {
				t.Fatalf("record result = %#v", result)
			}
		})
	}
	if _, err := os.Stat(store.CacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache exists before consent: %v", err)
	}
}

func TestInspectEmptyQueueDoesNotCreateCache(t *testing.T) {
	store, _, _ := testAggregateStore(t)
	snapshot, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FileCount() != 0 || snapshot.Bytes != 0 {
		t.Fatalf("empty snapshot = %#v", snapshot)
	}
	if _, err := os.Lstat(store.CacheDir); !os.IsNotExist(err) {
		t.Fatalf("inspection created telemetry cache: %v", err)
	}
}

func TestAggregateRecordsCanonicalDailyCounters(t *testing.T) {
	store, state, build := testAggregateStore(t)
	for _, key := range []MetricKey{MetricTurnRecordedCodex, MetricCommandStatusSuccess, MetricTurnRecordedCodex} {
		result, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, key)
		if err != nil || !result.Recorded {
			t.Fatalf("Record(%s) = %#v, %v", key, result, err)
		}
	}
	paths, err := filepath.Glob(filepath.Join(store.currentDir(), "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("current files = %v, %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := DecodeDailyAggregate(data)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Date != "2026-07-11" || aggregate.AnonymousID != *state.AnonymousID || aggregate.Build != build {
		t.Fatalf("aggregate identity = %#v", aggregate)
	}
	if len(aggregate.Metrics) != 2 || aggregate.Metrics[0] != (MetricCount{Key: MetricCommandStatusSuccess, Count: 1}) || aggregate.Metrics[1] != (MetricCount{Key: MetricTurnRecordedCodex, Count: 2}) {
		t.Fatalf("aggregate metrics = %#v", aggregate.Metrics)
	}
	assertMode(t, store.CacheDir, 0o700)
	assertMode(t, paths[0], 0o600)
}

func TestAggregateRecordManyUsesOneCanonicalUpdate(t *testing.T) {
	store, state, build := testAggregateStore(t)
	result, err := store.RecordMany(
		RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)},
		MetricInstallationActive,
		MetricCommandStatusSuccess,
		MetricCommandStatusSuccess,
	)
	if err != nil || !result.Recorded {
		t.Fatalf("RecordMany() = %#v, %v", result, err)
	}
	snapshot, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Current) != 1 || snapshot.FileCount() != 1 || snapshot.Bytes == 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	want := []MetricCount{
		{Key: MetricCommandStatusSuccess, Count: 2},
		{Key: MetricInstallationActive, Count: 1},
	}
	if len(snapshot.Current[0].Metrics) != len(want) {
		t.Fatalf("metrics = %#v", snapshot.Current[0].Metrics)
	}
	for index := range want {
		if snapshot.Current[0].Metrics[index] != want[index] {
			t.Fatalf("metrics = %#v, want %#v", snapshot.Current[0].Metrics, want)
		}
	}
}

func TestAggregateConcurrentWritersDoNotLoseCounts(t *testing.T) {
	store, state, build := testAggregateStore(t)
	store.LockTimeout = 5 * time.Second
	const writers = 40
	var wait sync.WaitGroup
	errs := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricTurnRecordedClaude)
			if err == nil && !result.Recorded {
				err = errors.New("metric was not recorded")
			}
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	batches, err := store.Rotate(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)})
	if err != nil || len(batches) != 1 {
		t.Fatalf("Rotate() = %#v, %v", batches, err)
	}
	if len(batches[0].Aggregate.Metrics) != 1 || batches[0].Aggregate.Metrics[0].Count != writers {
		t.Fatalf("concurrent count = %#v", batches[0].Aggregate.Metrics)
	}
}

func TestAggregateContentionDropsMetricWithinBudget(t *testing.T) {
	store, state, build := testAggregateStore(t)
	if err := os.MkdirAll(store.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Acquire(filepath.Join(store.CacheDir, ".lock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	started := time.Now()
	result, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrMetricDropped) || result.Recorded {
		t.Fatalf("Record() = %#v, %v", result, err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("contention took %s, want under 100ms", elapsed)
	}
}

func TestRotationIsIdempotentAcrossCrashReplay(t *testing.T) {
	store, state, build := testAggregateStore(t)
	if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
		t.Fatal(err)
	}
	current, err := filepath.Glob(filepath.Join(store.currentDir(), "*.json"))
	if err != nil || len(current) != 1 {
		t.Fatalf("current = %v, %v", current, err)
	}
	currentData, err := os.ReadFile(current[0])
	if err != nil {
		t.Fatal(err)
	}
	batches, err := store.Rotate(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)})
	if err != nil || len(batches) != 1 {
		t.Fatalf("first Rotate() = %#v, %v", batches, err)
	}
	batchID := batches[0].Aggregate.BatchID
	if err := os.MkdirAll(store.currentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current[0], currentData, 0o600); err != nil {
		t.Fatal(err)
	}
	batches, err = store.Rotate(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)})
	if err != nil || len(batches) != 1 || batches[0].Aggregate.BatchID != batchID {
		t.Fatalf("replayed Rotate() = %#v, %v", batches, err)
	}
}

func TestMalformedCurrentFileCannotBlockRecording(t *testing.T) {
	store, state, build := testAggregateStore(t)
	path, err := store.currentPath(*state.AnonymousID, store.now(), build)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"prompt":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := DecodeDailyAggregate(data)
	if err != nil || len(aggregate.Metrics) != 1 || aggregate.Metrics[0].Key != MetricInstallationActive {
		t.Fatalf("recovered aggregate = %#v, %v", aggregate, err)
	}
}

func TestAggregateEnforcesBatchAndAgeLimits(t *testing.T) {
	store, state, build := testAggregateStore(t)
	store.MaxBatchFiles = 2
	store.MaxLocalBytes = 1 << 20
	currentDay := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	clock := currentDay.Add(-15 * 24 * time.Hour)
	store.Now = func() time.Time { return clock }
	for index := 0; index < 4; index++ {
		if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Rotate(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(5 * 24 * time.Hour)
	}
	clock = currentDay
	batches, err := store.ListBatches(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches after limits = %d, want 2", len(batches))
	}
	for _, batch := range batches {
		if batch.Aggregate.Date < "2026-06-26" {
			t.Fatalf("expired batch retained: %s", batch.Aggregate.Date)
		}
	}
}

func TestAggregateEnforcesByteLimitOldestFirst(t *testing.T) {
	store, state, build := testAggregateStore(t)
	store.MaxLocalBytes = 1 << 20
	clock := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return clock }
	for index := 0; index < 3; index++ {
		if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Rotate(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(24 * time.Hour)
	}
	batches, err := store.ListBatches(0)
	if err != nil || len(batches) != 3 {
		t.Fatalf("initial batches = %#v, %v", batches, err)
	}
	newest := batches[len(batches)-1]
	store.MaxLocalBytes = newest.Size + 1
	if err := store.withLock(func() error { return store.enforceLimitsUnlocked(clock.Add(-24 * time.Hour)) }); err != nil {
		t.Fatal(err)
	}
	batches, err = store.ListBatches(0)
	if err != nil || len(batches) != 1 || batches[0].Aggregate.BatchID != newest.Aggregate.BatchID {
		t.Fatalf("byte-limited batches = %#v, %v", batches, err)
	}
}

func TestDeleteAllRefusesSymlinkAndRemovesQueue(t *testing.T) {
	store, state, build := testAggregateStore(t)
	if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if current, _ := filepath.Glob(filepath.Join(store.currentDir(), "*.json")); len(current) != 0 {
		t.Fatalf("current telemetry still exists: %v", current)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.RemoveAll(store.CacheDir); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, store.CacheDir); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAll(); err == nil {
		t.Fatal("cache symlink was accepted")
	}
}

func TestAggregateRefusesSymlinkedSubdirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows hosts")
	}
	store, state, build := testAggregateStore(t)
	if err := secureDirectory(store.CacheDir); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, store.currentDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(RecordOptions{State: state, Build: build, LookupEnv: mapEnv(nil)}, MetricInstallationActive); err == nil {
		t.Fatal("symlinked current directory was accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target received telemetry: %v, %v", entries, err)
	}
}

func testAggregateStore(t *testing.T) (AggregateStore, State, Build) {
	t.Helper()
	now := time.Date(2026, 7, 10, 23, 30, 0, 0, time.FixedZone("test", -5*60*60))
	id := mustUUID(t, "167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	state := State{
		Version:     StateVersion,
		Preference:  PreferenceOn,
		AnonymousID: &id,
		CreatedAt:   "2026-07-10T12:00:00Z",
		UpdatedAt:   "2026-07-10T12:00:00Z",
	}
	return AggregateStore{
		CacheDir: filepath.Join(t.TempDir(), "cache", "turnal", "telemetry"),
		Now:      func() time.Time { return now },
	}, state, supportedTestBuild()
}
