package fork

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestInspectReportsReadyCapturedTurn(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Fix the parser", true)

	report, err := NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Readiness != ReadinessReady || report.FidelityLevel != "L1" {
		t.Fatalf("readiness = %q fidelity = %q", report.Readiness, report.FidelityLevel)
	}
	if report.Instruction.Status != InstructionAvailable || report.Instruction.Text != "Fix the parser" {
		t.Fatalf("instruction = %#v", report.Instruction)
	}
	if report.Base.Status != "available" || report.Base.Ref == "" || report.Base.CommitSHA == "" {
		t.Fatalf("base = %#v", report.Base)
	}
	if report.Base.CapturedFiles != 1 {
		t.Fatalf("captured files = %d, want 1", report.Base.CapturedFiles)
	}
	if report.Source.Model != "test-model" || report.Source.PermissionMode != "workspace" {
		t.Fatalf("source metadata = %#v", report.Source)
	}
	if !report.Source.Complete {
		t.Fatal("complete turn reported incomplete")
	}
}

func TestInspectReportsRedactedInstructionWithoutExposingMarker(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, redactedPrompt, true)

	report, err := NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Readiness != ReadinessNeedsInstruction {
		t.Fatalf("readiness = %q", report.Readiness)
	}
	if report.Instruction.Status != InstructionRedacted || report.Instruction.Text != "" {
		t.Fatalf("instruction = %#v", report.Instruction)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) == "" || containsBytes(encoded, []byte(redactedPrompt)) {
		t.Fatalf("encoded report exposes redaction marker: %s", encoded)
	}
}

func TestInspectAllowsIncompleteTurnWhenPreCheckpointExists(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Continue from here", false)

	report, err := NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Readiness != ReadinessReady || report.Source.Complete {
		t.Fatalf("report = %#v", report)
	}
	if report.Base.Status != "available" || report.FidelityLevel != "L1" {
		t.Fatalf("base = %#v fidelity = %q", report.Base, report.FidelityLevel)
	}
}

func TestInspectRequiresRepo(t *testing.T) {
	_, err := (Analyzer{}).Inspect("demo", 1)
	if err == nil {
		t.Fatal("Inspect without repo succeeded")
	}
}

func readinessFixture(t *testing.T, prompt string, finish bool) (*checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sessionID, _ := primitives.ParseSessionID("fork-ready")
	turnID, _ := primitives.NewTurnID(1)
	adapter := primitives.AdapterCodex
	log := repo.EventLog()
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   adapter,
		Payload:   json.RawMessage(`{"provider_session_id":"fork-ready","model":"test-model","permission_mode":"workspace"}`),
	}); err != nil {
		t.Fatalf("append session start: %v", err)
	}
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: adapter}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	promptPayload, _ := json.Marshal(map[string]string{"text": prompt})
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   adapter,
		Payload:   promptPayload,
	}); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
	if finish {
		if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("after\n"), 0o644); err != nil {
			t.Fatalf("write result: %v", err)
		}
		if _, err := recorder.Finish(sessionID, turnID); err != nil {
			t.Fatalf("finish turn: %v", err)
		}
	}
	return repo, sessionID, turnID
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for index := 0; index <= len(haystack)-len(needle); index++ {
		matched := true
		for offset := range needle {
			if haystack[index+offset] != needle[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
