package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

type RawHookRecord struct {
	Version    int                    `json:"v"`
	Adapter    primitives.AdapterName `json:"adapter"`
	Hook       string                 `json:"hook"`
	ReceivedAt string                 `json:"received_at"`
	CWD        string                 `json:"cwd,omitempty"`
	Payload    json.RawMessage        `json:"payload,omitempty"`
	Raw        string                 `json:"raw,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

func RecordHookPayload(adapter primitives.AdapterName, hookName string, raw []byte) (string, error) {
	parsedAdapter, err := primitives.ParseAdapterName(adapter.String())
	if err != nil {
		return "", err
	}
	if hookName == "" {
		return "", fmt.Errorf("hook name is required")
	}

	cwd, err := hookWorkspaceCWD(raw)
	if err != nil {
		return "", err
	}

	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return "", nil
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return "", nil
	}

	record := RawHookRecord{
		Version:    1,
		Adapter:    parsedAdapter,
		Hook:       hookName,
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:        cwd,
	}
	if json.Valid(raw) {
		record.Payload = append(json.RawMessage(nil), raw...)
	} else {
		record.Raw = string(raw)
		record.Error = "malformed JSON payload"
	}

	return appendRawHookRecord(repo.MetadataDir, record)
}

func appendRawHookRecord(metadataDir string, record RawHookRecord) (string, error) {
	dir := filepath.Join(metadataDir, "log", "adapter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create adapter log dir: %w", err)
	}

	path := filepath.Join(dir, record.Adapter.String()+".jsonl")
	line, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("marshal adapter hook record: %w", err)
	}
	line = append(line, '\n')

	lockDir := path + ".lock"
	if err := acquireDirLock(lockDir); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(lockDir) }()

	lineNumber, err := nextJSONLLineNumber(path)
	if err != nil {
		return "", err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("open adapter log: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(line); err != nil {
		return "", fmt.Errorf("append adapter log: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync adapter log: %w", err)
	}
	return fmt.Sprintf("%s:%d", record.Adapter, lineNumber), nil
}

func hookWorkspaceCWD(raw []byte) (string, error) {
	processCWD, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	if _, err := checkpoint.FindRoot(processCWD); err == nil {
		return processCWD, nil
	}

	payloadCWD := cwdFromPayload(raw)
	if payloadCWD == "" {
		return processCWD, nil
	}
	if !filepath.IsAbs(payloadCWD) {
		return processCWD, nil
	}
	return payloadCWD, nil
}

func cwdFromPayload(raw []byte) string {
	var payload struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return payload.CWD
}

func acquireDirLock(lockDir string) error {
	const attempts = 100
	for i := 0; i < attempts; i++ {
		if err := os.Mkdir(lockDir, 0o700); err == nil {
			return nil
		} else if !os.IsExist(err) {
			return fmt.Errorf("create adapter log lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("adapter log lock busy: %s", strings.TrimSuffix(lockDir, ".lock"))
}

func nextJSONLLineNumber(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("read adapter log for line number: %w", err)
	}
	return strings.Count(string(data), "\n") + 1, nil
}
