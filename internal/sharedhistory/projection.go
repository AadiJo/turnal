package sharedhistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

type turnSource struct {
	Stream        eventlog.DurableStream
	WorkspaceRoot string
	TurnID        primitives.TurnID
	Events        []eventlog.Event
}

type builtBundle struct {
	Stored     StoredBundle
	EventsJSON []byte
	Manifest   []byte
	Path       string
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]{20,}|sk-[a-z0-9_-]{20,}|akia[0-9a-z]{16})\b`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key)\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`),
}

var absolutePathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:[a-z]:\\Users\\[^\\\s]+\\[^\s]*)`),
	regexp.MustCompile(`\\\\[^\\\s]+\\[^\s]+`),
	regexp.MustCompile(`(?:/(?:home|Users)/[^/\s]+/[^\s]*)`),
	regexp.MustCompile(`(?i)\b[a-z]:[\\/](?:[^\\/\s"'()]+[\\/])*[^\\/\s"'()]+`),
	regexp.MustCompile(`/(?:[a-zA-Z0-9._~-]+/)+[a-zA-Z0-9._~-]+`),
}

func listCompletedTurns(repo *checkpoint.Repo) ([]turnSource, error) {
	worktrees, err := repo.ListWorktrees()
	if err != nil {
		return nil, err
	}
	workspaceRoots := make(map[primitives.WorktreeID]string, len(worktrees))
	for _, worktree := range worktrees {
		workspaceRoots[worktree.WorktreeID] = worktree.Root
	}
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return nil, err
	}
	var turns []turnSource
	for _, stream := range streams {
		if stream.Workspace || stream.RepoID != repo.RepoID || stream.StreamID == "" || stream.ProducerID == "" {
			continue
		}
		byTurn := map[primitives.TurnID][]eventlog.Event{}
		for _, event := range stream.Events {
			if event.TurnID == nil {
				continue
			}
			byTurn[*event.TurnID] = append(byTurn[*event.TurnID], event)
		}
		for turnID, events := range byTurn {
			if turnCompleted(events) {
				turns = append(turns, turnSource{Stream: stream, WorkspaceRoot: workspaceRoots[stream.WorktreeID], TurnID: turnID, Events: events})
			}
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].Stream.SessionID != turns[j].Stream.SessionID {
			return turns[i].Stream.SessionID.String() < turns[j].Stream.SessionID.String()
		}
		if turns[i].TurnID != turns[j].TurnID {
			return turns[i].TurnID < turns[j].TurnID
		}
		return turns[i].Stream.StreamID.String() < turns[j].Stream.StreamID.String()
	})
	return turns, nil
}

func turnCompleted(events []eventlog.Event) bool {
	finished := false
	postCheckpoint := false
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeTurnFinish:
			finished = true
		case primitives.EventTypeCheckpoint:
			var payload struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Phase == primitives.CheckpointPhasePost.String() {
				postCheckpoint = true
			}
		}
	}
	return finished && postCheckpoint
}

func findCompletedTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, streamID primitives.EventStreamID) (turnSource, error) {
	turns, err := listCompletedTurns(repo)
	if err != nil {
		return turnSource{}, err
	}
	var matches []turnSource
	for _, source := range turns {
		if source.Stream.SessionID == sessionID && source.TurnID == turnID && (streamID == "" || source.Stream.StreamID == streamID) {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return turnSource{}, fmt.Errorf("completed turn not found: %s:%s", sessionID, turnID)
	}
	if len(matches) > 1 {
		return turnSource{}, fmt.Errorf("turn %s:%s is ambiguous across %d streams; rerun preview with --stream <stream-id>", sessionID, turnID, len(matches))
	}
	return matches[0], nil
}

func buildBundle(repo *checkpoint.Repo, identity deviceIdentity, policy policyFile, policyDigest string, source turnSource) (builtBundle, error) {
	if strings.TrimSpace(source.WorkspaceRoot) == "" {
		return builtBundle{}, fmt.Errorf("source worktree root is unavailable for stream %s; refusing to project paths without its privacy boundary", source.Stream.StreamID)
	}
	if containsDetectedSecret(source.Stream.SessionID.String()) {
		return builtBundle{}, fmt.Errorf("source session id contains secret-like material; refusing to publish it as manifest metadata")
	}
	for _, event := range source.Events {
		if containsDetectedSecret(event.Adapter.String()) {
			return builtBundle{}, fmt.Errorf("source adapter id contains secret-like material; refusing to publish it as event metadata")
		}
	}
	bundleID, err := primitives.DeriveBundleID(policy.RepoID, source.Stream.StreamID, source.TurnID)
	if err != nil {
		return builtBundle{}, err
	}
	omissions := map[string]int{}
	truncations := Truncations{}
	projected := make([]ContextEvent, 0, len(source.Events))
	sourceRefs := make([]SourceRef, 0, len(source.Events))
	sourceLinks := make([]SourceLink, 0)
	seenLinks := map[string]struct{}{}

	for _, event := range source.Events {
		ref := SourceRef{StreamID: source.Stream.StreamID, Seq: event.Seq, Hash: event.Hash}
		sourceRefs = append(sourceRefs, ref)
		projection, included, links := projectEvent(source.WorkspaceRoot, policy, event, ref, omissions, &truncations)
		if included {
			projected = append(projected, projection)
		}
		for _, link := range links {
			key := link.CommitSHA + "\x00" + link.Checkpoint
			if _, ok := seenLinks[key]; ok {
				continue
			}
			seenLinks[key] = struct{}{}
			sourceLinks = append(sourceLinks, link)
		}
	}
	if len(sourceRefs) == 0 {
		return builtBundle{}, fmt.Errorf("turn %s:%s has no durable events", source.Stream.SessionID, source.TurnID)
	}
	eventsJSON, err := marshalEventsJSONL(projected)
	if err != nil {
		return builtBundle{}, err
	}
	manifest := Manifest{
		SchemaVersion:  SchemaVersion,
		BundleID:       bundleID,
		RepoID:         policy.RepoID,
		DeviceID:       identity.DeviceID,
		ProducerID:     source.Stream.ProducerID,
		StoreID:        repo.StoreID,
		WorktreeID:     source.Stream.WorktreeID,
		StreamID:       source.Stream.StreamID,
		SessionID:      source.Stream.SessionID,
		TurnID:         source.TurnID,
		SourceSequence: SequenceRange{First: sourceRefs[0].Seq, Last: sourceRefs[len(sourceRefs)-1].Seq},
		SourceRefs:     sourceRefs,
		PolicyHash:     policyDigest,
		PromptMode:     policy.PromptMode,
		EvidenceClass:  EvidencePublisherClaim,
		SourceLinks:    sourceLinks,
		Omissions:      omissions,
		Truncations:    truncations,
		ContentHashes:  map[string]string{"events.jsonl": sha256Bytes(eventsJSON)},
		CreatedAt:      source.Events[len(source.Events)-1].Time.Time,
	}
	manifest, err = signManifest(identity, manifest)
	if err != nil {
		return builtBundle{}, err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return builtBundle{}, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if len(manifestJSON)+len(eventsJSON) > policy.BundleLimit {
		return builtBundle{}, fmt.Errorf("bundle %s is %d bytes after projection; limit is %d", bundleID, len(manifestJSON)+len(eventsJSON), policy.BundleLimit)
	}
	return builtBundle{
		Stored:     StoredBundle{Manifest: manifest, Events: projected, PublicKey: identity.PublicKey},
		EventsJSON: eventsJSON,
		Manifest:   manifestJSON,
		Path:       bundlePath(bundleID),
	}, nil
}

func projectEvent(workspaceRoot string, policy policyFile, event eventlog.Event, ref SourceRef, omissions map[string]int, truncations *Truncations) (ContextEvent, bool, []SourceLink) {
	projection := ContextEvent{SchemaVersion: SchemaVersion, Type: event.Type, Seq: event.Seq, Time: event.Time, Adapter: event.Adapter, Source: ref}
	switch event.Type {
	case primitives.EventTypeTurnStart:
		projection.Lifecycle = &LifecycleProjection{State: "started"}
	case primitives.EventTypeTurnFinish:
		projection.Lifecycle = &LifecycleProjection{State: "finished"}
	case primitives.EventTypePromptUser:
		var payload struct {
			Text     string `json:"text"`
			Redacted bool   `json:"redacted"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			omissions["invalid_prompt_payload"]++
			return ContextEvent{}, false, nil
		}
		if policy.PromptMode == PromptModeOmit {
			projection.Prompt = &PromptProjection{Omitted: true}
			omissions["prompt_policy"]++
			return projection, true, nil
		}
		text := sanitizeText(workspaceRoot, payload.Text, policy.FieldLimit, truncations)
		projection.Prompt = &PromptProjection{Text: text.Text, Redacted: payload.Redacted || text.Redacted, Truncated: text.Truncated, Bytes: text.OriginalBytes}
	case primitives.EventTypeAgentIntent:
		payload, err := provenance.ParseIntentPayload(event.Payload)
		if err != nil {
			omissions["invalid_intent_payload"]++
			return ContextEvent{}, false, nil
		}
		problem := sanitizeText(workspaceRoot, payload.Problem, policy.FieldLimit, truncations)
		scope, scopeRedacted := sanitizeList(workspaceRoot, payload.Scope, policy.FieldLimit, truncations)
		evidence, evidenceRedacted := sanitizeList(workspaceRoot, payload.Evidence, policy.FieldLimit, truncations)
		agentType := sanitizeIdentifier(workspaceRoot, payload.AgentType, truncations)
		projection.Intent = &IntentProjection{
			Problem:   TextProjection{Text: problem.Text, Redacted: problem.Redacted, Truncated: problem.Truncated, Bytes: problem.OriginalBytes},
			Scope:     scope,
			Evidence:  evidence,
			Redacted:  payload.Redacted || problem.Redacted || scopeRedacted || evidenceRedacted || agentType.Redacted,
			AgentType: agentType.Text,
		}
	case primitives.EventTypeAssistantMessage:
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			omissions["invalid_assistant_payload"]++
			return ContextEvent{}, false, nil
		}
		text := sanitizeText(workspaceRoot, payload.Text, policy.FieldLimit, truncations)
		projection.Assistant = &TextProjection{Text: text.Text, Redacted: text.Redacted, Truncated: text.Truncated, Bytes: text.OriginalBytes}
	case primitives.EventTypeToolCall:
		var payload struct {
			ToolName          string `json:"tool_name"`
			MutationCandidate bool   `json:"mutation_candidate"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.ToolName) == "" {
			omissions["invalid_tool_call_payload"]++
			return ContextEvent{}, false, nil
		}
		name := sanitizeIdentifier(workspaceRoot, payload.ToolName, truncations)
		if name.Text == "" {
			omissions["invalid_tool_call_name"]++
			return ContextEvent{}, false, nil
		}
		projection.Tool = &ToolProjection{Name: name.Text, Category: toolCategory(payload.ToolName, payload.MutationCandidate), Status: "started", MutationCandidate: payload.MutationCandidate}
		omissions["tool_input"]++
	case primitives.EventTypeToolResult:
		var payload struct {
			ToolName string          `json:"tool_name"`
			Output   json.RawMessage `json:"output"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || strings.TrimSpace(payload.ToolName) == "" {
			omissions["invalid_tool_result_payload"]++
			return ContextEvent{}, false, nil
		}
		name := sanitizeIdentifier(workspaceRoot, payload.ToolName, truncations)
		if name.Text == "" {
			omissions["invalid_tool_result_name"]++
			return ContextEvent{}, false, nil
		}
		projection.Tool = &ToolProjection{Name: name.Text, Category: toolCategory(payload.ToolName, false), Status: "completed"}
		if len(bytes.TrimSpace(payload.Output)) > 0 && string(bytes.TrimSpace(payload.Output)) != "null" {
			omissions["tool_output"]++
		}
	case primitives.EventTypeCheckpoint:
		var payload struct {
			Phase        string `json:"phase"`
			CheckpointID string `json:"checkpoint_id"`
			UserGit      struct {
				Head  string `json:"head"`
				Dirty bool   `json:"dirty"`
			} `json:"user_git"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			omissions["invalid_checkpoint_payload"]++
			return ContextEvent{}, false, nil
		}
		phase, phaseErr := primitives.ParseCheckpointPhase(payload.Phase)
		checkpointID, checkpointErr := primitives.ParseCheckpointID(payload.CheckpointID)
		if phaseErr != nil || checkpointErr != nil {
			omissions["invalid_checkpoint_identity"]++
			return ContextEvent{}, false, nil
		}
		commit := ""
		if strings.TrimSpace(payload.UserGit.Head) != "" {
			parsed, err := primitives.ParseCommitSHA(payload.UserGit.Head)
			if err != nil {
				omissions["invalid_checkpoint_commit"]++
				return ContextEvent{}, false, nil
			}
			commit = parsed.String()
		}
		projection.Checkpoint = &CheckpointProjection{Phase: phase.String(), CheckpointID: checkpointID.String(), SourceCommit: commit, Dirty: payload.UserGit.Dirty}
		return projection, true, []SourceLink{{CommitSHA: commit, Checkpoint: checkpointID.String()}}
	case primitives.EventTypeError:
		projection.CaptureError = &CaptureErrorProjection{Kind: "capture_error"}
		omissions["error_message"]++
	default:
		omissions["event_type:"+event.Type.String()]++
		return ContextEvent{}, false, nil
	}
	return projection, true, nil
}

type sanitizedText struct {
	Text          string
	Redacted      bool
	Truncated     bool
	OriginalBytes int
}

func sanitizeText(workspaceRoot, value string, limit int, truncations *Truncations) sanitizedText {
	result := sanitizedText{Text: value, OriginalBytes: len(value)}
	for _, variant := range workspaceRootVariants(workspaceRoot) {
		var redacted bool
		result.Text, redacted = replaceWorkspaceRoot(result.Text, variant)
		if redacted {
			result.Redacted = true
		}
	}
	for _, pattern := range absolutePathPatterns {
		if pattern.MatchString(result.Text) {
			result.Text = pattern.ReplaceAllString(result.Text, "[PATH_REDACTED]")
			result.Redacted = true
		}
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(result.Text) {
			result.Text = pattern.ReplaceAllString(result.Text, "[REDACTED]")
			result.Redacted = true
		}
	}
	if len(result.Text) > limit {
		result.Text = truncateUTF8(result.Text, limit)
		result.Truncated = true
		truncations.Count++
		truncations.OriginalBytes += result.OriginalBytes
	}
	return result
}

// workspaceRootVariants covers filesystem aliases that can appear in captured
// events even when Git registered a different spelling of the same worktree.
// In particular, macOS commonly exposes /var and /tmp through /private symlinks.
func workspaceRootVariants(workspaceRoot string) []string {
	if workspaceRoot == "" {
		return nil
	}

	roots := []string{workspaceRoot, filepath.Clean(workspaceRoot)}
	if resolved, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		roots = append(roots, resolved)
	}
	for _, root := range append([]string(nil), roots...) {
		slashRoot := filepath.ToSlash(root)
		switch {
		case strings.HasPrefix(slashRoot, "/private/var/") || strings.HasPrefix(slashRoot, "/private/tmp/"):
			roots = append(roots, strings.TrimPrefix(slashRoot, "/private"))
		case strings.HasPrefix(slashRoot, "/var/") || strings.HasPrefix(slashRoot, "/tmp/"):
			roots = append(roots, "/private"+slashRoot)
		}
	}

	seen := make(map[string]struct{}, len(roots)*3)
	variants := make([]string, 0, len(roots)*3)
	add := func(value string) {
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		variants = append(variants, value)
	}
	for _, root := range roots {
		add(root)
		add(filepath.ToSlash(root))
		add(filepath.FromSlash(filepath.ToSlash(root)))
	}
	sort.Slice(variants, func(i, j int) bool {
		return len(variants[i]) > len(variants[j])
	})
	return variants
}

func replaceWorkspaceRoot(value, workspaceRoot string) (string, bool) {
	if workspaceRoot == "" {
		return value, false
	}

	var result strings.Builder
	remaining := value
	replaced := false
	for {
		index := strings.Index(remaining, workspaceRoot)
		if index < 0 {
			result.WriteString(remaining)
			break
		}

		end := index + len(workspaceRoot)
		startsAtBoundary := workspacePathStartBoundary(remaining, index)
		endsAtBoundary := end == len(remaining) || isPathSeparator(remaining[end]) || isPathSeparator(workspaceRoot[len(workspaceRoot)-1])
		if startsAtBoundary && endsAtBoundary {
			result.WriteString(remaining[:index])
			result.WriteString("$WORKSPACE")
			remaining = remaining[end:]
			replaced = true
			continue
		}

		// A root-like substring outside path boundaries may be a sibling or an
		// enclosing path. Redact the field rather than create a misleading,
		// partially scrubbed $WORKSPACE value.
		return "[PATH_REDACTED]", true
	}
	return result.String(), replaced
}

func workspacePathStartBoundary(value string, index int) bool {
	if index == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:index])
	if unicode.IsSpace(previous) {
		return true
	}
	return strings.ContainsRune("\"'`()[]{}<>=,;:", previous)
}

func isPathSeparator(character byte) bool {
	return character == '/' || character == '\\'
}

func containsDetectedSecret(value string) bool {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func sanitizeList(workspaceRoot string, values []string, limit int, truncations *Truncations) ([]string, bool) {
	result := make([]string, 0, len(values))
	redacted := false
	for _, value := range values {
		sanitized := sanitizeText(workspaceRoot, value, limit, truncations)
		result = append(result, sanitized.Text)
		redacted = redacted || sanitized.Redacted
	}
	return result, redacted
}

func sanitizeIdentifier(workspaceRoot, value string, truncations *Truncations) sanitizedText {
	result := sanitizeText(workspaceRoot, strings.TrimSpace(value), 256, truncations)
	var builder strings.Builder
	for _, character := range result.Text {
		if character >= 0x20 && character != 0x7f {
			builder.WriteRune(character)
		}
	}
	result.Text = builder.String()
	return result
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func toolCategory(name string, mutation bool) string {
	if mutation {
		return "mutation"
	}
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "search"), strings.Contains(name, "find"), strings.Contains(name, "grep"):
		return "search"
	case strings.Contains(name, "read"), strings.Contains(name, "view"):
		return "read"
	case strings.Contains(name, "shell"), strings.Contains(name, "exec"), strings.Contains(name, "bash"):
		return "command"
	default:
		return "other"
	}
}

func marshalEventsJSONL(events []ContextEvent) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, fmt.Errorf("encode shared history event: %w", err)
		}
	}
	return buffer.Bytes(), nil
}

func bundlePath(bundleID primitives.BundleID) string {
	value := bundleID.String()
	digest := strings.TrimPrefix(value, "bundle_")
	return filepath.ToSlash(filepath.Join("bundles", digest[:2], value))
}

func locator(deviceID string, bundleID primitives.BundleID) string {
	return "v1:" + deviceID + ":" + bundleID.String()
}

func parseLocator(value string) (string, primitives.BundleID, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 || parts[0] != "v1" || len(parts[1]) != 32 {
		return "", "", fmt.Errorf("invalid shared history locator %q", value)
	}
	for _, character := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", "", fmt.Errorf("invalid shared history device id in locator")
		}
	}
	bundleID, err := primitives.ParseBundleID(parts[2])
	if err != nil {
		return "", "", err
	}
	return parts[1], bundleID, nil
}
