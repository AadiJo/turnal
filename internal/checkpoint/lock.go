package checkpoint

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
)

const workspaceLockName = "workspace.lock"

var workspaceLocks = struct {
	sync.Mutex
	held map[string]workspaceLock
}{
	held: map[string]workspaceLock{},
}

type workspaceLock struct {
	count int
	lock  *filelock.Lock
}

func (repo *Repo) WorkspaceLockPath() string {
	return filepath.Join(repo.TmpDir, workspaceLockName)
}

func (repo *Repo) WorkspaceLockHeld() bool {
	held, _ := repo.WorkspaceLockStatus()
	return held
}

func (repo *Repo) WorkspaceLockStatus() (bool, error) {
	return filelock.Held(repo.WorkspaceLockPath())
}

func (repo *Repo) WithWorkspaceLock(operation string, fn func() error) error {
	unlock, err := repo.acquireWorkspaceLock(operation)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (repo *Repo) acquireWorkspaceLock(operation string) (func(), error) {
	lockDir := repo.WorkspaceLockPath()

	workspaceLocks.Lock()
	if held, ok := workspaceLocks.held[lockDir]; ok {
		held.count++
		workspaceLocks.held[lockDir] = held
		workspaceLocks.Unlock()
		return func() { releaseWorkspaceLock(lockDir) }, nil
	}
	workspaceLocks.Unlock()

	timeout := repo.LockTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	lock, err := filelock.Acquire(lockDir, timeout)
	if err != nil {
		if errors.Is(err, filelock.ErrBusy) {
			err = fmt.Errorf("workspace lock busy: %s", repo.MetadataDir)
		}
		if operation == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", operation, err)
	}

	workspaceLocks.Lock()
	workspaceLocks.held[lockDir] = workspaceLock{count: 1, lock: lock}
	workspaceLocks.Unlock()

	return func() { releaseWorkspaceLock(lockDir) }, nil
}

func releaseWorkspaceLock(lockDir string) {
	workspaceLocks.Lock()
	defer workspaceLocks.Unlock()

	held, ok := workspaceLocks.held[lockDir]
	if !ok {
		return
	}
	if held.count <= 1 {
		delete(workspaceLocks.held, lockDir)
		_ = held.lock.Release()
		return
	}
	held.count--
	workspaceLocks.held[lockDir] = held
}
