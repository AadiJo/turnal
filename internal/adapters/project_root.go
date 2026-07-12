package adapters

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EffectiveHookRoot returns the project root where a provider reads and writes
// hook configuration. Codex uses the root checkout's project configuration for
// linked worktrees, while Claude uses the current worktree.
func EffectiveHookRoot(projectRoot string, target Target) string {
	if target != TargetCodex {
		return projectRoot
	}
	if rootCheckout, ok := linkedWorktreeRootCheckout(projectRoot); ok {
		return rootCheckout
	}
	return projectRoot
}

func linkedWorktreeRootCheckout(projectRoot string) (string, bool) {
	dotGit := filepath.Join(projectRoot, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || info.IsDir() {
		return "", false
	}
	data, err := readSmallGitMetadataFile(dotGit)
	if err != nil {
		return "", false
	}
	gitDirText, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !found {
		return "", false
	}
	gitDir := strings.TrimSpace(gitDirText)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(projectRoot, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	commonDir := ""
	if data, err := readSmallGitMetadataFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		commonDir = filepath.Clean(commonDir)
	} else if filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		commonDir = filepath.Dir(filepath.Dir(gitDir))
	}
	if filepath.Base(commonDir) != ".git" {
		return "", false
	}
	return filepath.Dir(commonDir), true
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
