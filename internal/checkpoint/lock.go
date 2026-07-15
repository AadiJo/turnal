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
	gate  chan struct{}
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
		held = &workspaceLock{gate: make(chan struct{}, 1)}
		held.gate <- struct{}{}
		workspaceLocks.held[lockDir] = held
	}
	held.users++
	workspaceLocks.Unlock()
	timeout := repo.LockTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-held.gate:
	case <-timer.C:
		releaseWorkspaceLockEntry(lockDir, held)
		return nil, workspaceLockError(repo, operation, filelock.ErrBusy)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		held.gate <- struct{}{}
		releaseWorkspaceLockEntry(lockDir, held)
		return nil, workspaceLockError(repo, operation, filelock.ErrBusy)
	}
	lock, err := filelock.Acquire(lockDir, remaining)
	if err != nil {
		held.gate <- struct{}{}
		releaseWorkspaceLockEntry(lockDir, held)
		return nil, workspaceLockError(repo, operation, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = lock.Release()
			held.gate <- struct{}{}
			releaseWorkspaceLockEntry(lockDir, held)
		})
	}, nil
}

func workspaceLockError(repo *Repo, operation string, err error) error {
	if errors.Is(err, filelock.ErrBusy) {
		err = fmt.Errorf("workspace lock busy: %s", repo.MetadataDir)
	}
	if operation == "" {
		return err
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func releaseWorkspaceLockEntry(lockDir string, held *workspaceLock) {
	workspaceLocks.Lock()
	defer workspaceLocks.Unlock()
	held.users--
	if held.users == 0 && workspaceLocks.held[lockDir] == held {
		delete(workspaceLocks.held, lockDir)
	}
}
