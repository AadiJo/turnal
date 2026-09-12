package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

const copilotTranscriptTailLimit int64 = 8 << 20

type copilotTranscriptEntry struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type copilotSessionStartData struct {
	SessionID string `json:"sessionId"`
	Context   struct {
		CWD string `json:"cwd"`
	} `json:"context"`
}

type copilotAssistantData struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

func copilotCompletedAssistant(path, sessionID, cwd string) (string, string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "events.jsonl" {
		return "", "", false
	}
	if filepath.Base(filepath.Dir(path)) != sessionID || filepath.Base(filepath.Dir(filepath.Dir(path))) != "session-state" {
		return "", "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", "", false
	}

	first := bufio.NewScanner(io.LimitReader(file, 1<<20))
	first.Buffer(make([]byte, 64<<10), 1<<20)
	if !first.Scan() || !validCopilotTranscriptStart(first.Bytes(), sessionID, cwd) {
		return "", "", false
	}

	start := info.Size() - copilotTranscriptTailLimit
	if start < 0 {
		start = 0
	}
	readStart := start
	readLimit := copilotTranscriptTailLimit
	if start > 0 {
		readStart--
		readLimit++
	}
	if _, err := file.Seek(readStart, io.SeekStart); err != nil {
		return "", "", false
	}
	scanner := bufio.NewScanner(io.LimitReader(file, readLimit))
	scanner.Buffer(make([]byte, 64<<10), int(readLimit))
	if start > 0 {
		scanner.Scan()
	}
	text := ""
	model := ""
	for scanner.Scan() {
		var entry copilotTranscriptEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "assistant.message" {
			continue
		}
		var data copilotAssistantData
		if json.Unmarshal(entry.Data, &data) != nil || strings.TrimSpace(data.Content) == "" {
			continue
		}
		text = data.Content
		model = strings.TrimSpace(data.Model)
	}
	return text, model, text != ""
}

func validCopilotTranscriptStart(raw []byte, sessionID, cwd string) bool {
	var entry copilotTranscriptEntry
	if json.Unmarshal(raw, &entry) != nil || entry.Type != "session.start" {
		return false
	}
	var data copilotSessionStartData
	return json.Unmarshal(entry.Data, &data) == nil && data.SessionID == sessionID && sameCleanPath(data.Context.CWD, cwd)
}

// FinalizeCopilotRun hydrates agentStop after the wrapped process has flushed
// its transcript, preserving the same assistant and checkpoint ordering as
// providers that include assistant text directly in their stop hook.
func FinalizeCopilotRun(repo *checkpoint.Repo, runID primitives.RunID) error {
	projection, err := runs.Read(repo, runID)
	if err != nil {
		return err
	}
	for _, capture := range projection.Captures {
		if capture.Kind != runs.CaptureProvider || capture.Adapter != primitives.AdapterCopilotCLI {
			continue
		}
		record, err := latestCopilotStopRecord(repo.MetadataDir, capture.SessionID)
		if err != nil {
			return err
		}
		var payload struct {
			SessionID      string `json:"sessionId"`
			CWD            string `json:"cwd"`
			TranscriptPath string `json:"transcriptPath"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return fmt.Errorf("decode GitHub Copilot CLI stop payload: %w", err)
		}
		text, model, ok := copilotCompletedAssistant(payload.TranscriptPath, payload.SessionID, payload.CWD)
		if !ok {
			return fmt.Errorf("GitHub Copilot CLI transcript did not contain a settled assistant response for session %s", capture.SessionID)
		}
		event := adaptersdk.Event{
			Type:           adaptersdk.EventAssistantMessage,
			SessionID:      payload.SessionID,
			CWD:            payload.CWD,
			Text:           text,
			Model:          model,
			TranscriptPath: payload.TranscriptPath,
		}
		if err := HandleNormalizedEventsWithRunID(primitives.AdapterCopilotCLI, "agentStop", record.Payload, []adaptersdk.Event{event}, runID.String()); err != nil {
			return err
		}
	}
	return nil
}

func latestCopilotStopRecord(metadataDir string, sessionID primitives.SessionID) (RawHookRecord, error) {
	path := filepath.Join(metadataDir, "log", "raw", sessionID.String(), primitives.AdapterCopilotCLI.String()+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return RawHookRecord{}, fmt.Errorf("open GitHub Copilot CLI raw hook log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRawRecordBytes)
	var latest RawHookRecord
	for scanner.Scan() {
		var record RawHookRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil && normalizedHookName(record.Hook) == "agentstop" {
			latest = record
		}
	}
	if err := scanner.Err(); err != nil {
		return RawHookRecord{}, fmt.Errorf("read GitHub Copilot CLI raw hook log: %w", err)
	}
	if latest.Hook == "" {
		return RawHookRecord{}, fmt.Errorf("GitHub Copilot CLI run has no agentStop hook for session %s", sessionID)
	}
	return latest, nil
}

func normalizedHookName(value string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(value))
}
