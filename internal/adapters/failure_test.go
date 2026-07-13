package adapters

import (
	"errors"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestRecordReadAndClearHookFailure(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	raw := []byte("{\"session_id\":\"failure-session\"}")
	if err := RecordHookFailure(primitives.AdapterCodex, "UserPromptSubmit", raw, errors.New("disk unavailable")); err != nil {
		t.Fatalf("RecordHookFailure: %v", err)
	}
	failures, err := ReadHookFailures(repo.MetadataDir)
	if err != nil {
		t.Fatalf("ReadHookFailures: %v", err)
	}
	if len(failures) != 1 || failures[0].SessionID != "failure-session" || failures[0].Error != "disk unavailable" {
		t.Fatalf("failures = %#v", failures)
	}
	count, err := ClearHookFailures(repo.MetadataDir)
	if err != nil {
		t.Fatalf("ClearHookFailures: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared = %d, want 1", count)
	}
	failures, err = ReadHookFailures(repo.MetadataDir)
	if err != nil || len(failures) != 0 {
		t.Fatalf("failures after clear = %#v, err=%v", failures, err)
	}
}
