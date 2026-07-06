package workspacegit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/primitives"
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
	runGit(t, parent.String(), "config", "user.email", "agent-vcs@example.test")
	runGit(t, parent.String(), "config", "user.name", "agent-vcs")
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
	if !strings.Contains(err.Error(), "git-sync requires the agent-vcs workspace root to be the Git worktree root") ||
		!strings.Contains(err.Error(), "Run agent-vcs init from the Git root") {
		t.Fatalf("Capture error = %v, want Git root guidance", err)
	}
}

func TestContextCapturesWorkspaceGitState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "agent-vcs@example.test")
	runGit(t, root.String(), "config", "user.name", "agent-vcs")
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
