package checkpoint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const permissionsVersionFileName = "PERMISSIONS_V2"

// ensureSecureMetadataPermissions performs a one-time upgrade sweep for
// metadata created by older Turnal versions. The hidden Git object database is
// deliberately excluded because Git manages its own object permissions.
func ensureSecureMetadataPermissions(repo *Repo) error {
	marker := filepath.Join(repo.MetadataDir, permissionsVersionFileName)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect metadata permission version: %w", err)
	}

	for _, root := range []string{
		repo.TmpDir,
		filepath.Join(repo.MetadataDir, logDirName),
		filepath.Join(repo.MetadataDir, indexDirName),
	} {
		if err := secureMetadataTree(root); err != nil {
			return err
		}
	}
	for _, name := range []string{versionFileName, configFileName} {
		path := filepath.Join(repo.MetadataDir, name)
		if err := chmodRegularFile(path, 0o600); err != nil {
			return err
		}
	}
	if err := os.Chmod(repo.MetadataDir, 0o700); err != nil {
		return fmt.Errorf("secure metadata directory: %w", err)
	}

	tmp, err := os.CreateTemp(repo.MetadataDir, ".permissions-v2-*")
	if err != nil {
		return fmt.Errorf("create metadata permission marker: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure metadata permission marker: %w", err)
	}
	if _, err := tmp.WriteString("2\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write metadata permission marker: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync metadata permission marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close metadata permission marker: %w", err)
	}
	if err := os.Rename(tmpPath, marker); err != nil {
		return fmt.Errorf("install metadata permission marker: %w", err)
	}
	return syncDirectory(repo.MetadataDir)
}

func secureMetadataTree(root string) error {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect metadata path %s: %w", root, err)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("secure metadata directory %s: %w", path, err)
			}
		case info.Mode().IsRegular():
			if err := os.Chmod(path, 0o600); err != nil {
				return fmt.Errorf("secure metadata file %s: %w", path, err)
			}
		}
		return nil
	})
}

func chmodRegularFile(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect metadata file %s: %w", path, err)
	}
	if info.Mode().IsRegular() {
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("secure metadata file %s: %w", path, err)
		}
	}
	return nil
}
