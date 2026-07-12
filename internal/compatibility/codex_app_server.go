package compatibility

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/adapters"
)

const (
	initializeRequestID = 1
	hooksListRequestID  = 2
)

var expectedCodexEventNames = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"}

type CodexHook struct {
	CWD         string `json:"cwd"`
	EventName   string `json:"eventName"`
	Command     string `json:"command"`
	SourcePath  string `json:"sourcePath"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
}

type CodexHooksResult struct {
	Hooks    []CodexHook
	Warnings []string
	Errors   []string
}

type AppServerProbe struct {
	Executable      string
	Timeout         time.Duration
	ShutdownTimeout time.Duration
	MaxMessageBytes int
	MaxOutputBytes  int
	Command         func(context.Context, string, ...string) *exec.Cmd
}

func DefaultAppServerProbe() AppServerProbe {
	return AppServerProbe{
		Executable:      "codex",
		Timeout:         8 * time.Second,
		ShutdownTimeout: time.Second,
		MaxMessageBytes: 1 << 20,
		MaxOutputBytes:  4 << 20,
		Command:         exec.CommandContext,
	}
}

func (probe AppServerProbe) Probe(parent context.Context, workspaceRoot, expectedCommand string) (result CodexHooksResult, returnedErr error) {
	probe = probe.withDefaults()
	ctx, cancel := context.WithTimeout(parent, probe.Timeout)
	defer cancel()

	command := probe.Command(ctx, probe.Executable, "app-server", "--enable", "hooks")
	command.Dir = workspaceRoot
	if command.Env == nil {
		command.Env = os.Environ()
	}
	processTree, err := prepareProcessTree(command)
	if err != nil {
		return result, fmt.Errorf("prepare Codex app-server process tree: %w", err)
	}
	defer releaseProcessTree(processTree)
	stdin, err := command.StdinPipe()
	if err != nil {
		return result, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return result, fmt.Errorf("open Codex app-server stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return result, fmt.Errorf("start Codex app-server: %w", err)
	}
	if err := attachProcessTree(processTree, command); err != nil {
		abortErr := abortAppServerStart(command, processTree, probe.ShutdownTimeout)
		return result, errors.Join(fmt.Errorf("supervise Codex app-server process tree: %w", err), abortErr)
	}

	var stderrCapture boundedBuffer
	stderrCapture.limit = probe.MaxOutputBytes
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrCapture, stderr)
		close(stderrDone)
	}()
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stdout.Close()
		case <-readDone:
		}
	}()

	defer func() {
		close(readDone)
		_ = stdin.Close()
		waitDone := make(chan error, 1)
		go func() { waitDone <- command.Wait() }()
		returnedErr = finishAppServer(command, processTree, waitDone, stderrDone, &stderrCapture, probe.ShutdownTimeout, returnedErr, stdout, stderr)
		if returnedErr == nil {
			if stderrText := strings.TrimSpace(stderrCapture.String()); stderrText != "" {
				result.Warnings = append(result.Warnings, "Codex app-server stderr: "+stderrText)
			}
		}
	}()

	if err := writeRPC(stdin, initializeRequestID, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "turnal", "title": "Turnal", "version": "capture-probe"},
	}); err != nil {
		return result, err
	}

	reader := bufio.NewReader(stdout)
	seenResponses := map[int]bool{}
	totalOutput := 0
	initialized := false
	for {
		line, readErr := readBoundedLine(reader, probe.MaxMessageBytes)
		totalOutput += len(line)
		if totalOutput > probe.MaxOutputBytes {
			return result, fmt.Errorf("Codex app-server output exceeds %d-byte limit", probe.MaxOutputBytes)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var message rpcMessage
			if err := json.Unmarshal(line, &message); err != nil {
				return result, fmt.Errorf("decode Codex app-server JSON: %w", err)
			}
			if message.ID != nil {
				id := *message.ID
				if seenResponses[id] {
					return result, fmt.Errorf("Codex app-server returned duplicate response id %d", id)
				}
				seenResponses[id] = true
				if message.Error != nil {
					return result, fmt.Errorf("Codex app-server request %d failed: %s", id, message.Error.message())
				}
				switch id {
				case initializeRequestID:
					if initialized {
						return result, errors.New("Codex app-server initialized more than once")
					}
					initialized = true
					if err := writeNotification(stdin, "initialized", map[string]any{}); err != nil {
						return result, err
					}
					if err := writeRPC(stdin, hooksListRequestID, "hooks/list", map[string]any{"cwds": []string{workspaceRoot}}); err != nil {
						return result, err
					}
				case hooksListRequestID:
					if !initialized {
						return result, errors.New("Codex app-server returned hooks/list before initialization")
					}
					return decodeHooksResult(message.Result, workspaceRoot)
				}
			}
		}
		if readErr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return result, fmt.Errorf("Codex app-server probe timed out after %s", probe.Timeout)
			}
			if errors.Is(readErr, errMessageTooLarge) {
				return result, fmt.Errorf("Codex app-server message exceeds %d-byte limit", probe.MaxMessageBytes)
			}
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, os.ErrClosed) {
				return result, errors.New("Codex app-server exited before hooks/list response")
			}
			return result, fmt.Errorf("read Codex app-server response: %w", readErr)
		}
	}
}

func abortAppServerStart(command *exec.Cmd, processTree *appServerProcessTree, timeout time.Duration) error {
	killErr := killProcessTree(processTree, command, timeout)
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitDone:
		return killErr
	case <-timer.C:
		return errors.Join(killErr, errors.New("timed out reaping Codex app-server after process-tree setup failure"))
	}
}

func (probe AppServerProbe) withDefaults() AppServerProbe {
	defaults := DefaultAppServerProbe()
	if probe.Executable == "" {
		probe.Executable = defaults.Executable
	}
	if probe.Timeout <= 0 {
		probe.Timeout = defaults.Timeout
	}
	if probe.ShutdownTimeout <= 0 {
		probe.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if probe.MaxMessageBytes <= 0 {
		probe.MaxMessageBytes = defaults.MaxMessageBytes
	}
	if probe.MaxOutputBytes <= 0 {
		probe.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if probe.Command == nil {
		probe.Command = defaults.Command
	}
	return probe
}

type rpcMessage struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (rpcErr rpcError) message() string {
	if rpcErr.Message == "" {
		return fmt.Sprintf("JSON-RPC error %d", rpcErr.Code)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s", rpcErr.Code, rpcErr.Message)
}

func writeRPC(writer io.Writer, id int, method string, params any) error {
	return writeJSONLine(writer, map[string]any{"id": id, "method": method, "params": params})
}

func writeNotification(writer io.Writer, method string, params any) error {
	return writeJSONLine(writer, map[string]any{"method": method, "params": params})
}

func writeJSONLine(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Codex app-server request: %w", err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

var errMessageTooLarge = errors.New("message too large")

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errMessageTooLarge
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func decodeHooksResult(raw json.RawMessage, workspaceRoot string) (CodexHooksResult, error) {
	var response struct {
		Data []struct {
			CWD      string            `json:"cwd"`
			Hooks    []CodexHook       `json:"hooks"`
			Warnings []json.RawMessage `json:"warnings"`
			Errors   []json.RawMessage `json:"errors"`
		} `json:"data"`
	}
	if len(raw) == 0 {
		return CodexHooksResult{}, errors.New("Codex app-server hooks/list response has no result")
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return CodexHooksResult{}, fmt.Errorf("decode Codex hooks/list result: %w", err)
	}
	cleanRoot := normalizeFilePath(workspaceRoot)
	var result CodexHooksResult
	for _, entry := range response.Data {
		if normalizeFilePath(entry.CWD) != cleanRoot {
			continue
		}
		for _, hook := range entry.Hooks {
			if hook.CWD == "" {
				hook.CWD = entry.CWD
			}
			result.Hooks = append(result.Hooks, hook)
		}
		result.Warnings = append(result.Warnings, stringifyMessages(entry.Warnings)...)
		result.Errors = append(result.Errors, stringifyMessages(entry.Errors)...)
	}
	return result, nil
}

func stringifyMessages(messages []json.RawMessage) []string {
	values := make([]string, 0, len(messages))
	for _, raw := range messages {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			values = append(values, text)
			continue
		}
		values = append(values, string(raw))
	}
	return values
}

func finishAppServer(command *exec.Cmd, processTree *appServerProcessTree, waitDone <-chan error, stderrDone <-chan struct{}, stderr *boundedBuffer, timeout time.Duration, current error, pipes ...io.Closer) error {
	waitErr, reaped, drained := awaitAppServerShutdown(waitDone, stderrDone, timeout, false, false)
	var shutdownErr error
	if !reaped || !drained {
		killErr := killProcessTree(processTree, command, timeout)
		for _, pipe := range pipes {
			_ = pipe.Close()
		}
		waitErr, reaped, drained = awaitAppServerShutdown(waitDone, stderrDone, timeout, reaped, drained)
		if killErr != nil && !reaped {
			shutdownErr = fmt.Errorf("terminate Codex app-server process tree: %w", killErr)
		}
	}
	if !reaped {
		shutdownErr = errors.Join(shutdownErr, errors.New("timed out reaping Codex app-server"))
	}
	if !drained {
		shutdownErr = errors.Join(shutdownErr, errors.New("timed out draining Codex app-server pipes"))
	}
	stderrText := strings.TrimSpace(stderr.String())
	if current != nil {
		if stderrText != "" {
			current = fmt.Errorf("%w; Codex app-server stderr: %s", current, stderrText)
		}
		return errors.Join(current, shutdownErr)
	}
	_ = waitErr // A complete hooks/list response makes forced shutdown non-fatal.
	return shutdownErr
}

func awaitAppServerShutdown(waitDone <-chan error, stderrDone <-chan struct{}, timeout time.Duration, reaped, drained bool) (error, bool, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	waitChannel := waitDone
	stderrChannel := stderrDone
	if reaped {
		waitChannel = nil
	}
	if drained {
		stderrChannel = nil
	}
	for !reaped || !drained {
		select {
		case err := <-waitChannel:
			waitErr = err
			reaped = true
			waitChannel = nil
		case <-stderrChannel:
			drained = true
			stderrChannel = nil
		case <-timer.C:
			return waitErr, reaped, drained
		}
	}
	return waitErr, reaped, drained
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(data)
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	value := buffer.buffer.String()
	if buffer.truncated {
		value += " [truncated]"
	}
	return value
}

func ClassifyCodexHooks(workspaceRoot, expectedCommand string, health adapters.HookHealth, probed CodexHooksResult) SurfaceResult {
	expectedEvents := make(map[string]struct{}, len(expectedCodexEventNames))
	for _, eventName := range expectedCodexEventNames {
		expectedEvents[normalizeEventName(eventName)] = struct{}{}
	}
	type eventState struct {
		enabled bool
		trusted bool
	}
	matching := make(map[string]eventState)
	expectedSources := codexProjectSourcePaths(workspaceRoot)
	for _, hook := range probed.Hooks {
		eventName := normalizeEventName(hook.EventName)
		_, expectedSource := expectedSources[normalizeFilePath(hook.SourcePath)]
		if normalizeFilePath(hook.CWD) != normalizeFilePath(workspaceRoot) ||
			hook.Command != expectedCommand ||
			!strings.EqualFold(hook.Source, "project") ||
			!expectedSource {
			continue
		}
		if _, expected := expectedEvents[eventName]; !expected {
			continue
		}
		state := matching[eventName]
		state.enabled = state.enabled || hook.Enabled
		state.trusted = state.trusted || (hook.Enabled && strings.EqualFold(hook.TrustStatus, "trusted"))
		matching[eventName] = state
	}

	result := SurfaceResult{
		Provider:      ProviderCodex,
		Surface:       SurfaceCodexAppServer,
		Configuration: health.Status,
		Visibility:    VisibilityConfirmed,
		Execution:     ExecutionConfirmed,
		Expectation:   CaptureAvailable,
		Certainty:     CertaintyConfirmed,
		Expected:      len(expectedEvents),
		Discovered:    len(matching),
		Warnings:      append(append([]string{}, probed.Warnings...), probed.Errors...),
		Guidance:      []string{"review project and hook definitions before trusting them in Codex's hooks UI"},
	}
	for _, state := range matching {
		if state.enabled {
			result.Enabled++
		}
		if state.trusted {
			result.Trusted++
		}
	}
	if len(probed.Errors) > 0 {
		result.Execution = ExecutionUnavailable
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyIncompatible
		result.Guidance = append([]string{"Codex app-server reported hook discovery errors"}, result.Guidance...)
		return applyStaticCodexHealth(result, health)
	}

	switch {
	case result.Discovered != result.Expected:
		result.Execution = ExecutionUnavailable
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyIncompatible
		result.Guidance = append([]string{fmt.Sprintf("Codex app-server discovered %s Turnal hooks", countSummary(result.Discovered, result.Expected))}, result.Guidance...)
	case result.Enabled != result.Expected:
		result.Execution = ExecutionDisabled
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyIncompatible
		result.Guidance = append([]string{"enable the disabled Turnal hooks in Codex after reviewing them"}, result.Guidance...)
	case result.Trusted != result.Expected:
		result.Execution = ExecutionUntrusted
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyConfirmed
		result.Guidance = append([]string{"Codex app-server will skip untrusted hooks; Turnal cannot grant hook trust"}, result.Guidance...)
	}
	return applyStaticCodexHealth(result, health)
}

func codexProjectSourcePaths(workspaceRoot string) map[string]struct{} {
	paths := map[string]struct{}{
		normalizeFilePath(filepath.Join(workspaceRoot, ".codex", "config.toml")): {},
	}
	if rootCheckout := adapters.EffectiveHookRoot(workspaceRoot, adapters.TargetCodex); normalizeFilePath(rootCheckout) != normalizeFilePath(workspaceRoot) {
		paths[normalizeFilePath(filepath.Join(rootCheckout, ".codex", "config.toml"))] = struct{}{}
	}
	return paths
}

func normalizeFilePath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func applyStaticCodexHealth(result SurfaceResult, health adapters.HookHealth) SurfaceResult {
	if health.Status == adapters.HookConfigurationConfigured {
		return result
	}
	result.Expectation = CaptureUnavailable
	result.Certainty = CertaintyIncompatible
	if health.Status == adapters.HookConfigurationDisabled {
		result.Execution = ExecutionDisabled
	} else {
		result.Execution = ExecutionUnavailable
	}
	result.Guidance = append(append([]string{}, health.Problems...), result.Guidance...)
	return result
}

func normalizeEventName(value string) string {
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return strings.ToLower(value)
}
