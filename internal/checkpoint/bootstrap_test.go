package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/primitives"
)

func TestBootstrapCreatesWorkspaceMetadataAndGitignore(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	result, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !result.GitignoreUpdated {
		t.Fatal("Bootstrap did not report gitignore update")
	}
	if !result.WorkspaceGitInitialized {
		t.Fatal("Bootstrap did not report workspace git initialization")
	}
	if result.WorkspaceGitPath != filepath.Join(root.String(), ".git") {
		t.Fatalf("workspace git path = %q, want root .git", result.WorkspaceGitPath)
	}

	for _, path := range []string{
		result.Repo.MetadataDir,
		result.Repo.GitDir,
		filepath.Join(result.Repo.MetadataDir, logDirName),
		filepath.Join(result.Repo.MetadataDir, indexDirName),
		result.Repo.TmpDir,
		filepath.Join(result.Repo.MetadataDir, versionFileName),
		filepath.Join(result.Repo.MetadataDir, configFileName),
		result.WorkspaceGitPath,
		result.GitignorePath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected bootstrap path %s: %v", path, err)
		}
	}

	inside, err := runGitNoRepo(root.String(), "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("verify workspace git repo: %v", err)
	}
	if strings.TrimSpace(inside) != "true" {
		t.Fatalf("is-inside-work-tree = %q, want true", inside)
	}

	gitignore, err := os.ReadFile(result.GitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if string(gitignore) != GitignoreEntry+"\n" {
		t.Fatalf("gitignore = %q, want %q", gitignore, GitignoreEntry+"\n")
	}

	configData, err := os.ReadFile(filepath.Join(result.Repo.MetadataDir, configFileName))
	if err != nil {
		t.Fatalf("read workspace config: %v", err)
	}
	if !strings.Contains(string(configData), "version = 1") || !strings.Contains(string(configData), "[run]") {
		t.Fatalf("workspace config missing template content:\n%s", configData)
	}

	status := Inspect(root)
	if !status.OK() {
		t.Fatalf("Inspect after bootstrap has problems: %v", status.Problems)
	}
	if status.Version != "1" {
		t.Fatalf("status version = %q, want 1", status.Version)
	}
	if !status.HiddenGitExists || !status.HiddenGitBare || !status.ConfigExists || !status.GitignoreHasEntry {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	first, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	second, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if !first.GitignoreUpdated {
		t.Fatal("first bootstrap should update gitignore")
	}
	if second.GitignoreUpdated {
		t.Fatal("second bootstrap should not update gitignore")
	}
	if !first.WorkspaceGitInitialized {
		t.Fatal("first bootstrap should initialize workspace git")
	}
	if second.WorkspaceGitInitialized {
		t.Fatal("second bootstrap should not reinitialize workspace git")
	}

	gitignore, err := os.ReadFile(first.GitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if count := strings.Count(string(gitignore), GitignoreEntry); count != 1 {
		t.Fatalf("gitignore contains %d entries, want 1:\n%s", count, gitignore)
	}
}

func TestBootstrapWithOptionsCanSkipWorkspaceGit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	result, err := BootstrapWithOptions(root, BootstrapOptions{
		InitWorkspaceGit: false,
		UpdateGitignore:  true,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions: %v", err)
	}
	if result.WorkspaceGitInitialized {
		t.Fatal("workspace git initialized despite InitWorkspaceGit=false")
	}
	if _, err := os.Lstat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
	}
	if !result.GitignoreUpdated {
		t.Fatal("gitignore was not updated")
	}
	if _, err := os.Stat(result.Repo.GitDir); err != nil {
		t.Fatalf("hidden git repo missing: %v", err)
	}
}

func TestBootstrapWithOptionsCanSkipGitignore(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	result, err := BootstrapWithOptions(root, BootstrapOptions{
		InitWorkspaceGit: true,
		UpdateGitignore:  false,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions: %v", err)
	}
	if !result.WorkspaceGitInitialized {
		t.Fatal("workspace git was not initialized")
	}
	if result.GitignoreUpdated {
		t.Fatal("gitignore updated despite UpdateGitignore=false")
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists or could not be checked: %v", err)
	}
}

func TestBootstrapDoesNotMutateExistingWorkspaceGit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := runGitNoRepo(root.String(), "init"); err != nil {
		t.Fatalf("init workspace git: %v", err)
	}
	configPath := filepath.Join(root.String(), ".git", "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read git config before Bootstrap: %v", err)
	}

	result, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.WorkspaceGitInitialized {
		t.Fatal("Bootstrap reinitialized existing workspace git")
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read git config after Bootstrap: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("workspace git config changed unexpectedly:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestBootstrapDoesNotCreateNestedGitRepoInsideParentWorktree(t *testing.T) {
	requireGit(t)

	parent := workspaceRoot(t)
	if _, err := runGitNoRepo(parent.String(), "init"); err != nil {
		t.Fatalf("init parent git: %v", err)
	}

	childPath := filepath.Join(parent.String(), "child")
	if err := os.MkdirAll(childPath, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	childRoot, err := primitives.ParseWorkspaceRoot(childPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}

	result, err := Bootstrap(childRoot)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if result.WorkspaceGitInitialized {
		t.Fatal("Bootstrap initialized nested workspace git inside parent worktree")
	}
	if _, err := os.Lstat(filepath.Join(childRoot.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("child .git exists or could not be checked: %v", err)
	}

	parentGitPath := filepath.Join(parent.String(), ".git")
	if result.WorkspaceGitPath != parentGitPath {
		t.Fatalf("workspace git path = %q, want parent git path %q", result.WorkspaceGitPath, parentGitPath)
	}
}

func TestBootstrapRefusesNestedGitInitWhenParentGitDiscoveryFails(t *testing.T) {
	requireGit(t)

	parent := workspaceRoot(t)
	parentGitPath := filepath.Join(parent.String(), ".git")
	if _, err := runGitNoRepo(parent.String(), "init"); err != nil {
		t.Fatalf("init parent git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentGitPath, "config"), []byte("[invalid\n"), 0o644); err != nil {
		t.Fatalf("corrupt parent git config: %v", err)
	}

	childPath := filepath.Join(parent.String(), "child")
	if err := os.MkdirAll(childPath, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	childRoot, err := primitives.ParseWorkspaceRoot(childPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	if _, err := runGitNoRepo(childRoot.String(), "rev-parse", "--is-inside-work-tree"); err == nil {
		t.Fatal("test setup unexpectedly created a discoverable parent git worktree")
	}

	_, err = Bootstrap(childRoot)
	if err == nil {
		t.Fatal("Bootstrap succeeded, want refusal to initialize nested workspace git")
	}
	if !strings.Contains(err.Error(), "refusing to initialize nested workspace git repo") {
		t.Fatalf("Bootstrap error = %v, want nested git refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(childRoot.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("child .git exists or could not be checked: %v", err)
	}
}

func TestBootstrapWorkspaceGitInitDropsInheritedGitEnv(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	badDir := t.TempDir()
	badGitDir := filepath.Join(badDir, "bad.git")
	t.Setenv("GIT_DIR", badGitDir)
	t.Setenv("GIT_WORK_TREE", badDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(badDir, "bad.index"))

	result, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !result.WorkspaceGitInitialized {
		t.Fatal("Bootstrap did not initialize workspace git")
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".git")); err != nil {
		t.Fatalf("expected root .git: %v", err)
	}
	if _, err := os.Stat(badGitDir); !os.IsNotExist(err) {
		t.Fatalf("inherited GIT_DIR was used or stat failed: %v", err)
	}
}

func TestEnsureGitignoreEntryAppendsAndPreservesMode(t *testing.T) {
	root := workspaceRoot(t)
	gitignorePath := filepath.Join(root.String(), ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("dist/\nnode_modules/"), 0o600); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	path, updated, err := EnsureGitignoreEntry(root)
	if err != nil {
		t.Fatalf("EnsureGitignoreEntry: %v", err)
	}
	if !updated {
		t.Fatal("EnsureGitignoreEntry did not report update")
	}
	if path != gitignorePath {
		t.Fatalf("path = %q, want %q", path, gitignorePath)
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	want := "dist/\nnode_modules/\n.agent-vcs/\n"
	if string(content) != want {
		t.Fatalf("gitignore = %q, want %q", content, want)
	}

	info, err := os.Stat(gitignorePath)
	if err != nil {
		t.Fatalf("stat gitignore: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("gitignore mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEnsureGitignoreEntryRecognizesExistingEntry(t *testing.T) {
	for _, existing := range []string{".agent-vcs", ".agent-vcs/", "/.agent-vcs", "/.agent-vcs/"} {
		t.Run(existing, func(t *testing.T) {
			root := workspaceRoot(t)
			gitignorePath := filepath.Join(root.String(), ".gitignore")
			initial := "# comment\n" + existing + "\n"
			if err := os.WriteFile(gitignorePath, []byte(initial), 0o644); err != nil {
				t.Fatalf("write gitignore: %v", err)
			}

			_, updated, err := EnsureGitignoreEntry(root)
			if err != nil {
				t.Fatalf("EnsureGitignoreEntry: %v", err)
			}
			if updated {
				t.Fatal("EnsureGitignoreEntry updated an already configured file")
			}

			content, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Fatalf("read gitignore: %v", err)
			}
			if string(content) != initial {
				t.Fatalf("gitignore changed unexpectedly: %q", content)
			}
		})
	}
}

func TestInspectReportsProblems(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	status := Inspect(root)
	if status.OK() {
		t.Fatal("Inspect on uninitialized workspace reported OK")
	}

	if _, err := Bootstrap(root); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), metadataDirName, configFileName)); err != nil {
		t.Fatalf("remove config: %v", err)
	}

	status = Inspect(root)
	if status.OK() {
		t.Fatal("Inspect after deleting config reported OK")
	}
	if !containsProblem(status.Problems, "config missing") {
		t.Fatalf("problems missing config issue: %v", status.Problems)
	}
}

func containsProblem(problems []string, want string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, want) {
			return true
		}
	}
	return false
}
