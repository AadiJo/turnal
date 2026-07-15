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
	held map[string]*workspaceLock
}{
	held: map[string]*workspaceLock{},
}

type workspaceLock struct {
	gate  sync.Mutex
	users int
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
	held, ok := workspaceLocks.held[lockDir]
	if !ok {
		held = &workspaceLock{}
		workspaceLocks.held[lockDir] = held
	}
	held.users++
	workspaceLocks.Unlock()
	held.gate.Lock()

	timeout := repo.LockTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	lock, err := filelock.Acquire(lockDir, timeout)
	if err != nil {
		held.gate.Unlock()
		releaseWorkspaceLockEntry(lockDir, held)
		if errors.Is(err, filelock.ErrBusy) {
			err = fmt.Errorf("workspace lock busy: %s", repo.MetadataDir)
		}
		if operation == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", operation, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = lock.Release()
			held.gate.Unlock()
			releaseWorkspaceLockEntry(lockDir, held)
		})
	}, nil
}

func releaseWorkspaceLockEntry(lockDir string, held *workspaceLock) {
	workspaceLocks.Lock()
	defer workspaceLocks.Unlock()
	held.users--
	if held.users == 0 && workspaceLocks.held[lockDir] == held {
		delete(workspaceLocks.held, lockDir)
	}
}
