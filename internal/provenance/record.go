package provenance

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
)

type RecordInput struct {
	SessionID primitives.SessionID
	Problem   string
	Scope     []string
	Evidence  []string
}

func Record(repo *checkpoint.Repo, input RecordInput) (eventlog.Event, error) {
	if repo == nil {
		return eventlog.Event{}, fmt.Errorf("intent requires checkpoint repo")
	}
	sessionID, err := primitives.ParseSessionID(input.SessionID.String())
	if err != nil {
		return eventlog.Event{}, err
	}
	problem := strings.TrimSpace(input.Problem)
	if problem == "" {
		return eventlog.Event{}, fmt.Errorf("intent problem is required")
	}

	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil {
		return eventlog.Event{}, err
	}
	if !ok {
		return eventlog.Event{}, fmt.Errorf("session %s has no active turn", sessionID)
	}

	scope, err := normalizeScope(input.Scope)
	if err != nil {
		return eventlog.Event{}, err
	}
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		return eventlog.Event{}, err
	}
	evidence, err := normalizeEvidence(input.Evidence, events, active.TurnID)
	if err != nil {
		return eventlog.Event{}, err
	}

	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return eventlog.Event{}, err
	}
	redacted := !effective.Secrets.StorePrompts
	if redacted {
		problem = primitives.SecretsRedactionText
		scope = nil
		evidence = nil
	}
	payload, err := json.Marshal(IntentPayload{
		Problem:  problem,
		Scope:    scope,
		Evidence: evidence,
		Redacted: redacted,
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("marshal agent intent: %w", err)
	}

	return repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &active.TurnID,
		Type:      primitives.EventTypeAgentIntent,
		Adapter:   turnAdapter(events, active.TurnID),
		Payload:   payload,
	})
}

func normalizeScope(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	broad := false
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "." {
			// An explicit workspace root is equivalent to leaving scope broad.
			broad = true
			continue
		}
		path, err := primitives.ParseRepoPath(value)
		if err != nil {
			return nil, fmt.Errorf("intent scope %q: %w", value, err)
		}
		if _, ok := seen[path.String()]; ok {
			continue
		}
		seen[path.String()] = struct{}{}
		result = append(result, path.String())
	}
	if broad {
		return nil, nil
	}
	return result, nil
}

func normalizeEvidence(values []string, events []eventlog.Event, turnID primitives.TurnID) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("intent evidence must not be empty")
		}
		switch {
		case strings.HasPrefix(value, "event:"):
			seq, err := primitives.ParseEventSeq(strings.TrimPrefix(value, "event:"))
			if err != nil {
				return nil, fmt.Errorf("intent evidence %q: %w", value, err)
			}
			if !eventBelongsToTurn(events, seq, turnID) {
				return nil, fmt.Errorf("intent evidence %q does not name an event in active turn %s", value, turnID)
			}
		case strings.HasPrefix(value, "path:"):
			if err := validatePathEvidence(strings.TrimPrefix(value, "path:")); err != nil {
				return nil, fmt.Errorf("intent evidence %q: %w", value, err)
			}
		case strings.HasPrefix(value, "test:"):
			if strings.TrimSpace(strings.TrimPrefix(value, "test:")) == "" {
				return nil, fmt.Errorf("intent evidence %q requires a test name", value)
			}
		default:
			return nil, fmt.Errorf("intent evidence %q must start with event:, path:, or test:", value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validatePathEvidence(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path is required")
	}
	pathText := value
	if index := strings.LastIndex(value, ":"); index > 0 {
		if line, err := strconv.Atoi(value[index+1:]); err == nil {
			if line <= 0 {
				return fmt.Errorf("line must be greater than zero")
			}
			pathText = value[:index]
		}
	}
	_, err := primitives.ParseRepoPath(pathText)
	return err
}

func eventBelongsToTurn(events []eventlog.Event, seq primitives.EventSeq, turnID primitives.TurnID) bool {
	for _, event := range events {
		if event.Seq == seq && event.TurnID != nil && *event.TurnID == turnID {
			return true
		}
	}
	return false
}

func turnAdapter(events []eventlog.Event, turnID primitives.TurnID) primitives.AdapterName {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.TurnID != nil && *event.TurnID == turnID && event.Adapter != "" {
			return event.Adapter
		}
	}
	return ""
}
