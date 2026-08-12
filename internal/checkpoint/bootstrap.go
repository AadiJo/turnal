package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
)

const GitignoreEntry = ".turnal/"

type BootstrapResult struct {
	Repo             *Repo
	Attached         bool
	GitignorePath    string
	GitignoreUpdated bool
}

type BootstrapOptions struct {
	UpdateGitignore bool
	StorePath       string
}

func DefaultBootstrapOptions() BootstrapOptions {
	return BootstrapOptions{
		UpdateGitignore: true,
	}
}

type Status struct {
	WorkspaceRoot     primitives.WorkspaceRoot
	MetadataDir       string
	RepoID            primitives.RepoID
	StoreID           primitives.StoreID
	WorktreeID        primitives.WorktreeID
	Attached          bool
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
	return BootstrapWithOptions(root, DefaultBootstrapOptions())
}

func BootstrapWithOptions(root primitives.WorkspaceRoot, opts BootstrapOptions) (BootstrapResult, error) {
	repo, attached, err := bootstrapRepo(root, opts.StorePath)
	if err != nil {
		return BootstrapResult{}, err
	}

	if gitIdentity, identityErr := discoverUserGit(root.String()); identityErr == nil {
		if err := repo.ensureIdentity(&gitIdentity); err != nil {
			return BootstrapResult{}, err
		}
	}

	gitignorePath := filepath.Join(root.String(), ".gitignore")
	var updated bool
	if opts.UpdateGitignore {
		gitignorePath, updated, err = EnsureGitignoreEntry(root)
		if err != nil {
			return BootstrapResult{}, err
		}
	}

	return BootstrapResult{
		Repo:             repo,
		Attached:         attached,
		GitignorePath:    gitignorePath,
		GitignoreUpdated: updated,
	}, nil
}

func bootstrapRepo(root primitives.WorkspaceRoot, explicitStorePath string) (*Repo, bool, error) {
	if strings.TrimSpace(explicitStorePath) != "" {
		storePath, err := resolveExplicitStorePath(explicitStorePath)
		if err != nil {
			return nil, false, fmt.Errorf("resolve explicit store path: %w", err)
		}
		if info, err := os.Stat(filepath.Join(storePath, gitDirName)); err == nil && info.IsDir() {
			repo, err := OpenAt(root, storePath)
			return repo, !sameIdentityPath(storePath, filepath.Join(root.String(), metadataDirName)), err
		}
		repo, err := InitAt(root, storePath)
		return repo, !sameIdentityPath(storePath, filepath.Join(root.String(), metadataDirName)), err
	}

	localMetadata := filepath.Join(root.String(), metadataDirName)
	if info, err := os.Stat(filepath.Join(localMetadata, gitDirName)); err == nil && info.IsDir() {
		repo, err := InitAt(root, localMetadata)
		return repo, false, err
	} else if err != nil && !os.IsNotExist(err) {
		return nil, false, fmt.Errorf("stat local Turnal store: %w", err)
	}

	gitIdentity, err := discoverUserGit(root.String())
	if err != nil {
		if isNoGitRepository(err) {
			repo, initErr := Init(root)
			return repo, false, initErr
		}
		if ancestorPath, found, ancestorErr := findAncestorGitPath(root); ancestorErr != nil {
			return nil, false, ancestorErr
		} else if found {
			return nil, false, fmt.Errorf("workspace git discovery failed under existing git metadata %s; refusing to select a Turnal store while Git metadata is invalid: %w", ancestorPath, err)
		}
		return nil, false, fmt.Errorf("discover workspace git identity: %w", err)
	}
	if store, ok, err := resolveRegisteredStore(gitIdentity); err != nil {
		return nil, false, err
	} else if ok {
		repo, openErr := OpenAt(root, store.StorePath)
		return repo, !sameIdentityPath(store.StorePath, localMetadata), openErr
	}

	storePath := filepath.Join(gitIdentity.PrimaryRoot, metadataDirName)
	if gitIdentity.PrimaryRoot == "" {
		return nil, false, fmt.Errorf("cannot choose Turnal store location: Git primary worktree is unavailable; pass --store <path>")
	}
	repo, err := InitAt(root, storePath)
	return repo, !sameIdentityPath(storePath, localMetadata), err
}

func resolveExplicitStorePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	return abs, nil
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
	if opened, err := OpenReadOnly(root); err == nil {
		repo = opened
	}
	status := Status{
		WorkspaceRoot: root,
		MetadataDir:   repo.MetadataDir,
		RepoID:        repo.RepoID,
		StoreID:       repo.StoreID,
		WorktreeID:    repo.WorktreeID,
		Attached:      !sameIdentityPath(repo.MetadataDir, filepath.Join(root.String(), metadataDirName)),
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
			status.Problems = append(status.Problems, ".gitignore does not contain .turnal/")
		}
	} else if os.IsNotExist(err) {
		status.Problems = append(status.Problems, ".gitignore missing .turnal/ entry")
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
		case ".turnal", GitignoreEntry, "/.turnal", "/.turnal/":
			return true
		}
	}
	return false
}
