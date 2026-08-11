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

	"github.com/AadiJo/turnal/internal/buildinfo"
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
	regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]{20,}|github_pat_[a-z0-9_]{20,}|gl(?:pat|ptt|rt|soat|ft|cbt)-[a-z0-9_-]{20,}|npm_[a-z0-9]{30,}|pypi-[a-z0-9_-]{16,}|hf_[a-z0-9]{20,}|sk-[a-z0-9_-]{20,}|[sr]k_(?:live|test)_[a-z0-9]{16,}|(?:akia|asia|abia|acca)[0-9a-z]{16}|xox[baprs]-[a-z0-9-]{10,}|aiza[0-9a-z_-]{20,})\b`),
	regexp.MustCompile(`(?i)\bhttps://hooks\.slack\.com/services/[a-z0-9/_-]{10,}`),
	regexp.MustCompile(`(?i)\b[a-z0-9_]*(?:password|passwd|pwd|secret|token|api[_-]?key)[a-z0-9_]*\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]{16,}=*`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`),
}

var fullFieldSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
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
				workspaceRoot := stream.WorkspaceRoot
				if workspaceRoot == "" {
					workspaceRoot = workspaceRoots[stream.WorktreeID]
				}
				turns = append(turns, turnSource{Stream: stream, WorkspaceRoot: workspaceRoot, TurnID: turnID, Events: events})
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
		streams := make([]string, 0, len(matches))
		for _, match := range matches {
			streams = append(streams, match.Stream.StreamID.String())
		}
		sort.Strings(streams)
		return turnSource{}, fmt.Errorf("turn %s:%s is ambiguous; rerun preview with --stream followed by one of: %s", sessionID, turnID, strings.Join(streams, ", "))
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
	if !isAbsoluteWorkspaceRoot(source.WorkspaceRoot) {
		return builtBundle{}, fmt.Errorf("source worktree root is invalid for stream %s; refusing to project paths without its privacy boundary", source.Stream.StreamID)
	}
	bundleID, err := primitives.DeriveBundleID(policy.RepoID, source.Stream.StreamID, source.TurnID)
	if err != nil {
		return builtBundle{}, err
	}
	omissions := map[string]int{}
	redactions := map[string]int{}
	truncations := Truncations{}
	projected := make([]ContextEvent, 0, len(source.Events))
	sourceRefs := make([]SourceRef, 0, len(source.Events))
	sourceLinks := make([]SourceLink, 0)
	seenLinks := map[string]struct{}{}

	for _, event := range source.Events {
		ref := SourceRef{StreamID: source.Stream.StreamID, Seq: event.Seq, Hash: event.Hash}
		sourceRefs = append(sourceRefs, ref)
		projection, included, links := projectEvent(source.WorkspaceRoot, policy, event, ref, omissions, redactions, &truncations)
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
		SchemaVersion:    SchemaVersion,
		BundleID:         bundleID,
		RepoID:           policy.RepoID,
		DeviceID:         identity.DeviceID,
		ProducerID:       source.Stream.ProducerID,
		StoreID:          repo.StoreID,
		WorktreeID:       source.Stream.WorktreeID,
		StreamID:         source.Stream.StreamID,
		SessionID:        source.Stream.SessionID,
		TurnID:           source.TurnID,
		SourceSequence:   SequenceRange{First: sourceRefs[0].Seq, Last: sourceRefs[len(sourceRefs)-1].Seq},
		SourceRefs:       sourceRefs,
		PolicyHash:       policyDigest,
		PromptMode:       policy.PromptMode,
		EvidenceClass:    EvidencePublisherClaim,
		AllowlistVersion: policy.AllowlistVersion,
		ScannerVersion:   policy.ScannerVersion,
		ProducerVersion:  producerVersion(),
		SourceLinks:      sourceLinks,
		Omissions:        omissions,
		Redactions:       redactions,
		Truncations:      truncations,
		ContentHashes:    map[string]string{"events.jsonl": sha256Bytes(eventsJSON)},
		CreatedAt:        source.Events[len(source.Events)-1].Time.Time,
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

func projectEvent(workspaceRoot string, policy policyFile, event eventlog.Event, ref SourceRef, omissions, redactions map[string]int, truncations *Truncations) (ContextEvent, bool, []SourceLink) {
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
		if policy.PromptMode != PromptModeRedactedText {
			projection.Prompt = &PromptProjection{Omitted: true}
			omissions["prompt_policy"]++
			return projection, true, nil
		}
		text := sanitizeText(workspaceRoot, payload.Text, policy.FieldLimit, truncations)
		recordRedactions(redactions, text)
		projection.Prompt = &PromptProjection{Text: text.Text, Redacted: payload.Redacted || text.Redacted, Truncated: text.Truncated, Bytes: text.OriginalBytes}
	case primitives.EventTypeAgentIntent:
		if policy.PromptMode == PromptModeMetadataOnly {
			omissions["intent_policy"]++
			return ContextEvent{}, false, nil
		}
		payload, err := provenance.ParseIntentPayload(event.Payload)
		if err != nil {
			omissions["invalid_intent_payload"]++
			return ContextEvent{}, false, nil
		}
		problem := sanitizeText(workspaceRoot, payload.Problem, policy.FieldLimit, truncations)
		recordRedactions(redactions, problem)
		scope, scopeRedacted := sanitizeList(workspaceRoot, payload.Scope, policy.FieldLimit, redactions, truncations)
		evidence, evidenceRedacted := sanitizeList(workspaceRoot, payload.Evidence, policy.FieldLimit, redactions, truncations)
		agentType := sanitizeIdentifier(workspaceRoot, payload.AgentType, truncations)
		recordRedactions(redactions, agentType)
		projection.Intent = &IntentProjection{
			Problem:   TextProjection{Text: problem.Text, Redacted: problem.Redacted, Truncated: problem.Truncated, Bytes: problem.OriginalBytes},
			Scope:     scope,
			Evidence:  evidence,
			Redacted:  payload.Redacted || problem.Redacted || scopeRedacted || evidenceRedacted || agentType.Redacted,
			AgentType: agentType.Text,
		}
	case primitives.EventTypeAssistantMessage:
		if policy.PromptMode == PromptModeMetadataOnly {
			omissions["assistant_policy"]++
			return ContextEvent{}, false, nil
		}
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			omissions["invalid_assistant_payload"]++
			return ContextEvent{}, false, nil
		}
		text := sanitizeText(workspaceRoot, payload.Text, policy.FieldLimit, truncations)
		recordRedactions(redactions, text)
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
		recordRedactions(redactions, name)
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
		recordRedactions(redactions, name)
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
				Head     string `json:"head"`
				Branch   string `json:"branch"`
				Detached bool   `json:"detached"`
				Dirty    bool   `json:"dirty"`
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
		branch, branchOmitted := projectBranch(policy, payload.UserGit.Branch, payload.UserGit.Detached)
		if branchOmitted != "" {
			omissions[branchOmitted]++
		}
		return projection, true, []SourceLink{{CommitSHA: commit, Checkpoint: checkpointID.String(), Branch: branch}}
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
	Text                string
	Redacted            bool
	Truncated           bool
	OriginalBytes       int
	PathFullyRedacted   bool
	WorkspaceNormalized bool
	SecretRedacted      bool
}

const workspaceProjectionMarker = "\x00turnal-workspace\x00"

func sanitizeText(workspaceRoot, value string, limit int, truncations *Truncations) sanitizedText {
	result := sanitizedText{Text: value, OriginalBytes: len(value)}
	if strings.Contains(result.Text, workspaceProjectionMarker) {
		result.Text = strings.ReplaceAll(result.Text, workspaceProjectionMarker, "[REDACTED]")
		result.Redacted = true
		result.SecretRedacted = true
	}
	for _, variant := range workspaceRootVariants(workspaceRoot) {
		var redacted bool
		result.Text, redacted = replaceWorkspaceRoot(result.Text, variant)
		if redacted {
			result.Redacted = true
			if result.Text == "[PATH_REDACTED]" {
				result.PathFullyRedacted = true
			} else {
				result.WorkspaceNormalized = true
			}
		}
	}
	var pathRedacted bool
	result.Text, pathRedacted = redactAbsolutePaths(result.Text)
	if pathRedacted {
		result.Redacted = true
		result.PathFullyRedacted = true
	}
	for _, pattern := range fullFieldSecretPatterns {
		if pattern.MatchString(result.Text) {
			result.Text = "[REDACTED]"
			result.Redacted = true
			result.SecretRedacted = true
			break
		}
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(result.Text) {
			result.Text = pattern.ReplaceAllString(result.Text, "[REDACTED]")
			result.Redacted = true
			result.SecretRedacted = true
		}
	}
	result.Text = strings.ReplaceAll(result.Text, workspaceProjectionMarker, "$WORKSPACE")
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

	roots := []string{trimWorkspaceTrailingSeparators(workspaceRoot), trimWorkspaceTrailingSeparators(filepath.Clean(workspaceRoot))}
	if resolved, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		roots = append(roots, trimWorkspaceTrailingSeparators(resolved))
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
		value = trimWorkspaceTrailingSeparators(value)
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

	// Match conservatively across case-insensitive Windows and macOS aliases.
	// On a case-sensitive filesystem, an extra redaction is safer than exposing
	// a path whose captured spelling differs from the registered worktree.
	matcher := regexp.MustCompile(`(?i:` + regexp.QuoteMeta(workspaceRoot) + `)`)
	matches := matcher.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return value, false
	}

	var result strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		separatorOnly := isPathSeparatorOnly(workspaceRoot)
		startsAtBoundary := workspacePathStartBoundary(value, start)
		if separatorOnly {
			startsAtBoundary = workspaceSeparatorStartBoundary(value, start)
		}
		if separatorOnly && !startsAtBoundary {
			continue
		}
		if !startsAtBoundary || !workspacePathEndBoundary(value, start, end, workspaceRoot) {
			// A root-like substring outside path boundaries may be a sibling or
			// an enclosing path. Redact the field rather than create a
			// misleading, partially scrubbed $WORKSPACE value.
			return "[PATH_REDACTED]", true
		}
		continuation := value[end:]
		if isPathSeparator(workspaceRoot[len(workspaceRoot)-1]) {
			continuation = workspaceRoot[len(workspaceRoot)-1:] + continuation
		}
		if containsParentPathComponent(continuation) {
			return "[PATH_REDACTED]", true
		}

		result.WriteString(value[last:start])
		result.WriteString(workspaceProjectionMarker)
		if isPathSeparator(workspaceRoot[len(workspaceRoot)-1]) && end < len(value) && !isPathSeparator(value[end]) {
			result.WriteByte(workspaceRoot[len(workspaceRoot)-1])
		}
		last = end
	}
	result.WriteString(value[last:])
	return result.String(), last > 0
}

func workspacePathStartBoundary(value string, index int) bool {
	if index == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:index])
	if strings.ContainsRune("\"'`", previous) {
		return true
	}
	if strings.ContainsAny(value[:index], `/\`) {
		return false
	}
	if unicode.IsSpace(previous) {
		return true
	}
	return strings.ContainsRune("\"'`()[]{}<>=,;:", previous)
}

func workspaceSeparatorStartBoundary(value string, index int) bool {
	if index == 0 {
		return true
	}
	if index >= len("file://") && strings.EqualFold(value[index-len("file://"):index], "file://") {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:index])
	if unicode.IsSpace(previous) {
		return true
	}
	if previous == '/' || previous == '\\' {
		return false
	}
	if previous == ':' {
		// Skip the first separator in :// while accepting a colon-delimited
		// filesystem path such as path:/private.txt.
		return index+1 == len(value) || !isPathSeparator(value[index+1])
	}
	return unicode.IsPunct(previous) || unicode.IsSymbol(previous)
}

func workspacePathEndBoundary(value string, start, end int, workspaceRoot string) bool {
	if end == len(value) || isPathSeparator(value[end]) || isPathSeparator(workspaceRoot[len(workspaceRoot)-1]) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(value[end:])
	if quote := workspacePathOpeningQuote(value, start); quote != 0 {
		return next == quote
	}
	return false
}

func workspacePathOpeningQuote(value string, start int) rune {
	if start == 0 {
		return 0
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:start])
	if strings.ContainsRune("\"'`", previous) {
		return previous
	}
	return 0
}

func trimWorkspaceTrailingSeparators(value string) string {
	for len(value) > 1 && isPathSeparator(value[len(value)-1]) && !isWindowsDriveRoot(value) {
		value = value[:len(value)-1]
	}
	return value
}

func isWindowsDriveRoot(value string) bool {
	if len(value) != 3 || value[1] != ':' || !isPathSeparator(value[2]) {
		return false
	}
	letter := value[0]
	return (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')
}

func isPathSeparatorOnly(value string) bool {
	return len(value) == 1 && isPathSeparator(value[0])
}

func isPathSeparator(character byte) bool {
	return character == '/' || character == '\\'
}

func redactAbsolutePaths(value string) (string, bool) {
	if !containsUnprotectedAbsolutePath(value) {
		return value, false
	}
	return "[PATH_REDACTED]", true
}

func containsUnprotectedAbsolutePath(value string) bool {
	if containsUnprotectedFileURI(value) {
		return true
	}
	for index := 0; index < len(value); index++ {
		if strings.HasPrefix(value[index:], workspaceProjectionMarker) {
			index += len(workspaceProjectionMarker) - 1
			continue
		}
		if isPathSeparator(value[index]) {
			if strings.HasSuffix(value[:index], workspaceProjectionMarker) {
				continue
			}
			if workspaceSeparatorStartBoundary(value, index) {
				return true
			}
			continue
		}
		if windowsDrivePathStart(value, index) {
			return true
		}
	}
	return false
}

func containsUnprotectedFileURI(value string) bool {
	searchFrom := 0
	for searchFrom < len(value) {
		relative := indexASCIIFold(value[searchFrom:], "file:")
		if relative < 0 {
			return false
		}
		index := searchFrom + relative
		if index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(value[:index])
			if !unicode.IsSpace(previous) && !unicode.IsPunct(previous) && !unicode.IsSymbol(previous) {
				searchFrom = index + len("file:")
				continue
			}
		}
		remainder := value[index+len("file:"):]
		if strings.HasPrefix(remainder, workspaceProjectionMarker) ||
			strings.HasPrefix(remainder, "//"+workspaceProjectionMarker) ||
			strings.HasPrefix(remainder, `\\`+workspaceProjectionMarker) {
			searchFrom = index + len("file:")
			continue
		}
		if strings.HasPrefix(remainder, "/") || strings.HasPrefix(remainder, `\`) {
			return true
		}
		searchFrom = index + len("file:")
	}
	return false
}

func indexASCIIFold(value, target string) int {
	for index := 0; index+len(target) <= len(value); index++ {
		if strings.EqualFold(value[index:index+len(target)], target) {
			return index
		}
	}
	return -1
}

func windowsDrivePathStart(value string, index int) bool {
	if index+2 >= len(value) || value[index+1] != ':' || !isPathSeparator(value[index+2]) {
		return false
	}
	letter := value[index]
	if !((letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')) {
		return false
	}
	if index == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(value[:index])
	return unicode.IsSpace(previous) || unicode.IsPunct(previous) || unicode.IsSymbol(previous)
}

func isAbsoluteWorkspaceRoot(value string) bool {
	if value == "" || strings.ContainsRune(value, 0) || containsDotPathComponent(value) {
		return false
	}
	if value[0] == '/' || windowsDrivePathStart(value, 0) {
		return true
	}
	if len(value) < 5 || value[0] != '\\' || value[1] != '\\' {
		return false
	}
	serverEnd := strings.IndexAny(value[2:], `/\`)
	if serverEnd <= 0 {
		return false
	}
	shareStart := 2 + serverEnd + 1
	return shareStart < len(value) && !isPathSeparator(value[shareStart])
}

func containsDotPathComponent(value string) bool {
	for _, component := range strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func containsParentPathComponent(value string) bool {
	for index := 0; index+2 < len(value); index++ {
		if !isPathSeparator(value[index]) || value[index+1:index+3] != ".." {
			continue
		}
		end := index + 3
		if end == len(value) || isPathSeparator(value[end]) {
			return true
		}
		next, _ := utf8.DecodeRuneInString(value[end:])
		if unicode.IsSpace(next) || strings.ContainsRune("\"'`.)]}>,;:!?", next) {
			return true
		}
	}
	return false
}

func containsDetectedSecret(value string) bool {
	for _, pattern := range fullFieldSecretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func sanitizeList(workspaceRoot string, values []string, limit int, redactions map[string]int, truncations *Truncations) ([]string, bool) {
	result := make([]string, 0, len(values))
	redacted := false
	for _, value := range values {
		sanitized := sanitizeText(workspaceRoot, value, limit, truncations)
		recordRedactions(redactions, sanitized)
		result = append(result, sanitized.Text)
		redacted = redacted || sanitized.Redacted
	}
	return result, redacted
}

func recordRedactions(redactions map[string]int, text sanitizedText) {
	if text.PathFullyRedacted {
		redactions["path_full"]++
	}
	if text.WorkspaceNormalized {
		redactions["workspace_path"]++
	}
	if text.SecretRedacted {
		redactions["secret"]++
	}
}

// producerVersion names the Turnal build that projected a bundle so a receiver
// can attribute a projection defect to a specific version. Dev builds report
// the placeholder version rather than omitting it, because "unknown producer"
// and "no producer field" mean different things to a receiver.
func producerVersion() string {
	version := strings.TrimSpace(buildinfo.Current().Version)
	if version == "" || len(version) > 64 {
		return ""
	}
	for _, character := range version {
		valid := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._+-", character)
		if !valid {
			return ""
		}
	}
	return version
}

// projectBranch reduces a captured symbolic ref to a short branch name and
// reports the omission reason when nothing is publishable. Branch names are
// author-controlled, so the character set is an allowlist: anything outside it
// is dropped rather than normalized. metadata_only publishes no source naming
// at all, and a detached HEAD has no branch to name.
func projectBranch(policy policyFile, branch string, detached bool) (string, string) {
	if policy.PromptMode == PromptModeMetadataOnly {
		if strings.TrimSpace(branch) != "" {
			return "", "branch_policy"
		}
		return "", ""
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		if detached {
			return "", "branch_detached"
		}
		return "", ""
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" || len(branch) > 256 {
		return "", "invalid_branch"
	}
	for _, character := range branch {
		valid := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._/-", character)
		if !valid {
			return "", "invalid_branch"
		}
	}
	return branch, ""
}

func sanitizeIdentifier(workspaceRoot, value string, truncations *Truncations) sanitizedText {
	result := sanitizeText(workspaceRoot, strings.TrimSpace(value), 256, truncations)
	var builder strings.Builder
	for _, character := range result.Text {
		if character >= 0x20 && character != 0x7f && !unicode.Is(unicode.Cf, character) {
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
