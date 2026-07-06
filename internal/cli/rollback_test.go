package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

func TestRollbackCommandRestoresCheckpoint(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("working copy\n"), 0o644); err != nil {
		t.Fatalf("write app.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "extra.txt"), []byte("remove me\n"), 0o644); err != nil {
		t.Fatalf("write extra.txt: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rollback", sessionID.String() + ":" + turnID.String() + ":pre"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rollback command: %v\n%s", err, out.String())
	}

	content, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read app.txt: %v", err)
	}
	if string(content) != "before\n" {
		t.Fatalf("app.txt = %q, want pre-checkpoint content", content)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.MetadataDir, "git")); err != nil {
		t.Fatalf("agent-vcs metadata missing after rollback: %v", err)
	}
	if !strings.Contains(out.String(), "rolled back to") {
		t.Fatalf("rollback output = %q", out.String())
	}

	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if countCLIEvents(events, primitives.EventTypeRollback) != 1 {
		t.Fatalf("rollback events = %d, want 1; events=%#v", countCLIEvents(events, primitives.EventTypeRollback), events)
	}
}

func countCLIEvents(events []eventlog.Event, eventType primitives.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
