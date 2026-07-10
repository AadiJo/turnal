package snapshotpolicy

import (
	"path"
	"path/filepath"
	"strings"
)

// Denied reports whether repoPath is excluded by the configured snapshot
// deny-list. Paths and patterns use repository-style forward slashes.
func Denied(repoPath string, patterns []string) bool {
	repoPath = filepath.ToSlash(repoPath)
	for _, pattern := range patterns {
		if globMatches(filepath.ToSlash(pattern), repoPath) {
			return true
		}
	}
	return false
}

func globMatches(pattern string, repoPath string) bool {
	if matched, _ := path.Match(pattern, repoPath); matched {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if matched, _ := path.Match(pattern, path.Base(repoPath)); matched {
			return true
		}
	}
	if suffix, ok := strings.CutPrefix(pattern, "**/"); ok {
		if matched, _ := path.Match(suffix, repoPath); matched {
			return true
		}
		parts := strings.Split(repoPath, "/")
		for i := 1; i < len(parts); i++ {
			if matched, _ := path.Match(suffix, strings.Join(parts[i:], "/")); matched {
				return true
			}
		}
	}
	return false
}
