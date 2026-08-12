package sessionhistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
)

type ImportPlan struct {
	Adapter  primitives.AdapterName `json:"adapter"`
	DryRun   bool                   `json:"dry_run"`
	Sessions []SessionPlan          `json:"sessions"`
	Warnings []string               `json:"warnings,omitempty"`
}

type SessionPlan struct {
	ProviderSessionID string               `json:"provider_session_id"`
	SessionID         primitives.SessionID `json:"session_id"`
	Path              string               `json:"path"`
	SHA256            string               `json:"sha256"`
	TurnCount         int                  `json:"turn_count"`
	EventCount        int                  `json:"event_count"`
	PendingEvents     int                  `json:"pending_events"`
	State             string               `json:"state"`
	Warnings          []string             `json:"warnings,omitempty"`
	candidate         Candidate
}

type ImportResult struct {
	ImportedSessions int      `json:"imported_sessions"`
	SkippedSessions  int      `json:"skipped_sessions"`
	ImportedTurns    int      `json:"imported_turns"`
	AppendedEvents   int      `json:"appended_events"`
	Warnings         []string `json:"warnings,omitempty"`
}

type appendSpec struct {
	SessionID primitives.SessionID
	TurnID    *primitives.TurnID
	Type      primitives.EventType
	Adapter   primitives.AdapterName
	Time      time.Time
	SourceID  string
	Payload   json.RawMessage
}

func PlanImport(repo *checkpoint.Repo, adapter primitives.AdapterName, candidates []Candidate, warnings []string) (ImportPlan, error) {
	if repo == nil {
		return ImportPlan{}, fmt.Errorf("plan transcript import: repo is required")
	}
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return ImportPlan{}, err
	}
	plan := ImportPlan{Adapter: adapter, DryRun: true, Warnings: append([]string(nil), warnings...)}
	for _, candidate := range candidates {
		sessionPlan, err := planCandidate(repo, candidate, effective)
		if err != nil {
			return ImportPlan{}, err
		}
		plan.Sessions = append(plan.Sessions, sessionPlan)
	}
	return plan, nil
}

func planCandidate(repo *checkpoint.Repo, candidate Candidate, effective agentconfig.Effective) (SessionPlan, error) {
	existingEvents, err := repo.EventLog().Read(candidate.SessionID)
	if err != nil {
		return SessionPlan{}, err
	}
	// Imported transcript evidence is deliberately weaker than native Turnal
	// capture. Never mix the two or duplicate a session the recorder already saw.
	if len(existingEvents) > 0 && InspectSession(existingEvents).Origin != OriginImported {
		return SessionPlan{
			ProviderSessionID: candidate.ProviderSessionID,
			SessionID:         candidate.SessionID,
			Path:              candidate.Path,
			SHA256:            candidate.SHA256,
			TurnCount:         len(candidate.Turns),
			State:             "already-recorded",
			Warnings:          append([]string(nil), candidate.Warnings...),
			candidate:         candidate,
		}, nil
	}
	specs, err := candidateSpecs(repo, candidate, effective)
	if err != nil {
		return SessionPlan{}, err
	}
	pending, err := preflightSpecs(repo.EventLog(), specs)
	if err != nil {
		return SessionPlan{}, fmt.Errorf("plan import of session %s: %w", candidate.ProviderSessionID, err)
	}
	state := "ready"
	if len(candidate.Turns) == 0 {
		state = "no-turns"
		pending = 0
	} else if pending == 0 {
		state = "already-imported"
	}
	return SessionPlan{
		ProviderSessionID: candidate.ProviderSessionID,
		SessionID:         candidate.SessionID,
		Path:              candidate.Path,
		SHA256:            candidate.SHA256,
		TurnCount:         len(candidate.Turns),
		EventCount:        len(specs),
		PendingEvents:     pending,
		State:             state,
		Warnings:          append([]string(nil), candidate.Warnings...),
		candidate:         candidate,
	}, nil
}

func ApplyImport(repo *checkpoint.Repo, plan ImportPlan) (ImportResult, error) {
	if repo == nil {
		return ImportResult{}, fmt.Errorf("apply transcript import: repo is required")
	}
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{Warnings: append([]string(nil), plan.Warnings...)}
	for _, sessionPlan := range plan.Sessions {
		result.Warnings = append(result.Warnings, sessionPlan.Warnings...)
		if sessionPlan.State == "no-turns" || sessionPlan.PendingEvents == 0 {
			result.SkippedSessions++
			continue
		}
		unlock, err := adapters.AcquireSessionLock(repo, sessionPlan.SessionID)
		if err != nil {
			return result, err
		}
		appended, importedTurns, applyErr := applyCandidateLocked(repo, sessionPlan.candidate, effective)
		unlock()
		if applyErr != nil {
			return result, applyErr
		}
		if appended == 0 {
			result.SkippedSessions++
			continue
		}
		result.ImportedSessions++
		result.ImportedTurns += importedTurns
		result.AppendedEvents += appended
	}
	if result.AppendedEvents > 0 {
		if err := queryindex.Invalidate(repo.MetadataDir); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("history was imported, but the disposable search index could not be invalidated: %v", err))
		}
	}
	return result, nil
}

func applyCandidateLocked(repo *checkpoint.Repo, candidate Candidate, effective agentconfig.Effective) (int, int, error) {
	if active, ok, err := turns.NewManager(repo).Active(candidate.SessionID); err != nil {
		return 0, 0, err
	} else if ok {
		return 0, 0, fmt.Errorf("cannot import session %s while turn %s is active", candidate.SessionID, active.TurnID)
	}
	existingEvents, err := repo.EventLog().Read(candidate.SessionID)
	if err != nil {
		return 0, 0, err
	}
	if len(existingEvents) > 0 && InspectSession(existingEvents).Origin != OriginImported {
		return 0, 0, nil
	}
	specs, err := candidateSpecs(repo, candidate, effective)
	if err != nil {
		return 0, 0, err
	}
	if _, err := preflightSpecs(repo.EventLog(), specs); err != nil {
		return 0, 0, fmt.Errorf("preflight import of session %s: %w", candidate.ProviderSessionID, err)
	}
	appended := 0
	importedTurns := 0
	for _, spec := range specs {
		existing, found, err := repo.EventLog().FindSourceID(spec.SessionID, spec.SourceID)
		if err != nil {
			return appended, importedTurns, err
		}
		if found {
			if err := compareSpec(existing, spec); err != nil {
				return appended, importedTurns, err
			}
			continue
		}
		at := primitives.Timestamp{}
		if !spec.Time.IsZero() {
			at, err = primitives.NewTimestamp(spec.Time)
			if err != nil {
				return appended, importedTurns, err
			}
		}
		if _, err := repo.EventLog().Append(eventlog.AppendInput{
			SessionID: spec.SessionID,
			TurnID:    spec.TurnID,
			Type:      spec.Type,
			Adapter:   spec.Adapter,
			Time:      at,
			SourceID:  spec.SourceID,
			Payload:   spec.Payload,
		}); err != nil {
			return appended, importedTurns, err
		}
		appended++
		if spec.Type == primitives.EventTypePromptUser {
			importedTurns++
		}
	}
	return appended, importedTurns, nil
}

func candidateSpecs(repo *checkpoint.Repo, candidate Candidate, effective agentconfig.Effective) ([]appendSpec, error) {
	events, err := repo.EventLog().Read(candidate.SessionID)
	if err != nil {
		return nil, err
	}
	turnIDs, err := assignTurnIDs(repo, candidate, events)
	if err != nil {
		return nil, err
	}
	base := importSourcePrefix(candidate)
	startedAt := candidate.StartedAt
	if startedAt.IsZero() {
		startedAt = candidate.ModifiedAt
	}
	startPayload, _ := json.Marshal(SessionStartPayload{
		ProviderSessionID: candidate.ProviderSessionID,
		TranscriptPath:    candidate.Path,
		Origin:            OriginImported,
		ReadOnly:          true,
	})
	modified := ""
	if !candidate.ModifiedAt.IsZero() {
		modified = candidate.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	importPayload, _ := json.Marshal(ImportPayload{
		Origin:         OriginImported,
		ReadOnly:       true,
		SourcePath:     candidate.Path,
		SourceSHA256:   candidate.SHA256,
		SourceModified: modified,
		TurnCount:      len(candidate.Turns),
	})
	specs := []appendSpec{
		{SessionID: candidate.SessionID, Type: primitives.EventTypeSessionStart, Adapter: candidate.Adapter, Time: startedAt, SourceID: base + ":session", Payload: startPayload},
		{SessionID: candidate.SessionID, Type: primitives.EventTypeSessionImport, Adapter: candidate.Adapter, Time: candidate.ModifiedAt, SourceID: base + ":source:" + candidate.SHA256, Payload: importPayload},
	}
	for index, transcriptTurn := range candidate.Turns {
		turnID := turnIDs[index]
		turnKey := transcriptTurn.ProviderTurnID
		if turnKey == "" {
			turnKey = fmt.Sprintf("ordinal-%d", index+1)
		}
		turnSource := base + ":turn:" + sourceComponent(turnKey)
		promptText := transcriptTurn.Prompt
		assistantText := transcriptTurn.Assistant
		redacted := !effective.Secrets.StorePrompts
		if redacted {
			promptText = primitives.SecretsRedactionText
			assistantText = primitives.SecretsRedactionText
		}
		promptPayload, _ := json.Marshal(PromptPayload{
			Text:           promptText,
			ProviderTurnID: transcriptTurn.ProviderTurnID,
			Model:          transcriptTurn.Model,
			Redacted:       redacted,
		})
		specs = append(specs, appendSpec{
			SessionID: candidate.SessionID, TurnID: &turnID, Type: primitives.EventTypePromptUser,
			Adapter: candidate.Adapter, Time: transcriptTurn.StartedAt, SourceID: turnSource + ":prompt", Payload: promptPayload,
		})
		for toolIndex, tool := range transcriptTurn.Tools {
			toolKey := tool.UseID
			if toolKey == "" {
				toolKey = fmt.Sprintf("ordinal-%d", toolIndex+1)
			}
			toolSource := turnSource + ":tool:" + sourceComponent(toolKey)
			input := tool.Input
			output := tool.Output
			if !effective.Secrets.StoreToolIO {
				input = json.RawMessage(`{"redacted":true,"policy":"turnal.secrets"}`)
				output = json.RawMessage(`{"redacted":true,"policy":"turnal.secrets"}`)
			}
			callPayload, _ := json.Marshal(ToolCallPayload{
				ToolName: tool.Name, ToolUseID: tool.UseID, ProviderTurnID: transcriptTurn.ProviderTurnID, Input: defaultJSON(input),
			})
			specs = append(specs, appendSpec{
				SessionID: candidate.SessionID, TurnID: &turnID, Type: primitives.EventTypeToolCall,
				Adapter: candidate.Adapter, Time: tool.CalledAt, SourceID: toolSource + ":call", Payload: callPayload,
			})
			if len(tool.Output) > 0 {
				resultPayload, _ := json.Marshal(ToolResultPayload{
					ToolName: tool.Name, ToolUseID: tool.UseID, ProviderTurnID: transcriptTurn.ProviderTurnID, Output: defaultJSON(output),
				})
				specs = append(specs, appendSpec{
					SessionID: candidate.SessionID, TurnID: &turnID, Type: primitives.EventTypeToolResult,
					Adapter: candidate.Adapter, Time: tool.ReturnedAt, SourceID: toolSource + ":result", Payload: resultPayload,
				})
			}
		}
		if strings.TrimSpace(transcriptTurn.Assistant) != "" {
			assistantPayload, _ := json.Marshal(AssistantPayload{
				Text: assistantText, ProviderTurnID: transcriptTurn.ProviderTurnID, Model: transcriptTurn.Model, Redacted: redacted,
			})
			specs = append(specs, appendSpec{
				SessionID: candidate.SessionID, TurnID: &turnID, Type: primitives.EventTypeAssistantMessage,
				Adapter: candidate.Adapter, Time: transcriptTurn.FinishedAt, SourceID: turnSource + ":assistant", Payload: assistantPayload,
			})
		}
	}
	return specs, nil
}

func assignTurnIDs(repo *checkpoint.Repo, candidate Candidate, events []eventlog.Event) ([]primitives.TurnID, error) {
	bySourceID := map[string]primitives.TurnID{}
	byProviderID := map[string]primitives.TurnID{}
	byPrompt := map[string]primitives.TurnID{}
	var maxTurn uint64
	for _, event := range events {
		if event.TurnID == nil {
			continue
		}
		if event.TurnID.Uint64() > maxTurn {
			maxTurn = event.TurnID.Uint64()
		}
		if event.Type != primitives.EventTypePromptUser {
			continue
		}
		if event.SourceID != "" {
			bySourceID[event.SourceID] = *event.TurnID
		}
		var payload PromptPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		if payload.ProviderTurnID != "" {
			byProviderID[payload.ProviderTurnID] = *event.TurnID
		}
		if text := normalizedPrompt(payload.Text); text != "" && text != normalizedPrompt(primitives.SecretsRedactionText) {
			byPrompt[text] = *event.TurnID
		}
	}
	refs, err := repo.ListCheckpointRefs(candidate.SessionID)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		parts, err := ref.Parts()
		if err != nil {
			return nil, err
		}
		if parts.TurnID.Uint64() > maxTurn {
			maxTurn = parts.TurnID.Uint64()
		}
	}
	assigned := make([]primitives.TurnID, len(candidate.Turns))
	used := map[primitives.TurnID]struct{}{}
	base := importSourcePrefix(candidate)
	for index, transcriptTurn := range candidate.Turns {
		turnKey := transcriptTurn.ProviderTurnID
		if turnKey == "" {
			turnKey = fmt.Sprintf("ordinal-%d", index+1)
		}
		promptSourceID := base + ":turn:" + sourceComponent(turnKey) + ":prompt"
		turnID, ok := bySourceID[promptSourceID]
		if !ok {
			turnID, ok = byProviderID[transcriptTurn.ProviderTurnID]
		}
		if !ok {
			turnID, ok = byPrompt[normalizedPrompt(transcriptTurn.Prompt)]
		}
		if ok {
			if _, duplicate := used[turnID]; duplicate {
				ok = false
			}
		}
		if !ok {
			maxTurn++
			turnID, err = primitives.NewTurnID(maxTurn)
			if err != nil {
				return nil, err
			}
		}
		used[turnID] = struct{}{}
		assigned[index] = turnID
	}
	return assigned, nil
}

func preflightSpecs(log eventlog.Log, specs []appendSpec) (int, error) {
	bySession := make(map[primitives.SessionID]map[string]eventlog.Event)
	for _, spec := range specs {
		if _, ok := bySession[spec.SessionID]; ok {
			continue
		}
		events, err := log.Read(spec.SessionID)
		if err != nil {
			return 0, err
		}
		bySource := make(map[string]eventlog.Event, len(events))
		for _, event := range events {
			if event.SourceID != "" {
				bySource[event.SourceID] = event
			}
		}
		bySession[spec.SessionID] = bySource
	}
	pending := 0
	planned := make(map[string]appendSpec, len(specs))
	for _, spec := range specs {
		key := spec.SessionID.String() + "\x00" + spec.SourceID
		if prior, found := planned[key]; found {
			if err := compareSpec(eventlog.Event{
				TurnID: prior.TurnID, Type: prior.Type, Adapter: prior.Adapter, Payload: prior.Payload,
			}, spec); err != nil {
				return 0, err
			}
			continue
		}
		planned[key] = spec
		existing, found := bySession[spec.SessionID][spec.SourceID]
		if !found {
			pending++
			continue
		}
		if err := compareSpec(existing, spec); err != nil {
			return 0, err
		}
	}
	return pending, nil
}

func compareSpec(existing eventlog.Event, spec appendSpec) error {
	turnMatches := existing.TurnID == nil && spec.TurnID == nil
	if existing.TurnID != nil && spec.TurnID != nil {
		turnMatches = *existing.TurnID == *spec.TurnID
	}
	payloadMatches := bytes.Equal(existing.Payload, spec.Payload)
	if existing.Type == primitives.EventTypeSessionAttach && spec.Type == primitives.EventTypeSessionAttach {
		var left, right AttachmentPayload
		payloadMatches = json.Unmarshal(existing.Payload, &left) == nil && json.Unmarshal(spec.Payload, &right) == nil &&
			left.CommitSHA == right.CommitSHA && !left.HistoryRewritten && !right.HistoryRewritten
	}
	if existing.Type == primitives.EventTypeSessionImport && spec.Type == primitives.EventTypeSessionImport {
		var left, right ImportPayload
		payloadMatches = json.Unmarshal(existing.Payload, &left) == nil && json.Unmarshal(spec.Payload, &right) == nil &&
			left.Origin == right.Origin && left.ReadOnly == right.ReadOnly && left.SourceSHA256 == right.SourceSHA256 && left.TurnCount == right.TurnCount
	}
	if existing.Type != spec.Type || existing.Adapter != spec.Adapter || !turnMatches || !payloadMatches {
		return fmt.Errorf("history source collision for %s: existing event does not match the planned record", spec.SourceID)
	}
	return nil
}

func importSourcePrefix(candidate Candidate) string {
	return "turnal:import:v1:" + candidate.Adapter.String() + ":" + sourceComponent(candidate.ProviderSessionID)
}

func sourceComponent(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func normalizedPrompt(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

type SessionInfo struct {
	ProviderSessionID string       `json:"provider_session_id,omitempty"`
	Origin            string       `json:"origin,omitempty"`
	ReadOnly          bool         `json:"read_only,omitempty"`
	TranscriptPath    string       `json:"transcript_path,omitempty"`
	Attachments       []Attachment `json:"attachments,omitempty"`
}

func InspectSession(events []eventlog.Event) SessionInfo {
	var info SessionInfo
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeSessionStart:
			var payload SessionStartPayload
			if json.Unmarshal(event.Payload, &payload) != nil {
				continue
			}
			if info.ProviderSessionID == "" {
				info.ProviderSessionID = payload.ProviderSessionID
			}
			if info.TranscriptPath == "" {
				info.TranscriptPath = payload.TranscriptPath
			}
			if payload.Origin == OriginImported {
				info.Origin = OriginImported
				info.ReadOnly = payload.ReadOnly
			}
		case primitives.EventTypeSessionImport:
			info.Origin = OriginImported
			info.ReadOnly = true
		case primitives.EventTypeSessionAttach:
			var payload AttachmentPayload
			if json.Unmarshal(event.Payload, &payload) != nil || payload.CommitSHA == "" {
				continue
			}
			info.Attachments = append(info.Attachments, Attachment{CommitSHA: payload.CommitSHA, Revision: payload.Revision, Time: event.Time.Time})
		}
	}
	if info.Origin == "" {
		info.Origin = OriginNative
	}
	sort.Slice(info.Attachments, func(i, j int) bool { return info.Attachments[i].Time.Before(info.Attachments[j].Time) })
	return info
}

func Attach(repo *checkpoint.Repo, sessionID primitives.SessionID, commit primitives.CommitSHA, revision string) (eventlog.Event, bool, error) {
	if repo == nil {
		return eventlog.Event{}, false, fmt.Errorf("attach session: repo is required")
	}
	unlock, err := adapters.AcquireSessionLock(repo, sessionID)
	if err != nil {
		return eventlog.Event{}, false, err
	}
	defer unlock()
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		return eventlog.Event{}, false, err
	}
	if len(events) == 0 {
		return eventlog.Event{}, false, fmt.Errorf("session %s has no recorded or imported history", sessionID)
	}
	payload, _ := json.Marshal(AttachmentPayload{CommitSHA: commit, Revision: strings.TrimSpace(revision), HistoryRewritten: false})
	sourceID := "turnal:attach:v1:" + sessionID.String() + ":" + commit.String()
	spec := appendSpec{SessionID: sessionID, Type: primitives.EventTypeSessionAttach, Adapter: events[0].Adapter, SourceID: sourceID, Payload: payload}
	if existing, found, err := repo.EventLog().FindSourceID(sessionID, sourceID); err != nil {
		return eventlog.Event{}, false, err
	} else if found {
		if err := compareSpec(existing, spec); err != nil {
			return eventlog.Event{}, false, err
		}
		return existing, false, nil
	}
	event, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionAttach,
		Adapter:   spec.Adapter,
		SourceID:  sourceID,
		Payload:   payload,
	})
	if err != nil {
		return eventlog.Event{}, false, err
	}
	return event, true, nil
}

func AttachmentExists(repo *checkpoint.Repo, sessionID primitives.SessionID, commit primitives.CommitSHA) (bool, error) {
	if repo == nil {
		return false, fmt.Errorf("inspect session attachment: repo is required")
	}
	sourceID := "turnal:attach:v1:" + sessionID.String() + ":" + commit.String()
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.SourceID == sourceID {
			payload, _ := json.Marshal(AttachmentPayload{CommitSHA: commit, HistoryRewritten: false})
			if err := compareSpec(event, appendSpec{
				SessionID: sessionID, Type: primitives.EventTypeSessionAttach,
				Adapter: event.Adapter, SourceID: sourceID, Payload: payload,
			}); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
