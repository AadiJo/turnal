package adapters

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const claudeTranscriptTailLimit int64 = 8 << 20

type claudeTranscriptEntry struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Message   struct {
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeTranscriptContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Claude Code does not guarantee model on SessionStart. When it is absent,
// the completed assistant entry is the narrowest reliable transcript fallback.
func claudeCompletedTurnModel(payload hookPayload) string {
	path := strings.TrimSpace(payload.TranscriptPath)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ""
	}
	if !strings.EqualFold(filepath.Base(path), payload.SessionID+".jsonl") {
		return ""
	}
	lastAssistant := strings.TrimSpace(payload.LastAssistantMessage)
	if lastAssistant == "" {
		return ""
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	start := info.Size() - claudeTranscriptTailLimit
	if start < 0 {
		start = 0
	}
	readStart := start
	readLimit := claudeTranscriptTailLimit
	if start > 0 {
		// Include the preceding byte so a tail that starts exactly on a record
		// boundary does not accidentally discard its first complete record.
		readStart--
		readLimit++
	}
	if _, err := file.Seek(readStart, io.SeekStart); err != nil {
		return ""
	}

	scanner := bufio.NewScanner(io.LimitReader(file, readLimit))
	scanner.Buffer(make([]byte, 64<<10), int(readLimit))
	if start > 0 {
		// The tail normally starts mid-record. Discard that partial JSON line.
		scanner.Scan()
	}

	model := ""
	for scanner.Scan() {
		var entry claudeTranscriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || entry.Message.Role != "assistant" {
			continue
		}
		if !strings.EqualFold(entry.SessionID, payload.SessionID) || !sameCleanPath(entry.CWD, payload.CWD) {
			continue
		}
		text, ok := claudeTranscriptText(entry.Message.Content)
		if !ok || strings.TrimSpace(text) != lastAssistant {
			continue
		}
		if reported := strings.TrimSpace(entry.Message.Model); reported != "" {
			model = reported
		}
	}
	return model
}

func claudeTranscriptText(raw json.RawMessage) (string, bool) {
	var blocks []claudeTranscriptContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var text strings.Builder
	found := false
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		text.WriteString(block.Text)
		found = true
	}
	return text.String(), found
}

func sameCleanPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}
