package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestRootHelpShowsPorcelainCommands(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help command: %v", err)
	}

	output := out.String()
	for _, want := range []string{"init", "destroy", "status", "log", "sessions", "show", "diff", "blame", "replay", "share", "sync"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
	for _, hidden := range []string{"  checkpoint  ", "  turn  ", "  claude-hook  ", "  codex-hook  "} {
		if strings.Contains(output, hidden) {
			t.Fatalf("help output includes hidden command %q:\n%s", hidden, output)
		}
	}
}

func TestRootHelpCommandShowsRootHelp(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help command: %v", err)
	}

	output := out.String()
	for _, want := range []string{"Usage:", "Available Commands:", "--help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
}

func TestUnknownCommandGuidesUserToHelp(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"bogus"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("unknown command succeeded")
	}

	output := out.String()
	for _, want := range []string{
		`Error: unknown command "bogus" for "turnal"`,
		"Run 'turnal --help' for usage.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("unknown command output missing %q:\n%s", want, output)
		}
	}
}

func TestDiffCommandAcceptsTurnTarget(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"diff", sessionID.String() + ":" + turnID.String()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diff command: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{"diff --git a/app.txt b/app.txt", "-before", "+after"} {
		if !strings.Contains(output, want) {
			t.Fatalf("diff output missing %q:\n%s", want, output)
		}
	}
	_ = repo
}

func TestDiffHelpDocumentsTargetForm(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"diff", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diff help: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "turnal diff [session:turn]") {
		t.Fatalf("diff help missing target usage:\n%s", output)
	}
	for _, hidden := range []string{"--session", "--turn", "--pre-ref", "--post-ref"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("diff help includes hidden flag %q:\n%s", hidden, output)
		}
	}
}

func TestShowCommandAcceptsTurnTarget(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("append prompt event: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", sessionID.String() + ":" + turnID.String()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("show command: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"session demo turn 1",
		"adapters: codex",
		"complete: false",
		"turn events:",
		"prompt.user",
		`"text": "change app.txt"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("show output missing %q:\n%s", want, output)
		}
	}
}

func createTurnWithDiff(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	return root, repo, sessionID, turnID
}
