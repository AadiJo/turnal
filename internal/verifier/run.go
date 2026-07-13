package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/config"
)

const processWaitDelay = time.Second

type Request struct {
	Root                     string
	Target                   Target
	Verifiers                []config.Verifier
	Environment              []string
	OutputLimit              int
	Now                      func() time.Time
	newProcessControllerFunc func(*exec.Cmd) (processController, error)
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
	request.Verifiers = append([]config.Verifier(nil), request.Verifiers...)
	if err := ValidateDefinitions(request.Verifiers); err != nil {
		return Report{}, err
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
	if request.newProcessControllerFunc == nil {
		request.newProcessControllerFunc = newProcessController
	}

	started := request.Now().UTC()
	report := Report{
		SchemaVersion: SchemaVersion,
		Target:        request.Target,
		StartedAt:     started,
		Checks:        make([]Check, 0, len(request.Verifiers)),
		Summary:       Summary{Outcome: "passed"},
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
			report.Summary.Outcome = "failed"
		case StatusTimedOut:
			report.Summary.TimedOut++
			report.Summary.Outcome = "failed"
		case StatusLaunchError:
			report.Summary.LaunchError++
			report.Summary.Outcome = "failed"
		}
		if len(result.InfrastructureErrors) > 0 {
			report.Summary.InfrastructureErrors += len(result.InfrastructureErrors)
			report.Summary.Outcome = "failed"
		}
		if err := ctx.Err(); err != nil {
			report.FinishedAt = request.Now().UTC()
			report.DurationMS = elapsedMilliseconds(report.StartedAt, report.FinishedAt)
			return report, fmt.Errorf("verification interrupted: %w", err)
		}
	}
	report.FinishedAt = request.Now().UTC()
	report.DurationMS = elapsedMilliseconds(report.StartedAt, report.FinishedAt)
	return report, nil
}

// ValidateDefinitions validates an effective repository verifier contract
// without executing it.
func ValidateDefinitions(definitions []config.Verifier) error {
	if len(definitions) > config.MaxVerifierCount {
		return fmt.Errorf("verify must contain at most %d entries", config.MaxVerifierCount)
	}
	seen := make(map[string]int, len(definitions))
	for index, definition := range definitions {
		position := index + 1
		rawName := definition.Name
		name := strings.TrimSpace(rawName)
		label := fmt.Sprintf("verify[%d]", position)
		if name != "" {
			label += fmt.Sprintf(" %q", name)
		}
		if name == "" {
			return fmt.Errorf("%s: name must not be empty", label)
		}
		if err := config.ValidateVerifierName(rawName); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		definitions[index].Name = name
		switch {
		case strings.TrimSpace(definition.Command) == "":
			return fmt.Errorf("%s: command must not be empty", label)
		case strings.ContainsRune(definition.Command, 0):
			return fmt.Errorf("%s: command must not contain NUL", label)
		case len(definition.Command) > config.MaxVerifierCommandBytes:
			return fmt.Errorf("%s: command must be at most %d bytes", label, config.MaxVerifierCommandBytes)
		case len(definition.Args) > config.MaxVerifierArgCount:
			return fmt.Errorf("%s: args must contain at most %d arguments", label, config.MaxVerifierArgCount)
		case definition.Timeout <= 0:
			return fmt.Errorf("%s: timeout must be positive", label)
		case definition.Timeout > config.MaxVerifierTimeout:
			return fmt.Errorf("%s: timeout must not exceed %s", label, config.MaxVerifierTimeout)
		}
		if previous, ok := seen[name]; ok {
			return fmt.Errorf("%s: name duplicates verify[%d]", label, previous)
		}
		seen[name] = position
		for argIndex, arg := range definition.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("%s: args[%d] must not contain NUL", label, argIndex+1)
			}
			if len(arg) > config.MaxVerifierArgBytes {
				return fmt.Errorf("%s: args[%d] must be at most %d bytes", label, argIndex+1, config.MaxVerifierArgBytes)
			}
		}
	}
	return nil
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
	cmd.WaitDelay = processWaitDelay
	controller, err := request.newProcessControllerFunc(cmd)
	if err != nil {
		result.LaunchError = fmt.Sprintf("prepare process containment: %v", err)
		finishCheck(&result, request.Now().UTC(), newBoundedBuffer(request.OutputLimit), newBoundedBuffer(request.OutputLimit))
		return result
	}
	cmd.Cancel = controller.Cancel
	stdout := newBoundedBuffer(request.OutputLimit)
	stderr := newBoundedBuffer(request.OutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		closeErr := controller.Close()
		result.LaunchError = err.Error()
		if closeErr != nil {
			result.LaunchError = errors.Join(err, fmt.Errorf("release process containment: %w", closeErr)).Error()
		}
		finishCheck(&result, request.Now().UTC(), stdout, stderr)
		return result
	}
	if err := controller.AfterStart(); err != nil {
		_ = controller.Cancel()
		_ = cmd.Wait()
		closeErr := controller.Close()
		result.LaunchError = errors.Join(fmt.Errorf("activate process containment: %w", err), closeErr).Error()
		finishCheck(&result, request.Now().UTC(), stdout, stderr)
		return result
	}
	waitErr := cmd.Wait()
	closeErr := controller.Close()
	finishCheck(&result, request.Now().UTC(), stdout, stderr)
	classifyWaitResult(&result, waitErr, ctx.Err(), cmd.ProcessState)
	if closeErr != nil {
		result.InfrastructureErrors = append(result.InfrastructureErrors, InfrastructureError{
			Stage:   "containment_cleanup",
			Message: closeErr.Error(),
		})
	}
	return result
}

func classifyWaitResult(result *Check, waitErr error, contextErr error, processState *os.ProcessState) {
	if waitErr == nil {
		result.Status = StatusPassed
		code := 0
		result.ExitCode = &code
		return
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) && processState != nil && processState.Success() {
		result.Status = StatusPassed
		code := 0
		result.ExitCode = &code
		return
	}
	if errors.Is(contextErr, context.DeadlineExceeded) {
		result.Status = StatusTimedOut
		result.TimedOut = true
		if processState != nil {
			code := processState.ExitCode()
			result.ExitCode = &code
		}
		return
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.Status = StatusFailed
		code := exitErr.ExitCode()
		result.ExitCode = &code
		return
	}
	result.Status = StatusLaunchError
	result.LaunchError = waitErr.Error()
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
