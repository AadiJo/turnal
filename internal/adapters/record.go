package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
)

type RawHookRecord struct {
	Version    int                    `json:"v"`
	Sequence   uint64                 `json:"seq,omitempty"`
	SessionID  string                 `json:"session_id,omitempty"`
	Adapter    primitives.AdapterName `json:"adapter"`
	Hook       string                 `json:"hook"`
	ReceivedAt string                 `json:"received_at"`
	CWD        string                 `json:"cwd,omitempty"`
	Payload    json.RawMessage        `json:"payload,omitempty"`
	Raw        string                 `json:"raw,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

const MaxHookPayloadBytes = 8 << 20

const maxRawRecordBytes = MaxHookPayloadBytes*6 + 64*1024

type RawHookRef struct {
	Version    int
	SessionID  primitives.SessionID
	Adapter    primitives.AdapterName
	LineNumber uint64
}

func ParseRawHookRef(value string) (RawHookRef, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	var ref RawHookRef
	var adapterText, lineText string
	switch {
	case len(parts) == 2:
		ref.Version = 1
		adapterText, lineText = parts[0], parts[1]
	case len(parts) == 4 && parts[0] == "v2":
		ref.Version = 2
		sessionID, err := primitives.ParseSessionID(parts[1])
		if err != nil {
			return RawHookRef{}, fmt.Errorf("invalid raw adapter ref %q: %w", value, err)
		}
		ref.SessionID = sessionID
		adapterText, lineText = parts[2], parts[3]
	default:
		return RawHookRef{}, fmt.Errorf("invalid raw adapter ref %q: must be <adapter>:<line> or v2:<session>:<adapter>:<line>", value)
	}

	adapter, err := primitives.ParseAdapterName(adapterText)
	if err != nil {
		return RawHookRef{}, fmt.Errorf("invalid raw adapter ref %q: %w", value, err)
	}

	lineNumber, err := strconv.ParseUint(lineText, 10, 64)
	if err != nil || lineNumber == 0 {
		return RawHookRef{}, fmt.Errorf("invalid raw adapter ref %q: line must be a positive integer", value)
	}

	ref.Adapter = adapter
	ref.LineNumber = lineNumber
	return ref, nil
}

func (ref RawHookRef) String() string {
	if ref.Version == 2 {
		return fmt.Sprintf("v2:%s:%s:%d", ref.SessionID, ref.Adapter, ref.LineNumber)
	}
	return fmt.Sprintf("%s:%d", ref.Adapter, ref.LineNumber)
}

func RecordHookPayload(adapter primitives.AdapterName, hookName string, raw []byte) (string, error) {
	return recordHookPayload(adapter, hookName, raw, false)
}

func recordHookPayload(adapter primitives.AdapterName, hookName string, raw []byte, forceIntentResultRedaction bool) (string, error) {
	if len(raw) > MaxHookPayloadBytes {
		return "", fmt.Errorf("hook payload is %d bytes; maximum is %d bytes", len(raw), MaxHookPayloadBytes)
	}
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
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return "", err
	}
	sessionID := sessionIDFromRawPayload(raw)
	if sessionID == "" {
		sessionID, _ = primitives.ParseSessionID("unassigned")
	}
	storedRaw := redactRawHookPayload(raw, effective.Secrets, effective.Hooks.Command, forceIntentResultRedaction)

	record := RawHookRecord{
		Version:    1,
		SessionID:  sessionID.String(),
		Adapter:    parsedAdapter,
		Hook:       hookName,
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:        cwd,
	}
	if json.Valid(storedRaw) {
		record.Payload = append(json.RawMessage(nil), storedRaw...)
	} else {
		record.Raw = string(storedRaw)
		record.Error = "malformed JSON payload"
	}

	return appendRawHookRecord(repo.MetadataDir, record)
}

// RecordExternalHookPayload persists provider input after an external adapter
// supplies the routing fields. External adapters never receive repository or
// event-log handles and therefore cannot write durable Turnal state.
func RecordExternalHookPayload(adapter primitives.AdapterName, hookName string, raw []byte, cwd string, sessionID primitives.SessionID) (string, error) {
	return recordExternalHookPayload(adapter, hookName, raw, cwd, sessionID, false)
}

func recordExternalHookPayload(adapter primitives.AdapterName, hookName string, raw []byte, cwd string, sessionID primitives.SessionID, forceIntentRedaction bool) (string, error) {
	if len(raw) > MaxHookPayloadBytes {
		return "", fmt.Errorf("hook payload is %d bytes; maximum is %d bytes", len(raw), MaxHookPayloadBytes)
	}
	parsedAdapter, err := primitives.ParseAdapterName(adapter.String())
	if err != nil {
		return "", err
	}
	parsedSession, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	if hookName == "" {
		return "", fmt.Errorf("hook name is required")
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("external adapter cwd must be absolute")
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return "", nil
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return "", nil
	}
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return "", err
	}
	storedRaw := redactExternalHookPayload(raw, hookName, effective.Secrets, effective.Hooks.Command, forceIntentRedaction)
	record := RawHookRecord{
		Version:    2,
		SessionID:  parsedSession.String(),
		Adapter:    parsedAdapter,
		Hook:       hookName,
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:        cwd,
	}
	if json.Valid(storedRaw) {
		record.Payload = append(json.RawMessage(nil), storedRaw...)
	} else {
		record.Raw = string(storedRaw)
		record.Error = "malformed JSON payload"
	}
	return appendRawHookRecord(repo.MetadataDir, record)
}

func redactExternalHookPayload(raw []byte, hookName string, secrets agentconfig.Secrets, hookCommand string, forceIntentRedaction bool) []byte {
	if !secrets.StorePrompts && forceIntentRedaction {
		redacted, err := json.Marshal(map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"})
		if err == nil {
			return redacted
		}
	}
	if secrets.StorePrompts && secrets.StoreToolIO || !json.Valid(raw) {
		return raw
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	if !secrets.StorePrompts && valueContainsIntentCommand(value, hookCommand) {
		redacted, err := json.Marshal(map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"})
		if err == nil {
			return redacted
		}
	}
	if !secrets.StorePrompts && externalHookContainsAssistantText(hookName) {
		if object, ok := value.(map[string]any); ok {
			for _, key := range []string{"text", "response"} {
				if _, exists := object[key]; exists {
					object[key] = redactedText("", false)
				}
			}
		}
	}
	redactExternalValue(value, secrets, hookCommand)
	redacted, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return redacted
}

func externalHookContainsAssistantText(hookName string) bool {
	switch normalizeHookName(hookName) {
	case "afteragentresponse", "agentsettled":
		return true
	default:
		return false
	}
}

func redactExternalValue(value any, secrets agentconfig.Secrets, hookCommand string) {
	object, ok := value.(map[string]any)
	if !ok {
		if array, ok := value.([]any); ok {
			for _, item := range array {
				redactExternalValue(item, secrets, hookCommand)
			}
		}
		return
	}
	intentCommand := false
	if !secrets.StorePrompts {
		for key, child := range object {
			switch normalizedExternalKey(key) {
			case "toolinput", "toolargs", "args", "input":
				if valueContainsIntentCommand(child, hookCommand) {
					intentCommand = true
					object[key] = map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"}
				}
			}
		}
	}
	for key, child := range object {
		normalized := normalizedExternalKey(key)
		if !secrets.StorePrompts {
			switch normalized {
			case "prompt", "initialprompt", "promptresponse", "lastassistantmessage":
				object[key] = redactedText("", false)
				continue
			}
			if intentCommand {
				switch normalized {
				case "toolinput", "toolargs", "args", "input", "toolresponse", "toolresult", "tooloutput", "output", "result", "error", "errormessage":
					object[key] = map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"}
					continue
				}
			}
		}
		if !secrets.StoreToolIO {
			switch normalized {
			case "toolinput", "toolargs", "args", "input", "toolresponse", "toolresult", "tooloutput", "output", "result", "error", "errormessage":
				object[key] = map[string]any{"redacted": true, "policy": "turnal.secrets"}
				continue
			}
		}
		redactExternalValue(child, secrets, hookCommand)
	}
}

func normalizedExternalKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", ""))
}

func appendRawHookRecord(metadataDir string, record RawHookRecord) (string, error) {
	ref := RawHookRef{Version: 1, Adapter: record.Adapter}
	dir := filepath.Join(metadataDir, "log", "adapter")
	if record.SessionID != "" {
		sessionID, err := primitives.ParseSessionID(record.SessionID)
		if err != nil {
			return "", fmt.Errorf("raw hook record session: %w", err)
		}
		ref.Version = 2
		ref.SessionID = sessionID
		dir = filepath.Join(metadataDir, "log", "raw", sessionID.String())
		record.Version = 2
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create adapter log dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure adapter log dir: %w", err)
	}

	path := filepath.Join(dir, record.Adapter.String()+".jsonl")
	lock, err := filelock.Acquire(path+".lock", 30*time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Release() }()

	if _, err := recoverTrailingPartialJSONL(path); err != nil {
		return "", err
	}

	lineNumber, err := nextJSONLLineNumber(path, ref.Version)
	if err != nil {
		return "", err
	}
	ref.LineNumber = uint64(lineNumber)
	if ref.Version == 2 {
		record.Sequence = uint64(lineNumber)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("marshal adapter hook record: %w", err)
	}
	line = append(line, '\n')

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open adapter log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure adapter log: %w", err)
	}

	if _, err := file.Write(line); err != nil {
		return "", fmt.Errorf("append adapter log: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync adapter log: %w", err)
	}
	return ref.String(), nil
}

func ReadRawHookRecord(metadataDir string, rawRef string) (RawHookRecord, error) {
	ref, err := ParseRawHookRef(rawRef)
	if err != nil {
		return RawHookRecord{}, err
	}

	path := filepath.Join(metadataDir, "log", "adapter", ref.Adapter.String()+".jsonl")
	if ref.Version == 2 {
		path = filepath.Join(metadataDir, "log", "raw", ref.SessionID.String(), ref.Adapter.String()+".jsonl")
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: adapter log missing", ref)
		}
		return RawHookRecord{}, fmt.Errorf("read raw adapter record %s: %w", ref, err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	for lineNumber := uint64(1); ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && len(line) == 0 {
			if readErr == io.EOF {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: line not found", ref)
			}
			return RawHookRecord{}, fmt.Errorf("read raw adapter record %s: %w", ref, readErr)
		}

		if lineNumber == ref.LineNumber {
			line = bytes.TrimRight(line, "\r\n")
			var record RawHookRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: malformed JSON: %w", ref, err)
			}
			if record.Version != ref.Version {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: unsupported version %d", ref, record.Version)
			}
			if record.Adapter != ref.Adapter {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: record adapter %s does not match ref adapter %s", ref, record.Adapter, ref.Adapter)
			}
			if ref.Version == 2 && (record.SessionID != ref.SessionID.String() || record.Sequence != ref.LineNumber) {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: session or sequence does not match ref", ref)
			}
			if record.Hook == "" {
				return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: hook is empty", ref)
			}
			return record, nil
		}

		if readErr == io.EOF {
			return RawHookRecord{}, fmt.Errorf("raw adapter record invariant failed for %s: line not found", ref)
		}
	}
}

// RedactRawHookSession removes retained hook payloads for a session while
// preserving JSONL line numbers referenced by other sessions.
func RedactRawHookSession(metadataDir string, sessionID primitives.SessionID, dryRun bool) ([]string, error) {
	parsedSession, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(metadataDir, "log", "adapter")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read adapter log dir: %w", err)
	}
	var changed []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		modified, err := redactRawHookSessionFile(path, parsedSession, dryRun)
		if err != nil {
			return nil, err
		}
		if modified {
			changed = append(changed, path)
		}
	}
	return changed, nil
}

func redactRawHookSessionFile(path string, sessionID primitives.SessionID, dryRun bool) (bool, error) {
	lock, err := filelock.Acquire(path+".lock", 30*time.Second)
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Release() }()

	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read adapter log for session deletion: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	modified := false
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record RawHookRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if rawRecordSessionID(record) != sessionID.String() {
			continue
		}
		modified = true
		tombstone := RawHookRecord{
			Version:    1,
			Adapter:    record.Adapter,
			Hook:       "deleted-session-record",
			ReceivedAt: record.ReceivedAt,
			Error:      "payload deleted by session retention policy",
		}
		encoded, err := json.Marshal(tombstone)
		if err != nil {
			return false, fmt.Errorf("marshal adapter log tombstone: %w", err)
		}
		lines[index] = encoded
	}
	if !modified || dryRun {
		return modified, nil
	}
	return true, replacePrivateFile(path, bytes.Join(lines, []byte{'\n'}))
}

func rawRecordSessionID(record RawHookRecord) string {
	if record.SessionID != "" {
		return record.SessionID
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if len(record.Payload) == 0 || json.Unmarshal(record.Payload, &payload) != nil {
		return ""
	}
	return payload.SessionID
}

func sessionIDFromRawPayload(raw []byte) primitives.SessionID {
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	sessionID, err := primitives.ParseSessionID(payload.SessionID)
	if err != nil {
		return ""
	}
	return sessionID
}

func replacePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".turnal-rewrite-*")
	if err != nil {
		return fmt.Errorf("create adapter log rewrite: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure adapter log rewrite: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write adapter log rewrite: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync adapter log rewrite: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close adapter log rewrite: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install adapter log rewrite: %w", err)
	}
	return nil
}

func hookWorkspaceCWD(raw []byte) (string, error) {
	processCWD, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	payloadCWD := cwdFromPayload(raw)
	if payloadCWD != "" && filepath.IsAbs(payloadCWD) {
		return payloadCWD, nil
	}
	return processCWD, nil
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

func nextJSONLLineNumber(path string, version int) (int, error) {
	if version == 2 {
		return nextV2JSONLLineNumber(path)
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("open adapter log for line number: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxRawRecordBytes)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan legacy adapter log for line number: %w", err)
	}
	return lines + 1, nil
}

func nextV2JSONLLineNumber(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("open adapter log tail: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat adapter log tail: %w", err)
	}
	if info.Size() == 0 {
		return 1, nil
	}
	readSize := info.Size()
	if readSize > int64(maxRawRecordBytes+2) {
		readSize = int64(maxRawRecordBytes + 2)
	}
	data := make([]byte, readSize)
	if _, err := file.ReadAt(data, info.Size()-readSize); err != nil && err != io.EOF {
		return 0, fmt.Errorf("read adapter log tail: %w", err)
	}
	data = bytes.TrimSuffix(data, []byte{'\n'})
	if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
		data = data[index+1:]
	} else if info.Size() > readSize {
		return 0, fmt.Errorf("adapter log tail record exceeds %d-byte limit", maxRawRecordBytes)
	}
	var tail struct {
		Sequence uint64 `json:"seq"`
	}
	if err := json.Unmarshal(data, &tail); err != nil {
		return 0, fmt.Errorf("parse adapter log tail: %w", err)
	}
	if tail.Sequence == 0 || tail.Sequence >= uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("adapter log tail has invalid sequence %d", tail.Sequence)
	}
	return int(tail.Sequence + 1), nil
}

func recoverTrailingPartialJSONL(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open JSONL log for recovery: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat JSONL log for recovery: %w", err)
	}
	if info.Size() == 0 {
		return false, nil
	}
	last := []byte{0}
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return false, fmt.Errorf("read JSONL log tail for recovery: %w", err)
	}
	if last[0] == '\n' {
		return false, nil
	}
	truncateAt := int64(0)
	const chunkSize = int64(64 * 1024)
	for end := info.Size(); end > 0; {
		start := end - chunkSize
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, end-start)
		if _, err := file.ReadAt(chunk, start); err != nil && err != io.EOF {
			return false, fmt.Errorf("scan JSONL log tail for recovery: %w", err)
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			truncateAt = start + int64(index) + 1
			break
		}
		end = start
	}
	if err := file.Truncate(truncateAt); err != nil {
		return false, fmt.Errorf("recover JSONL log trailing partial line: %w", err)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync recovered JSONL log: %w", err)
	}
	return true, nil
}
