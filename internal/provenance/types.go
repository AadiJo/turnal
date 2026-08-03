package provenance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	IntentStatusCaptured   = "captured"
	IntentStatusLate       = "late"
	IntentStatusOutOfScope = "out_of_scope"

	IntentTimingBefore = "before"
	IntentTimingAfter  = "after"

	IntentConfidenceHigh = "high"
	IntentConfidenceLow  = "low"
)

// IntentPayload is the agent's compact, explicit account of the problem an
// upcoming change is meant to address. It is a statement, not inferred truth.
type IntentPayload struct {
	Problem  string   `json:"problem"`
	Scope    []string `json:"scope,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Redacted bool     `json:"redacted,omitempty"`
}

// Attribution adds recorded timing and scope facts to an agent's statement.
// Confidence is derived from those facts rather than supplied by the agent.
type Attribution struct {
	Problem    string              `json:"problem"`
	Scope      []string            `json:"scope,omitempty"`
	Evidence   []string            `json:"evidence,omitempty"`
	EventSeq   primitives.EventSeq `json:"event_seq"`
	Status     string              `json:"status"`
	Timing     string              `json:"timing"`
	Confidence string              `json:"confidence"`
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

func Attribute(payload IntentPayload, seq primitives.EventSeq, timing string, path string) Attribution {
	status := IntentStatusCaptured
	confidence := IntentConfidenceHigh
	if timing == IntentTimingAfter {
		status = IntentStatusLate
		confidence = IntentConfidenceLow
	} else if !ScopeMatches(payload.Scope, path) {
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
	}
}
