package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

type cursorTranscriptEntry struct {
	Role    string `json:"role"`
	Status  string `json:"status"`
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// FinalizeCursorRun fills lifecycle events omitted by Cursor CLI's hook
// surface from the provider-owned settled transcript after the child exits.
func FinalizeCursorRun(repo *checkpoint.Repo, runID primitives.RunID) error {
	projection, err := runs.Read(repo, runID)
	if err != nil {
		return err
	}
	for _, capture := range projection.Captures {
		if capture.Kind != runs.CaptureProvider || capture.Adapter != primitives.AdapterCursor {
			continue
		}
		prompt, assistant, ok := completedCursorTranscript(capture.SessionID)
		if !ok {
			return fmt.Errorf("Cursor transcript did not contain a successful settled turn for session %s", capture.SessionID)
		}
		existing, err := repo.EventLog().Read(capture.SessionID)
		if err != nil {
			return err
		}
		hasPrompt := false
		hasAssistant := false
		for _, event := range existing {
			hasPrompt = hasPrompt || event.Type == primitives.EventTypePromptUser
			hasAssistant = hasAssistant || event.Type == primitives.EventTypeAssistantMessage
		}
		if hasAssistant {
			continue
		}
		raw, err := json.Marshal(map[string]any{
			"session_id": capture.SessionID,
			"cwd":        repo.WorkspaceRoot.String(),
			"prompt":     prompt,
			"response":   assistant,
		})
		if err != nil {
			return err
		}
		var normalized []adaptersdk.Event
		if !hasPrompt {
			normalized = append(normalized, adaptersdk.Event{
				Type:      adaptersdk.EventPromptUser,
				SessionID: capture.SessionID.String(),
				CWD:       repo.WorkspaceRoot.String(),
				Text:      prompt,
				SourceID:  capture.SessionID.String() + ":transcript:prompt",
			})
		}
		normalized = append(normalized, adaptersdk.Event{
			Type:      adaptersdk.EventAssistantMessage,
			SessionID: capture.SessionID.String(),
			CWD:       repo.WorkspaceRoot.String(),
			Text:      assistant,
			SourceID:  capture.SessionID.String() + ":transcript:assistant",
		})
		if err := HandleNormalizedEventsWithRunID(primitives.AdapterCursor, "transcript", raw, normalized, runID.String()); err != nil {
			return err
		}
	}
	return nil
}

func completedCursorTranscript(sessionID primitives.SessionID) (string, string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	cursorHome := filepath.Join(home, ".cursor")
	if configured := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR")); configured != "" {
		cursorHome = configured
	}
	pattern := filepath.Join(cursorHome, "projects", "*", "agent-transcripts", sessionID.String(), sessionID.String()+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) != 1 {
		return "", "", false
	}
	file, err := os.Open(matches[0])
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRawRecordBytes)
	prompt := ""
	assistant := ""
	completed := false
	for scanner.Scan() {
		var entry cursorTranscriptEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		switch entry.Role {
		case "user":
			prompt = cursorTranscriptText(entry, true)
			assistant = ""
			completed = false
		case "assistant":
			assistant = cursorTranscriptText(entry, false)
		case "":
			if entry.Type == "turn_ended" && entry.Status == "success" && prompt != "" && assistant != "" {
				completed = true
			}
		}
	}
	return prompt, assistant, scanner.Err() == nil && completed
}

func cursorTranscriptText(entry cursorTranscriptEntry, unwrapQuery bool) string {
	var text strings.Builder
	for _, block := range entry.Message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	value := text.String()
	if !unwrapQuery {
		return value
	}
	const start = "<user_query>\n"
	const end = "\n</user_query>"
	begin := strings.Index(value, start)
	finish := strings.LastIndex(value, end)
	if begin < 0 || finish < begin+len(start) {
		return value
	}
	return value[begin+len(start) : finish]
}
