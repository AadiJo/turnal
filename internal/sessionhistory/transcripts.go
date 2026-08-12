package sessionhistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/fsidentity"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	DefaultImportLookback = 30 * 24 * time.Hour
	maxTranscriptBytes    = 64 << 20
	maxTranscriptFiles    = 10_000
)

var errNotProviderTranscript = errors.New("not a supported provider transcript")

type DiscoverOptions struct {
	Adapter       primitives.AdapterName
	WorkspaceRoot primitives.WorkspaceRoot
	Path          string
	SessionIDs    []string
	Now           time.Time
}

type Candidate struct {
	Adapter           primitives.AdapterName `json:"adapter"`
	ProviderSessionID string                 `json:"provider_session_id"`
	SessionID         primitives.SessionID   `json:"session_id"`
	Path              string                 `json:"path"`
	WorkspaceRoot     string                 `json:"workspace_root"`
	Model             string                 `json:"model,omitempty"`
	StartedAt         time.Time              `json:"started_at,omitempty"`
	ModifiedAt        time.Time              `json:"modified_at"`
	SHA256            string                 `json:"sha256"`
	Turns             []TranscriptTurn       `json:"turns"`
	Warnings          []string               `json:"warnings,omitempty"`
}

type TranscriptTurn struct {
	ProviderTurnID string           `json:"provider_turn_id,omitempty"`
	Model          string           `json:"model,omitempty"`
	Prompt         string           `json:"prompt"`
	Assistant      string           `json:"assistant,omitempty"`
	StartedAt      time.Time        `json:"started_at,omitempty"`
	FinishedAt     time.Time        `json:"finished_at,omitempty"`
	Tools          []TranscriptTool `json:"tools,omitempty"`
}

type TranscriptTool struct {
	Name       string          `json:"name"`
	UseID      string          `json:"use_id,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	CalledAt   time.Time       `json:"called_at,omitempty"`
	ReturnedAt time.Time       `json:"returned_at,omitempty"`
}

type transcriptAdapter interface {
	Name() primitives.AdapterName
	DefaultRoot() (string, error)
	Parse(path string, data []byte, modifiedAt time.Time) (Candidate, error)
}

func Discover(options DiscoverOptions) ([]Candidate, []string, error) {
	adapter, err := importAdapter(options.Adapter)
	if err != nil {
		return nil, nil, err
	}
	root := strings.TrimSpace(options.Path)
	customRoot := root != ""
	if root == "" {
		root, err = adapter.DefaultRoot()
		if err != nil {
			return nil, nil, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve transcript directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) && !customRoot {
			return nil, []string{fmt.Sprintf("%s transcript directory does not exist: %s", adapter.Name(), root)}, nil
		}
		return nil, nil, fmt.Errorf("inspect transcript directory %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("transcript path is not a directory: %s", root)
	}

	selected := make(map[string]struct{}, len(options.SessionIDs))
	for _, value := range options.SessionIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil, fmt.Errorf("session filter must not be empty")
		}
		selected[strings.ToLower(value)] = struct{}{}
	}
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.Add(-DefaultImportLookback)
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		if len(paths) >= maxTranscriptFiles {
			return fmt.Errorf("transcript directory contains more than %d JSONL files", maxTranscriptFiles)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if len(selected) == 0 && fileInfo.ModTime().Before(cutoff) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan transcript directory: %w", err)
	}
	sort.Strings(paths)

	var candidates []Candidate
	var warnings []string
	foundSelected := make(map[string]struct{})
	for _, path := range paths {
		fileInfo, err := os.Stat(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if !fileInfo.Mode().IsRegular() {
			continue
		}
		if fileInfo.Size() > maxTranscriptBytes {
			warnings = append(warnings, fmt.Sprintf("%s: transcript exceeds %d-byte limit", path, maxTranscriptBytes))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		candidate, err := adapter.Parse(path, data, fileInfo.ModTime().UTC())
		if errors.Is(err, errNotProviderTranscript) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if _, ok := selected[strings.ToLower(candidate.ProviderSessionID)]; len(selected) > 0 && !ok {
			continue
		}
		if !fsidentity.Same(candidate.WorkspaceRoot, options.WorkspaceRoot.String()) {
			continue
		}
		if len(selected) > 0 {
			foundSelected[strings.ToLower(candidate.ProviderSessionID)] = struct{}{}
		}
		candidate.SessionID = importedSessionID(candidate.Adapter, candidate.ProviderSessionID)
		candidate.SHA256 = sha256Hex(data)
		candidates = append(candidates, candidate)
	}
	for requested := range selected {
		if _, ok := foundSelected[requested]; !ok {
			warnings = append(warnings, fmt.Sprintf("session %s was not found in %s transcripts", requested, adapter.Name()))
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].StartedAt.Equal(candidates[j].StartedAt) {
			return candidates[i].StartedAt.Before(candidates[j].StartedAt)
		}
		return candidates[i].ProviderSessionID < candidates[j].ProviderSessionID
	})
	return candidates, warnings, nil
}

func importedSessionID(adapter primitives.AdapterName, providerSessionID string) primitives.SessionID {
	if parsed, err := primitives.ParseSessionID(providerSessionID); err == nil {
		return parsed
	}
	digest := sha256.Sum256([]byte(adapter.String() + "\x00" + providerSessionID))
	parsed, _ := primitives.ParseSessionID("import-" + hex.EncodeToString(digest[:16]))
	return parsed
}

func importAdapter(name primitives.AdapterName) (transcriptAdapter, error) {
	switch name {
	case primitives.AdapterClaudeCode:
		return claudeImportAdapter{}, nil
	case primitives.AdapterCodex:
		return codexImportAdapter{}, nil
	default:
		return nil, fmt.Errorf("transcript import does not support adapter %q; supported adapters: claude-code, codex", name)
	}
}

type claudeImportAdapter struct{}

func (claudeImportAdapter) Name() primitives.AdapterName { return primitives.AdapterClaudeCode }

func (claudeImportAdapter) DefaultRoot() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		return filepath.Join(configured, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Claude transcript directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

type claudeRecord struct {
	UUID        string          `json:"uuid"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	Timestamp   string          `json:"timestamp"`
	PromptID    string          `json:"promptId"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type providerMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

func (claudeImportAdapter) Parse(path string, data []byte, modifiedAt time.Time) (Candidate, error) {
	records, warnings := decodeJSONLLines[claudeRecord](data)
	candidate := Candidate{Adapter: primitives.AdapterClaudeCode, Path: path, ModifiedAt: modifiedAt, Warnings: warnings}
	var current *TranscriptTurn
	toolIndexes := map[string]int{}
	finishCurrent := func() {
		if current != nil && strings.TrimSpace(current.Prompt) != "" {
			current.Prompt = strings.TrimSpace(current.Prompt)
			current.Assistant = strings.TrimSpace(current.Assistant)
			candidate.Turns = append(candidate.Turns, *current)
		}
		current = nil
		toolIndexes = map[string]int{}
	}
	for _, item := range records {
		record := item.Value
		if record.IsSidechain {
			continue
		}
		if candidate.ProviderSessionID == "" {
			candidate.ProviderSessionID = strings.TrimSpace(record.SessionID)
		}
		if candidate.WorkspaceRoot == "" && strings.TrimSpace(record.CWD) != "" {
			candidate.WorkspaceRoot = filepath.Clean(record.CWD)
		}
		at := parseProviderTime(record.Timestamp)
		if candidate.StartedAt.IsZero() && !at.IsZero() {
			candidate.StartedAt = at
		}
		var message providerMessage
		if json.Unmarshal(record.Message, &message) != nil {
			continue
		}
		blocks := decodeContentBlocks(message.Content)
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "user":
			prompt := contentText(message.Content, blocks, "text")
			if prompt != "" {
				finishCurrent()
				providerTurnID := firstNonEmpty(record.PromptID, record.UUID)
				current = &TranscriptTurn{ProviderTurnID: providerTurnID, Prompt: prompt, StartedAt: at}
			}
			if current == nil {
				continue
			}
			for _, block := range blocks {
				if block.Type != "tool_result" {
					continue
				}
				output := normalizeJSONContent(block.Content)
				if index, ok := toolIndexes[block.ToolUseID]; ok {
					current.Tools[index].Output = output
					current.Tools[index].ReturnedAt = at
					continue
				}
				current.Tools = append(current.Tools, TranscriptTool{Name: "tool", UseID: block.ToolUseID, Output: output, ReturnedAt: at})
				toolIndexes[block.ToolUseID] = len(current.Tools) - 1
			}
		case "assistant":
			if current == nil {
				continue
			}
			if candidate.Model == "" {
				candidate.Model = strings.TrimSpace(message.Model)
			}
			if current.Model == "" {
				current.Model = strings.TrimSpace(message.Model)
			}
			if text := contentText(message.Content, blocks, "text"); text != "" {
				current.Assistant = joinText(current.Assistant, text)
				current.FinishedAt = at
			}
			for _, block := range blocks {
				if block.Type != "tool_use" || strings.TrimSpace(block.Name) == "" {
					continue
				}
				current.Tools = append(current.Tools, TranscriptTool{Name: block.Name, UseID: block.ID, Input: defaultJSON(block.Input), CalledAt: at})
				toolIndexes[block.ID] = len(current.Tools) - 1
			}
		}
	}
	finishCurrent()
	if candidate.ProviderSessionID == "" || candidate.WorkspaceRoot == "" {
		return Candidate{}, errNotProviderTranscript
	}
	if len(candidate.Turns) == 0 {
		candidate.Warnings = append(candidate.Warnings, "transcript contains no importable prompt turns")
	}
	return candidate, nil
}

type codexImportAdapter struct{}

func (codexImportAdapter) Name() primitives.AdapterName { return primitives.AdapterCodex }

func (codexImportAdapter) DefaultRoot() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		return filepath.Join(configured, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Codex transcript directory: %w", err)
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CWD              string          `json:"cwd"`
	Model            string          `json:"model"`
	Role             string          `json:"role"`
	Message          string          `json:"message"`
	LastAgentMessage string          `json:"last_agent_message"`
	TurnID           string          `json:"turn_id"`
	Name             string          `json:"name"`
	CallID           string          `json:"call_id"`
	Arguments        json.RawMessage `json:"arguments"`
	Input            json.RawMessage `json:"input"`
	Output           json.RawMessage `json:"output"`
	Content          json.RawMessage `json:"content"`
}

func (codexImportAdapter) Parse(path string, data []byte, modifiedAt time.Time) (Candidate, error) {
	records, warnings := decodeJSONLLines[codexRecord](data)
	candidate := Candidate{Adapter: primitives.AdapterCodex, Path: path, ModifiedAt: modifiedAt, Warnings: warnings}
	var current *TranscriptTurn
	var latestModel, latestTurnID, fallbackAssistant string
	toolIndexes := map[string]int{}
	finishCurrent := func() {
		if current != nil && strings.TrimSpace(current.Prompt) != "" {
			if strings.TrimSpace(current.Assistant) == "" {
				current.Assistant = strings.TrimSpace(fallbackAssistant)
			}
			current.Prompt = strings.TrimSpace(current.Prompt)
			current.Assistant = strings.TrimSpace(current.Assistant)
			candidate.Turns = append(candidate.Turns, *current)
		}
		current = nil
		fallbackAssistant = ""
		toolIndexes = map[string]int{}
	}
	for _, item := range records {
		record := item.Value
		var payload codexPayload
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		at := parseProviderTime(record.Timestamp)
		if candidate.StartedAt.IsZero() && !at.IsZero() {
			candidate.StartedAt = at
		}
		switch record.Type {
		case "session_meta":
			candidate.ProviderSessionID = strings.TrimSpace(payload.ID)
			if strings.TrimSpace(payload.CWD) != "" {
				candidate.WorkspaceRoot = filepath.Clean(payload.CWD)
			}
		case "turn_context":
			if strings.TrimSpace(payload.Model) != "" {
				latestModel = strings.TrimSpace(payload.Model)
				candidate.Model = latestModel
			}
			if strings.TrimSpace(payload.TurnID) != "" {
				latestTurnID = strings.TrimSpace(payload.TurnID)
			}
		case "event_msg":
			switch payload.Type {
			case "user_message":
				finishCurrent()
				current = &TranscriptTurn{ProviderTurnID: latestTurnID, Model: latestModel, Prompt: payload.Message, StartedAt: at}
			case "agent_message":
				if current != nil && strings.TrimSpace(payload.Message) != "" {
					current.Assistant = strings.TrimSpace(payload.Message)
					current.FinishedAt = at
				}
			case "task_complete":
				if current != nil {
					current.Assistant = strings.TrimSpace(payload.LastAgentMessage)
					current.FinishedAt = at
				}
			}
		case "response_item":
			if current == nil {
				continue
			}
			switch payload.Type {
			case "message":
				if payload.Role == "assistant" {
					fallbackAssistant = joinText(fallbackAssistant, extractCodexText(payload.Content))
				}
			case "function_call", "custom_tool_call":
				if strings.TrimSpace(payload.Name) == "" {
					continue
				}
				input := payload.Arguments
				if len(input) == 0 {
					input = payload.Input
				}
				current.Tools = append(current.Tools, TranscriptTool{Name: payload.Name, UseID: payload.CallID, Input: normalizeJSONContent(input), CalledAt: at})
				toolIndexes[payload.CallID] = len(current.Tools) - 1
			case "function_call_output", "custom_tool_call_output":
				if index, ok := toolIndexes[payload.CallID]; ok {
					current.Tools[index].Output = normalizeJSONContent(payload.Output)
					current.Tools[index].ReturnedAt = at
				}
			}
		}
	}
	finishCurrent()
	if candidate.ProviderSessionID == "" || candidate.WorkspaceRoot == "" {
		return Candidate{}, errNotProviderTranscript
	}
	if len(candidate.Turns) == 0 {
		candidate.Warnings = append(candidate.Warnings, "transcript contains no importable prompt turns")
	}
	return candidate, nil
}

type decodedLine[T any] struct {
	Value T
}

func decodeJSONLLines[T any](data []byte) ([]decodedLine[T], []string) {
	lines := bytes.Split(data, []byte{'\n'})
	records := make([]decodedLine[T], 0, len(lines))
	var warnings []string
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var value T
		if err := json.Unmarshal(line, &value); err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d is malformed JSON", index+1))
			continue
		}
		records = append(records, decodedLine[T]{Value: value})
	}
	return records, warnings
}

func decodeContentBlocks(raw json.RawMessage) []contentBlock {
	var blocks []contentBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

func contentText(raw json.RawMessage, blocks []contentBlock, blockType string) string {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return strings.TrimSpace(direct)
	}
	var texts []string
	for _, block := range blocks {
		if block.Type == blockType && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(texts, "\n")
}

func extractCodexText(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		if block.Type == "output_text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(texts, "\n")
}

func normalizeJSONContent(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`null`)
	}
	if json.Valid(raw) {
		var encodedString string
		if json.Unmarshal(raw, &encodedString) == nil && json.Valid([]byte(encodedString)) {
			return json.RawMessage(encodedString)
		}
		return append(json.RawMessage(nil), raw...)
	}
	encoded, _ := json.Marshal(string(raw))
	return encoded
}

func defaultJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(`null`)
	}
	return raw
}

func parseProviderTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func joinText(left, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n\n" + right
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
