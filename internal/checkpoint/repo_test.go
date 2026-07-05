package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/primitives"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func TestInitCreatesHiddenBareRepo(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	output, err := runGitNoRepo(root.String(), "--git-dir", repo.GitDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		t.Fatalf("verify bare repo: %v", err)
	}
	if strings.TrimSpace(output) != "true" {
		t.Fatalf("is-bare-repository = %q, want true", output)
	}
}

func TestCreateCheckpointSnapshotsWorktreeAndExcludesMetadata(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, "src/app.txt", "hello\n")
	writeFile(t, root, ".git/config", "user git metadata\n")
	writeFile(t, root, ".agent-vcs/tmp/internal.txt", "tool metadata\n")
	writeFile(t, root, "nested/.git/config", "nested git metadata\n")
	writeFile(t, root, "nested/.AGENT-VCS/tmp/internal.txt", "nested tool metadata\n")

	sessionID, _ := primitives.ParseSessionID("Demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":src/app.txt")
	if err != nil {
		t.Fatalf("show captured file: %v", err)
	}
	if content != "hello\n" {
		t.Fatalf("captured content = %q, want hello", content)
	}

	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":.git/config"); err == nil {
		t.Fatal(".git/config was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":.agent-vcs/tmp/internal.txt"); err == nil {
		t.Fatal(".agent-vcs metadata was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.git/config"); err == nil {
		t.Fatal("nested .git/config was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.AGENT-VCS/tmp/internal.txt"); err == nil {
		t.Fatal("nested .AGENT-VCS metadata was captured, want excluded")
	}
}

func TestCreateCheckpointBypassesGitFilters(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, ".gitattributes", "*.txt text eol=lf\n")
	writeFile(t, root, "crlf.txt", "hello\r\n")

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":crlf.txt")
	if err != nil {
		t.Fatalf("show crlf file: %v", err)
	}
	if content != "hello\r\n" {
		t.Fatalf("captured content = %q, want raw CRLF bytes", content)
	}
}

func TestCreateCheckpointStoresSymlinkWithoutFollowing(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, "target.txt", "target content\n")
	if err := os.Symlink("target.txt", filepath.Join(root.String(), "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	tree, err := runHiddenGit(repo, "", "ls-tree", checkpoint.Commit.String(), "link.txt")
	if err != nil {
		t.Fatalf("ls-tree symlink: %v", err)
	}
	if !strings.Contains(tree, "120000 blob") {
		t.Fatalf("symlink tree entry = %q, want mode 120000", tree)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":link.txt")
	if err != nil {
		t.Fatalf("show symlink: %v", err)
	}
	if content != "target.txt" {
		t.Fatalf("symlink blob = %q, want link target", content)
	}
}

func TestCleanGitEnvDropsInheritedGitVariables(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"GIT_DIR=/bad",
		"GIT_WORK_TREE=/bad",
		"GIT_INDEX_FILE=/bad",
		"GIT_OBJECT_DIRECTORY=/bad",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/bad",
		"HOME=/home/test",
	}

	cleaned := cleanGitEnv(env)
	for _, entry := range cleaned {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Fatalf("cleaned env still contains %s in %v", key, cleaned)
		}
	}
	for _, want := range []string{"PATH=/bin", "HOME=/home/test"} {
		if !containsString(cleaned, want) {
			t.Fatalf("cleaned env missing %s: %v", want, cleaned)
		}
	}
}

func TestDiffTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "src/app.txt", "hello\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}

	writeFile(t, root, "src/app.txt", "hello world\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	diffText := string(diff)
	for _, want := range []string{"diff --git a/src/app.txt b/src/app.txt", "-hello", "+hello world"} {
		if !strings.Contains(diffText, want) {
			t.Fatalf("diff missing %q:\n%s", want, diffText)
		}
	}
}

func workspaceRoot(t *testing.T) primitives.WorkspaceRoot {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func writeFile(t *testing.T, root primitives.WorkspaceRoot, relPath, content string) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
