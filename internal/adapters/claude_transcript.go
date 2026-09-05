package adapters

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/usage"
)

const claudeTranscriptTailLimit int64 = 8 << 20

type claudeTranscriptEntry struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
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

	file, err := openTranscriptFile(path)
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

// claudeCumulativeUsage sums the final streamed record for each provider
// message. Claude repeats message ids while streaming output tokens.
func claudeCumulativeUsage(payload hookPayload) *transcriptUsage {
	path := strings.TrimSpace(payload.TranscriptPath)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!strings.EqualFold(filepath.Base(path), payload.SessionID+".jsonl") {
		return nil
	}
	file, err := openTranscriptFile(path)
	if os.IsNotExist(err) {
		return &transcriptUsage{}
	}
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}

	byMessage := make(map[string]usage.TokenUsage)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var entry claudeTranscriptEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "assistant" || entry.Message.Role != "assistant" ||
			!strings.EqualFold(entry.SessionID, payload.SessionID) || !sameCleanPath(entry.CWD, payload.CWD) || entry.Message.ID == "" || entry.Message.Usage == nil {
			continue
		}
		candidate := usage.TokenUsage{
			InputTokens: entry.Message.Usage.InputTokens, CacheReadTokens: entry.Message.Usage.CacheReadInputTokens,
			CacheWriteTokens: entry.Message.Usage.CacheCreationInputTokens, OutputTokens: entry.Message.Usage.OutputTokens,
		}
		if !candidate.Valid() {
			continue
		}
		current, ok := byMessage[entry.Message.ID]
		if !ok || candidate.OutputTokens > current.OutputTokens {
			byMessage[entry.Message.ID] = candidate
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	total := usage.TokenUsage{APICalls: int64(len(byMessage))}
	for _, item := range byMessage {
		total.Add(item)
	}
	return &transcriptUsage{Total: total, HasUsage: len(byMessage) > 0}
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
