package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestReplayCheckoutCreatesIsolatedWorktree(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "working copy\n")
	writeFile(t, root, "extra.txt", "do not copy\n")
	replayPath := filepath.Join(t.TempDir(), "replay-turn")

	output := runRootStdout(t, "replay", "checkout", sessionID.String()+":turn:1", "--path", replayPath)
	for _, want := range []string{
		"replay worktree: " + replayPath,
		"state: demo turn 1 post",
		"turnal replay next",
		"turnal replay prev",
		"turnal replay goto demo:turn:1:post",
		"turnal replay diff",
		"turnal replay show",
		"turnal replay keep",
		"turnal replay stop",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("replay output missing %q:\n%s", want, output)
		}
	}

	assertFileContent(t, replayPath, "app.txt", "after 1\n")
	assertFileContent(t, root.String(), "app.txt", "working copy\n")
	assertFileContent(t, root.String(), "extra.txt", "do not copy\n")
	if _, err := os.Stat(filepath.Join(replayPath, ".turnal")); !os.IsNotExist(err) {
		t.Fatalf("replay worktree contains .turnal or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(replayPath, ".turnal-replay.json")); err != nil {
		t.Fatalf("replay marker missing: %v", err)
	}
}

func TestReplayPrevNextAndDiff(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 2)
	t.Chdir(root.String())

	replayPath := filepath.Join(t.TempDir(), "replay-turn")
	runRootStdout(t, "replay", "checkout", sessionID.String()+":turn:1", "--path", replayPath)

	output := runRootStdout(t, "replay", "prev")
	if !strings.Contains(output, "state: demo turn 1 pre") {
		t.Fatalf("prev output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "before 1\n")

	output = runRootStdout(t, "replay", "next")
	if !strings.Contains(output, "state: demo turn 1 post") {
		t.Fatalf("next output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "after 1\n")

	diff := runRootStdout(t, "replay", "diff")
	for _, want := range []string{"diff --git a/app.txt b/app.txt", "-before 1", "+after 1"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("replay diff missing %q:\n%s", want, diff)
		}
	}
}

func TestReplayRangeTargetNavigatesOnlyRange(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 3)
	t.Chdir(root.String())

	replayPath := filepath.Join(t.TempDir(), "replay-range")
	output := runRootStdout(t, "replay", sessionID.String()+":turn:2..2", "--path", replayPath)
	if !strings.Contains(output, "state: demo turn 2 pre") {
		t.Fatalf("range output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "after 1\n")

	output = runRootStdout(t, "replay", "next")
	if !strings.Contains(output, "state: demo turn 2 post") {
		t.Fatalf("range next output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "after 2\n")

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"replay", "next"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("range next past end succeeded")
	}
	if !strings.Contains(err.Error(), "already at last replay checkpoint") {
		t.Fatalf("range next error = %v", err)
	}
}

func TestReplayRejectsSourceWorkspacePath(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"replay", "checkout", sessionID.String() + ":turn:1", "--path", filepath.Join(root.String(), "replay")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("replay checkout into source workspace succeeded")
	}
	if !strings.Contains(err.Error(), "outside the source workspace") {
		t.Fatalf("replay checkout error = %v", err)
	}
}

func TestReplayRejectsSymlinkPathIntoSourceWorkspace(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	linkPath := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(root.String(), linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"replay", "checkout", sessionID.String() + ":turn:1", "--path", filepath.Join(linkPath, "replay")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("replay checkout through source workspace symlink succeeded")
	}
	if !strings.Contains(err.Error(), "outside the source workspace") {
		t.Fatalf("replay checkout error = %v", err)
	}
}

func TestReplayStopRemovesActiveWorktree(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	output := runRootStdout(t, "replay", "checkout", sessionID.String()+":turn:1")
	replayPath := replayPathFromOutput(t, output)
	if _, err := os.Stat(replayPath); err != nil {
		t.Fatalf("replay worktree missing: %v", err)
	}

	output = runRootStdout(t, "replay", "stop")
	if !strings.Contains(output, "removed replay worktree: "+replayPath) {
		t.Fatalf("stop output = %s", output)
	}
	if _, err := os.Stat(replayPath); !os.IsNotExist(err) {
		t.Fatalf("replay worktree still exists or stat failed: %v", err)
	}

	output = runRootStdout(t, "replay", "list")
	if !strings.Contains(output, "no replay sessions") {
		t.Fatalf("list output = %s", output)
	}
}

func TestReplayKeepCopiesCurrentState(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	runRootStdout(t, "replay", "checkout", sessionID.String()+":turn:1")
	keepPath := filepath.Join(t.TempDir(), "kept")
	output := runRootStdout(t, "replay", "keep", keepPath)
	if !strings.Contains(output, "kept replay state: "+keepPath) {
		t.Fatalf("keep output = %s", output)
	}
	assertFileContent(t, keepPath, "app.txt", "after 1\n")
	if _, err := os.Stat(filepath.Join(keepPath, ".turnal-replay.json")); !os.IsNotExist(err) {
		t.Fatalf("keep copy contains replay marker or stat failed: %v", err)
	}
}

func TestReplayGotoListRemoveAndDiffModes(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 2)
	t.Chdir(root.String())

	replayPath := filepath.Join(t.TempDir(), "replay")
	output := runRootStdout(t, "replay", sessionID.String(), "--path", replayPath)
	if !strings.Contains(output, "state: demo turn 1 pre") {
		t.Fatalf("session replay output = %s", output)
	}

	diff := runRootStdout(t, "replay", "diff", "--next")
	for _, want := range []string{"diff --git a/app.txt b/app.txt", "-before 1", "+after 1"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("replay diff --next missing %q:\n%s", want, diff)
		}
	}

	writeFile(t, root, "app.txt", "working copy\n")
	diff = runRootStdout(t, "replay", "diff", "--workspace")
	for _, want := range []string{"diff --git a/app.txt b/app.txt", "-before 1", "+working copy"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("replay diff --workspace missing %q:\n%s", want, diff)
		}
	}

	output = runRootStdout(t, "replay", "goto", sessionID.String()+":turn:2:post")
	if !strings.Contains(output, "state: demo turn 2 post") {
		t.Fatalf("goto output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "after 2\n")

	output = runRootStdout(t, "replay", "list")
	for _, want := range []string{"*", "demo:turn:2:post", replayPath} {
		if !strings.Contains(output, want) {
			t.Fatalf("replay list missing %q:\n%s", want, output)
		}
	}

	output = runRootStdout(t, "replay", "remove", replayPath)
	if !strings.Contains(output, "removed replay worktree: "+replayPath) {
		t.Fatalf("remove output = %s", output)
	}
	if _, err := os.Stat(replayPath); !os.IsNotExist(err) {
		t.Fatalf("removed replay worktree still exists or stat failed: %v", err)
	}
}

func TestReplayKeepWithoutPathMakesStopPreserveWorktree(t *testing.T) {
	root, _, sessionID, _ := createReplayTurns(t, 1)
	t.Chdir(root.String())

	output := runRootStdout(t, "replay", "checkout", sessionID.String()+":turn:1")
	replayPath := replayPathFromOutput(t, output)
	output = runRootStdout(t, "replay", "keep")
	if !strings.Contains(output, "kept replay worktree: "+replayPath) {
		t.Fatalf("keep output = %s", output)
	}

	output = runRootStdout(t, "replay", "stop")
	if !strings.Contains(output, "kept replay worktree: "+replayPath) {
		t.Fatalf("stop output = %s", output)
	}
	assertFileContent(t, replayPath, "app.txt", "after 1\n")
	if _, err := os.Stat(filepath.Join(replayPath, ".turnal-replay.json")); !os.IsNotExist(err) {
		t.Fatalf("kept stopped worktree still has replay marker or stat failed: %v", err)
	}
}

func createReplayTurns(t *testing.T, count int) (primitives.WorkspaceRoot, *checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")

	var lastTurn primitives.TurnID
	for i := 1; i <= count; i++ {
		turnID, err := primitives.NewTurnID(uint64(i))
		if err != nil {
			t.Fatalf("turn id: %v", err)
		}
		lastTurn = turnID
		if i == 1 {
			writeFile(t, root, "app.txt", "before 1\n")
		}
		if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
			t.Fatalf("pre checkpoint %d: %v", i, err)
		}
		writeFile(t, root, "app.txt", "after "+turnID.String()+"\n")
		if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
			t.Fatalf("post checkpoint %d: %v", i, err)
		}
	}
	return root, repo, sessionID, lastTurn
}

func assertFileContent(t *testing.T, root string, relPath string, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read %s under %s: %v", relPath, root, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", relPath, data, want)
	}
}

func replayPathFromOutput(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if path, ok := strings.CutPrefix(line, "replay worktree: "); ok {
			return path
		}
	}
	t.Fatalf("replay worktree path missing from output:\n%s", output)
	return ""
}
