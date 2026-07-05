package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	gitignore, err := os.ReadFile(first.GitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if count := strings.Count(string(gitignore), GitignoreEntry); count != 1 {
		t.Fatalf("gitignore contains %d entries, want 1:\n%s", count, gitignore)
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
