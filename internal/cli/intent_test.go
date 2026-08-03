package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestIntentCommandRecordsExplicitAgentStatement(t *testing.T) {
	requireGit(t)
	rootPath := t.TempDir()
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionID(t, "intent-command")
	if _, err := turns.NewManager(repo).Start(sessionID, 0); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	t.Chdir(rootPath)

	output := runRootStdout(t,
		"intent",
		"--session", sessionID.String(),
		"--turn", "1",
		"--problem", "retry delay survives a successful request",
		"--scope", "internal/retry.go",
		"--evidence", "test:TestRetryReset",
	)
	if !strings.Contains(output, "recorded intent") {
		t.Fatalf("output = %q", output)
	}
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != primitives.EventTypeAgentIntent {
		t.Fatalf("events = %#v", events)
	}
	var payload provenance.IntentPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Problem != "retry delay survives a successful request" || len(payload.Scope) != 1 || payload.Evidence[0] != "test:TestRetryReset" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestIntentCommandRedactsAllPromptLikeFields(t *testing.T) {
	requireGit(t)
	rootPath := t.TempDir()
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(`version = 1

[secrets]
store_prompts = false
store_tool_io = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID := sessionID(t, "private-intent-command")
	if _, err := turns.NewManager(repo).Start(sessionID, 0); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	t.Chdir(rootPath)

	runRootStdout(t,
		"intent",
		"--session", sessionID.String(),
		"--turn", "1",
		"--problem", "private customer retry failure",
		"--scope", "customers/private/retry.go",
		"--evidence", "test:PrivateCustomerRetry",
	)
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var payload provenance.IntentPayload
	if len(events) != 1 || json.Unmarshal(events[0].Payload, &payload) != nil {
		t.Fatalf("events = %#v", events)
	}
	if !payload.Redacted || payload.Problem != primitives.SecretsRedactionText || len(payload.Scope) != 0 || len(payload.Evidence) != 0 {
		t.Fatalf("redacted payload = %#v", payload)
	}
}

func TestIntentCommandRejectsACommandFromAnOlderTurn(t *testing.T) {
	requireGit(t)
	rootPath := t.TempDir()
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionID(t, "delayed-intent-command")
	manager := turns.NewManager(repo)
	first, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("start first turn: %v", err)
	}
	if _, err := manager.Finish(sessionID, first.TurnID); err != nil {
		t.Fatalf("finish first turn: %v", err)
	}
	if _, err := manager.Start(sessionID, 0); err != nil {
		t.Fatalf("start second turn: %v", err)
	}
	t.Chdir(rootPath)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"intent", "--session", sessionID.String(), "--turn", first.TurnID.String(), "--problem", "stale background intent"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "active turn is 2") {
		t.Fatalf("delayed intent error = %v", err)
	}
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == primitives.EventTypeAgentIntent {
			t.Fatalf("delayed intent was recorded: %#v", event)
		}
	}
}
