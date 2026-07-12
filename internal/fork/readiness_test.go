package fork

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestInspectRequiresUnrecordedConversationContext(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Fix the parser", true)

	report, err := NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Readiness != ReadinessNeedsContext || report.FidelityLevel != "L1" {
		t.Fatalf("readiness = %q fidelity = %q", report.Readiness, report.FidelityLevel)
	}
	if report.Instruction.Status != InstructionAvailable || report.Instruction.Text != "Fix the parser" {
		t.Fatalf("instruction = %#v", report.Instruction)
	}
	if report.Instruction.Adapter != primitives.AdapterCodex || report.Source.MetadataAdapter != primitives.AdapterCodex {
		t.Fatalf("instruction/metadata adapters = %q / %q", report.Instruction.Adapter, report.Source.MetadataAdapter)
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
	if report.Conditions.ConversationContext.Status != "not_recorded" {
		t.Fatalf("conversation context = %#v", report.Conditions.ConversationContext)
	}
	if report.Conditions.WorkspaceVCS.Status != "not_applicable" {
		t.Fatalf("workspace VCS = %#v", report.Conditions.WorkspaceVCS)
	}
	if !report.Source.Complete {
		t.Fatal("complete turn reported incomplete")
	}
}

func TestInspectReportsRedactedInstructionWithoutExposingMarker(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, primitives.SecretsRedactionText, true)

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
	if string(encoded) == "" || bytes.Contains(encoded, []byte(primitives.SecretsRedactionText)) {
		t.Fatalf("encoded report exposes redaction marker: %s", encoded)
	}
}

func TestInspectRejectsCheckpointRefCommitMismatch(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Fix the parser", true)
	ref, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CheckpointRefFor: %v", err)
	}
	if _, err := repo.CreateSyntheticSnapshotRef(ref.String(), "replace pre ref", []checkpoint.SyntheticTreeEntry{
		{Path: "other.txt", Mode: primitives.GitFileModeRegular, Content: []byte("other\n")},
	}); err != nil {
		t.Fatalf("replace checkpoint ref: %v", err)
	}

	_, err = NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err == nil {
		t.Fatal("Inspect with mismatched checkpoint ref succeeded")
	}
	if !strings.Contains(err.Error(), "checkpoint ref") || !strings.Contains(err.Error(), "event records") {
		t.Fatalf("Inspect error = %v", err)
	}
}

func TestInspectRejectsCanonicalCheckpointRefCommitMismatch(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Fix the parser", true)
	infos, err := repo.FindCheckpointTargets(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("FindCheckpointTargets: %v", err)
	}
	if len(infos) != 1 || infos[0].CanonicalRef == "" {
		t.Fatalf("pre checkpoint infos = %#v", infos)
	}
	if _, err := repo.CreateSyntheticSnapshotRef(infos[0].CanonicalRef.String(), "replace canonical pre ref", []checkpoint.SyntheticTreeEntry{
		{Path: "other.txt", Mode: primitives.GitFileModeRegular, Content: []byte("other\n")},
	}); err != nil {
		t.Fatalf("replace canonical checkpoint ref: %v", err)
	}

	_, err = NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err == nil || !strings.Contains(err.Error(), "canonical pre-turn checkpoint ref") {
		t.Fatalf("Inspect error = %v", err)
	}
}

func TestInspectAllowsIncompleteTurnWhenPreCheckpointExists(t *testing.T) {
	repo, sessionID, turnID := readinessFixture(t, "Continue from here", false)

	report, err := NewAnalyzer(repo).Inspect(sessionID, turnID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Readiness != ReadinessNeedsContext || report.Source.Complete {
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

func TestInspectInstructionRejectsMalformedPayloads(t *testing.T) {
	for name, payload := range map[string]json.RawMessage{
		"invalid json":        json.RawMessage(`{"text":`),
		"missing field":       json.RawMessage(`{}`),
		"null field":          json.RawMessage(`{"text":null}`),
		"wrong field type":    json.RawMessage(`{"text":42}`),
		"wrong provider type": json.RawMessage(`{"text":"fix","provider_turn_id":42}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := inspectInstruction([]eventlog.Event{{
				Seq:     1,
				Type:    primitives.EventTypePromptUser,
				Payload: payload,
			}})
			if err == nil || !strings.Contains(err.Error(), "malformed prompt.user payload at event 1") {
				t.Fatalf("inspectInstruction error = %v", err)
			}
		})
	}
}

func TestInspectInstructionRejectsMultiplePrompts(t *testing.T) {
	turnID, _ := primitives.NewTurnID(1)
	_, err := inspectInstruction([]eventlog.Event{
		{Seq: 1, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, Payload: json.RawMessage(`{"text":"first"}`)},
		{Seq: 2, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterClaudeCode, Payload: json.RawMessage(`{"text":"second"}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple prompt.user events") {
		t.Fatalf("inspectInstruction error = %v", err)
	}
}

func TestSessionMetadataRejectsMalformedPayloads(t *testing.T) {
	for name, payload := range map[string]json.RawMessage{
		"invalid json":          json.RawMessage(`{"model":`),
		"missing provider":      json.RawMessage(`{"model":"valid"}`),
		"null provider":         json.RawMessage(`{"provider_session_id":null}`),
		"null field":            json.RawMessage(`{"provider_session_id":"session","model":null}`),
		"wrong field type":      json.RawMessage(`{"provider_session_id":"session","model":false}`),
		"wrong transcript type": json.RawMessage(`{"provider_session_id":"session","transcript_path":42}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := sessionMetadata([]eventlog.Event{{
				Seq:     2,
				Type:    primitives.EventTypeSessionStart,
				Adapter: primitives.AdapterCodex,
				Payload: payload,
			}}, primitives.AdapterCodex)
			if err == nil || !strings.Contains(err.Error(), "malformed session.start payload at event 2") {
				t.Fatalf("sessionMetadata error = %v", err)
			}
		})
	}
}

func TestSessionMetadataUsesInstructionAdapter(t *testing.T) {
	events := []eventlog.Event{
		{
			Seq:     1,
			Type:    primitives.EventTypeSessionStart,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"provider_session_id":"claude-session","model":"claude-model","permission_mode":"claude-mode"}`),
		},
		{
			Seq:     2,
			Type:    primitives.EventTypeSessionStart,
			Adapter: primitives.AdapterCodex,
			Payload: json.RawMessage(`{"provider_session_id":"codex-session","model":"codex-model","permission_mode":"codex-mode"}`),
		},
	}

	model, permissionMode, found, err := sessionMetadata(events, primitives.AdapterCodex)
	if err != nil {
		t.Fatalf("sessionMetadata: %v", err)
	}
	if !found || model != "codex-model" || permissionMode != "codex-mode" {
		t.Fatalf("metadata = %q / %q / %t", model, permissionMode, found)
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
