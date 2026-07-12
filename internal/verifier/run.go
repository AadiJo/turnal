package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/config"
)

type Request struct {
	Root        string
	Target      Target
	Verifiers   []config.Verifier
	Environment []string
	OutputLimit int
	Now         func() time.Time
}

func Run(ctx context.Context, request Request) (Report, error) {
	if ctx == nil {
		return Report{}, fmt.Errorf("verifier context is required")
	}
	if request.Root == "" {
		return Report{}, fmt.Errorf("verifier evaluation root is required")
	}
	if len(request.Verifiers) == 0 {
		return Report{}, fmt.Errorf("no repository verifiers are configured")
	}
	if request.OutputLimit <= 0 {
		request.OutputLimit = DefaultOutputLimit
	}
	if request.Now == nil {
		request.Now = time.Now
	}
	if request.Environment == nil {
		request.Environment = os.Environ()
	}

	started := request.Now().UTC()
	report := Report{
		SchemaVersion: SchemaVersion,
		Target:        request.Target,
		StartedAt:     started,
		Checks:        make([]Check, 0, len(request.Verifiers)),
	}
	for _, definition := range request.Verifiers {
		result := runOne(ctx, request, definition)
		report.Checks = append(report.Checks, result)
		report.Summary.Total++
		switch result.Status {
		case StatusPassed:
			report.Summary.Passed++
		case StatusFailed:
			report.Summary.Failed++
		case StatusTimedOut:
			report.Summary.TimedOut++
		case StatusLaunchError:
			report.Summary.LaunchError++
		}
	}
	report.FinishedAt = request.Now().UTC()
	report.DurationMS = elapsedMilliseconds(report.StartedAt, report.FinishedAt)
	return report, nil
}

func runOne(parent context.Context, request Request, definition config.Verifier) Check {
	started := request.Now().UTC()
	result := Check{
		Name:      definition.Name,
		Command:   definition.Command,
		Args:      append([]string(nil), definition.Args...),
		Status:    StatusLaunchError,
		StartedAt: started,
		Timeout:   definition.Timeout.String(),
	}

	ctx, cancel := context.WithTimeout(parent, definition.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, definition.Command, definition.Args...)
	cmd.Dir = request.Root
	cmd.Env = append([]string(nil), request.Environment...)
	configureProcess(cmd)
	stdout := newBoundedBuffer(request.OutputLimit)
	stderr := newBoundedBuffer(request.OutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		result.LaunchError = err.Error()
		finishCheck(&result, request.Now().UTC(), stdout, stderr)
		return result
	}
	err := cmd.Wait()
	finishCheck(&result, request.Now().UTC(), stdout, stderr)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = StatusTimedOut
		result.TimedOut = true
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			result.ExitCode = &code
		}
		return result
	}
	if err == nil {
		result.Status = StatusPassed
		code := 0
		result.ExitCode = &code
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.Status = StatusFailed
		code := exitErr.ExitCode()
		result.ExitCode = &code
		return result
	}
	result.LaunchError = err.Error()
	return result
}

func finishCheck(result *Check, finished time.Time, stdout, stderr *boundedBuffer) {
	result.FinishedAt = finished
	result.DurationMS = elapsedMilliseconds(result.StartedAt, result.FinishedAt)
	result.Stdout, result.StdoutTruncated = stdout.result()
	result.Stderr, result.StderrTruncated = stderr.result()
}

func elapsedMilliseconds(start, finish time.Time) int64 {
	value := finish.Sub(start).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{data: make([]byte, 0, limit), limit: limit}
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	written := len(data)
	remaining := buffer.limit - len(buffer.data)
	if remaining > 0 {
		if len(data) < remaining {
			remaining = len(data)
		}
		buffer.data = append(buffer.data, data[:remaining]...)
	}
	if len(data) > remaining {
		buffer.truncated = true
	}
	return written, nil
}

func (buffer *boundedBuffer) result() (string, bool) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(append([]byte(nil), buffer.data...)), buffer.truncated
}
