// Package fsidentity compares filesystem paths by identity rather than spelling.
package fsidentity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Same reports whether left and right identify the same existing filesystem
// object. It falls back to canonical path spelling when either path cannot be
// inspected.
func Same(left, right string) bool {
	left = canonical(left)
	right = canonical(right)
	if left == "" || right == "" {
		return false
	}

	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	return sameSpelling(left, right)
}

// Within reports whether path identifies root or an object below root. For
// existing paths it walks parent identities, which handles symlink aliases,
// macOS /var versus /private/var paths, and Windows short versus long names.
func Within(path, root string) bool {
	path = canonical(path)
	root = canonical(root)
	if path == "" || root == "" {
		return false
	}
	if Same(path, root) {
		return true
	}

	rootInfo, rootErr := os.Stat(root)
	if rootErr == nil && rootInfo.IsDir() {
		current := path
		if pathInfo, err := os.Stat(current); err == nil && !pathInfo.IsDir() {
			current = filepath.Dir(current)
		}
		for {
			if currentInfo, err := os.Stat(current); err == nil && os.SameFile(currentInfo, rootInfo) {
				return true
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	left, right := root, path
	if runtime.GOOS == "windows" {
		left = strings.ToLower(left)
		right = strings.ToLower(right)
	}
	rel, err := filepath.Rel(left, right)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func canonical(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if abs, err := filepath.Abs(value); err == nil {
		value = abs
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return filepath.Clean(value)
}

func sameSpelling(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
