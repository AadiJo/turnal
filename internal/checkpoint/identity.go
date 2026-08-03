package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/fsidentity"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	identityVersion      = 2
	identityFileName     = "identity.json"
	worktreesDirName     = "worktrees"
	registryVersion      = 1
	registryFileName     = "registry.json"
	stateDirEnv          = "TURNAL_STATE_DIR"
	registryLockAttempts = 100
	registryLockWait     = 10 * time.Millisecond
)

type StoreIdentity struct {
	Version         int                `json:"version"`
	RepoID          primitives.RepoID  `json:"repo_id"`
	StoreID         primitives.StoreID `json:"store_id"`
	GitObjectFormat string             `json:"git_object_format"`
	CreatedAt       string             `json:"created_at"`
}

type WorktreeIdentity struct {
	Version      int                        `json:"version"`
	WorktreeID   primitives.WorktreeID      `json:"worktree_id"`
	ProducerID   primitives.EventProducerID `json:"event_producer_id"`
	Root         string                     `json:"root"`
	GitTopLevel  string                     `json:"git_toplevel,omitempty"`
	GitCommonDir string                     `json:"git_common_dir,omitempty"`
	GitDir       string                     `json:"git_dir,omitempty"`
	Primary      bool                       `json:"primary"`
	AttachedAt   string                     `json:"attached_at"`
	LastSeenAt   string                     `json:"last_seen_at"`
}

type RekeyResult struct {
	OldStoreID primitives.StoreID
	NewStoreID primitives.StoreID
	Worktrees  int
}

type UserGitIdentity struct {
	TopLevel     string
	GitDir       string
	GitCommonDir string
	PrimaryRoot  string
}

type registry struct {
	Version int             `json:"version"`
	Stores  []registryStore `json:"stores"`
}

type registryStore struct {
	GitCommonDir string                      `json:"git_common_dir"`
	RepoID       primitives.RepoID           `json:"repo_id"`
	StoreID      primitives.StoreID          `json:"store_id"`
	StorePath    string                      `json:"store_path"`
	Worktrees    map[string]registryWorktree `json:"worktrees,omitempty"`
}

type registryWorktree struct {
	Root     string `json:"root"`
	GitDir   string `json:"git_dir,omitempty"`
	LastSeen string `json:"last_seen_at"`
}

func (repo *Repo) Identity() StoreIdentity {
	return StoreIdentity{
		Version:         repo.IdentityVersion,
		RepoID:          repo.RepoID,
		StoreID:         repo.StoreID,
		GitObjectFormat: repo.GitObjectFormat,
	}
}

func (repo *Repo) WorktreeIdentity() WorktreeIdentity {
	return WorktreeIdentity{
		Version:      1,
		WorktreeID:   repo.WorktreeID,
		ProducerID:   repo.EventProducerID,
		Root:         repo.WorkspaceRoot.String(),
		GitTopLevel:  repo.GitTopLevel,
		GitCommonDir: repo.GitCommonDir,
		GitDir:       repo.UserGitDir,
		Primary:      repo.PrimaryWorktree,
	}
}

func (repo *Repo) ListWorktrees() ([]WorktreeIdentity, error) {
	if repo == nil {
		return nil, fmt.Errorf("list worktrees requires repo")
	}
	return listWorktreeIdentities(repo.MetadataDir)
}

func (repo *Repo) primaryWorktreeBinding() (WorktreeIdentity, bool) {
	bindings, err := listWorktreeIdentities(repo.MetadataDir)
	if err != nil {
		return WorktreeIdentity{}, false
	}
	for _, binding := range bindings {
		if binding.Primary {
			return binding, true
		}
	}
	return WorktreeIdentity{}, false
}

func (repo *Repo) RepairRegistration() error {
	if repo == nil {
		return fmt.Errorf("repair registration requires repo")
	}
	identity, err := discoverUserGit(repo.WorkspaceRoot.String())
	if err != nil {
		return fmt.Errorf("repair registration requires a Git worktree: %w", err)
	}
	return repo.ensureIdentity(&identity)
}

func (repo *Repo) RekeyStore() (RekeyResult, error) {
	if repo == nil {
		return RekeyResult{}, fmt.Errorf("rekey requires repo")
	}
	var result RekeyResult
	err := repo.WithWorkspaceLock("rekey store", func() error {
		identity, err := readOrCreateStoreIdentity(repo)
		if err != nil {
			return err
		}
		bindings, err := listWorktreeIdentities(repo.MetadataDir)
		if err != nil {
			return err
		}
		infos, err := repo.ListAllCheckpointRefInfos()
		if err != nil {
			return err
		}
		for _, info := range infos {
			parts, err := info.Ref.Parts()
			if err != nil || parts.Scoped || parts.Canonical || parts.Manual {
				continue
			}
			alias, err := primitives.NewScopedCheckpointRef(info.WorktreeID, info.StreamID, info.SessionID, info.TurnID, info.Phase)
			if err != nil {
				return err
			}
			if err := repo.EnsureImportedCheckpointAlias(alias, info.Commit); err != nil {
				return err
			}
			if _, err := runHiddenGit(repo, "", "update-ref", "-d", info.Ref.String()); err != nil {
				return err
			}
		}

		newStoreID, err := primitives.NewStoreID()
		if err != nil {
			return err
		}
		result.OldStoreID = identity.StoreID
		result.NewStoreID = newStoreID
		result.Worktrees = len(bindings)
		identity.StoreID = newStoreID
		if err := writeJSONAtomic(filepath.Join(repo.MetadataDir, identityFileName), identity, 0o600); err != nil {
			return err
		}
		for _, binding := range bindings {
			oldPath := filepath.Join(repo.MetadataDir, worktreesDirName, binding.WorktreeID.String()+".json")
			worktreeID, err := primitives.NewWorktreeID()
			if err != nil {
				return err
			}
			producerID, err := primitives.NewEventProducerID()
			if err != nil {
				return err
			}
			binding.WorktreeID = worktreeID
			binding.ProducerID = producerID
			binding.AttachedAt = time.Now().UTC().Format(time.RFC3339Nano)
			binding.LastSeenAt = binding.AttachedAt
			if err := writeWorktreeIdentity(repo.MetadataDir, binding); err != nil {
				return err
			}
			if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			if sameIdentityPath(binding.Root, repo.WorkspaceRoot.String()) {
				repo.WorktreeID = binding.WorktreeID
				repo.EventProducerID = binding.ProducerID
			}
		}
		repo.StoreID = newStoreID
		if repo.GitCommonDir == "" {
			return nil
		}
		return repo.RepairRegistration()
	})
	return result, err
}

func (repo *Repo) StreamID(sessionID primitives.SessionID) (primitives.EventStreamID, error) {
	if repo.EventProducerID == "" {
		return "", fmt.Errorf("event stream invariant failed: repo has no event producer id")
	}
	return primitives.DeriveEventStreamID(repo.EventProducerID, sessionID)
}

func (repo *Repo) ensureIdentity(gitIdentity *UserGitIdentity) error {
	identity, err := readOrCreateStoreIdentity(repo)
	if err != nil {
		return err
	}
	repo.IdentityVersion = identity.Version
	repo.RepoID = identity.RepoID
	repo.StoreID = identity.StoreID
	repo.GitObjectFormat = identity.GitObjectFormat

	binding, err := readOrCreateWorktreeIdentity(repo, gitIdentity)
	if err != nil {
		return err
	}
	repo.WorktreeID = binding.WorktreeID
	repo.EventProducerID = binding.ProducerID
	repo.GitTopLevel = binding.GitTopLevel
	repo.GitCommonDir = binding.GitCommonDir
	repo.UserGitDir = binding.GitDir
	repo.PrimaryWorktree = binding.Primary
	repo.ScopedRefs = !binding.Primary && binding.GitCommonDir != ""

	if binding.GitCommonDir != "" {
		if err := registerRepo(repo, binding); err != nil {
			return err
		}
	}
	return nil
}

func (repo *Repo) readIdentity(gitIdentity *UserGitIdentity) error {
	identity, err := readOrCreateStoreIdentityReadOnly(repo)
	if err != nil {
		return err
	}
	bindings, err := listWorktreeIdentities(repo.MetadataDir)
	if err != nil {
		return err
	}
	root := cleanIdentityPath(repo.WorkspaceRoot.String())
	gitDir := ""
	if gitIdentity != nil {
		gitDir = cleanIdentityPath(gitIdentity.GitDir)
	}
	var binding *WorktreeIdentity
	for i := range bindings {
		if gitDir != "" && bindings[i].GitDir != "" && sameIdentityPath(bindings[i].GitDir, gitDir) {
			binding = &bindings[i]
			break
		}
		if sameIdentityPath(bindings[i].Root, root) {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		return fmt.Errorf("worktree identity invariant failed: no existing binding for %s; a read-only open cannot attach a worktree", root)
	}
	if err := validateReadOnlyWorktreeIdentity(*binding, root, gitIdentity); err != nil {
		return err
	}

	repo.IdentityVersion = identity.Version
	repo.RepoID = identity.RepoID
	repo.StoreID = identity.StoreID
	repo.GitObjectFormat = identity.GitObjectFormat
	repo.WorktreeID = binding.WorktreeID
	repo.EventProducerID = binding.ProducerID
	repo.GitTopLevel = binding.GitTopLevel
	repo.GitCommonDir = binding.GitCommonDir
	repo.UserGitDir = binding.GitDir
	repo.PrimaryWorktree = binding.Primary
	repo.ScopedRefs = !binding.Primary && binding.GitCommonDir != ""
	return nil
}

func validateReadOnlyWorktreeIdentity(binding WorktreeIdentity, root string, gitIdentity *UserGitIdentity) error {
	if !sameIdentityPath(binding.Root, root) {
		return fmt.Errorf("worktree identity invariant failed: binding root %s does not match current root %s; a read-only open cannot refresh it", binding.Root, root)
	}
	if gitIdentity == nil {
		if binding.GitTopLevel != "" || binding.GitCommonDir != "" || binding.GitDir != "" {
			return fmt.Errorf("worktree identity invariant failed: binding for %s records Git identity but the current workspace is not a Git worktree", root)
		}
		return nil
	}

	expectedPrimary := sameIdentityPath(root, gitIdentity.PrimaryRoot)
	checks := []struct {
		name    string
		stored  string
		current string
	}{
		{name: "Git top-level", stored: binding.GitTopLevel, current: gitIdentity.TopLevel},
		{name: "Git common directory", stored: binding.GitCommonDir, current: gitIdentity.GitCommonDir},
		{name: "Git directory", stored: binding.GitDir, current: gitIdentity.GitDir},
	}
	for _, check := range checks {
		if check.stored == "" || check.current == "" || !sameIdentityPath(check.stored, check.current) {
			return fmt.Errorf("worktree identity invariant failed: stored %s %s does not match current %s; a read-only open cannot refresh it", check.name, check.stored, check.current)
		}
	}
	if binding.Primary != expectedPrimary {
		return fmt.Errorf("worktree identity invariant failed: stored primary status %t does not match current status %t; a read-only open cannot refresh it", binding.Primary, expectedPrimary)
	}
	return nil
}

func readOrCreateStoreIdentity(repo *Repo) (StoreIdentity, error) {
	path := filepath.Join(repo.MetadataDir, identityFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		var identity StoreIdentity
		if err := json.Unmarshal(data, &identity); err != nil {
			return StoreIdentity{}, fmt.Errorf("store identity invariant failed: parse %s: %w", path, err)
		}
		if identity.Version != identityVersion {
			return StoreIdentity{}, fmt.Errorf("store identity invariant failed: unsupported version %d", identity.Version)
		}
		if identity.RepoID, err = primitives.ParseRepoID(identity.RepoID.String()); err != nil {
			return StoreIdentity{}, err
		}
		if identity.StoreID, err = primitives.ParseStoreID(identity.StoreID.String()); err != nil {
			return StoreIdentity{}, err
		}
		if identity.GitObjectFormat == "" {
			return StoreIdentity{}, fmt.Errorf("store identity invariant failed: git_object_format is required")
		}
		return identity, nil
	}
	if !os.IsNotExist(err) {
		return StoreIdentity{}, fmt.Errorf("read store identity: %w", err)
	}

	repoID, err := primitives.NewRepoID()
	if err != nil {
		return StoreIdentity{}, err
	}
	storeID, err := primitives.NewStoreID()
	if err != nil {
		return StoreIdentity{}, err
	}
	objectFormat, err := hiddenGitObjectFormat(repo)
	if err != nil {
		return StoreIdentity{}, err
	}
	identity := StoreIdentity{
		Version:         identityVersion,
		RepoID:          repoID,
		StoreID:         storeID,
		GitObjectFormat: objectFormat,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(path, identity, 0o600); err != nil {
		return StoreIdentity{}, err
	}
	return identity, nil
}

func hiddenGitObjectFormat(repo *Repo) (string, error) {
	output, err := runHiddenGit(repo, "", "rev-parse", "--show-object-format")
	if err != nil {
		return "", fmt.Errorf("inspect hidden git object format: %w", err)
	}
	format := strings.TrimSpace(output)
	if format == "" {
		return "", fmt.Errorf("hidden git object format invariant failed: empty format")
	}
	return format, nil
}

func readOrCreateWorktreeIdentity(repo *Repo, gitIdentity *UserGitIdentity) (WorktreeIdentity, error) {
	if err := os.MkdirAll(filepath.Join(repo.MetadataDir, worktreesDirName), 0o755); err != nil {
		return WorktreeIdentity{}, fmt.Errorf("create worktree identity dir: %w", err)
	}
	bindings, err := listWorktreeIdentities(repo.MetadataDir)
	if err != nil {
		return WorktreeIdentity{}, err
	}
	root := cleanIdentityPath(repo.WorkspaceRoot.String())
	gitDir := ""
	gitTop := ""
	gitCommon := ""
	primary := true
	if gitIdentity != nil {
		gitDir = cleanIdentityPath(gitIdentity.GitDir)
		gitTop = cleanIdentityPath(gitIdentity.TopLevel)
		gitCommon = cleanIdentityPath(gitIdentity.GitCommonDir)
		primary = sameIdentityPath(root, gitIdentity.PrimaryRoot)
		for _, existing := range bindings {
			if existing.GitCommonDir != "" && !sameIdentityPath(existing.GitCommonDir, gitCommon) {
				return WorktreeIdentity{}, fmt.Errorf("worktree attach invariant failed: store %s belongs to Git common dir %s, current checkout uses %s", repo.MetadataDir, existing.GitCommonDir, gitCommon)
			}
		}
	}

	for _, binding := range bindings {
		if gitDir != "" && binding.GitDir != "" && sameIdentityPath(binding.GitDir, gitDir) {
			return refreshWorktreeIdentity(repo.MetadataDir, binding, root, gitTop, gitCommon, gitDir, primary)
		}
		if sameIdentityPath(binding.Root, root) {
			return refreshWorktreeIdentity(repo.MetadataDir, binding, root, gitTop, gitCommon, gitDir, primary)
		}
	}

	worktreeID, err := primitives.NewWorktreeID()
	if err != nil {
		return WorktreeIdentity{}, err
	}
	producerID, err := primitives.NewEventProducerID()
	if err != nil {
		return WorktreeIdentity{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	binding := WorktreeIdentity{
		Version:      1,
		WorktreeID:   worktreeID,
		ProducerID:   producerID,
		Root:         root,
		GitTopLevel:  gitTop,
		GitCommonDir: gitCommon,
		GitDir:       gitDir,
		Primary:      primary,
		AttachedAt:   now,
		LastSeenAt:   now,
	}
	if err := writeWorktreeIdentity(repo.MetadataDir, binding); err != nil {
		return WorktreeIdentity{}, err
	}
	return binding, nil
}

func refreshWorktreeIdentity(metadataDir string, binding WorktreeIdentity, root, gitTop, gitCommon, gitDir string, primary bool) (WorktreeIdentity, error) {
	binding.Root = root
	binding.GitTopLevel = gitTop
	binding.GitCommonDir = gitCommon
	binding.GitDir = gitDir
	binding.Primary = primary
	binding.LastSeenAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeWorktreeIdentity(metadataDir, binding); err != nil {
		return WorktreeIdentity{}, err
	}
	return binding, nil
}

func listWorktreeIdentities(metadataDir string) ([]WorktreeIdentity, error) {
	dir := filepath.Join(metadataDir, worktreesDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktree identity dir: %w", err)
	}
	var bindings []WorktreeIdentity
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read worktree identity %s: %w", path, err)
		}
		var binding WorktreeIdentity
		if err := json.Unmarshal(data, &binding); err != nil {
			return nil, fmt.Errorf("worktree identity invariant failed at %s: %w", path, err)
		}
		if binding.Version != 1 {
			return nil, fmt.Errorf("worktree identity invariant failed at %s: unsupported version %d", path, binding.Version)
		}
		if binding.WorktreeID, err = primitives.ParseWorktreeID(binding.WorktreeID.String()); err != nil {
			return nil, err
		}
		if binding.ProducerID, err = primitives.ParseEventProducerID(binding.ProducerID.String()); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].WorktreeID.String() < bindings[j].WorktreeID.String() })
	return bindings, nil
}

func writeWorktreeIdentity(metadataDir string, binding WorktreeIdentity) error {
	path := filepath.Join(metadataDir, worktreesDirName, binding.WorktreeID.String()+".json")
	return writeJSONAtomic(path, binding, 0o600)
}

func discoverUserGit(root string) (UserGitIdentity, error) {
	top, err := runGitNoRepo(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return UserGitIdentity{}, err
	}
	gitDir, err := runGitNoRepo(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return UserGitIdentity{}, err
	}
	common, err := runGitNoRepo(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return UserGitIdentity{}, err
	}
	primary, err := primaryWorktreeRoot(root)
	if err != nil {
		return UserGitIdentity{}, err
	}
	return UserGitIdentity{
		TopLevel:     cleanIdentityPath(strings.TrimSpace(top)),
		GitDir:       cleanIdentityPath(strings.TrimSpace(gitDir)),
		GitCommonDir: cleanIdentityPath(strings.TrimSpace(common)),
		PrimaryRoot:  cleanIdentityPath(primary),
	}, nil
}

func primaryWorktreeRoot(root string) (string, error) {
	output, err := runGitNoRepo(root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("list git worktrees: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "worktree "); ok {
			return cleanIdentityPath(value), nil
		}
	}
	return "", fmt.Errorf("git worktree discovery invariant failed: no primary worktree")
}

func registryPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(stateDirEnv)); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("%s must be an absolute path", stateDirEnv)
		}
		return filepath.Join(filepath.Clean(override), registryFileName), nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(filepath.Clean(base), "turnal", registryFileName), nil
}

func registerRepo(repo *Repo, binding WorktreeIdentity) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	return withRegistryLock(path, func() error {
		registryValue, err := readRegistry(path)
		if err != nil {
			return err
		}
		storePath := cleanIdentityPath(repo.MetadataDir)
		common := cleanIdentityPath(binding.GitCommonDir)
		index := -1
		for i := range registryValue.Stores {
			candidate := registryValue.Stores[i]
			if candidate.StoreID == repo.StoreID || sameIdentityPath(candidate.StorePath, storePath) {
				index = i
				break
			}
		}
		if index < 0 {
			registryValue.Stores = append(registryValue.Stores, registryStore{
				GitCommonDir: common,
				RepoID:       repo.RepoID,
				StoreID:      repo.StoreID,
				StorePath:    storePath,
				Worktrees:    map[string]registryWorktree{},
			})
			index = len(registryValue.Stores) - 1
		}
		entry := &registryValue.Stores[index]
		entry.GitCommonDir = common
		entry.RepoID = repo.RepoID
		entry.StoreID = repo.StoreID
		entry.StorePath = storePath
		if entry.Worktrees == nil {
			entry.Worktrees = map[string]registryWorktree{}
		}
		entry.Worktrees[binding.WorktreeID.String()] = registryWorktree{
			Root:     cleanIdentityPath(binding.Root),
			GitDir:   cleanIdentityPath(binding.GitDir),
			LastSeen: binding.LastSeenAt,
		}
		sort.Slice(registryValue.Stores, func(i, j int) bool {
			if registryValue.Stores[i].GitCommonDir != registryValue.Stores[j].GitCommonDir {
				return registryValue.Stores[i].GitCommonDir < registryValue.Stores[j].GitCommonDir
			}
			return registryValue.Stores[i].StorePath < registryValue.Stores[j].StorePath
		})
		return writeJSONAtomic(path, registryValue, 0o600)
	})
}

func registryCandidates(gitCommonDir string) ([]registryStore, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	registryValue, err := readRegistry(path)
	if err != nil {
		return nil, err
	}
	common := cleanIdentityPath(gitCommonDir)
	if common == "" {
		// Non-Git workspaces are registered too, but they share no Git common
		// dir, so two of them must never resolve to each other's store.
		return nil, nil
	}
	var candidates []registryStore
	for _, store := range registryValue.Stores {
		if store.GitCommonDir != "" && sameIdentityPath(store.GitCommonDir, common) {
			if info, err := os.Stat(filepath.Join(store.StorePath, gitDirName)); err == nil && info.IsDir() {
				candidates = append(candidates, store)
			}
		}
	}
	return candidates, nil
}

func readRegistry(path string) (registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return registry{Version: registryVersion}, nil
		}
		return registry{}, fmt.Errorf("read turnal registry: %w", err)
	}
	var value registry
	if err := json.Unmarshal(data, &value); err != nil {
		return registry{}, fmt.Errorf("turnal registry invariant failed at %s: %w", path, err)
	}
	if value.Version != registryVersion {
		return registry{}, fmt.Errorf("turnal registry invariant failed: unsupported version %d", value.Version)
	}
	return value, nil
}

func withRegistryLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}
	lock := path + ".lock"
	for attempt := 0; attempt < registryLockAttempts; attempt++ {
		if err := os.Mkdir(lock, 0o700); err == nil {
			defer func() { _ = os.Remove(lock) }()
			return fn()
		} else if !os.IsExist(err) {
			return fmt.Errorf("create registry lock: %w", err)
		}
		time.Sleep(registryLockWait)
	}
	return fmt.Errorf("turnal registry lock busy: %s", lock)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".turnal-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temporary file for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit %s: %w", path, err)
	}
	cleanup = false
	return nil
}

func cleanIdentityPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	abs, err := filepath.Abs(value)
	if err == nil {
		value = abs
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return filepath.Clean(value)
}

func sameIdentityPath(left, right string) bool {
	return fsidentity.Same(left, right)
}

func validateStoreCandidate(store registryStore, identity UserGitIdentity) error {
	if !sameIdentityPath(store.GitCommonDir, identity.GitCommonDir) {
		return fmt.Errorf("registered store git common dir mismatch: store=%s checkout=%s", store.GitCommonDir, identity.GitCommonDir)
	}
	data, err := os.ReadFile(filepath.Join(store.StorePath, identityFileName))
	if err != nil {
		return fmt.Errorf("read registered store identity: %w", err)
	}
	var parsed StoreIdentity
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parse registered store identity: %w", err)
	}
	if parsed.StoreID != store.StoreID || parsed.RepoID != store.RepoID {
		return fmt.Errorf("registered store identity mismatch at %s", store.StorePath)
	}
	return nil
}

func selfHealCandidates(identity UserGitIdentity) ([]registryStore, error) {
	output, err := runGitNoRepo(identity.TopLevel, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var candidates []registryStore
	for _, line := range strings.Split(output, "\n") {
		root, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		metadataDir := filepath.Join(cleanIdentityPath(root), metadataDirName)
		data, err := os.ReadFile(filepath.Join(metadataDir, identityFileName))
		if err != nil {
			continue
		}
		var storeIdentity StoreIdentity
		if json.Unmarshal(data, &storeIdentity) != nil {
			continue
		}
		if _, ok := seen[storeIdentity.StoreID.String()]; ok {
			continue
		}
		if info, err := os.Stat(filepath.Join(metadataDir, gitDirName)); err != nil || !info.IsDir() {
			continue
		}
		candidate := registryStore{
			GitCommonDir: identity.GitCommonDir,
			RepoID:       storeIdentity.RepoID,
			StoreID:      storeIdentity.StoreID,
			StorePath:    metadataDir,
		}
		candidates = append(candidates, candidate)
		seen[storeIdentity.StoreID.String()] = struct{}{}
	}
	return candidates, nil
}

func resolveRegisteredStore(identity UserGitIdentity) (registryStore, bool, error) {
	candidates, err := registryCandidates(identity.GitCommonDir)
	if err != nil {
		return registryStore{}, false, err
	}
	if len(candidates) == 0 {
		candidates, err = selfHealCandidates(identity)
		if err != nil {
			return registryStore{}, false, err
		}
	}
	valid := candidates[:0]
	for _, candidate := range candidates {
		if err := validateStoreCandidate(candidate, identity); err == nil {
			valid = append(valid, candidate)
		}
	}
	if len(valid) == 0 {
		return registryStore{}, false, nil
	}
	if len(valid) > 1 {
		paths := make([]string, 0, len(valid))
		for _, candidate := range valid {
			paths = append(paths, candidate.StorePath)
		}
		sort.Strings(paths)
		return registryStore{}, false, fmt.Errorf("multiple Turnal stores match Git common dir %s: %s; run turnal worktree attach --store <path>", identity.GitCommonDir, strings.Join(paths, ", "))
	}
	return valid[0], true, nil
}

func isNoGitRepository(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "not a git repository") || errors.Is(err, os.ErrNotExist))
}
