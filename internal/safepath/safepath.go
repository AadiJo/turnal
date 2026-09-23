// Package safepath protects workspace-owned paths from pre-existing symlinks.
package safepath

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ValidateNoSymlinks rejects symlinks in the existing portion of relative.
// The trusted root itself is not inspected.
func ValidateNoSymlinks(root, relative string) error {
	return walk(root, relative, 0, false)
}

// MkdirAllNoSymlinks creates relative below root without following existing
// symlink components.
func MkdirAllNoSymlinks(root, relative string, mode fs.FileMode) error {
	return walk(root, relative, mode, true)
}

func walk(root, relative string, mode fs.FileMode, create bool) error {
	clean := filepath.Clean(relative)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must stay below root: %s", relative)
	}
	if clean == "." {
		return nil
	}

	parts := strings.Split(clean, string(filepath.Separator))
	current := filepath.Clean(root)
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("inspect path %s: %w", current, err)
			}
			if !create {
				return nil
			}
			if err := os.Mkdir(current, mode); err != nil {
				if !os.IsExist(err) {
					return fmt.Errorf("create directory %s: %w", current, err)
				}
				info, err = os.Lstat(current)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("concurrently created path is not a real directory: %s", current)
				}
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in workspace-owned path: %s", current)
		}
		if (create || index < len(parts)-1) && !info.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
	}
	return nil
}
