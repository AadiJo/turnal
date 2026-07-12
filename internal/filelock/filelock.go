package filelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var ErrBusy = errors.New("lock busy")

type Lock struct {
	file     *os.File
	path     string
	identity Identity
}

type Identity struct {
	PID        int
	AcquiredAt string
}

func Acquire(path string, timeout time.Duration) (*Lock, error) {
	if path == "" {
		return nil, fmt.Errorf("lock path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return nil, fmt.Errorf("legacy directory lock present at %s; ensure no older Turnal process is running, then remove the directory manually", path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect lock path: %w", err)
	}
	file, err := openRegularLockFile(path, true)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		acquired, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire lock: %w", err)
		}
		if acquired {
			lock := &Lock{file: file, path: path}
			if err := lock.writeOwner(); err != nil {
				_ = unlock(file)
				_ = file.Close()
				return nil, err
			}
			return lock, nil
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, ErrBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (lock *Lock) Identity() Identity {
	if lock == nil {
		return Identity{}
	}
	return lock.identity
}

// Inspect reports both kernel lock occupancy and the identity written by the
// process that acquired it. Callers can bind authorization to the original
// owner rather than trusting occupancy of a predictable path.
func Inspect(path string) (Identity, bool, error) {
	held, err := Held(path)
	if err != nil || !held {
		return Identity{}, held, err
	}
	file, err := openRegularLockFile(path, false)
	if err != nil {
		return Identity{}, false, fmt.Errorf("open lock owner: %w", err)
	}
	data, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil {
		return Identity{}, false, fmt.Errorf("read lock owner: %w", err)
	}
	if closeErr != nil {
		return Identity{}, false, fmt.Errorf("close lock owner: %w", closeErr)
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, false, fmt.Errorf("decode lock owner: %w", err)
	}
	if identity.PID <= 0 || identity.AcquiredAt == "" {
		return Identity{}, false, fmt.Errorf("invalid lock owner identity at %s", path)
	}
	return identity, true, nil
}

func Held(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("lock path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("legacy directory lock present at %s", path)
	}
	file, err := openRegularLockFile(path, false)
	if err != nil {
		return false, fmt.Errorf("open lock file: %w", err)
	}
	acquired, err := tryLock(file)
	if err != nil {
		_ = file.Close()
		return false, fmt.Errorf("inspect lock: %w", err)
	}
	if !acquired {
		_ = file.Close()
		return true, nil
	}
	if err := unlock(file); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("release inspected lock: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return false, nil
}

func openRegularLockFile(path string, create bool) (*os.File, error) {
	file, err := openLockFileNoFollow(path, create)
	if err != nil {
		return nil, err
	}
	regular, err := isRegularLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened lock file: %w", err)
	}
	if !regular {
		_ = file.Close()
		return nil, fmt.Errorf("lock path %s is not a regular file", path)
	}
	return file, nil
}

func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	if err := unlock(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("unlock %s: %w", lock.path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close lock %s: %w", lock.path, err)
	}
	return nil
}

func (lock *Lock) writeOwner() error {
	lock.identity = Identity{
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(lock.identity)
	if err != nil {
		return fmt.Errorf("encode lock owner: %w", err)
	}
	data = append(data, '\n')
	if err := lock.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate lock owner: %w", err)
	}
	if _, err := lock.file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek lock owner: %w", err)
	}
	if _, err := lock.file.Write(data); err != nil {
		return fmt.Errorf("write lock owner: %w", err)
	}
	if err := lock.file.Sync(); err != nil {
		return fmt.Errorf("sync lock owner: %w", err)
	}
	return nil
}
