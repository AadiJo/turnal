package provenance

import (
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestAttributeDerivesConfidenceFromRecordedFacts(t *testing.T) {
	seq, _ := primitives.NewEventSeq(4)
	payload := IntentPayload{Problem: "retry delay survives success", Scope: []string{"internal/retry"}}

	captured := Attribute(payload, seq, IntentTimingBefore, "internal/retry/reset.go")
	if captured.Status != IntentStatusCaptured || captured.Confidence != IntentConfidenceHigh {
		t.Fatalf("captured attribution = %#v", captured)
	}
	outOfScope := Attribute(payload, seq, IntentTimingBefore, "internal/http/client.go")
	if outOfScope.Status != IntentStatusOutOfScope || outOfScope.Confidence != IntentConfidenceLow {
		t.Fatalf("out-of-scope attribution = %#v", outOfScope)
	}
	late := Attribute(payload, seq, IntentTimingAfter, "internal/retry/reset.go")
	if late.Status != IntentStatusLate || late.Confidence != IntentConfidenceLow {
		t.Fatalf("late attribution = %#v", late)
	}
}

func TestNormalizeScopeAcceptsWorkspaceRootAsBroadScope(t *testing.T) {
	scope, err := normalizeScope([]string{".", "internal/retry"})
	if err != nil || len(scope) != 0 {
		t.Fatalf("scope = %#v, err=%v", scope, err)
	}
	if _, err := normalizeScope([]string{".", "/absolute"}); err == nil {
		t.Fatal("scope accepted an invalid path after workspace root")
	}
}
