package telemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
)

const (
	DefaultMaxAge        = 14 * 24 * time.Hour
	DefaultMaxBatchFiles = 128
	DefaultMaxLocalBytes = 512 * 1024
	MaxAggregateFileSize = 64 * 1024
	aggregateLockTimeout = 15 * time.Millisecond
)

var ErrMetricDropped = errors.New("telemetry metric dropped")

type RecordOptions struct {
	State     State
	Build     Build
	LookupEnv func(string) (string, bool)
}

type RecordResult struct {
	Recorded bool
	Policy   PolicyResult
}

type QueuedBatch struct {
	Path      string
	Aggregate DailyAggregate
	Size      int64
}

type QueueSnapshot struct {
	Current     []DailyAggregate `json:"current"`
	Batches     []DailyAggregate `json:"batches"`
	Quarantined []DailyAggregate `json:"quarantined"`
	Bytes       int64            `json:"bytes"`
}

func (snapshot QueueSnapshot) FileCount() int {
	return len(snapshot.Current) + len(snapshot.Batches) + len(snapshot.Quarantined)
}

type AggregateStore struct {
	CacheDir      string
	Now           func() time.Time
	LockTimeout   time.Duration
	MaxAge        time.Duration
	MaxBatchFiles int
	MaxLocalBytes int64
}

func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w", err)
	}
	return filepath.Join(dir, "turnal", "telemetry"), nil
}

func DefaultAggregateStore() (AggregateStore, error) {
	dir, err := DefaultCacheDir()
	if err != nil {
		return AggregateStore{}, err
	}
	return AggregateStore{CacheDir: dir}, nil
}

func (store AggregateStore) Record(options RecordOptions, key MetricKey) (RecordResult, error) {
	return store.RecordMany(options, key)
}

func (store AggregateStore) RecordMany(options RecordOptions, keys ...MetricKey) (RecordResult, error) {
	policy := EvaluatePolicy(PolicyOptions{
		Preference: options.State.Preference,
		Build:      options.Build,
		LookupEnv:  options.LookupEnv,
	})
	result := RecordResult{Policy: policy}
	if !policy.Enabled {
		return result, nil
	}
	if err := options.State.Validate(); err != nil {
		return result, fmt.Errorf("validate telemetry state: %w", err)
	}
	if options.State.AnonymousID == nil {
		return result, errors.New("enabled telemetry state has no installation ID")
	}
	if len(keys) == 0 {
		return result, errors.New("at least one telemetry metric is required")
	}
	increments := make(map[MetricKey]uint64, len(keys))
	for _, key := range keys {
		if !key.Valid() {
			return result, fmt.Errorf("invalid telemetry metric %d", key)
		}
		increments[key]++
	}

	err := store.withLock(func() error {
		now := store.now()
		if err := store.rotateOtherCurrentUnlocked(*options.State.AnonymousID, now, options.Build); err != nil {
			return err
		}
		path, err := store.currentPath(*options.State.AnonymousID, now, options.Build)
		if err != nil {
			return err
		}
		aggregate, created, err := store.loadCurrentOrNewUnlocked(path, *options.State.AnonymousID, now, options.Build, increments)
		if err != nil {
			return err
		}
		if !created {
			positions := make(map[MetricKey]int, len(aggregate.Metrics))
			for index, metric := range aggregate.Metrics {
				positions[metric.Key] = index
			}
			for key, increment := range increments {
				index, found := positions[key]
				if found {
					if aggregate.Metrics[index].Count > MaxMetricCount-increment {
						return fmt.Errorf("%w: metric %s reached its daily limit", ErrMetricDropped, key)
					}
					aggregate.Metrics[index].Count += increment
					continue
				}
				aggregate.Metrics = append(aggregate.Metrics, MetricCount{Key: key, Count: increment})
			}
			sort.Slice(aggregate.Metrics, func(i, j int) bool {
				return aggregate.Metrics[i].Key.String() < aggregate.Metrics[j].Key.String()
			})
		}
		data, err := EncodeDailyAggregate(aggregate)
		if err != nil {
			return err
		}
		if err := atomicWriteFileRelaxed(path, data, 0o600); err != nil {
			return err
		}
		if err := store.enforceLimitsUnlocked(now); err != nil {
			return err
		}
		result.Recorded = true
		return nil
	})
	if errors.Is(err, filelock.ErrBusy) {
		return result, fmt.Errorf("%w: aggregate lock busy", ErrMetricDropped)
	}
	return result, err
}

func (store AggregateStore) Rotate(options RecordOptions) ([]QueuedBatch, error) {
	policy := EvaluatePolicy(PolicyOptions{
		Preference: options.State.Preference,
		Build:      options.Build,
		LookupEnv:  options.LookupEnv,
	})
	if !policy.Enabled {
		return nil, nil
	}
	if err := options.State.Validate(); err != nil {
		return nil, err
	}
	if options.State.AnonymousID == nil {
		return nil, errors.New("enabled telemetry state has no installation ID")
	}
	var batches []QueuedBatch
	err := store.withLock(func() error {
		if err := store.rotateAllCurrentUnlocked(*options.State.AnonymousID); err != nil {
			return err
		}
		if err := store.enforceLimitsUnlocked(store.now()); err != nil {
			return err
		}
		var err error
		batches, err = store.listBatchesUnlocked(14)
		return err
	})
	if errors.Is(err, filelock.ErrBusy) {
		return nil, fmt.Errorf("%w: aggregate lock busy", ErrMetricDropped)
	}
	return batches, err
}

func (store AggregateStore) ListBatches(limit int) ([]QueuedBatch, error) {
	var batches []QueuedBatch
	err := store.withLock(func() error {
		var err error
		batches, err = store.listBatchesUnlocked(limit)
		return err
	})
	return batches, err
}

func (store AggregateStore) Inspect() (QueueSnapshot, error) {
	var snapshot QueueSnapshot
	info, err := os.Lstat(store.CacheDir)
	if os.IsNotExist(err) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, fmt.Errorf("inspect telemetry cache: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return snapshot, fmt.Errorf("telemetry cache is not a directory: %s", store.CacheDir)
	}
	err = store.withLock(func() error {
		var err error
		snapshot.Current, err = store.inspectDirectoryUnlocked(store.currentDir(), false)
		if err != nil {
			return err
		}
		snapshot.Batches, err = store.inspectDirectoryUnlocked(store.batchesDir(), true)
		if err != nil {
			return err
		}
		snapshot.Quarantined, err = store.inspectDirectoryUnlocked(store.quarantineDir(), true)
		if err != nil {
			return err
		}
		for _, dir := range []string{store.currentDir(), store.batchesDir(), store.quarantineDir()} {
			paths, globErr := filepath.Glob(filepath.Join(dir, "*.json"))
			if globErr != nil {
				return globErr
			}
			for _, path := range paths {
				info, statErr := os.Stat(path)
				if statErr == nil {
					snapshot.Bytes += info.Size()
				}
			}
		}
		return nil
	})
	return snapshot, err
}

func (store AggregateStore) RemoveBatch(batchID UUID) error {
	if !batchID.Valid() {
		return errors.New("invalid telemetry batch ID")
	}
	return store.withLock(func() error {
		path := filepath.Join(store.batchesDir(), batchID.String()+".json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove telemetry batch: %w", err)
		}
		return nil
	})
}

func (store AggregateStore) QuarantineBatch(batchID UUID) error {
	if !batchID.Valid() {
		return errors.New("invalid telemetry batch ID")
	}
	return store.withLock(func() error {
		source := filepath.Join(store.batchesDir(), batchID.String()+".json")
		if _, err := os.Lstat(source); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := secureDirectory(store.quarantineDir()); err != nil {
			return err
		}
		destination := filepath.Join(store.quarantineDir(), batchID.String()+".json")
		if err := replaceFile(source, destination); err != nil {
			return fmt.Errorf("quarantine telemetry batch: %w", err)
		}
		return store.enforceLimitsUnlocked(store.now())
	})
}

func (store AggregateStore) DeleteAll() error {
	if strings.TrimSpace(store.CacheDir) == "" {
		return errors.New("telemetry cache directory is required")
	}
	info, err := os.Lstat(store.CacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse telemetry cache symlink %s", store.CacheDir)
	}
	return store.withLock(func() error {
		for _, dir := range []string{store.currentDir(), store.batchesDir(), store.quarantineDir()} {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("delete telemetry queue: %w", err)
			}
		}
		return nil
	})
}

func (store AggregateStore) withLock(action func() error) error {
	if strings.TrimSpace(store.CacheDir) == "" {
		return errors.New("telemetry cache directory is required")
	}
	if err := secureDirectory(store.CacheDir); err != nil {
		return err
	}
	for _, dir := range []string{store.currentDir(), store.batchesDir(), store.quarantineDir()} {
		if err := secureDirectory(dir); err != nil {
			return err
		}
	}
	timeout := store.LockTimeout
	if timeout == 0 {
		timeout = aggregateLockTimeout
	}
	lock, err := filelock.AcquireQuiet(filepath.Join(store.CacheDir, ".lock"), timeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	return action()
}

func (store AggregateStore) loadCurrentOrNewUnlocked(path string, id UUID, now time.Time, build Build, counts map[MetricKey]uint64) (DailyAggregate, bool, error) {
	data, err := readRegularFile(path, MaxAggregateFileSize)
	if err == nil {
		aggregate, decodeErr := DecodeDailyAggregate(data)
		if decodeErr == nil && aggregate.AnonymousID == id && aggregate.Date == now.UTC().Format(time.DateOnly) && aggregate.Build == build {
			return aggregate, false, nil
		}
		_ = os.Remove(path)
	} else if !os.IsNotExist(err) {
		_ = os.Remove(path)
	}
	aggregate, err := NewDailyAggregate(id, now, build, counts)
	return aggregate, true, err
}

func (store AggregateStore) rotateOtherCurrentUnlocked(id UUID, now time.Time, build Build) error {
	target, err := store.currentPath(id, now, build)
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(store.currentDir(), "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == target {
			continue
		}
		data, err := readRegularFile(path, MaxAggregateFileSize)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		aggregate, err := DecodeDailyAggregate(data)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		if aggregate.AnonymousID != id {
			_ = os.Remove(path)
			continue
		}
		date, _ := time.Parse(time.DateOnly, aggregate.Date)
		if date.After(midnightUTC(now)) {
			_ = os.Remove(path)
			continue
		}
		if err := store.rotateFileUnlocked(path, aggregate, data); err != nil {
			return err
		}
	}
	return nil
}

func (store AggregateStore) rotateAllCurrentUnlocked(id UUID) error {
	paths, err := filepath.Glob(filepath.Join(store.currentDir(), "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := readRegularFile(path, MaxAggregateFileSize)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		aggregate, err := DecodeDailyAggregate(data)
		if err != nil || aggregate.AnonymousID != id {
			_ = os.Remove(path)
			continue
		}
		if err := store.rotateFileUnlocked(path, aggregate, data); err != nil {
			return err
		}
	}
	return nil
}

func (store AggregateStore) rotateFileUnlocked(currentPath string, aggregate DailyAggregate, canonical []byte) error {
	if err := secureDirectory(store.batchesDir()); err != nil {
		return err
	}
	batchPath := filepath.Join(store.batchesDir(), aggregate.BatchID.String()+".json")
	existing, err := readRegularFile(batchPath, MaxAggregateFileSize)
	if err == nil {
		if !bytes.Equal(existing, canonical) {
			return fmt.Errorf("batch ID collision for %s", aggregate.BatchID)
		}
		return removeCurrent(currentPath)
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := atomicWriteFile(batchPath, canonical, 0o600); err != nil {
		return err
	}
	return removeCurrent(currentPath)
}

func removeCurrent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rotated telemetry aggregate: %w", err)
	}
	return nil
}

func (store AggregateStore) listBatchesUnlocked(limit int) ([]QueuedBatch, error) {
	paths, err := filepath.Glob(filepath.Join(store.batchesDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	batches := make([]QueuedBatch, 0, len(paths))
	for _, path := range paths {
		data, err := readRegularFile(path, MaxAggregateFileSize)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		aggregate, err := DecodeDailyAggregate(data)
		if err != nil || filepath.Base(path) != aggregate.BatchID.String()+".json" {
			_ = os.Remove(path)
			continue
		}
		batches = append(batches, QueuedBatch{Path: path, Aggregate: aggregate, Size: int64(len(data))})
	}
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].Aggregate.Date == batches[j].Aggregate.Date {
			return batches[i].Aggregate.BatchID.String() < batches[j].Aggregate.BatchID.String()
		}
		return batches[i].Aggregate.Date < batches[j].Aggregate.Date
	})
	if limit > 0 && len(batches) > limit {
		batches = batches[:limit]
	}
	return batches, nil
}

func (store AggregateStore) inspectDirectoryUnlocked(dir string, requireBatchName bool) ([]DailyAggregate, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	aggregates := make([]DailyAggregate, 0, len(paths))
	for _, path := range paths {
		data, err := readRegularFile(path, MaxAggregateFileSize)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		aggregate, err := DecodeDailyAggregate(data)
		if err != nil || (requireBatchName && filepath.Base(path) != aggregate.BatchID.String()+".json") {
			_ = os.Remove(path)
			continue
		}
		aggregates = append(aggregates, aggregate)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		if aggregates[i].Date == aggregates[j].Date {
			return aggregates[i].BatchID.String() < aggregates[j].BatchID.String()
		}
		return aggregates[i].Date < aggregates[j].Date
	})
	return aggregates, nil
}

type localFile struct {
	path string
	date time.Time
	size int64
	kind string
}

func (store AggregateStore) enforceLimitsUnlocked(now time.Time) error {
	files, err := store.localFilesUnlocked()
	if err != nil {
		return err
	}
	maxAge := store.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	cutoff := midnightUTC(now).Add(-maxAge)
	today := midnightUTC(now)
	kept := files[:0]
	for _, file := range files {
		if file.date.Before(cutoff) || file.date.After(today) {
			_ = os.Remove(file.path)
			continue
		}
		kept = append(kept, file)
	}
	files = kept

	maxBatches := store.MaxBatchFiles
	if maxBatches <= 0 {
		maxBatches = DefaultMaxBatchFiles
	}
	batchCount := 0
	for _, file := range files {
		if file.kind == "batch" || file.kind == "quarantine" {
			batchCount++
		}
	}
	for index := range files {
		if batchCount <= maxBatches {
			break
		}
		if files[index].kind != "batch" && files[index].kind != "quarantine" {
			continue
		}
		_ = os.Remove(files[index].path)
		files[index].size = 0
		batchCount--
	}

	maxBytes := store.MaxLocalBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLocalBytes
	}
	total := int64(0)
	for _, file := range files {
		total += file.size
	}
	for index := range files {
		if total <= maxBytes {
			break
		}
		if files[index].size == 0 {
			continue
		}
		_ = os.Remove(files[index].path)
		total -= files[index].size
		files[index].size = 0
	}
	return nil
}

func (store AggregateStore) localFilesUnlocked() ([]localFile, error) {
	var files []localFile
	for kind, pattern := range map[string]string{
		"current":    filepath.Join(store.currentDir(), "*.json"),
		"batch":      filepath.Join(store.batchesDir(), "*.json"),
		"quarantine": filepath.Join(store.quarantineDir(), "*.json"),
	} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			data, err := readRegularFile(path, MaxAggregateFileSize)
			if err != nil {
				_ = os.Remove(path)
				continue
			}
			aggregate, err := DecodeDailyAggregate(data)
			if err != nil {
				_ = os.Remove(path)
				continue
			}
			date, _ := time.Parse(time.DateOnly, aggregate.Date)
			files = append(files, localFile{path: path, date: date, size: int64(len(data)), kind: kind})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].date.Equal(files[j].date) {
			return files[i].path < files[j].path
		}
		return files[i].date.Before(files[j].date)
	})
	return files, nil
}

func (store AggregateStore) currentPath(id UUID, now time.Time, build Build) (string, error) {
	if !id.Valid() {
		return "", errors.New("invalid telemetry installation ID")
	}
	if err := build.Validate(); err != nil {
		return "", err
	}
	identity := id.String() + "\x00" + build.Version + "\x00" + string(build.Channel) + "\x00" +
		string(build.InstallSource) + "\x00" + build.OS + "\x00" + build.Arch
	digest := sha256.Sum256([]byte(identity))
	name := now.UTC().Format(time.DateOnly) + "-" + hex.EncodeToString(digest[:8]) + ".json"
	return filepath.Join(store.currentDir(), name), nil
}

func (store AggregateStore) currentDir() string {
	return filepath.Join(store.CacheDir, "current")
}

func (store AggregateStore) batchesDir() string {
	return filepath.Join(store.CacheDir, "batches")
}

func (store AggregateStore) quarantineDir() string {
	return filepath.Join(store.CacheDir, "quarantine")
}

func (store AggregateStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func midnightUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func readRegularFile(path string, maxSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("telemetry path is not a regular file: %s", path)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("telemetry file exceeds %d bytes", maxSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("telemetry file exceeds %d bytes", maxSize)
	}
	return data, nil
}
