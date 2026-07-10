package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
)

const hookFailureLogName = "hook-failures.jsonl"

type HookFailure struct {
	Version   int                    `json:"v"`
	Time      string                 `json:"time"`
	Adapter   primitives.AdapterName `json:"adapter"`
	Hook      string                 `json:"hook"`
	SessionID string                 `json:"session_id,omitempty"`
	Error     string                 `json:"error"`
}

func RecordHookFailure(adapter primitives.AdapterName, hookName string, raw []byte, cause error) error {
	if cause == nil {
		return nil
	}
	cwd, err := hookWorkspaceCWD(raw)
	if err != nil {
		return err
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return err
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return err
	}

	var payload struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &payload)
	failure := HookFailure{
		Version:   1,
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		Adapter:   adapter,
		Hook:      hookName,
		SessionID: payload.SessionID,
		Error:     cause.Error(),
	}
	data, err := json.Marshal(failure)
	if err != nil {
		return fmt.Errorf("marshal hook failure: %w", err)
	}
	data = append(data, '\n')

	path := HookFailureLogPath(repo.MetadataDir)
	lock, err := filelock.Acquire(path+".lock", 5*time.Second)
	if err != nil {
		return fmt.Errorf("lock hook failure log: %w", err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := recoverTrailingPartialJSONL(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open hook failure log: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("append hook failure log: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync hook failure log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close hook failure log: %w", err)
	}
	return nil
}

func HookFailureLogPath(metadataDir string) string {
	return filepath.Join(metadataDir, "log", hookFailureLogName)
}

func ReadHookFailures(metadataDir string) ([]HookFailure, error) {
	path := HookFailureLogPath(metadataDir)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hook failure log: %w", err)
	}
	defer func() { _ = file.Close() }()

	var failures []HookFailure
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		var failure HookFailure
		if err := json.Unmarshal(scanner.Bytes(), &failure); err != nil {
			return nil, fmt.Errorf("hook failure log line %d malformed: %w", line, err)
		}
		if failure.Version != 1 || failure.Error == "" || strings.TrimSpace(failure.Time) == "" {
			return nil, fmt.Errorf("hook failure log line %d invalid", line)
		}
		failures = append(failures, failure)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan hook failure log: %w", err)
	}
	return failures, nil
}

func ClearHookFailures(metadataDir string) (int, error) {
	failures, err := ReadHookFailures(metadataDir)
	if err != nil {
		return 0, err
	}
	path := HookFailureLogPath(metadataDir)
	lock, err := filelock.Acquire(path+".lock", 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("lock hook failure log: %w", err)
	}
	defer func() { _ = lock.Release() }()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("clear hook failure log: %w", err)
	}
	return len(failures), nil
}
