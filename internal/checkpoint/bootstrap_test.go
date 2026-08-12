package checkpoint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestBootstrapCreatesWorkspaceMetadataWithoutWorkspaceGit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	result, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !result.GitignoreUpdated {
		t.Fatal("Bootstrap did not report gitignore update")
	}
	if _, err := os.Lstat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
	}

	for _, path := range []string{
		result.Repo.MetadataDir,
		result.Repo.GitDir,
		filepath.Join(result.Repo.MetadataDir, logDirName),
		filepath.Join(result.Repo.MetadataDir, indexDirName),
		result.Repo.TmpDir,
		filepath.Join(result.Repo.MetadataDir, versionFileName),
		filepath.Join(result.Repo.MetadataDir, configFileName),
		result.GitignorePath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected bootstrap path %s: %v", path, err)
		}
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
	configText := string(configData)
	for _, want := range []string{"version = 1", "[run]", "[git_sync]", "[rollback]"} {
		if !strings.Contains(configText, want) {
			t.Fatalf("workspace config missing %q:\n%s", want, configData)
		}
	}
	if !strings.Contains(configText, `mode = "checkpoint"`) || !strings.Contains(configText, "enabled = false") {
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
	if _, err := os.Lstat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
	}

	gitignore, err := os.ReadFile(first.GitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if count := strings.Count(string(gitignore), GitignoreEntry); count != 1 {
		t.Fatalf("gitignore contains %d entries, want 1:\n%s", count, gitignore)
	}
}

func TestBootstrapNeverInitializesWorkspaceGit(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	result, err := BootstrapWithOptions(root, BootstrapOptions{
		UpdateGitignore: true,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions: %v", err)
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
		UpdateGitignore: false,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions: %v", err)
	}
	if result.GitignoreUpdated {
		t.Fatal("gitignore updated despite UpdateGitignore=false")
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists or could not be checked: %v", err)
	}
}

func TestBootstrapSupportsExplicitExternalStore(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	storePath := filepath.Join(t.TempDir(), "store", metadataDirName)
	result, err := BootstrapWithOptions(root, BootstrapOptions{
		UpdateGitignore: false,
		StorePath:       storePath,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions: %v", err)
	}
	if !result.Attached || !sameIdentityPath(result.Repo.MetadataDir, storePath) {
		t.Fatalf("explicit store result = attached:%t path:%q", result.Attached, result.Repo.MetadataDir)
	}
}

func TestExplicitStoreSupportsSymlinkAlias(t *testing.T) {
	requireGit(t)

	storePath := filepath.Join(t.TempDir(), metadataDirName)
	if _, err := InitAt(workspaceRoot(t), storePath); err != nil {
		t.Fatalf("InitAt: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(storePath, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := BootstrapWithOptions(workspaceRoot(t), BootstrapOptions{
		UpdateGitignore: false,
		StorePath:       alias,
	})
	if err != nil {
		t.Fatalf("BootstrapWithOptions alias: %v", err)
	}
	if !result.Attached || !sameIdentityPath(result.Repo.MetadataDir, storePath) {
		t.Fatalf("explicit alias result = attached:%t path:%q", result.Attached, result.Repo.MetadataDir)
	}
	opened, err := OpenExplicitAt(workspaceRoot(t), alias)
	if err != nil {
		t.Fatalf("OpenExplicitAt alias: %v", err)
	}
	if !sameIdentityPath(opened.MetadataDir, storePath) {
		t.Fatalf("opened explicit alias path = %q, want %q", opened.MetadataDir, storePath)
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
	_ = result

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

	_, err = Bootstrap(childRoot)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(childRoot.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("child .git exists or could not be checked: %v", err)
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
		t.Fatal("Bootstrap succeeded, want refusal to use invalid parent Git metadata")
	}
	if !strings.Contains(err.Error(), "refusing to select a Turnal store") {
		t.Fatalf("Bootstrap error = %v, want nested git refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(childRoot.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("child .git exists or could not be checked: %v", err)
	}
}

func TestBootstrapDoesNotCreateWorkspaceGitWithInheritedGitEnv(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	badDir := t.TempDir()
	badGitDir := filepath.Join(badDir, "bad.git")
	t.Setenv("GIT_DIR", badGitDir)
	t.Setenv("GIT_WORK_TREE", badDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(badDir, "bad.index"))

	_, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
	}
	if _, err := os.Stat(badGitDir); !os.IsNotExist(err) {
		t.Fatalf("inherited GIT_DIR was used or stat failed: %v", err)
	}
}

func TestBootstrapRejectsSymlinkedMetadataDirectory(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	target := t.TempDir()
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root.String(), metadataDirName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err = BootstrapWithOptions(root, BootstrapOptions{UpdateGitignore: false})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Bootstrap error = %v, want symlink refusal", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("outside directory mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory was modified: %v", entries)
	}
}

func TestBootstrapRejectsSymlinksInsideMetadata(t *testing.T) {
	requireGit(t)

	for _, test := range []struct {
		name   string
		path   string
		target func(*testing.T) string
	}{
		{name: "directory", path: tmpDirName, target: func(t *testing.T) string { return t.TempDir() }},
		{name: "hidden git", path: gitDirName, target: func(t *testing.T) string { return t.TempDir() }},
		{name: "config", path: configFileName, target: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "outside.toml")
			if err := os.WriteFile(path, []byte("outside\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := workspaceRoot(t)
			metadata := filepath.Join(root.String(), metadataDirName)
			if err := os.Mkdir(metadata, 0o755); err != nil {
				t.Fatal(err)
			}
			target := test.target(t)
			if err := os.Symlink(target, filepath.Join(metadata, test.path)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			var before []byte
			if test.name == "config" {
				var err error
				before, err = os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
			}

			_, err := BootstrapWithOptions(root, BootstrapOptions{UpdateGitignore: false})
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Bootstrap error = %v, want symlink refusal", err)
			}
			if test.name == "config" {
				after, readErr := os.ReadFile(target)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(after) != string(before) {
					t.Fatalf("outside config changed from %q to %q", before, after)
				}
			}
		})
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
	want := "dist/\nnode_modules/\n.turnal/\n"
	if string(content) != want {
		t.Fatalf("gitignore = %q, want %q", content, want)
	}

	info, err := os.Stat(gitignorePath)
	if err != nil {
		t.Fatalf("stat gitignore: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("gitignore mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEnsureGitignoreEntryRejectsSymlink(t *testing.T) {
	root := workspaceRoot(t)
	target := filepath.Join(t.TempDir(), "outside-gitignore")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root.String(), ".gitignore")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, _, err := EnsureGitignoreEntry(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("EnsureGitignoreEntry error = %v, want symlink refusal", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep\n" {
		t.Fatalf("outside gitignore changed to %q", content)
	}
}

func TestEnsureGitignoreEntryRecognizesExistingEntry(t *testing.T) {
	for _, existing := range []string{".turnal", ".turnal/", "/.turnal", "/.turnal/"} {
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
