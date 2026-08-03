package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/AadiJo/turnal/internal/primitives"
)

// RegisteredStore is one entry from the user-state registry that turnal init
// writes. It is the machine-wide answer to "which projects are recorded", and
// is what lets tooling enumerate stores without walking the filesystem.
type RegisteredStore struct {
	RepoID       primitives.RepoID
	StoreID      primitives.StoreID
	StorePath    string
	GitCommonDir string
	Worktrees    []RegisteredWorktree
}

// RegisteredWorktree is one workspace attached to a store. A store always has
// at least one, and gains more when Git worktrees are linked.
type RegisteredWorktree struct {
	Root       string
	GitDir     string
	LastSeenAt string
}

// StateDir returns the directory holding machine-wide Turnal state, honoring
// TURNAL_STATE_DIR and XDG_STATE_HOME the same way the registry does.
func StateDir() (string, error) {
	path, err := registryPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// RegistryPath returns the absolute path of the user-state registry file.
func RegistryPath() (string, error) {
	return registryPath()
}

// ListRegisteredStores reports every store in the user-state registry, sorted
// by store path so output is stable. Entries whose store directory is missing
// are still returned: recorded history can outlive a deleted working tree, and
// silently dropping the entry would look like the history never existed.
// Callers decide how to present a store whose path no longer resolves.
func ListRegisteredStores() ([]RegisteredStore, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	value, err := readRegistry(path)
	if err != nil {
		return nil, err
	}
	stores := make([]RegisteredStore, 0, len(value.Stores))
	for _, entry := range value.Stores {
		store := RegisteredStore{
			RepoID:       entry.RepoID,
			StoreID:      entry.StoreID,
			StorePath:    entry.StorePath,
			GitCommonDir: entry.GitCommonDir,
		}
		for _, worktree := range entry.Worktrees {
			store.Worktrees = append(store.Worktrees, RegisteredWorktree{
				Root:       worktree.Root,
				GitDir:     worktree.GitDir,
				LastSeenAt: worktree.LastSeen,
			})
		}
		sort.Slice(store.Worktrees, func(i, j int) bool {
			return store.Worktrees[i].Root < store.Worktrees[j].Root
		})
		stores = append(stores, store)
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].StorePath < stores[j].StorePath })
	return stores, nil
}

// StoreExists reports whether a registered store still has its hidden Git
// directory on disk. A registered store whose path is gone is stale, not
// corrupt.
func StoreExists(storePath string) bool {
	info, err := os.Stat(filepath.Join(storePath, gitDirName))
	return err == nil && info.IsDir()
}

// RegisterStore adds a store to the user-state registry explicitly.
//
// Automatic registration during ensureIdentity only covers Git workspaces,
// because it keys stores by Git common dir. Turnal records non-Git directories
// too, and those projects must still appear in the machine-wide inventory, so
// callers that intentionally adopt a project (turnal init, the viewer's add
// flow) call this to cover both cases. It is idempotent.
func (repo *Repo) RegisterStore() error {
	if repo == nil {
		return fmt.Errorf("register store requires repo")
	}
	binding, ok := repo.primaryWorktreeBinding()
	if !ok {
		binding = repo.WorktreeIdentity()
	}
	return registerRepo(repo, binding)
}

// DeregisterStore removes a store from the user-state registry. It deletes no
// files: the .turnal directory, its recorded history, and any installed agent
// hooks are left exactly as they are, so the store can be re-registered later
// with turnal init or turnal worktree attach. Use internal/destroy to actually
// remove recorded history.
func DeregisterStore(storeID primitives.StoreID) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	removed := false
	if err := withRegistryLock(path, func() error {
		value, err := readRegistry(path)
		if err != nil {
			return err
		}
		kept := make([]registryStore, 0, len(value.Stores))
		for _, entry := range value.Stores {
			if entry.StoreID == storeID {
				removed = true
				continue
			}
			kept = append(kept, entry)
		}
		if !removed {
			return nil
		}
		value.Stores = kept
		value.Version = registryVersion
		return writeJSONAtomic(path, value, 0o600)
	}); err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("store %s is not registered", storeID)
	}
	return nil
}
