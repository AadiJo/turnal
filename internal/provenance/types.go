package provenance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
)

type IntentStatus string

const (
	IntentStatusCaptured       IntentStatus = "captured"
	IntentStatusLate           IntentStatus = "late"
	IntentStatusOutOfScope     IntentStatus = "out_of_scope"
	IntentStatusLateOutOfScope IntentStatus = "late_out_of_scope"
	IntentStatusRedacted       IntentStatus = "redacted"
)

type IntentTiming string

const (
	IntentTimingBefore IntentTiming = "before"
	IntentTimingAfter  IntentTiming = "after"
)

type IntentConfidence string

const (
	IntentConfidenceHigh IntentConfidence = "high"
	IntentConfidenceLow  IntentConfidence = "low"
)

type ActionSnapshotPhase string

const (
	ActionSnapshotPhasePre  ActionSnapshotPhase = "pre"
	ActionSnapshotPhasePost ActionSnapshotPhase = "post"
)

// IntentPayload is the agent's compact, explicit account of the problem an
// upcoming change is meant to address. It is a statement, not inferred truth.
type IntentPayload struct {
	Problem   string   `json:"problem"`
	Scope     []string `json:"scope,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
	Redacted  bool     `json:"redacted,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"`
	AgentType string   `json:"agent_type,omitempty"`
}

// Attribution adds recorded timing and scope facts to an agent's statement.
// Confidence is derived from those facts rather than supplied by the agent.
type Attribution struct {
	Problem    string              `json:"problem"`
	Scope      []string            `json:"scope,omitempty"`
	Evidence   []string            `json:"evidence,omitempty"`
	EventSeq   primitives.EventSeq `json:"event_seq"`
	Status     IntentStatus        `json:"status"`
	Timing     IntentTiming        `json:"timing"`
	Confidence IntentConfidence    `json:"confidence"`
	AgentID    string              `json:"agent_id,omitempty"`
	AgentType  string              `json:"agent_type,omitempty"`
	Redacted   bool                `json:"redacted,omitempty"`
}

type ActionSnapshot struct {
	Ref    string               `json:"ref"`
	Commit primitives.CommitSHA `json:"commit"`
}

func ParseIntentPayload(data json.RawMessage) (IntentPayload, error) {
	var payload IntentPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return IntentPayload{}, fmt.Errorf("parse agent intent payload: %w", err)
	}
	payload.Problem = strings.TrimSpace(payload.Problem)
	if payload.Problem == "" {
		return IntentPayload{}, fmt.Errorf("agent intent problem is required")
	}
	return payload, nil
}

// ScopeMatches reports whether a file is inside the agent's stated scope.
// An empty scope means the agent did not narrow the expected change surface.
func ScopeMatches(scope []string, path string) bool {
	if len(scope) == 0 {
		return true
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	for _, item := range scope {
		item = strings.Trim(strings.TrimSpace(item), "/")
		if path == item || strings.HasPrefix(path, item+"/") {
			return true
		}
	}
	return false
}

func Attribute(payload IntentPayload, seq primitives.EventSeq, timing IntentTiming, paths ...string) Attribution {
	status := IntentStatusCaptured
	confidence := IntentConfidenceHigh
	inScope := false
	if payload.Redacted {
		status = IntentStatusRedacted
		confidence = IntentConfidenceLow
	} else {
		inScope = len(payload.Scope) == 0
		for _, path := range paths {
			if ScopeMatches(payload.Scope, path) {
				inScope = true
				break
			}
		}
	}
	switch {
	case payload.Redacted:
	case timing == IntentTimingAfter && !inScope:
		status = IntentStatusLateOutOfScope
		confidence = IntentConfidenceLow
	case timing == IntentTimingAfter:
		status = IntentStatusLate
		confidence = IntentConfidenceLow
	case !inScope:
		status = IntentStatusOutOfScope
		confidence = IntentConfidenceLow
	}
	return Attribution{
		Problem:    payload.Problem,
		Scope:      append([]string(nil), payload.Scope...),
		Evidence:   append([]string(nil), payload.Evidence...),
		EventSeq:   seq,
		Status:     status,
		Timing:     timing,
		Confidence: confidence,
		AgentID:    payload.AgentID,
		AgentType:  payload.AgentType,
		Redacted:   payload.Redacted,
	}
}
