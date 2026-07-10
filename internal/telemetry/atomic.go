package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
)

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	return atomicWriteFileWithDurability(path, data, mode, true)
}

// atomicWriteFileRelaxed preserves atomic replacement but does not force the
// disposable aggregate through stable storage on every increment. A process
// crash cannot expose a partial JSON file; a machine-level storage failure may
// lose the latest approximate counter. Consent state and rotated batches use
// atomicWriteFile and retain full durability.
func atomicWriteFileRelaxed(path string, data []byte, mode os.FileMode) error {
	return atomicWriteFileWithDurability(path, data, mode, false)
}

func atomicWriteFileWithDurability(path string, data []byte, mode os.FileMode, durable bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create telemetry directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure telemetry directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse telemetry symlink %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("telemetry path is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect telemetry file: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".turnal-telemetry-*.tmp")
	if err != nil {
		return fmt.Errorf("create telemetry temp file: %w", err)
	}
	tempPath := temp.Name()
	keepTemp := true
	defer func() {
		_ = temp.Close()
		if keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("secure telemetry temp file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write telemetry temp file: %w", err)
	}
	if durable {
		if err := temp.Sync(); err != nil {
			return fmt.Errorf("sync telemetry temp file: %w", err)
		}
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close telemetry temp file: %w", err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("replace telemetry file: %w", err)
	}
	keepTemp = false
	if durable {
		if err := syncDirectory(dir); err != nil {
			return fmt.Errorf("sync telemetry directory: %w", err)
		}
	}
	return nil
}
