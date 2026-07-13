package adapters

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/fsidentity"
)

// EffectiveHookRoot returns the project root where a provider reads and writes
// hook configuration. Codex uses a verified root checkout for real linked
// worktrees, while Claude and unverified workspaces stay local.
func EffectiveHookRoot(projectRoot string, target Target) string {
	if target != TargetCodex {
		return projectRoot
	}
	if rootCheckout, ok := verifiedLinkedWorktreeRoot(projectRoot); ok {
		return rootCheckout
	}
	return projectRoot
}

type gitWorktreeLayout struct {
	topLevel  string
	gitDir    string
	commonDir string
}

func verifiedLinkedWorktreeRoot(projectRoot string) (string, bool) {
	dotGit := filepath.Join(projectRoot, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	pointer, err := readGitPathFile(dotGit, projectRoot, "gitdir:")
	if err != nil {
		return "", false
	}
	layout, ok := inspectGitWorktree(projectRoot)
	if !ok || !fsidentity.Same(layout.topLevel, projectRoot) || !fsidentity.Same(layout.gitDir, pointer) {
		return "", false
	}
	worktreesDir := filepath.Join(layout.commonDir, "worktrees")
	if !fsidentity.Same(filepath.Dir(layout.gitDir), worktreesDir) {
		return "", false
	}
	backlink, err := readGitPathFile(filepath.Join(layout.gitDir, "gitdir"), layout.gitDir, "")
	if err != nil || !fsidentity.Same(backlink, dotGit) {
		return "", false
	}

	if filepath.Base(layout.commonDir) != ".git" {
		return "", false
	}
	rootCheckout := filepath.Dir(layout.commonDir)
	rootLayout, ok := inspectGitWorktree(rootCheckout)
	if !ok || !fsidentity.Same(rootLayout.topLevel, rootCheckout) ||
		!fsidentity.Same(rootLayout.gitDir, layout.commonDir) ||
		!fsidentity.Same(rootLayout.commonDir, layout.commonDir) {
		return "", false
	}
	return rootCheckout, true
}

func inspectGitWorktree(root string) (gitWorktreeLayout, bool) {
	command := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	command.Env = append(cleanHookGitEnvironment(os.Environ()), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return gitWorktreeLayout{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 {
		return gitWorktreeLayout{}, false
	}
	return gitWorktreeLayout{
		topLevel:  strings.TrimSpace(lines[0]),
		gitDir:    strings.TrimSpace(lines[1]),
		commonDir: strings.TrimSpace(lines[2]),
	}, true
}

func readGitPathFile(path, relativeTo, prefix string) (string, error) {
	data, err := readSmallGitMetadataFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if prefix != "" {
		var found bool
		value, found = strings.CutPrefix(value, prefix)
		if !found {
			return "", os.ErrInvalid
		}
		value = strings.TrimSpace(value)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(relativeTo, value)
	}
	return filepath.Clean(value), nil
}

func readSmallGitMetadataFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	if len(data) > 4096 {
		return nil, io.ErrShortBuffer
	}
	return data, nil
}

func cleanHookGitEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if !found || strings.HasPrefix(name, "GIT_") {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}
