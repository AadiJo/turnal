package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const workspaceLockName = "workspace.lock"

var workspaceLocks = struct {
	sync.Mutex
	held map[string]int
}{
	held: map[string]int{},
}

func (repo *Repo) WorkspaceLockPath() string {
	return filepath.Join(repo.TmpDir, workspaceLockName)
}

func (repo *Repo) WorkspaceLockHeld() bool {
	_, err := os.Stat(repo.WorkspaceLockPath())
	return err == nil
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
	if workspaceLocks.held[lockDir] > 0 {
		workspaceLocks.held[lockDir]++
		workspaceLocks.Unlock()
		return func() { releaseWorkspaceLock(lockDir, false) }, nil
	}
	workspaceLocks.Unlock()

	if err := os.MkdirAll(filepath.Dir(lockDir), 0o755); err != nil {
		return nil, fmt.Errorf("create workspace lock dir: %w", err)
	}
	if err := acquireWorkspaceDirLock(lockDir); err != nil {
		if operation == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", operation, err)
	}

	workspaceLocks.Lock()
	workspaceLocks.held[lockDir] = 1
	workspaceLocks.Unlock()

	return func() { releaseWorkspaceLock(lockDir, true) }, nil
}

func releaseWorkspaceLock(lockDir string, removeDir bool) {
	workspaceLocks.Lock()
	defer workspaceLocks.Unlock()

	count := workspaceLocks.held[lockDir]
	if count <= 1 {
		delete(workspaceLocks.held, lockDir)
		if removeDir {
			_ = os.Remove(lockDir)
		}
		return
	}
	workspaceLocks.held[lockDir] = count - 1
}

func acquireWorkspaceDirLock(lockDir string) error {
	const attempts = 100
	for i := 0; i < attempts; i++ {
		if err := os.Mkdir(lockDir, 0o700); err == nil {
			return nil
		} else if !os.IsExist(err) {
			return fmt.Errorf("create workspace lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("workspace lock busy: %s", strings.TrimSuffix(lockDir, ".lock"))
}
