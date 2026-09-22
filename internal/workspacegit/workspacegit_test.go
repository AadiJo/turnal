package workspacegit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestRestoreRefusesTargetNonDirectoryOverDeniedParent(t *testing.T) {
	requireGit(t)
	for _, test := range []struct {
		name    string
		install func(root string) error
	}{
		{name: "symlink outside workspace", install: func(root string) error {
			return os.Symlink("..", filepath.Join(root, "secrets"))
		}},
		{name: "symlink inside workspace", install: func(root string) error {
			if err := os.MkdirAll(filepath.Join(root, "shared"), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, "shared", "app.yaml"), []byte("shared: true\n"), 0o644); err != nil {
				return err
			}
			return os.Symlink("shared", filepath.Join(root, "secrets"))
		}},
		{name: "regular file", install: func(root string) error {
			return os.WriteFile(filepath.Join(root, "secrets"), []byte("tracked file\n"), 0o644)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := workspaceRoot(t, t.TempDir())
			runGit(t, root.String(), "init", "-q")
			runGit(t, root.String(), "config", "user.email", "turnal@example.test")
			runGit(t, root.String(), "config", "user.name", "turnal")
			writeFile(t, root.String(), "README.md", "target\n")
			if err := test.install(root.String()); err != nil {
				t.Skipf("install target parent: %v", err)
			}
			runGit(t, root.String(), "add", "-A")
			runGit(t, root.String(), "commit", "-q", "-m", "target with non-directory parent")
			target, err := Open(root).Capture()
			if err != nil {
				t.Fatalf("Capture target: %v", err)
			}

			runGit(t, root.String(), "rm", "-q", "-r", "--cached", "secrets")
			if err := os.RemoveAll(filepath.Join(root.String(), "secrets")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, root.String(), "README.md", "current\n")
			runGit(t, root.String(), "add", "README.md")
			runGit(t, root.String(), "commit", "-q", "-m", "current without parent")
			currentHead := runGit(t, root.String(), "rev-parse", "HEAD")
			writeFile(t, root.String(), "secrets/.env", "SECRET=preserve-me\n")
			outside := filepath.Join(filepath.Dir(root.String()), ".env")
			if err := os.WriteFile(outside, []byte("OUTSIDE=unchanged\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := Open(root).PreflightRestore(target); err == nil || !strings.Contains(err.Error(), "secrets/.env") {
				t.Fatalf("PreflightRestore error = %v, want refusal naming secrets/.env", err)
			}
			if err := Open(root).Restore(target); err == nil || !strings.Contains(err.Error(), "secrets/.env") {
				t.Fatalf("Restore error = %v, want refusal naming secrets/.env", err)
			}
			if head := runGit(t, root.String(), "rev-parse", "HEAD"); head != currentHead {
				t.Fatalf("HEAD moved to %q after refused restore, want %q", head, currentHead)
			}
			readme, err := os.ReadFile(filepath.Join(root.String(), "README.md"))
			if err != nil || string(readme) != "current\n" {
				t.Fatalf("README after refused restore = %q, err=%v", readme, err)
			}
			info, err := os.Lstat(filepath.Join(root.String(), "secrets"))
			if err != nil || info.Mode().Type() != os.ModeDir {
				t.Fatalf("secrets parent info=%v err=%v, want untouched real directory", info, err)
			}
			secret, err := os.ReadFile(filepath.Join(root.String(), "secrets", ".env"))
			if err != nil || string(secret) != "SECRET=preserve-me\n" {
				t.Fatalf("secret after refused restore = %q, err=%v", secret, err)
			}
			outsideContent, err := os.ReadFile(outside)
			if err != nil || string(outsideContent) != "OUTSIDE=unchanged\n" {
				t.Fatalf("outside file = %q, err=%v", outsideContent, err)
			}
			if _, err := os.Lstat(filepath.Join(root.String(), "shared", ".env")); !os.IsNotExist(err) {
				t.Fatalf("secret leaked into symlink target directory: %v", err)
			}
		})
	}
}

func TestRestoreDoesNotFollowTargetSymlinkForAbsentDeniedFile(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	if err := os.Symlink("..", filepath.Join(root.String(), "secrets")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runGit(t, root.String(), "add", "secrets")
	runGit(t, root.String(), "commit", "-q", "-m", "target with symlink")
	target, err := Open(root).Capture()
	if err != nil {
		t.Fatalf("Capture target: %v", err)
	}

	runGit(t, root.String(), "rm", "-q", "secrets")
	writeFile(t, root.String(), "secrets/.env", "SECRET=committed\n")
	runGit(t, root.String(), "add", "secrets/.env")
	runGit(t, root.String(), "commit", "-q", "-m", "current with denied file")
	if err := os.Remove(filepath.Join(root.String(), "secrets", ".env")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root.String()), ".env")
	if err := os.WriteFile(outside, []byte("OUTSIDE=unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Open(root).Restore(target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	link, err := os.Readlink(filepath.Join(root.String(), "secrets"))
	if err != nil || link != ".." {
		t.Fatalf("restored symlink = %q, err=%v", link, err)
	}
	outsideContent, err := os.ReadFile(outside)
	if err != nil || string(outsideContent) != "OUTSIDE=unchanged\n" {
		t.Fatalf("outside file = %q, err=%v", outsideContent, err)
	}
}

func TestRestorePreservesDeniedSymlinkWithoutCapturingOutsideDescendant(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root.String(), ".turnal/config.toml", "version = 1\n[secrets]\nsnapshot_deny_globs = [\"secrets\"]\n")
	writeFile(t, root.String(), ".gitignore", ".turnal/\n")
	writeFile(t, root.String(), "secrets/config.yaml", "target: tracked\n")
	runGit(t, root.String(), "add", ".gitignore", "secrets/config.yaml")
	runGit(t, root.String(), "commit", "-q", "-m", "target with denied descendant")
	target, err := Open(root).Capture()
	if err != nil {
		t.Fatalf("Capture target: %v", err)
	}

	runGit(t, root.String(), "rm", "-q", "-r", "secrets")
	runGit(t, root.String(), "commit", "-q", "-m", "current without secrets")
	outsideDir := t.TempDir()
	outsideConfig := filepath.Join(outsideDir, "config.yaml")
	if err := os.WriteFile(outsideConfig, []byte("outside: private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root.String(), "secrets")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	preserved, err := Open(root).captureDeniedWorkspaceState(target.State.Head.Commit, []string{"secrets"})
	if err != nil {
		t.Fatalf("captureDeniedWorkspaceState: %v", err)
	}
	descendantFound := false
	for _, entry := range preserved {
		if entry.Path.String() != "secrets/config.yaml" {
			continue
		}
		descendantFound = true
		if entry.Exists || len(entry.Content) != 0 {
			t.Fatalf("outside descendant captured as %#v", entry)
		}
	}
	if !descendantFound {
		t.Fatal("target deny-listed descendant was not considered for preservation")
	}

	if err := Open(root).Restore(target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	link, err := os.Readlink(filepath.Join(root.String(), "secrets"))
	if err != nil || link != outsideDir {
		t.Fatalf("restored denied symlink = %q, err=%v, want %q", link, err, outsideDir)
	}
	outsideContent, err := os.ReadFile(outsideConfig)
	if err != nil || string(outsideContent) != "outside: private\n" {
		t.Fatalf("outside config = %q, err=%v", outsideContent, err)
	}
}

func TestRestoreReplacesFileAncestorForDeniedFile(t *testing.T) {
	root := workspaceRoot(t, t.TempDir())
	writeFile(t, root.String(), "secrets", "target file\n")
	repoPath, err := primitives.ParseRepoPath("secrets/.env")
	if err != nil {
		t.Fatal(err)
	}
	entries := preservedDeniedPaths{{
		Path:    repoPath,
		Exists:  true,
		Mode:    0o600,
		Content: []byte("SECRET=preserve-me\n"),
	}}

	if err := Open(root).restoreDeniedWorkspaceState(entries); err != nil {
		t.Fatalf("restoreDeniedWorkspaceState: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root.String(), "secrets"))
	if err != nil || info.Mode().Type() != os.ModeDir {
		t.Fatalf("secrets ancestor info=%v err=%v, want real directory", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("replacement secrets ancestor mode = %v, want 0700", info.Mode().Perm())
	}
	secret, err := os.ReadFile(filepath.Join(root.String(), "secrets", ".env"))
	if err != nil || string(secret) != "SECRET=preserve-me\n" {
		t.Fatalf("restored secret = %q, err=%v", secret, err)
	}
}

func TestRestoreDeniedStateContinuesPastFailedPath(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX directory permissions enforced for the current user")
	}
	root := workspaceRoot(t, t.TempDir())
	locked := filepath.Join(root.String(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	entries := preservedDeniedPaths{}
	for _, path := range []string{"locked/.env", "open/.env"} {
		repoPath, err := primitives.ParseRepoPath(path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, preservedDeniedPath{
			Path:    repoPath,
			Exists:  true,
			Mode:    0o600,
			Content: []byte("SECRET=" + path + "\n"),
		})
	}

	err := Open(root).restoreDeniedWorkspaceState(entries)
	if err == nil || !strings.Contains(err.Error(), "locked/.env") {
		t.Fatalf("restoreDeniedWorkspaceState error = %v, want failure naming locked/.env", err)
	}
	secret, readErr := os.ReadFile(filepath.Join(root.String(), "open", ".env"))
	if readErr != nil || string(secret) != "SECRET=open/.env\n" {
		t.Fatalf("secret after earlier failure = %q, err=%v; want it restored anyway", secret, readErr)
	}
}

func TestRestorePreservesTrackedDeniedStagedAndWorkingContent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t, t.TempDir())
	runGit(t, root.String(), "init", "-q")
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	runGit(t, root.String(), "config", "core.autocrlf", "false")
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
