package manualcheckpoints

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestAppendIsIdempotentAndReadValidatesCheckpoint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	original := []byte{'a', 0, 'b', '\n'}
	if err := os.WriteFile(filepath.Join(root.String(), "data.bin"), original, 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	first, err := Append(repo, created, "known good")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	second, err := Append(repo, created, "known good")
	if err != nil {
		t.Fatalf("idempotent Append: %v", err)
	}
	if first.Hash != second.Hash || first.Seq != second.Seq {
		t.Fatalf("duplicate append created a new event: first=%#v second=%#v", first, second)
	}
	saves, err := Read(repo, false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(saves) != 1 || saves[0].Message != "known good" || saves[0].Checkpoint.Commit != created.Commit {
		t.Fatalf("saves = %#v", saves)
	}
}

func TestReadRejectsMalformedManualCheckpointEvent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, _ := primitives.ParseWorkspaceRoot(t.TempDir())
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := AppendEvent(repo, primitives.EventTypeCheckpoint, "malformed", "", []byte(`{}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := Read(repo, false); err == nil || !strings.Contains(err.Error(), "origin invariant failed") {
		t.Fatalf("Read error = %v", err)
	}
}

func TestAppendRejectsInvalidMessageBeforeWritingEvent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, _ := primitives.ParseWorkspaceRoot(t.TempDir())
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	for _, message := range []string{strings.Repeat("x", MaxMessageBytes+1), string([]byte{0xff})} {
		if _, err := Append(repo, created, message); err == nil {
			t.Fatalf("Append accepted invalid message %q", message)
		}
	}
	events, err := ReadEvents(repo, false)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("invalid message wrote durable events: %#v", events)
	}
}

func TestValidateRollbackEventRejectsCheckpointRefMismatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, _ := primitives.ParseWorkspaceRoot(t.TempDir())
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	checkpointA, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	checkpointB, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint B: %v", err)
	}
	payload, _ := json.Marshal(RollbackPayload{
		Mode: primitives.RollbackModeCheckpoint.String(), Target: checkpointB.Commit.String(),
		Ref: checkpointA.Ref.String(), CommitSHA: checkpointB.Commit.String(),
		SafetyRef: "refs/agent-vcs/rollback-safety/manual/test", SafetyCommitSHA: checkpointA.Commit.String(),
	})
	event := eventlog.Event{
		Type: primitives.EventTypeRollback, Adapter: primitives.AdapterManual, WorktreeID: repo.WorktreeID,
		RawRef: checkpointB.Commit.String(), SourceID: fmt.Sprintf("turnal:rollback:checkpoint:%s:%s", checkpointB.Commit, checkpointA.Commit), Payload: payload,
	}
	if _, err := ValidateRollbackEvent(repo, event); err == nil || !strings.Contains(err.Error(), "checkpoint ref invariant failed") {
		t.Fatalf("ValidateRollbackEvent error = %v", err)
	}
}
