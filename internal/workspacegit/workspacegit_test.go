package workspacegit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestCaptureRequiresGitWorktree(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	_, err := Open(root).Capture()
	if err == nil {
		t.Fatal("Capture succeeded outside a Git worktree")
	}
	if !strings.Contains(err.Error(), "git-sync requires an initialized Git worktree") {
		t.Fatalf("Capture error = %v, want initialized Git worktree guidance", err)
	}
}

func TestCaptureRequiresInitialCommit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")

	_, err := Open(root).Capture()
	if err == nil {
		t.Fatal("Capture succeeded in unborn Git repo")
	}
	if !strings.Contains(err.Error(), "git-sync requires workspace Git to have an initial HEAD commit") {
		t.Fatalf("Capture error = %v, want initial HEAD commit guidance", err)
	}
}

func TestCaptureRequiresWorkspaceRootAtGitRoot(t *testing.T) {
	requireGit(t)

	parent := workspaceRoot(t, t.TempDir())
	runGit(t, parent.String(), "init", "-q")
	runGit(t, parent.String(), "config", "user.email", "turnal@example.test")
	runGit(t, parent.String(), "config", "user.name", "turnal")
	writeFile(t, parent.String(), "README.md", "base\n")
	runGit(t, parent.String(), "add", "README.md")
	runGit(t, parent.String(), "commit", "-q", "-m", "base")

	child := filepath.Join(parent.String(), "nested")
	writeFile(t, parent.String(), "nested/file.txt", "nested\n")
	root := workspaceRoot(t, child)

	_, err := Open(root).Capture()
	if err == nil {
		t.Fatal("Capture succeeded from nested workspace root")
	}
	if !strings.Contains(err.Error(), "git-sync requires the turnal workspace root to be the Git worktree root") ||
		!strings.Contains(err.Error(), "Run turnal init from the Git root") {
		t.Fatalf("Capture error = %v, want Git root guidance", err)
	}
}

func TestContextCapturesWorkspaceGitState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root.String(), "README.md", "base\n")
	runGit(t, root.String(), "add", "README.md")
	runGit(t, root.String(), "commit", "-q", "-m", "base")

	context, err := Open(root).Context()
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if !context.Exists || context.Head == "" || context.Branch == "" || context.Detached {
		t.Fatalf("context missing head/branch: %#v", context)
	}
	if context.WorktreeRoot != root.String() {
		t.Fatalf("worktree root = %q, want %q", context.WorktreeRoot, root.String())
	}
	if context.Dirty {
		t.Fatalf("context dirty = true for clean repo: %#v", context)
	}

	writeFile(t, root.String(), "README.md", "changed\n")
	context, err = Open(root).Context()
	if err != nil {
		t.Fatalf("dirty Context: %v", err)
	}
	if !context.Dirty {
		t.Fatalf("context dirty = false after worktree change: %#v", context)
	}
}

func TestContextReportsMissingWorkspaceGit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	context, err := Open(root).Context()
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if context.Exists {
		t.Fatalf("context Exists = true outside Git repo: %#v", context)
	}
}

func TestCaptureExcludesSnapshotDeniedFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root.String(), "README.md", "base\n")
	runGit(t, root.String(), "add", "README.md")
	runGit(t, root.String(), "commit", "-q", "-m", "base")

	writeFile(t, root.String(), ".env", "TOP_SECRET=untracked\n")
	runGit(t, root.String(), "add", ".env")
	writeFile(t, root.String(), "nested/credentials.json", "{\"token\":\"secret\"}\n")
	writeFile(t, root.String(), "app.txt", "safe\n")

	capture, err := Open(root).Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(capture.State.Untracked) != 1 || capture.State.Untracked[0].Path.String() != "app.txt" {
		t.Fatalf("captured untracked = %#v, want only app.txt", capture.State.Untracked)
	}
	if len(capture.State.Staged.Paths) != 0 || len(capture.StagedPatch) != 0 {
		t.Fatalf("denied staged file entered capture: paths=%#v patch=%s", capture.State.Staged.Paths, capture.StagedPatch)
	}
	for _, content := range capture.UntrackedContent {
		if bytes.Contains(content, []byte("TOP_SECRET")) || bytes.Contains(content, []byte("token")) {
			t.Fatal("capture contains denied secret content")
		}
	}
}

func TestEnsureNoOperationInProgressResolvesRelativeGitPathAtWorkspace(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	writeFile(t, root.String(), ".git/MERGE_HEAD", strings.Repeat("0", 40)+"\n")

	err := Open(root).ensureNoOperationInProgress()
	if err == nil || !strings.Contains(err.Error(), "MERGE_HEAD") {
		t.Fatalf("ensureNoOperationInProgress error = %v, want MERGE_HEAD", err)
	}
}

func TestRestorePreservesCurrentDeniedUntrackedFile(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root.String(), "README.md", "base\n")
	runGit(t, root.String(), "add", "README.md")
	runGit(t, root.String(), "commit", "-q", "-m", "base")
	target, err := Open(root).Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	writeFile(t, root.String(), ".env", "TOP_SECRET=preserve-me\n")
	writeFile(t, root.String(), "remove.txt", "remove me\n")

	if err := Open(root).Restore(target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	secret, err := os.ReadFile(filepath.Join(root.String(), ".env"))
	if err != nil {
		t.Fatalf("read preserved .env: %v", err)
	}
	if string(secret) != "TOP_SECRET=preserve-me\n" {
		t.Fatalf("preserved .env = %q", secret)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "remove.txt")); !os.IsNotExist(err) {
		t.Fatalf("ordinary untracked file remains after restore: %v", err)
	}
}

func TestRestorePreservesTrackedDeniedStagedAndWorkingContent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root.String(), "README.md", "base\n")
	writeFile(t, root.String(), ".env", "BASE=committed\n")
	runGit(t, root.String(), "add", "README.md", ".env")
	runGit(t, root.String(), "commit", "-q", "-m", "base")
	target, err := Open(root).Capture()
	if err != nil {
		t.Fatalf("Capture target: %v", err)
	}

	writeFile(t, root.String(), ".env", "SECRET=staged\n")
	runGit(t, root.String(), "add", ".env")
	writeFile(t, root.String(), ".env", "SECRET=working\n")
	writeFile(t, root.String(), "README.md", "ordinary change\n")

	if err := Open(root).Restore(target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	working, err := os.ReadFile(filepath.Join(root.String(), ".env"))
	if err != nil {
		t.Fatalf("read working .env: %v", err)
	}
	if string(working) != "SECRET=working\n" {
		t.Fatalf("working .env = %q, want preserved content", working)
	}
	staged := runGit(t, root.String(), "show", ":.env")
	if staged != "SECRET=staged\n" {
		t.Fatalf("staged .env = %q, want preserved staged content", staged)
	}
	readme, err := os.ReadFile(filepath.Join(root.String(), "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(readme) != "base\n" {
		t.Fatalf("ordinary tracked change survived restore: %q", readme)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func workspaceRoot(t *testing.T, path string) primitives.WorkspaceRoot {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(path)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}
