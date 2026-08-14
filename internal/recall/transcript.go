package recall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fsidentity"
	"github.com/AadiJo/turnal/internal/primitives"
)

const maxTranscriptBytes = 64 * 1024 * 1024

type Transcript struct {
	Path     string                 `json:"path"`
	Adapter  primitives.AdapterName `json:"adapter,omitempty"`
	Messages []TranscriptMessage    `json:"messages,omitempty"`
	Errors   []string               `json:"errors,omitempty"`
}

type TranscriptMessage struct {
	Index     int    `json:"index"`
	Role      string `json:"role"`
	ID        string `json:"id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Text      string `json:"text"`
}

type promptContext struct {
	Text            string
	ProviderTurnID  string
	Ordinal         int
	SameTextOrdinal int
}

type transcriptLocator struct {
	Path    string
	Adapter primitives.AdapterName
}

func (reader Reader) transcript(turn Turn, allEvents []eventlog.Event) *Transcript {
	locator, locatorErrors := transcriptLocatorFromSessionEvents(turn.SessionEvents)
	if importedSession(turn.SessionEvents) {
		transcript := &Transcript{Path: locator.Path, Adapter: locator.Adapter, Errors: locatorErrors}
		for _, event := range turn.Events {
			role := ""
			switch event.Type {
			case primitives.EventTypePromptUser:
				role = "user"
			case primitives.EventTypeAssistantMessage:
				role = "assistant"
			default:
				continue
			}
			var payload struct {
				Text           string `json:"text"`
				ProviderTurnID string `json:"provider_turn_id"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.Text) == "" {
				continue
			}
			transcript.Messages = append(transcript.Messages, TranscriptMessage{
				Index: len(transcript.Messages) + 1, Role: role, TurnID: payload.ProviderTurnID,
				Timestamp: event.Time.String(), Text: payload.Text,
			})
		}
		return transcript
	}
	transcript := &Transcript{
		Path:    locator.Path,
		Adapter: locator.Adapter,
		Errors:  locatorErrors,
	}
	if locator.Path == "" {
		transcript.Errors = append(transcript.Errors, "transcript_path was not captured for this session")
		return transcript
	}

	messages, readErrors := readTranscriptMessages(locator.Path, locator.Adapter)
	transcript.Errors = append(transcript.Errors, readErrors...)
	if len(messages) == 0 {
		return transcript
	}

	context, ok := promptContextForTurn(allEvents, turn.TurnID)
	if !ok {
		transcript.Errors = append(transcript.Errors, "turn prompt was not found in the event log")
		return transcript
	}

	selected, selectionErrors := selectTurnMessages(messages, context)
	transcript.Messages = selected
	transcript.Errors = append(transcript.Errors, selectionErrors...)
	return transcript
}

func importedSession(events []eventlog.Event) bool {
	for _, event := range events {
		if event.Type == primitives.EventTypeSessionImport {
			return true
		}
	}
	return false
}

func transcriptLocatorFromSessionEvents(events []eventlog.Event) (transcriptLocator, []string) {
	var errors []string
	for _, event := range events {
		if event.Type != primitives.EventTypeSessionStart {
			continue
		}
		var payload sessionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			errors = append(errors, fmt.Sprintf("malformed session.start payload: %v", err))
			continue
		}
		if strings.TrimSpace(payload.TranscriptPath) == "" {
			continue
		}
		return transcriptLocator{
			Path:    payload.TranscriptPath,
			Adapter: event.Adapter,
		}, errors
	}
	return transcriptLocator{}, errors
}

func readTranscriptMessages(path string, adapter primitives.AdapterName) ([]TranscriptMessage, []string) {
	cleanedPath, err := validateTranscriptPath(path, adapter)
	if err != nil {
		return nil, []string{err.Error()}
	}

	info, err := os.Stat(cleanedPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("read transcript: %v", err)}
	}
	if info.IsDir() {
		return nil, []string{"transcript path is a directory"}
	}
	if !info.Mode().IsRegular() {
		return nil, []string{"transcript path is not a regular file"}
	}
	if info.Size() > maxTranscriptBytes {
		return nil, []string{fmt.Sprintf("transcript file is too large: %d bytes exceeds %d", info.Size(), maxTranscriptBytes)}
	}

	data, err := os.ReadFile(cleanedPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("read transcript: %v", err)}
	}
	return parseTranscriptData(data)
}

func validateTranscriptPath(path string, adapter primitives.AdapterName) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("transcript path is empty")
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("transcript path must not contain NUL")
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("transcript path must be absolute: %s", path)
	}
	if pathHasSegment(cleaned, ".git") {
		return "", fmt.Errorf("transcript path must not point inside .git: %s", cleaned)
	}

	resolved := cleaned
	if evaluated, err := filepath.EvalSymlinks(cleaned); err == nil {
		resolved = evaluated
		if pathHasSegment(resolved, ".git") {
			return "", fmt.Errorf("transcript path must not resolve inside .git: %s", resolved)
		}
	}

	roots, err := transcriptAllowedRoots(adapter)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		if pathWithinRoot(resolved, root) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("transcript path is outside allowed %s transcript roots: %s", adapter, resolved)
}

func transcriptAllowedRoots(adapter primitives.AdapterName) ([]string, error) {
	switch adapter {
	case primitives.AdapterClaudeCode:
		if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
			root, err := cleanAbsoluteRoot(configured)
			if err != nil {
				return nil, fmt.Errorf("invalid CLAUDE_CONFIG_DIR: %w", err)
			}
			return []string{root}, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory for Claude transcript root: %w", err)
		}
		return []string{filepath.Join(home, ".claude")}, nil
	case primitives.AdapterCodex:
		if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
			root, err := cleanAbsoluteRoot(configured)
			if err != nil {
				return nil, fmt.Errorf("invalid CODEX_HOME: %w", err)
			}
			return []string{root}, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory for Codex transcript root: %w", err)
		}
		return []string{filepath.Join(home, ".codex")}, nil
	default:
		return nil, fmt.Errorf("transcript adapter %q is not supported", adapter)
	}
}

func cleanAbsoluteRoot(root string) (string, error) {
	if strings.ContainsRune(root, 0) {
		return "", fmt.Errorf("must not contain NUL")
	}
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("must be absolute: %s", root)
	}
	return cleaned, nil
}

func pathWithinRoot(path, root string) bool {
	return fsidentity.Within(path, root)
}

func pathHasSegment(path string, segment string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

func parseTranscriptData(data []byte) ([]TranscriptMessage, []string) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, []string{"transcript file is empty"}
	}

	switch trimmed[0] {
	case '[':
		var records []json.RawMessage
		if err := json.Unmarshal(trimmed, &records); err != nil {
			return nil, []string{fmt.Sprintf("parse transcript JSON array: %v", err)}
		}
		return parseTranscriptRecords(records, 1)
	case '{':
		if json.Valid(trimmed) {
			records, ok, err := recordsFromJSONContainer(trimmed)
			if err != nil {
				return nil, []string{err.Error()}
			}
			if ok {
				return parseTranscriptRecords(records, 1)
			}
			return parseTranscriptRecords([]json.RawMessage{append(json.RawMessage(nil), trimmed...)}, 1)
		}
	}
	return parseTranscriptJSONL(data)
}

func recordsFromJSONContainer(data []byte) ([]json.RawMessage, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, false, fmt.Errorf("parse transcript JSON object: %w", err)
	}
	for _, key := range []string{"messages", "items", "entries", "events", "conversation"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var records []json.RawMessage
		if err := json.Unmarshal(raw, &records); err != nil {
			return nil, false, fmt.Errorf("parse transcript %s array: %w", key, err)
		}
		return records, true, nil
	}
	return nil, false, nil
}

func parseTranscriptJSONL(data []byte) ([]TranscriptMessage, []string) {
	lines := bytes.Split(data, []byte{'\n'})
	var records []json.RawMessage
	var errors []string
	for i, line := range lines {
		lineNumber := i + 1
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			errors = append(errors, fmt.Sprintf("line %d malformed JSON", lineNumber))
			continue
		}
		records = append(records, append(json.RawMessage(nil), line...))
	}
	messages, recordErrors := parseTranscriptRecords(records, 1)
	return messages, append(errors, recordErrors...)
}

func parseTranscriptRecords(records []json.RawMessage, startIndex int) ([]TranscriptMessage, []string) {
	messages := make([]TranscriptMessage, 0, len(records))
	var errors []string
	for i, record := range records {
		message, ok, err := parseTranscriptRecord(startIndex+i, record)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}
		if !ok {
			continue
		}
		messages = append(messages, message)
	}
	return messages, errors
}

func parseTranscriptRecord(index int, raw json.RawMessage) (TranscriptMessage, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return TranscriptMessage{}, false, fmt.Errorf("record %d malformed JSON: %w", index, err)
	}

	messageObject := map[string]json.RawMessage{}
	if rawMessage, ok := object["message"]; ok {
		_ = json.Unmarshal(rawMessage, &messageObject)
	} else if rawPayload, ok := object["payload"]; ok {
		_ = json.Unmarshal(rawPayload, &messageObject)
	}

	role := firstString(object, "role", "author_role")
	if role == "" {
		role = firstString(messageObject, "role", "author_role")
	}
	if role == "" {
		recordType := firstString(object, "type")
		switch normalizeRole(recordType) {
		case "assistant", "user", "system":
			role = recordType
		}
	}
	role = normalizeRole(role)
	if role == "" {
		return TranscriptMessage{}, false, nil
	}

	textParts := extractTextParts(object["text"])
	textParts = append(textParts, extractTextParts(object["content"])...)
	textParts = append(textParts, extractTextParts(object["output"])...)
	textParts = append(textParts, extractTextParts(messageObject["text"])...)
	textParts = append(textParts, extractTextParts(messageObject["content"])...)
	text := strings.Join(nonEmptyStrings(textParts), "\n")
	if strings.TrimSpace(text) == "" {
		return TranscriptMessage{}, false, nil
	}

	return TranscriptMessage{
		Index:     index,
		Role:      role,
		ID:        firstNonEmpty(firstString(object, "id", "uuid", "message_id", "request_id"), firstString(messageObject, "id", "uuid", "message_id", "request_id")),
		ParentID:  firstNonEmpty(firstString(object, "parent_id", "parent_uuid", "parentUuid"), firstString(messageObject, "parent_id", "parent_uuid", "parentUuid")),
		TurnID:    firstNonEmpty(firstString(object, "turn_id", "turnId", "conversation_turn"), firstString(messageObject, "turn_id", "turnId", "conversation_turn")),
		Timestamp: firstString(object, "timestamp", "created_at", "time"),
		Text:      text,
	}, true, nil
}

func extractTextParts(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		var parts []string
		for _, item := range items {
			parts = append(parts, extractTextParts(item)...)
		}
		return parts
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}

	var parts []string
	for _, key := range []string{"text", "content", "output_text", "value"} {
		parts = append(parts, extractTextParts(object[key])...)
	}
	return parts
}

func firstString(object map[string]json.RawMessage, keys ...string) string {
	if object == nil {
		return ""
	}
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "agent":
		return "assistant"
	case "user", "human":
		return "user"
	case "system":
		return "system"
	default:
		return ""
	}
}

func promptContextForTurn(events []eventlog.Event, turnID primitives.TurnID) (promptContext, bool) {
	var ordinal int
	sameTextCounts := map[string]int{}
	for _, event := range events {
		if event.Type != primitives.EventTypePromptUser || event.TurnID == nil {
			continue
		}
		var payload promptPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		ordinal++
		normalizedText := normalizePromptText(payload.Text)
		sameTextCounts[normalizedText]++
		if *event.TurnID != turnID {
			continue
		}
		return promptContext{
			Text:            payload.Text,
			ProviderTurnID:  payload.ProviderTurnID,
			Ordinal:         ordinal,
			SameTextOrdinal: sameTextCounts[normalizedText],
		}, true
	}
	return promptContext{}, false
}

func selectTurnMessages(messages []TranscriptMessage, context promptContext) ([]TranscriptMessage, []string) {
	start := -1
	if context.ProviderTurnID != "" {
		for i, message := range messages {
			if message.ID == context.ProviderTurnID || message.TurnID == context.ProviderTurnID {
				start = i
				break
			}
		}
	}

	if start == -1 && context.Text != "" {
		sameTextSeen := 0
		normalizedTarget := normalizePromptText(context.Text)
		for i, message := range messages {
			if message.Role != "user" {
				continue
			}
			if !promptTextMatches(normalizedTarget, normalizePromptText(message.Text)) {
				continue
			}
			sameTextSeen++
			if sameTextSeen == context.SameTextOrdinal {
				start = i
				break
			}
		}
	}

	if start == -1 && context.Ordinal > 0 {
		userSeen := 0
		for i, message := range messages {
			if message.Role != "user" {
				continue
			}
			userSeen++
			if userSeen == context.Ordinal {
				start = i
				break
			}
		}
	}

	if start == -1 {
		return nil, []string{"turn prompt was not found in transcript"}
	}
	if messages[start].Role == "assistant" {
		for i := start - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				start = i
				break
			}
		}
	}

	end := len(messages)
	for i := start + 1; i < len(messages); i++ {
		if messages[i].Role == "user" {
			end = i
			break
		}
	}

	var selected []TranscriptMessage
	var assistantCount int
	for _, message := range messages[start:end] {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if message.Role == "assistant" {
			assistantCount++
		}
		selected = append(selected, message)
	}
	if assistantCount == 0 {
		return nil, []string{"no assistant transcript messages found for turn"}
	}
	return selected, nil
}

func normalizePromptText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func promptTextMatches(target, candidate string) bool {
	if target == "" || candidate == "" {
		return false
	}
	return target == candidate || strings.Contains(candidate, target) || strings.Contains(target, candidate)
}
