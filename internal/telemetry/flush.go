package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const FlushInterval = 6 * time.Hour

type FlushStatus string

const (
	FlushDisabled    FlushStatus = "disabled"
	FlushThrottled   FlushStatus = "throttled"
	FlushEmpty       FlushStatus = "empty"
	FlushAccepted    FlushStatus = "accepted"
	FlushRetryable   FlushStatus = "retryable"
	FlushQuarantined FlushStatus = "quarantined"
	FlushKillSwitch  FlushStatus = "kill_switch"
)

type FlushResult struct {
	Status      FlushStatus
	BatchCount  int
	Disposition SendDisposition
	StatusCode  int
	RetryAfter  time.Duration
}

type Flusher struct {
	Aggregates AggregateStore
	State      StateStore
	Sender     Sender
	Build      Build
	LookupEnv  func(string) (string, bool)
	Now        func() time.Time
}

func (flusher Flusher) Flush(ctx context.Context, explicit bool) (FlushResult, error) {
	if !flusher.Sender.Enabled() {
		return FlushResult{Status: FlushDisabled, Disposition: SendDisabled}, nil
	}
	state, err := flusher.State.Load()
	if err != nil {
		return FlushResult{}, fmt.Errorf("load telemetry state: %w", err)
	}
	policy := EvaluatePolicy(PolicyOptions{Preference: state.Preference, Build: flusher.Build, LookupEnv: flusher.LookupEnv})
	if !policy.Enabled {
		return FlushResult{Status: FlushDisabled, Disposition: SendDisabled}, nil
	}
	if state.AnonymousID == nil || !flusher.Sender.EnabledFor(*state.AnonymousID) {
		return FlushResult{Status: FlushDisabled, Disposition: SendDisabled}, nil
	}
	now := flusher.now()
	if !explicit && recentlyAttempted(state.LastFlushAttemptAt, now) {
		return FlushResult{Status: FlushThrottled}, nil
	}
	batches, err := flusher.Aggregates.Rotate(RecordOptions{State: state, Build: flusher.Build, LookupEnv: flusher.LookupEnv})
	if err != nil {
		return FlushResult{}, fmt.Errorf("rotate telemetry aggregates: %w", err)
	}
	if len(batches) == 0 {
		return FlushResult{Status: FlushEmpty}, nil
	}
	if len(batches) > MaxBatchesPerRequest {
		batches = batches[:MaxBatchesPerRequest]
	}
	if _, err := flusher.State.MarkFlush(now, false); err != nil {
		return FlushResult{}, fmt.Errorf("mark telemetry flush attempt: %w", err)
	}
	aggregates := make([]DailyAggregate, len(batches))
	for index, batch := range batches {
		aggregates[index] = batch.Aggregate
	}
	sendResult, sendErr := flusher.Sender.Send(ctx, aggregates)
	result := FlushResult{
		BatchCount:  len(batches),
		Disposition: sendResult.Disposition,
		StatusCode:  sendResult.StatusCode,
		RetryAfter:  sendResult.RetryAfter,
	}
	switch sendResult.Disposition {
	case SendAccepted:
		for _, batch := range batches {
			if err := flusher.Aggregates.RemoveBatch(batch.Aggregate.BatchID); err != nil {
				return result, err
			}
		}
		if _, err := flusher.State.MarkFlush(now, true); err != nil {
			return result, err
		}
		result.Status = FlushAccepted
	case SendRejected:
		for _, batch := range batches {
			if err := flusher.Aggregates.QuarantineBatch(batch.Aggregate.BatchID); err != nil {
				return result, err
			}
		}
		result.Status = FlushQuarantined
	case SendKillSwitch:
		result.Status = FlushKillSwitch
	default:
		result.Status = FlushRetryable
	}
	if sendErr != nil && !errors.Is(sendErr, context.Canceled) && !errors.Is(sendErr, context.DeadlineExceeded) {
		return result, sendErr
	}
	return result, sendErr
}

func recentlyAttempted(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	attempt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return now.UTC().Before(attempt.Add(FlushInterval))
}

func (flusher Flusher) now() time.Time {
	if flusher.Now != nil {
		return flusher.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

type DetachedScheduler struct {
	Sender         Sender
	InstallationID *UUID
	Executable     func() (string, error)
	Start          func(*exec.Cmd) error
}

func (scheduler DetachedScheduler) Schedule() (bool, error) {
	if !scheduler.Sender.Enabled() || scheduler.InstallationID == nil || !scheduler.Sender.EnabledFor(*scheduler.InstallationID) {
		return false, nil
	}
	executable := scheduler.Executable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return false, err
	}
	command := exec.Command(path, "__telemetry-flush")
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	configureDetachedProcess(command)
	start := scheduler.Start
	if start == nil {
		start = func(command *exec.Cmd) error { return command.Start() }
	}
	if err := start(command); err != nil {
		return false, err
	}
	if scheduler.Start == nil && command.Process != nil {
		if err := command.Process.Release(); err != nil {
			return true, err
		}
	}
	return true, nil
}
