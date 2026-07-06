package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-vcs-again/internal/primitives"
)

const GitignoreEntry = ".agent-vcs/"

type BootstrapResult struct {
	Repo                    *Repo
	WorkspaceGitPath        string
	WorkspaceGitInitialized bool
	GitignorePath           string
	GitignoreUpdated        bool
}

type Status struct {
	WorkspaceRoot     primitives.WorkspaceRoot
	MetadataDir       string
	GitDir            string
	LogDir            string
	IndexDir          string
	TmpDir            string
	Version           string
	ConfigPath        string
	ConfigExists      bool
	GitignorePath     string
	GitignoreHasEntry bool
	HiddenGitExists   bool
	HiddenGitBare     bool
	Problems          []string
}

func Bootstrap(root primitives.WorkspaceRoot) (BootstrapResult, error) {
	repo, err := Init(root)
	if err != nil {
		return BootstrapResult{}, err
	}

	workspaceGitPath, workspaceGitInitialized, err := ensureWorkspaceGit(root)
	if err != nil {
		return BootstrapResult{}, err
	}

	gitignorePath, updated, err := EnsureGitignoreEntry(root)
	if err != nil {
		return BootstrapResult{}, err
	}

	return BootstrapResult{
		Repo:                    repo,
		WorkspaceGitPath:        workspaceGitPath,
		WorkspaceGitInitialized: workspaceGitInitialized,
		GitignorePath:           gitignorePath,
		GitignoreUpdated:        updated,
	}, nil
}

func ensureWorkspaceGit(root primitives.WorkspaceRoot) (string, bool, error) {
	gitPath := filepath.Join(root.String(), ".git")
	if _, err := os.Lstat(gitPath); err == nil {
		return gitPath, false, nil
	} else if !os.IsNotExist(err) {
		return gitPath, false, fmt.Errorf("stat workspace git repo: %w", err)
	}

	output, err := runGitNoRepo(root.String(), "rev-parse", "--is-inside-work-tree")
	if err == nil && strings.TrimSpace(output) == "true" {
		gitDir, gitDirErr := runGitNoRepo(root.String(), "rev-parse", "--absolute-git-dir")
		if gitDirErr != nil {
			return gitPath, false, fmt.Errorf("locate existing workspace git repo: %w", gitDirErr)
		}
		return strings.TrimSpace(gitDir), false, nil
	}
	if err != nil && !gitDiscoveryReportedNoRepo(err) {
		ancestorGitPath, hasAncestorGit, ancestorErr := findAncestorGitPath(root)
		if ancestorErr != nil {
			return gitPath, false, ancestorErr
		}
		if hasAncestorGit {
			return ancestorGitPath, false, fmt.Errorf("workspace git discovery failed under existing git metadata %s; refusing to initialize nested workspace git repo: %w", ancestorGitPath, err)
		}
		return gitPath, false, fmt.Errorf("workspace git discovery failed: %w", err)
	}

	if _, err := runGitNoRepo(root.String(), "init"); err != nil {
		return gitPath, false, fmt.Errorf("init workspace git repo: %w", err)
	}
	return gitPath, true, nil
}

func findAncestorGitPath(root primitives.WorkspaceRoot) (string, bool, error) {
	current := filepath.Dir(root.String())
	for {
		candidate := filepath.Join(current, ".git")
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("stat ancestor workspace git repo: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		current = parent
	}
}

func gitDiscoveryReportedNoRepo(err error) bool {
	return strings.Contains(err.Error(), "not a git repository")
}

func EnsureGitignoreEntry(root primitives.WorkspaceRoot) (string, bool, error) {
	gitignorePath := filepath.Join(root.String(), ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return gitignorePath, false, fmt.Errorf("read .gitignore: %w", err)
		}
		if err := os.WriteFile(gitignorePath, []byte(GitignoreEntry+"\n"), 0o644); err != nil {
			return gitignorePath, false, fmt.Errorf("write .gitignore: %w", err)
		}
		return gitignorePath, true, nil
	}

	if gitignoreHasEntry(string(content)) {
		return gitignorePath, false, nil
	}

	info, err := os.Stat(gitignorePath)
	if err != nil {
		return gitignorePath, false, fmt.Errorf("stat .gitignore: %w", err)
	}

	var builder strings.Builder
	builder.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		builder.WriteByte('\n')
	}
	builder.WriteString(GitignoreEntry)
	builder.WriteByte('\n')

	if err := os.WriteFile(gitignorePath, []byte(builder.String()), info.Mode().Perm()); err != nil {
		return gitignorePath, false, fmt.Errorf("write .gitignore: %w", err)
	}
	return gitignorePath, true, nil
}

func Inspect(root primitives.WorkspaceRoot) Status {
	repo := paths(root)
	status := Status{
		WorkspaceRoot: root,
		MetadataDir:   repo.MetadataDir,
		GitDir:        repo.GitDir,
		LogDir:        filepath.Join(repo.MetadataDir, logDirName),
		IndexDir:      filepath.Join(repo.MetadataDir, indexDirName),
		TmpDir:        repo.TmpDir,
		ConfigPath:    filepath.Join(repo.MetadataDir, configFileName),
		GitignorePath: filepath.Join(root.String(), ".gitignore"),
	}

	checkDir := func(label, path string) {
		info, err := os.Stat(path)
		if err != nil {
			status.Problems = append(status.Problems, fmt.Sprintf("%s missing: %s", label, path))
			return
		}
		if !info.IsDir() {
			status.Problems = append(status.Problems, fmt.Sprintf("%s is not a directory: %s", label, path))
		}
	}

	checkDir("metadata dir", status.MetadataDir)
	checkDir("log dir", status.LogDir)
	checkDir("index dir", status.IndexDir)
	checkDir("tmp dir", status.TmpDir)

	if info, err := os.Stat(status.GitDir); err == nil && info.IsDir() {
		status.HiddenGitExists = true
		if bare, err := repo.HiddenGitBare(); err == nil {
			status.HiddenGitBare = bare
			if !bare {
				status.Problems = append(status.Problems, "hidden git repo is not bare")
			}
		} else {
			status.Problems = append(status.Problems, fmt.Sprintf("hidden git repo verification failed: %v", err))
		}
	} else if err != nil {
		status.Problems = append(status.Problems, fmt.Sprintf("hidden git repo missing: %s", status.GitDir))
	} else {
		status.Problems = append(status.Problems, fmt.Sprintf("hidden git repo is not a directory: %s", status.GitDir))
	}

	versionPath := filepath.Join(status.MetadataDir, versionFileName)
	if version, err := os.ReadFile(versionPath); err == nil {
		status.Version = strings.TrimSpace(string(version))
		if status.Version == "" {
			status.Problems = append(status.Problems, "VERSION is empty")
		}
	} else {
		status.Problems = append(status.Problems, fmt.Sprintf("VERSION missing: %s", versionPath))
	}

	if info, err := os.Stat(status.ConfigPath); err == nil && !info.IsDir() {
		status.ConfigExists = true
	} else if err != nil {
		status.Problems = append(status.Problems, fmt.Sprintf("config missing: %s", status.ConfigPath))
	} else {
		status.Problems = append(status.Problems, fmt.Sprintf("config is not a file: %s", status.ConfigPath))
	}

	if gitignore, err := os.ReadFile(status.GitignorePath); err == nil {
		status.GitignoreHasEntry = gitignoreHasEntry(string(gitignore))
		if !status.GitignoreHasEntry {
			status.Problems = append(status.Problems, ".gitignore does not contain .agent-vcs/")
		}
	} else if os.IsNotExist(err) {
		status.Problems = append(status.Problems, ".gitignore missing .agent-vcs/ entry")
	} else {
		status.Problems = append(status.Problems, fmt.Sprintf("read .gitignore: %v", err))
	}

	return status
}

func (status Status) OK() bool {
	return len(status.Problems) == 0
}

func (repo *Repo) HiddenGitBare() (bool, error) {
	output, err := runGitNoRepo(repo.WorkspaceRoot.String(), "--git-dir", repo.GitDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "true", nil
}

func gitignoreHasEntry(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch line {
		case ".agent-vcs", GitignoreEntry, "/.agent-vcs", "/.agent-vcs/":
			return true
		}
	}
	return false
}
