package agentskills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/safepath"
)

const managedDirectory = "skills"

type Result struct {
	Agent string
	Path  string
}

// Install writes Turnal's bundled skills into its metadata directory and links
// them into each selected agent's project-scoped skill directory.
func Install(projectRoot, metadataDir string, agents []string) ([]Result, error) {
	sourceRoot := filepath.Join(metadataDir, managedDirectory)
	if err := writeBundledSkills(metadataDir); err != nil {
		return nil, err
	}

	var results []Result
	for _, agent := range uniqueStrings(agents) {
		destinationRoot, ok := agentSkillDirectory(projectRoot, agent)
		if !ok {
			continue
		}
		relative, err := filepath.Rel(projectRoot, destinationRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve %s skill directory: %w", agent, err)
		}
		if err := safepath.MkdirAllNoSymlinks(projectRoot, relative, 0o755); err != nil {
			return nil, fmt.Errorf("create %s skill directory %s: %w", agent, destinationRoot, err)
		}
		for _, skill := range bundledSkillNames() {
			source := filepath.Join(sourceRoot, skill)
			destination := filepath.Join(destinationRoot, skill)
			if err := ensureSymlink(source, destination); err != nil {
				return nil, fmt.Errorf("install %s skill %s: %w", agent, skill, err)
			}
		}
		results = append(results, Result{Agent: agent, Path: destinationRoot})
	}
	return results, nil
}

func agentSkillDirectory(projectRoot, agent string) (string, bool) {
	switch agent {
	case "codex":
		return filepath.Join(projectRoot, ".agents", "skills"), true
	case "claude":
		return filepath.Join(projectRoot, ".claude", "skills"), true
	default:
		return "", false
	}
}

func writeBundledSkills(metadataDir string) error {
	if info, err := os.Lstat(metadataDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("metadata path must be a real directory; symlinks are not allowed: %s", metadataDir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect metadata directory: %w", err)
	} else if err := os.MkdirAll(metadataDir, 0o700); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	paths := make([]string, 0, len(bundledSkillFiles))
	for path := range bundledSkillFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		relative := filepath.Join(managedDirectory, filepath.FromSlash(path))
		destination := filepath.Join(metadataDir, relative)
		if err := safepath.MkdirAllNoSymlinks(metadataDir, filepath.Dir(relative), 0o755); err != nil {
			return fmt.Errorf("create bundled skill directory: %w", err)
		}
		if err := safepath.ValidateNoSymlinks(metadataDir, relative); err != nil {
			return fmt.Errorf("inspect bundled skill path: %w", err)
		}
		if err := os.WriteFile(destination, bundledSkillFiles[path], 0o644); err != nil {
			return fmt.Errorf("write bundled skill %s: %w", destination, err)
		}
	}
	return nil
}

func bundledSkillNames() []string {
	seen := make(map[string]struct{})
	for path := range bundledSkillFiles {
		name := path
		if separator := strings.IndexByte(path, '/'); separator >= 0 {
			name = path[:separator]
		}
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func ensureSymlink(source, destination string) error {
	info, err := os.Lstat(destination)
	if err == nil {
		current, err := os.Readlink(destination)
		if err != nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("read existing symlink %s: %w", destination, err)
			}
			return fmt.Errorf("destination already exists and is not a directory link: %s", destination)
		}
		resolved := current
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(destination), resolved)
		}
		if filepath.Clean(resolved) == filepath.Clean(source) {
			return nil
		}
		return fmt.Errorf("destination is a directory link to %s, expected %s", current, source)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination %s: %w", destination, err)
	}
	target, err := filepath.Rel(filepath.Dir(destination), source)
	if err != nil {
		target = source
	}
	if err := createDirectoryLink(source, target, destination); err != nil {
		return fmt.Errorf("create symlink %s -> %s: %w", destination, target, err)
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
