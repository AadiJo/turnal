package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/snapshotpolicy"
)

const (
	metadataDirName = ".turnal"
	gitDirName      = "git"
	tmpDirName      = "tmp"
	logDirName      = "log"
	indexDirName    = "index"
	versionFileName = "VERSION"
	configFileName  = "config.toml"
)

const workspaceConfigTemplate = `# turnal workspace configuration
version = 1

# Workspace-specific overrides go here. Global defaults live in:
#   ~/.config/turnal/config.toml
#
# [init]
# agent = "auto"
# install_hooks = true
#
# [run]
# install_hooks = true
# quiet = false
# bypass_hook_trust = false
#
# [hooks]
# command = "turnal"
#
# [bootstrap]
# update_gitignore = true
#
# [git_sync]
# enabled = false
#
# [rollback]
# mode = "checkpoint" # checkpoint | workspace-git
#
# [retention]
# Hidden Git objects are retained while private refs exist. Use
# turnal session drop, turnal retention prune, then explicit
# turnal maintenance gc to delete refs first and garbage-collect later.
#
# [secrets]
# turnal stores local snapshots byte-exact unless paths are denied here.
# Metadata stays local to this workspace unless you copy or sync .turnal.
# store_prompts = true
# store_tool_io = true
# snapshot_deny_globs = [".env", ".env.*", "**/.env", "**/.env.*", "**/credentials.*"]
`

type Repo struct {
	WorkspaceRoot primitives.WorkspaceRoot
	MetadataDir   string
	GitDir        string
	TmpDir        string
	LockTimeout   time.Duration

	IdentityVersion int
	RepoID          primitives.RepoID
	StoreID         primitives.StoreID
	WorktreeID      primitives.WorktreeID
	EventProducerID primitives.EventProducerID
	GitObjectFormat string
	GitTopLevel     string
	GitCommonDir    string
	UserGitDir      string
	PrimaryWorktree bool
	ScopedRefs      bool
}

type Checkpoint struct {
	ID           primitives.CheckpointID
	Ref          primitives.CheckpointRef
	CanonicalRef primitives.CheckpointRef
	Commit       primitives.CommitSHA
	WorktreeID   primitives.WorktreeID
	StreamID     primitives.EventStreamID
}

type Snapshot struct {
	Ref    string
	Commit primitives.CommitSHA
}

type SyntheticTreeEntry struct {
	Path    string
	Mode    primitives.GitFileMode
	Content []byte
}

type CheckpointRefInfo struct {
	ID           primitives.CheckpointID
	Ref          primitives.CheckpointRef
	CanonicalRef primitives.CheckpointRef
	SessionID    primitives.SessionID
	TurnID       primitives.TurnID
	Phase        primitives.CheckpointPhase
	HasPhase     bool
	WorktreeID   primitives.WorktreeID
	StreamID     primitives.EventStreamID
	Commit       primitives.CommitSHA
	Time         time.Time
}

type DiffFileStat struct {
	Path      string
	Additions int
	Deletions int
	Binary    bool
}

type DiffFileChange struct {
	Status     string
	Path       string
	OldPath    string
	Similarity int
}

type DiffSummary struct {
	Files       []DiffFileStat
	Additions   int
	Deletions   int
	BinaryFiles int
}

type TreeEntry struct {
	Path     string
	Mode     string
	ObjectID string
}

type RestoreAction string

const (
	RestoreActionAdded       RestoreAction = "added"
	RestoreActionModified    RestoreAction = "modified"
	RestoreActionDeleted     RestoreAction = "deleted"
	RestoreActionModeChanged RestoreAction = "mode-changed"
)

type RestoreChange struct {
	Path   string        `json:"path"`
	Action RestoreAction `json:"action"`
}

type RestorePlan struct {
	TargetCommit primitives.CommitSHA `json:"target_commit"`
	Changes      []RestoreChange      `json:"changes"`
}

type MaterializeOptions struct {
	PreservePaths []string
}

func Init(root primitives.WorkspaceRoot) (*Repo, error) {
	return InitAt(root, filepath.Join(root.String(), metadataDirName))
}

func InitAt(root primitives.WorkspaceRoot, metadataDir string) (*Repo, error) {
	metadataDir, err := filepath.Abs(metadataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve metadata dir: %w", err)
	}
	repo := pathsAt(root, metadataDir)

	for _, dir := range []string{repo.MetadataDir, repo.TmpDir, filepath.Join(repo.MetadataDir, logDirName), filepath.Join(repo.MetadataDir, indexDirName)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure %s: %w", dir, err)
		}
	}

	if _, err := os.Stat(repo.GitDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat hidden git repo: %w", err)
		}
		if _, err := runGitNoRepo(root.String(), "init", "--bare", repo.GitDir); err != nil {
			return nil, err
		}
	}

	if err := writeFileIfMissing(filepath.Join(repo.MetadataDir, versionFileName), []byte("1\n")); err != nil {
		return nil, err
	}
	if err := writeFileIfMissing(filepath.Join(repo.MetadataDir, configFileName), []byte(workspaceConfigTemplate)); err != nil {
		return nil, err
	}
	if err := configureHiddenGit(repo); err != nil {
		return nil, err
	}

	bare, err := repo.HiddenGitBare()
	if err != nil {
		return nil, fmt.Errorf("verify hidden git repo: %w", err)
	}
	if !bare {
		return nil, fmt.Errorf("hidden git repo is not bare: %s", repo.GitDir)
	}
	var gitIdentity *UserGitIdentity
	if discovered, discoverErr := discoverUserGit(root.String()); discoverErr == nil {
		gitIdentity = &discovered
	} else if !isNoGitRepository(discoverErr) {
		return nil, fmt.Errorf("discover workspace git identity: %w", discoverErr)
	}
	if err := repo.ensureIdentity(gitIdentity); err != nil {
		return nil, err
	}
	if err := ensureSecureMetadataPermissions(repo); err != nil {
		return nil, err
	}

	return repo, nil
}

func Open(root primitives.WorkspaceRoot) (*Repo, error) {
	local := paths(root)
	if info, err := os.Stat(local.GitDir); err == nil && info.IsDir() {
		return OpenAt(root, local.MetadataDir)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
	}

	gitIdentity, err := discoverUserGit(root.String())
	if err != nil {
		return nil, fmt.Errorf("hidden git repo not initialized at %s and attached store discovery failed: %w", local.GitDir, err)
	}
	store, ok, err := resolveRegisteredStore(gitIdentity)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("hidden git repo not initialized at %s and no attached Turnal store is registered for %s", local.GitDir, gitIdentity.GitCommonDir)
	}
	return OpenAt(root, store.StorePath)
}

// OpenReadOnly opens an existing checkpoint store without refreshing identity,
// registry, permissions, hidden Git configuration, or other durable metadata.
func OpenReadOnly(root primitives.WorkspaceRoot) (*Repo, error) {
	local := paths(root)
	if info, err := os.Stat(local.GitDir); err == nil && info.IsDir() {
		return OpenAtReadOnly(root, local.MetadataDir)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
	}

	gitIdentity, err := discoverUserGit(root.String())
	if err != nil {
		return nil, fmt.Errorf("hidden git repo not initialized at %s and attached store discovery failed: %w", local.GitDir, err)
	}
	store, ok, err := resolveRegisteredStore(gitIdentity)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("hidden git repo not initialized at %s and no attached Turnal store is registered for %s", local.GitDir, gitIdentity.GitCommonDir)
	}
	return OpenAtReadOnly(root, store.StorePath)
}

func OpenAt(root primitives.WorkspaceRoot, metadataDir string) (*Repo, error) {
	metadataDir, err := filepath.Abs(metadataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve metadata dir: %w", err)
	}
	repo := pathsAt(root, metadataDir)
	if _, err := os.Stat(repo.GitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hidden git repo not initialized at %s", repo.GitDir)
		}
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
	}
	if err := configureHiddenGit(repo); err != nil {
		return nil, err
	}
	var gitIdentity *UserGitIdentity
	if discovered, discoverErr := discoverUserGit(root.String()); discoverErr == nil {
		gitIdentity = &discovered
	} else if !isNoGitRepository(discoverErr) {
		return nil, fmt.Errorf("discover workspace git identity: %w", discoverErr)
	}
	if err := repo.ensureIdentity(gitIdentity); err != nil {
		return nil, err
	}
	if err := ensureSecureMetadataPermissions(repo); err != nil {
		return nil, err
	}
	return repo, nil
}

// OpenAtReadOnly loads an existing store and worktree binding without writing
// migrations or last-seen bookkeeping.
func OpenAtReadOnly(root primitives.WorkspaceRoot, metadataDir string) (*Repo, error) {
	metadataDir, err := filepath.Abs(metadataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve metadata dir: %w", err)
	}
	repo := pathsAt(root, metadataDir)
	if _, err := os.Stat(repo.GitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hidden git repo not initialized at %s", repo.GitDir)
		}
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
	}
	var gitIdentity *UserGitIdentity
	if discovered, discoverErr := discoverUserGit(root.String()); discoverErr == nil {
		gitIdentity = &discovered
	} else if !isNoGitRepository(discoverErr) {
		return nil, fmt.Errorf("discover workspace git identity: %w", discoverErr)
	}
	if err := repo.readIdentity(gitIdentity); err != nil {
		return nil, err
	}
	return repo, nil
}

func FindRoot(start string) (primitives.WorkspaceRoot, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve start path: %w", err)
	}

	for {
		candidate := filepath.Join(abs, metadataDirName, gitDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return primitives.ParseWorkspaceRoot(abs)
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}

	gitIdentity, err := discoverUserGit(start)
	if err == nil {
		if _, ok, resolveErr := resolveRegisteredStore(gitIdentity); resolveErr != nil {
			return "", resolveErr
		} else if ok {
			return primitives.ParseWorkspaceRoot(gitIdentity.TopLevel)
		}
	} else if !isNoGitRepository(err) {
		return "", fmt.Errorf("discover attached Turnal workspace: %w", err)
	}

	return "", fmt.Errorf("not a turnal workspace: run turnal init")
}

func (repo *Repo) CreateCheckpoint(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (Checkpoint, error) {
	var checkpoint Checkpoint
	err := repo.WithWorkspaceLock("create checkpoint", func() error {
		var err error
		checkpoint, err = repo.createCheckpoint(sessionID, turnID, phase)
		return err
	})
	return checkpoint, err
}

func (repo *Repo) createCheckpoint(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (Checkpoint, error) {
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		return Checkpoint{}, err
	}
	ref, err := repo.friendlyCheckpointRef(sessionID, turnID, phase, streamID)
	if err != nil {
		return Checkpoint{}, err
	}
	checkpointID, err := repo.pendingCheckpointID(sessionID, turnID, phase)
	if err != nil {
		return Checkpoint{}, err
	}
	canonicalRef, err := primitives.NewCheckpointIDRef(checkpointID)
	if err != nil {
		return Checkpoint{}, err
	}

	message := fmt.Sprintf("turnal checkpoint %s %s turn %s", checkpointID, sessionID, turnID)
	if phase != "" {
		message += " " + phase.String()
	}
	commit, err := repo.createSnapshotCommit(message)
	if err != nil {
		return Checkpoint{}, err
	}

	if _, err := runHiddenGit(repo, "", "update-ref", canonicalRef.String(), commit.String(), ""); err != nil {
		return Checkpoint{}, err
	}
	if _, err := runHiddenGit(repo, "", "update-ref", ref.String(), commit.String(), ""); err != nil {
		_, _ = runHiddenGit(repo, "", "update-ref", "-d", canonicalRef.String())
		return Checkpoint{}, err
	}

	return Checkpoint{
		ID:           checkpointID,
		Ref:          ref,
		CanonicalRef: canonicalRef,
		Commit:       commit,
		WorktreeID:   repo.WorktreeID,
		StreamID:     streamID,
	}, nil
}

func (repo *Repo) friendlyCheckpointRef(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, streamID primitives.EventStreamID) (primitives.CheckpointRef, error) {
	if repo.ScopedRefs {
		return primitives.NewScopedCheckpointRef(repo.WorktreeID, streamID, sessionID, turnID, phase)
	}
	return primitives.NewCheckpointRef(sessionID, turnID, phase)
}

func (repo *Repo) CheckpointRefFor(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (primitives.CheckpointRef, error) {
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		return "", err
	}
	return repo.friendlyCheckpointRef(sessionID, turnID, phase, streamID)
}

func (repo *Repo) GitSyncRefFor(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (string, error) {
	if !repo.ScopedRefs {
		ref, err := primitives.NewGitSyncRef(sessionID, turnID, phase)
		return ref.String(), err
	}
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		return "", err
	}
	if _, err := primitives.ParseCheckpointPhase(phase.String()); err != nil {
		return "", err
	}
	ref := fmt.Sprintf("refs/agent-vcs/git-sync/by-worktree/%s/%s/%s/turn/%s/%s", repo.WorktreeID, streamID, sessionID, turnID.RefSegment(), phase)
	if _, err := repo.validatePrivateRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

func (repo *Repo) EnsureCanonicalCheckpointRef(checkpointID primitives.CheckpointID, commit primitives.CommitSHA) (primitives.CheckpointRef, error) {
	ref, err := primitives.NewCheckpointIDRef(checkpointID)
	if err != nil {
		return "", err
	}
	if existing, resolveErr := repo.RefCommit(ref.String()); resolveErr == nil {
		if existing != commit {
			return "", fmt.Errorf("checkpoint identity collision: %s points to %s, want %s", ref, existing, commit)
		}
		return ref, nil
	}
	if _, err := runHiddenGit(repo, "", "update-ref", ref.String(), commit.String(), ""); err != nil {
		return "", err
	}
	return ref, nil
}

func (repo *Repo) pendingCheckpointID(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (primitives.CheckpointID, error) {
	if journal, ok, err := repo.ReadCheckpointJournal(sessionID, turnID, phase); err != nil {
		return "", err
	} else if ok && journal.CheckpointID != "" {
		return primitives.ParseCheckpointID(journal.CheckpointID.String())
	}
	return primitives.NewCheckpointID()
}

func (repo *Repo) CreateSnapshotRef(ref string, message string) (Snapshot, error) {
	var snapshot Snapshot
	err := repo.WithWorkspaceLock("create snapshot", func() error {
		var err error
		snapshot, err = repo.createSnapshotRef(ref, message)
		return err
	})
	return snapshot, err
}

func (repo *Repo) createSnapshotRef(ref string, message string) (Snapshot, error) {
	ref, err := repo.validatePrivateRef(ref)
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(message) == "" {
		message = "turnal snapshot"
	}

	commit, err := repo.createSnapshotCommit(message)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := runHiddenGit(repo, "", "update-ref", ref, commit.String()); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Ref: ref, Commit: commit}, nil
}

func (repo *Repo) CreateSyntheticSnapshotRef(ref string, message string, entries []SyntheticTreeEntry) (Snapshot, error) {
	var snapshot Snapshot
	err := repo.WithWorkspaceLock("create synthetic snapshot", func() error {
		var err error
		snapshot, err = repo.createSyntheticSnapshotRef(ref, message, entries)
		return err
	})
	return snapshot, err
}

func (repo *Repo) createSyntheticSnapshotRef(ref string, message string, entries []SyntheticTreeEntry) (Snapshot, error) {
	ref, err := repo.validatePrivateRef(ref)
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(message) == "" {
		message = "turnal snapshot"
	}
	commit, err := repo.createSyntheticCommit(message, entries)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := runHiddenGit(repo, "", "update-ref", ref, commit.String()); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Ref: ref, Commit: commit}, nil
}

func (repo *Repo) createSnapshotCommit(message string) (primitives.CommitSHA, error) {
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()

	if _, err := runHiddenGit(repo, indexPath, "read-tree", "--empty"); err != nil {
		return "", err
	}

	modes, err := repo.snapshotWorktree(indexPath)
	if err != nil {
		return "", err
	}

	tree, err := runHiddenGit(repo, indexPath, "write-tree")
	if err != nil {
		return "", err
	}

	manifestData, err := json.Marshal(modeManifest{Version: 1, Modes: fileModesToUint32(modes)})
	if err != nil {
		return "", fmt.Errorf("marshal checkpoint mode manifest: %w", err)
	}
	manifestHash := sha256.Sum256(manifestData)
	message += "\n\nturnal-mode-manifest: " + hex.EncodeToString(manifestHash[:])
	commitOutput, err := runHiddenGit(repo, indexPath, "commit-tree", strings.TrimSpace(tree), "-m", message)
	if err != nil {
		return "", err
	}

	commit, err := primitives.ParseCommitSHA(strings.TrimSpace(commitOutput))
	if err != nil {
		return "", fmt.Errorf("parse checkpoint commit: %w", err)
	}
	if err := repo.writeModeManifest(commit, manifestData); err != nil {
		return "", err
	}

	return commit, nil
}

func (repo *Repo) createSyntheticCommit(message string, entries []SyntheticTreeEntry) (primitives.CommitSHA, error) {
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()

	if _, err := runHiddenGit(repo, indexPath, "read-tree", "--empty"); err != nil {
		return "", err
	}

	for _, entry := range entries {
		repoPath, err := primitives.ParseRepoPath(entry.Path)
		if err != nil {
			return "", err
		}
		mode, err := primitives.ParseGitFileMode(entry.Mode.String())
		if err != nil {
			return "", err
		}
		output, err := runHiddenGitWithInput(repo, indexPath, bytes.NewReader(entry.Content), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		blob, err := primitives.ParseGitObjectID(strings.TrimSpace(output))
		if err != nil {
			return "", fmt.Errorf("parse synthetic blob id for %s: %w", repoPath, err)
		}
		if _, err := runHiddenGit(repo, indexPath, "update-index", "--add", "--cacheinfo", mode.String(), blob.String(), repoPath.String()); err != nil {
			return "", err
		}
	}

	tree, err := runHiddenGit(repo, indexPath, "write-tree")
	if err != nil {
		return "", err
	}
	commitOutput, err := runHiddenGit(repo, indexPath, "commit-tree", strings.TrimSpace(tree), "-m", message)
	if err != nil {
		return "", err
	}
	commit, err := primitives.ParseCommitSHA(strings.TrimSpace(commitOutput))
	if err != nil {
		return "", fmt.Errorf("parse synthetic commit: %w", err)
	}
	return commit, nil
}

func (repo *Repo) DiffRefs(preRef, postRef primitives.CheckpointRef) ([]byte, error) {
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	output, err := runHiddenGit(repo, indexPath,
		"diff",
		"--patch",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
	)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) DiffCommitToWorkspace(commit primitives.CommitSHA) ([]byte, error) {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return nil, err
	}
	currentTree, cleanup, err := repo.snapshotWorktreeTree()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	output, err := runHiddenGit(repo, "",
		"diff",
		"--patch",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		parsedCommit.String()+"^{commit}",
		currentTree,
	)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) DiffRefsPath(preRef, postRef primitives.CheckpointRef, repoPath string) ([]byte, error) {
	parsedPath, err := primitives.ParseRepoPath(repoPath)
	if err != nil {
		return nil, err
	}

	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	output, err := runHiddenGit(repo, indexPath,
		"diff",
		"--patch",
		"--unified=0",
		"--no-renames",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
		"--",
		parsedPath.String(),
	)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) DiffRefsPathWithRenames(preRef, postRef primitives.CheckpointRef, prePath, postPath string) ([]byte, error) {
	parsedPrePath, err := primitives.ParseRepoPath(prePath)
	if err != nil {
		return nil, err
	}
	parsedPostPath, err := primitives.ParseRepoPath(postPath)
	if err != nil {
		return nil, err
	}

	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	args := []string{
		"diff",
		"--patch",
		"--unified=0",
		"-M",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String() + "^{commit}",
		postRef.String() + "^{commit}",
		"--",
		parsedPrePath.String(),
	}
	if parsedPostPath != parsedPrePath {
		args = append(args, parsedPostPath.String())
	}

	output, err := runHiddenGit(repo, indexPath, args...)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) DiffTurn(sessionID primitives.SessionID, turnID primitives.TurnID) ([]byte, error) {
	preRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return nil, err
	}
	postRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return nil, err
	}
	return repo.DiffRefs(preRef, postRef)
}

func (repo *Repo) ListCheckpointRefs(sessionID primitives.SessionID) ([]primitives.CheckpointRef, error) {
	infos, err := repo.ListCheckpointRefInfos(sessionID)
	if err != nil {
		return nil, err
	}
	refs := make([]primitives.CheckpointRef, 0, len(infos))
	for _, info := range infos {
		refs = append(refs, info.Ref)
	}
	return refs, nil
}

func (repo *Repo) ListAllCheckpointRefInfos() ([]CheckpointRefInfo, error) {
	return repo.listCheckpointRefInfos(primitives.CheckpointRefsPrefix())
}

func (repo *Repo) ListCheckpointRefInfos(sessionID primitives.SessionID) ([]CheckpointRefInfo, error) {
	parsed, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return nil, err
	}
	infos, err := repo.listCheckpointRefInfos(primitives.CheckpointRefsPrefix())
	if err != nil {
		return nil, err
	}
	filtered := infos[:0]
	for _, info := range infos {
		if info.SessionID != parsed {
			continue
		}
		if repo.WorktreeID != "" && info.WorktreeID != "" && info.WorktreeID != repo.WorktreeID {
			continue
		}
		filtered = append(filtered, info)
	}
	return filtered, nil
}

func (repo *Repo) FindCheckpointIDPrefix(prefix string) ([]CheckpointRefInfo, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if !strings.HasPrefix(prefix, "chk_") {
		return nil, fmt.Errorf("checkpoint id prefix must start with chk_")
	}
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	var matches []CheckpointRefInfo
	for _, info := range infos {
		if info.ID != "" && strings.HasPrefix(info.ID.String(), prefix) {
			matches = append(matches, info)
		}
	}
	return matches, nil
}

func (repo *Repo) FindCheckpointTargets(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) ([]CheckpointRefInfo, error) {
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	var matches []CheckpointRefInfo
	for _, info := range infos {
		if info.SessionID == sessionID && info.TurnID == turnID && info.Phase == phase {
			matches = append(matches, info)
		}
	}
	return matches, nil
}

func (repo *Repo) listCheckpointRefInfos(refPrefix string) ([]CheckpointRefInfo, error) {
	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)%09%(objectname)%09%(committerdate:iso-strict)", refPrefix)
	if err != nil {
		return nil, err
	}

	var infos []CheckpointRefInfo
	canonicalByCommit := make(map[string]struct {
		id  primitives.CheckpointID
		ref primitives.CheckpointRef
	})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("checkpoint ref listing invariant failed for %q: expected ref, commit and time", line)
		}

		ref, err := primitives.ParseCheckpointRef(fields[0])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", fields[0], err)
		}
		refParts, err := ref.Parts()
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", ref, err)
		}
		commit, err := primitives.ParseCommitSHA(fields[1])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref %s commit invariant failed: %w", ref, err)
		}
		createdAt, err := time.Parse(time.RFC3339, fields[2])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref %s time invariant failed: %w", ref, err)
		}

		if refParts.Canonical {
			canonicalByCommit[commit.String()] = struct {
				id  primitives.CheckpointID
				ref primitives.CheckpointRef
			}{id: refParts.CheckpointID, ref: ref}
			continue
		}
		worktreeID := refParts.WorktreeID
		streamID := refParts.StreamID
		if worktreeID == "" {
			if primary, ok := repo.primaryWorktreeBinding(); ok {
				worktreeID = primary.WorktreeID
			} else {
				worktreeID = repo.WorktreeID
			}
		}
		if streamID == "" && repo.StoreID != "" {
			streamID, _ = primitives.DeriveLegacyEventStreamID(repo.StoreID, refParts.SessionID)
		}
		infos = append(infos, CheckpointRefInfo{
			Ref:        ref,
			SessionID:  refParts.SessionID,
			TurnID:     refParts.TurnID,
			Phase:      refParts.Phase,
			HasPhase:   refParts.HasPhase,
			WorktreeID: worktreeID,
			StreamID:   streamID,
			Commit:     commit,
			Time:       createdAt,
		})
	}
	for index := range infos {
		if canonical, ok := canonicalByCommit[infos[index].Commit.String()]; ok {
			infos[index].ID = canonical.id
			infos[index].CanonicalRef = canonical.ref
			if parts, err := infos[index].Ref.Parts(); err == nil && !parts.Scoped {
				producerID := repo.EventProducerID
				if primary, ok := repo.primaryWorktreeBinding(); ok {
					producerID = primary.ProducerID
				}
				if streamID, streamErr := primitives.DeriveEventStreamID(producerID, infos[index].SessionID); streamErr == nil {
					infos[index].StreamID = streamID
				}
			}
		}
	}

	sort.Slice(infos, func(i, j int) bool {
		left, right := infos[i], infos[j]
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		if left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		if phaseRank(left.Phase) != phaseRank(right.Phase) {
			return phaseRank(left.Phase) < phaseRank(right.Phase)
		}
		return left.Ref.String() < right.Ref.String()
	})

	return infos, nil
}

func (repo *Repo) DiffStatRefs(preRef, postRef primitives.CheckpointRef) (DiffSummary, error) {
	output, err := runHiddenGit(repo, "",
		"-c",
		"core.quotePath=false",
		"diff",
		"--numstat",
		"--no-renames",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
	)
	if err != nil {
		return DiffSummary{}, err
	}

	var summary DiffSummary
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return DiffSummary{}, fmt.Errorf("diff stat invariant failed for %q: expected additions, deletions and path", line)
		}

		stat := DiffFileStat{Path: fields[2]}
		if fields[0] == "-" || fields[1] == "-" {
			stat.Binary = true
			summary.BinaryFiles++
		} else {
			additions, err := strconv.Atoi(fields[0])
			if err != nil {
				return DiffSummary{}, fmt.Errorf("parse additions for %q: %w", line, err)
			}
			deletions, err := strconv.Atoi(fields[1])
			if err != nil {
				return DiffSummary{}, fmt.Errorf("parse deletions for %q: %w", line, err)
			}
			stat.Additions = additions
			stat.Deletions = deletions
			summary.Additions += additions
			summary.Deletions += deletions
		}
		summary.Files = append(summary.Files, stat)
	}
	return summary, nil
}

func (repo *Repo) DiffNameStatusRefs(preRef, postRef primitives.CheckpointRef) ([]DiffFileChange, error) {
	output, err := runHiddenGit(repo, "",
		"diff",
		"--name-status",
		"-z",
		"-M",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
	)
	if err != nil {
		return nil, err
	}
	return parseDiffNameStatus(output)
}

func (repo *Repo) DiffStatTurn(sessionID primitives.SessionID, turnID primitives.TurnID) (DiffSummary, error) {
	preRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return DiffSummary{}, err
	}
	postRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return DiffSummary{}, err
	}
	return repo.DiffStatRefs(preRef, postRef)
}

func parseDiffNameStatus(output string) ([]DiffFileChange, error) {
	if output == "" {
		return nil, nil
	}

	fields := strings.Split(output, "\x00")
	var changes []DiffFileChange
	for index := 0; index < len(fields); {
		statusText := fields[index]
		index++
		if statusText == "" {
			continue
		}

		status := statusText[:1]
		change := DiffFileChange{Status: status}
		if len(statusText) > 1 {
			similarity, err := strconv.Atoi(statusText[1:])
			if err != nil {
				return nil, fmt.Errorf("parse diff status similarity for %q: %w", statusText, err)
			}
			change.Similarity = similarity
		}

		switch status {
		case "R", "C":
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("diff name-status invariant failed for %q: expected old and new path", statusText)
			}
			change.OldPath = fields[index]
			change.Path = fields[index+1]
			index += 2
		default:
			if index >= len(fields) {
				return nil, fmt.Errorf("diff name-status invariant failed for %q: expected path", statusText)
			}
			change.Path = fields[index]
			index++
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func (repo *Repo) DeleteCheckpointRef(ref primitives.CheckpointRef) error {
	parsedRef, err := primitives.ParseCheckpointRef(ref.String())
	if err != nil {
		return err
	}
	return repo.DeletePrivateRef(parsedRef.String())
}

func (repo *Repo) ListPrivateRefs(prefix string) ([]string, error) {
	prefix, err := repo.validatePrivateRef(prefix)
	if err != nil {
		return nil, err
	}
	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref, err := repo.validatePrivateRef(line)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func (repo *Repo) ListAllPrivateRefs() ([]string, error) {
	return repo.ListPrivateRefs("refs/agent-vcs")
}

func (repo *Repo) DeletePrivateRef(ref string) error {
	parsedRef, err := repo.validatePrivateRef(ref)
	if err != nil {
		return err
	}
	return repo.WithWorkspaceLock("delete private ref", func() error {
		if _, err := runHiddenGit(repo, "", "update-ref", "-d", parsedRef); err != nil {
			return err
		}
		return nil
	})
}

func (repo *Repo) RunHiddenGitGC() error {
	return repo.WithWorkspaceLock("garbage collect hidden repository", func() error {
		if _, err := repo.pruneModeManifests(); err != nil {
			return err
		}
		if _, err := runHiddenGit(repo, "", "reflog", "expire", "--expire=all", "--all"); err != nil {
			return err
		}
		if _, err := runHiddenGit(repo, "", "gc", "--prune=now"); err != nil {
			return err
		}
		return nil
	})
}

// PruneModeManifests removes permission sidecars for commits no longer
// reachable from any hidden-repository ref.
func (repo *Repo) PruneModeManifests() ([]string, error) {
	var removed []string
	err := repo.WithWorkspaceLock("prune checkpoint mode manifests", func() error {
		var err error
		removed, err = repo.pruneModeManifests()
		return err
	})
	return removed, err
}

func (repo *Repo) pruneModeManifests() ([]string, error) {
	reachableOutput, err := runHiddenGit(repo, "", "rev-list", "--all")
	if err != nil {
		return nil, fmt.Errorf("list commits for mode-manifest pruning: %w", err)
	}
	reachable := make(map[string]struct{})
	for _, value := range strings.Fields(reachableOutput) {
		reachable[value] = struct{}{}
	}
	dir := filepath.Join(repo.MetadataDir, "manifests", "modes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint mode manifests: %w", err)
	}
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		commitText := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := primitives.ParseCommitSHA(commitText); err != nil {
			continue
		}
		if _, ok := reachable[commitText]; ok {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("remove unreferenced mode manifest %s: %w", path, err)
		}
		removed = append(removed, path)
	}
	sort.Strings(removed)
	return removed, nil
}

func (repo *Repo) CheckpointCommit(ref primitives.CheckpointRef) (primitives.CommitSHA, error) {
	parsedRef, err := primitives.ParseCheckpointRef(ref.String())
	if err != nil {
		return "", err
	}
	return repo.RefCommit(parsedRef.String())
}

func (repo *Repo) RefCommit(ref string) (primitives.CommitSHA, error) {
	parsedRef, err := repo.validatePrivateRef(ref)
	if err != nil {
		return "", err
	}
	output, err := runHiddenGitReadOnly(repo, "rev-parse", parsedRef+"^{commit}")
	if err != nil {
		return "", err
	}
	return primitives.ParseCommitSHA(strings.TrimSpace(output))
}

func (repo *Repo) CommitFileBytes(commit primitives.CommitSHA, repoPath string) ([]byte, error) {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return nil, err
	}
	parsedPath, err := primitives.ParseRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	output, err := runHiddenGit(repo, "", "show", parsedCommit.String()+":"+parsedPath.String())
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) CommitFileBytesIfExists(commit primitives.CommitSHA, repoPath string) ([]byte, bool, error) {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return nil, false, err
	}
	parsedPath, err := primitives.ParseRepoPath(repoPath)
	if err != nil {
		return nil, false, err
	}

	spec := parsedCommit.String() + ":" + parsedPath.String()
	objectType, err := runHiddenGit(repo, "", "cat-file", "-t", spec)
	if err != nil {
		if _, commitErr := runHiddenGit(repo, "", "rev-parse", parsedCommit.String()+"^{commit}"); commitErr != nil {
			return nil, false, commitErr
		}
		return nil, false, nil
	}
	objectType = strings.TrimSpace(objectType)
	if objectType != "blob" {
		return nil, false, fmt.Errorf("%s at %s is a %s, not a file", parsedPath, parsedCommit, objectType)
	}

	output, err := runHiddenGit(repo, "", "cat-file", "blob", spec)
	if err != nil {
		return nil, false, err
	}
	return []byte(output), true, nil
}

func (repo *Repo) RestoreCheckpoint(ref primitives.CheckpointRef) error {
	parsedRef, err := primitives.ParseCheckpointRef(ref.String())
	if err != nil {
		return err
	}
	commit, err := repo.CheckpointCommit(parsedRef)
	if err != nil {
		return err
	}
	return repo.RestoreCommit(commit)
}

func (repo *Repo) PlanRestoreCommit(commit primitives.CommitSHA) (RestorePlan, error) {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return RestorePlan{}, err
	}

	currentTree, cleanup, err := repo.snapshotWorktreeTree()
	if err != nil {
		return RestorePlan{}, err
	}
	defer cleanup()

	output, err := runHiddenGit(repo, "",
		"diff",
		"--raw",
		"-z",
		"--no-renames",
		"--no-abbrev",
		currentTree,
		parsedCommit.String(),
	)
	if err != nil {
		return RestorePlan{}, err
	}

	changes, err := parseRestoreChanges(output)
	if err != nil {
		return RestorePlan{}, err
	}
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return RestorePlan{}, err
	}
	changes = filterSecretDeniedChanges(changes, denyGlobs)
	return RestorePlan{TargetCommit: parsedCommit, Changes: changes}, nil
}

func (repo *Repo) RestoreCommit(commit primitives.CommitSHA) error {
	return repo.WithWorkspaceLock("restore checkpoint", func() error {
		return repo.restoreCommit(commit)
	})
}

// PreflightRestoreCommit verifies every target object and the optional mode
// manifest before a caller records a destructive restore transition.
func (repo *Repo) PreflightRestoreCommit(commit primitives.CommitSHA) error {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return err
	}
	if _, err := runHiddenGit(repo, "", "rev-parse", parsedCommit.String()+"^{commit}"); err != nil {
		return fmt.Errorf("preflight restore commit: %w", err)
	}
	if _, err := repo.readModeManifest(parsedCommit); err != nil {
		return fmt.Errorf("preflight restore permissions: %w", err)
	}
	entries, err := repo.ListCommitTree(parsedCommit)
	if err != nil {
		return fmt.Errorf("preflight restore tree: %w", err)
	}
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return fmt.Errorf("preflight restore policy: %w", err)
	}
	for _, entry := range entries {
		if secretDeniedPath(entry.Path, denyGlobs) {
			continue
		}
		if _, err := runHiddenGit(repo, "", "cat-file", "-e", entry.ObjectID+"^{blob}"); err != nil {
			return fmt.Errorf("preflight restore blob %s: %w", entry.Path, err)
		}
		if entry.Mode == "120000" {
			if _, err := repo.blobBytesLimited(entry.ObjectID, 1<<20); err != nil {
				return fmt.Errorf("preflight restore symlink %s: %w", entry.Path, err)
			}
		}
	}
	return nil
}

func (repo *Repo) MaterializeCommit(commit primitives.CommitSHA, root string, options MaterializeOptions) error {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("materialize root is required")
	}
	materializeRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve materialize root: %w", err)
	}
	if _, err := runHiddenGit(repo, "", "rev-parse", parsedCommit.String()+"^{commit}"); err != nil {
		return err
	}
	if err := os.MkdirAll(materializeRoot, 0o755); err != nil {
		return fmt.Errorf("create materialize root: %w", err)
	}

	entries, err := repo.ListCommitTree(parsedCommit)
	if err != nil {
		return err
	}
	modes, err := repo.readModeManifest(parsedCommit)
	if err != nil {
		return err
	}
	preservePaths, err := materializePreserveSet(options.PreservePaths)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		mode, hasMode := modes[entry.Path]
		var err error
		if hasMode {
			err = repo.restoreTreeEntryAtRoot(materializeRoot, entry, mode)
		} else {
			err = repo.restoreTreeEntryAtRoot(materializeRoot, entry)
		}
		if err != nil {
			return err
		}
	}
	if err := materializeDeleteFilesAbsentFrom(materializeRoot, entries, preservePaths); err != nil {
		return err
	}
	return materializeRemoveEmptyDirs(materializeRoot)
}

func (repo *Repo) restoreCommit(commit primitives.CommitSHA) error {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return err
	}
	if _, err := runHiddenGit(repo, "", "rev-parse", parsedCommit.String()+"^{commit}"); err != nil {
		return err
	}
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return err
	}
	defer cleanup()

	entries, err := repo.ListCommitTree(parsedCommit)
	if err != nil {
		return err
	}
	modes, err := repo.readModeManifest(parsedCommit)
	if err != nil {
		return err
	}
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if secretDeniedPath(entry.Path, denyGlobs) {
			continue
		}
		mode, hasMode := modes[entry.Path]
		var err error
		if hasMode {
			err = repo.restoreTreeEntryWithMode(entry, mode)
		} else {
			err = repo.restoreTreeEntry(entry)
		}
		if err != nil {
			return err
		}
	}
	if err := repo.deleteFilesAbsentFrom(entries, indexPath, denyGlobs); err != nil {
		return err
	}
	return repo.removeEmptyDirs(indexPath)
}

func (repo *Repo) ListCommitTree(commit primitives.CommitSHA) ([]TreeEntry, error) {
	parsedCommit, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return nil, err
	}
	output, err := runHiddenGitReadOnly(repo, "ls-tree", "-r", "-z", "--full-tree", parsedCommit.String())
	if err != nil {
		return nil, err
	}

	var entries []TreeEntry
	for _, record := range strings.Split(output, "\x00") {
		if record == "" {
			continue
		}
		header, repoPath, ok := strings.Cut(record, "\t")
		if !ok {
			return nil, fmt.Errorf("tree entry invariant failed for %q: missing path separator", record)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return nil, fmt.Errorf("tree entry invariant failed for %q: expected mode, type and object id", record)
		}
		if fields[1] != "blob" {
			return nil, fmt.Errorf("tree entry %s has unsupported object type %s", repoPath, fields[1])
		}
		if _, err := primitives.ParseRepoPath(repoPath); err != nil {
			return nil, fmt.Errorf("tree entry path invariant failed for %q: %w", repoPath, err)
		}
		entries = append(entries, TreeEntry{
			Path:     repoPath,
			Mode:     fields[0],
			ObjectID: fields[2],
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

func (repo *Repo) snapshotWorktreeTree() (string, func(), error) {
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return "", nil, err
	}
	if _, err := runHiddenGit(repo, indexPath, "read-tree", "--empty"); err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err := repo.snapshotWorktree(indexPath); err != nil {
		cleanup()
		return "", nil, err
	}
	tree, err := runHiddenGit(repo, indexPath, "write-tree")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return strings.TrimSpace(tree), cleanup, nil
}

func parseRestoreChanges(raw string) ([]RestoreChange, error) {
	if raw == "" {
		return nil, nil
	}

	tokens := strings.Split(raw, "\x00")
	changes := make([]RestoreChange, 0, len(tokens)/2)
	for i := 0; i < len(tokens); {
		header := tokens[i]
		i++
		if header == "" {
			continue
		}
		if i >= len(tokens) {
			return nil, fmt.Errorf("raw diff invariant failed for %q: missing path", header)
		}
		repoPath := tokens[i]
		i++
		if _, err := primitives.ParseRepoPath(repoPath); err != nil {
			return nil, fmt.Errorf("raw diff path invariant failed for %q: %w", repoPath, err)
		}

		fields := strings.Fields(header)
		if len(fields) != 5 {
			return nil, fmt.Errorf("raw diff invariant failed for %q: expected modes, object ids and status", header)
		}
		status := fields[4]
		if status == "" {
			return nil, fmt.Errorf("raw diff invariant failed for %q: empty status", header)
		}

		changes = append(changes, RestoreChange{
			Path:   repoPath,
			Action: restoreAction(fields[0], fields[1], fields[2], fields[3], status),
		})
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func restoreAction(oldMode, newMode, oldObject, newObject, status string) RestoreAction {
	switch status[0] {
	case 'A':
		return RestoreActionAdded
	case 'D':
		return RestoreActionDeleted
	case 'T':
		return RestoreActionModeChanged
	case 'M':
		if oldMode != newMode && oldObject == newObject {
			return RestoreActionModeChanged
		}
		return RestoreActionModified
	default:
		return RestoreActionModified
	}
}

func filterSecretDeniedChanges(changes []RestoreChange, denyGlobs []string) []RestoreChange {
	if len(changes) == 0 || len(denyGlobs) == 0 {
		return changes
	}
	filtered := changes[:0]
	for _, change := range changes {
		if secretDeniedPath(change.Path, denyGlobs) {
			continue
		}
		filtered = append(filtered, change)
	}
	return filtered
}

func (repo *Repo) deleteFilesAbsentFrom(entries []TreeEntry, indexPath string, denyGlobs []string) error {
	targetPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		targetPaths[entry.Path] = struct{}{}
	}

	root := repo.WorkspaceRoot.String()
	return filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}

		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if secretDeniedPath(repoPath, denyGlobs) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		ignored, err := repo.gitignoredPath(indexPath, repoPath)
		if err != nil {
			return err
		}
		if ignored {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := targetPaths[repoPath]; ok {
			return nil
		}
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove worktree file %s: %w", repoPath, err)
		}
		return nil
	})
}

func (repo *Repo) restoreTreeEntry(entry TreeEntry) error {
	return repo.restoreTreeEntryAtRoot(repo.WorkspaceRoot.String(), entry)
}

func (repo *Repo) restoreTreeEntryWithMode(entry TreeEntry, mode fs.FileMode) error {
	return repo.restoreTreeEntryAtRoot(repo.WorkspaceRoot.String(), entry, mode)
}

func (repo *Repo) restoreTreeEntryAtRoot(root string, entry TreeEntry, storedMode ...fs.FileMode) error {
	repoPath, err := primitives.ParseRepoPath(entry.Path)
	if err != nil {
		return err
	}
	if err := ensureParentDirsAtRoot(root, repoPath); err != nil {
		return err
	}

	absPath := filepath.Join(root, repoPath.OSPath())
	if err := repo.removePathForRestore(absPath, entry.Path); err != nil {
		return err
	}

	switch entry.Mode {
	case "100644", "100755":
		return repo.restoreRegularFile(absPath, entry, storedMode...)
	case "120000":
		return repo.restoreSymlink(absPath, entry)
	default:
		return fmt.Errorf("tree entry %s has unsupported mode %s", entry.Path, entry.Mode)
	}
}

func (repo *Repo) ensureParentDirs(repoPath primitives.RepoPath) error {
	return ensureParentDirsAtRoot(repo.WorkspaceRoot.String(), repoPath)
}

func ensureParentDirsAtRoot(root string, repoPath primitives.RepoPath) error {
	segments := strings.Split(repoPath.String(), "/")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err == nil {
			if info.IsDir() {
				continue
			}
			if err := os.Remove(current); err != nil {
				relPath, _ := filepath.Rel(root, current)
				return fmt.Errorf("replace parent path %s: %w", filepath.ToSlash(relPath), err)
			}
		} else if !os.IsNotExist(err) {
			relPath, _ := filepath.Rel(root, current)
			return fmt.Errorf("stat parent path %s: %w", filepath.ToSlash(relPath), err)
		}
		if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
			relPath, _ := filepath.Rel(root, current)
			return fmt.Errorf("create parent path %s: %w", filepath.ToSlash(relPath), err)
		}
	}
	return nil
}

func materializePreserveSet(paths []string) (map[string]struct{}, error) {
	preserved := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		repoPath, err := primitives.ParseRepoPath(filepath.ToSlash(value))
		if err != nil {
			return nil, fmt.Errorf("preserve path %q: %w", value, err)
		}
		preserved[repoPath.String()] = struct{}{}
	}
	return preserved, nil
}

func materializeDeleteFilesAbsentFrom(root string, entries []TreeEntry, preservePaths map[string]struct{}) error {
	targetPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		targetPaths[entry.Path] = struct{}{}
	}

	return filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}

		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if _, ok := preservePaths[repoPath]; ok {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := targetPaths[repoPath]; ok {
			return nil
		}
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove materialized file %s: %w", repoPath, err)
		}
		return nil
	})
}

func materializeRemoveEmptyDirs(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}
		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, absPath)
		}
		return nil
	}); err != nil {
		return err
	}

	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !isDirectoryNotEmpty(err) {
			relPath, _ := filepath.Rel(root, dir)
			return fmt.Errorf("remove empty materialized dir %s: %w", filepath.ToSlash(relPath), err)
		}
	}
	return nil
}

func (repo *Repo) removePathForRestore(absPath string, repoPath string) error {
	info, err := os.Lstat(absPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat worktree path %s: %w", repoPath, err)
	}
	if info.IsDir() {
		if err := ensureNoExcludedDescendants(absPath, repoPath); err != nil {
			return err
		}
		if err := os.RemoveAll(absPath); err != nil {
			return fmt.Errorf("replace directory %s: %w", repoPath, err)
		}
		return nil
	}
	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("replace worktree path %s: %w", repoPath, err)
	}
	return nil
}

func ensureNoExcludedDescendants(absPath string, repoPath string) error {
	return filepath.WalkDir(absPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absPath {
			return nil
		}
		relPath, err := filepath.Rel(absPath, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		nestedRepoPath := filepath.ToSlash(filepath.Join(repoPath, relPath))
		if excludedPath(nestedRepoPath) {
			return fmt.Errorf("cannot replace directory %s because it contains excluded metadata path %s", repoPath, nestedRepoPath)
		}
		return nil
	})
}

func (repo *Repo) restoreRegularFile(absPath string, entry TreeEntry, storedMode ...fs.FileMode) error {
	dir := filepath.Dir(absPath)
	tmpFile, err := os.CreateTemp(dir, ".turnal-restore-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", entry.Path, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := repo.writeBlobTo(entry.ObjectID, tmpFile); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("stream blob for %s: %w", entry.Path, err)
	}
	mode := fs.FileMode(0o644)
	if entry.Mode == "100755" {
		mode = 0o755
	}
	if len(storedMode) > 0 && storedMode[0].IsRegular() {
		mode = storedMode[0].Perm()
	}
	if err := tmpFile.Chmod(mode); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp file for %s: %w", entry.Path, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", entry.Path, err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return fmt.Errorf("restore file %s: %w", entry.Path, err)
	}
	cleanup = false
	return nil
}

func (repo *Repo) restoreSymlink(absPath string, entry TreeEntry) error {
	target, err := repo.blobBytesLimited(entry.ObjectID, 1<<20)
	if err != nil {
		return fmt.Errorf("read symlink blob for %s: %w", entry.Path, err)
	}
	if strings.ContainsRune(string(target), 0) {
		return fmt.Errorf("restore symlink %s: target contains NUL", entry.Path)
	}
	if err := os.Symlink(string(target), absPath); err != nil {
		return fmt.Errorf("restore symlink %s: %w", entry.Path, err)
	}
	return nil
}

func (repo *Repo) blobBytes(objectID string) ([]byte, error) {
	output, err := runHiddenGit(repo, "", "cat-file", "blob", objectID)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) blobBytesLimited(objectID string, limit int64) ([]byte, error) {
	sizeText, err := runHiddenGit(repo, "", "cat-file", "-s", objectID)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse blob size: %w", err)
	}
	if size > limit {
		return nil, fmt.Errorf("blob is %d bytes; limit is %d", size, limit)
	}
	return repo.blobBytes(objectID)
}

func (repo *Repo) writeBlobTo(objectID string, destination io.Writer) error {
	cmd := exec.Command("git", "cat-file", "blob", objectID)
	cmd.Dir = repo.WorkspaceRoot.String()
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.WorkspaceRoot.String(),
	)
	cmd.Stdout = destination
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git cat-file blob %s: %w: %s", objectID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (repo *Repo) removeEmptyDirs(indexPath string) error {
	root := repo.WorkspaceRoot.String()
	var dirs []string
	if err := filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}
		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		ignored, err := repo.gitignoredPath(indexPath, repoPath)
		if err != nil {
			return err
		}
		if ignored {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, absPath)
		}
		return nil
	}); err != nil {
		return err
	}

	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !isDirectoryNotEmpty(err) {
			relPath, _ := filepath.Rel(root, dir)
			return fmt.Errorf("remove empty worktree dir %s: %w", filepath.ToSlash(relPath), err)
		}
	}
	return nil
}

func (repo *Repo) validatePrivateRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("private ref is required")
	}
	if ref != "refs/agent-vcs" && !strings.HasPrefix(ref, "refs/agent-vcs/") {
		return "", fmt.Errorf("private ref %q must be under refs/agent-vcs", ref)
	}
	if strings.ContainsRune(ref, 0) {
		return "", fmt.Errorf("private ref %q must not contain NUL", ref)
	}
	if _, err := runHiddenGitReadOnly(repo, "check-ref-format", ref); err != nil {
		return "", err
	}
	return ref, nil
}

func (repo *Repo) snapshotWorktree(indexPath string) (map[string]fs.FileMode, error) {
	root := repo.WorkspaceRoot.String()
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return nil, err
	}
	modes := make(map[string]fs.FileMode)
	err = filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}

		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if secretDeniedPath(repoPath, denyGlobs) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		ignored, err := repo.gitignoredPath(indexPath, repoPath)
		if err != nil {
			return err
		}
		if ignored {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", repoPath, err)
		}

		var mode string
		var blob string
		switch {
		case info.Mode().IsRegular():
			modes[repoPath] = info.Mode()
			mode = gitRegularFileMode(info.Mode())
			output, err := runHiddenGit(repo, indexPath, "hash-object", "-w", "--no-filters", "--", repoPath)
			if err != nil {
				return err
			}
			blob = strings.TrimSpace(output)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(absPath)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", repoPath, err)
			}
			mode = "120000"
			output, err := runHiddenGitWithInput(repo, indexPath, strings.NewReader(target), "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			blob = strings.TrimSpace(output)
		default:
			return nil
		}

		if _, err := runHiddenGit(repo, indexPath, "update-index", "--add", "--cacheinfo", mode, blob, repoPath); err != nil {
			return err
		}
		return nil
	})
	return modes, err
}

type modeManifest struct {
	Version int               `json:"version"`
	Modes   map[string]uint32 `json:"modes"`
}

func fileModesToUint32(modes map[string]fs.FileMode) map[string]uint32 {
	encoded := make(map[string]uint32, len(modes))
	for repoPath, mode := range modes {
		encoded[repoPath] = uint32(mode.Perm())
	}
	return encoded
}

func (repo *Repo) modeManifestPath(commit primitives.CommitSHA) string {
	return filepath.Join(repo.MetadataDir, "manifests", "modes", commit.String()+".json")
}

func (repo *Repo) writeModeManifest(commit primitives.CommitSHA, data []byte) error {
	dir := filepath.Dir(repo.modeManifestPath(commit))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create checkpoint mode manifest dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure checkpoint mode manifest dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mode-manifest-*")
	if err != nil {
		return fmt.Errorf("create checkpoint mode manifest: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure checkpoint mode manifest: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write checkpoint mode manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync checkpoint mode manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close checkpoint mode manifest: %w", err)
	}
	if err := os.Rename(tmpPath, repo.modeManifestPath(commit)); err != nil {
		return fmt.Errorf("install checkpoint mode manifest: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync checkpoint mode manifest dir: %w", err)
	}
	return nil
}

func (repo *Repo) readModeManifest(commit primitives.CommitSHA) (map[string]fs.FileMode, error) {
	data, err := os.ReadFile(repo.modeManifestPath(commit))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint mode manifest: %w", err)
	}
	manifestHash := sha256.Sum256(data)
	wantHash := hex.EncodeToString(manifestHash[:])
	message, err := runHiddenGit(repo, "", "show", "-s", "--format=%B", commit.String())
	if err != nil {
		return nil, fmt.Errorf("read checkpoint mode manifest trailer: %w", err)
	}
	const trailerPrefix = "turnal-mode-manifest:"
	foundHash := ""
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), trailerPrefix) {
			foundHash = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), trailerPrefix))
		}
	}
	if foundHash == "" || foundHash != wantHash {
		return nil, fmt.Errorf("checkpoint mode manifest hash does not match commit trailer")
	}
	var manifest modeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse checkpoint mode manifest: %w", err)
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("checkpoint mode manifest has unsupported version %d", manifest.Version)
	}
	modes := make(map[string]fs.FileMode, len(manifest.Modes))
	for value, rawMode := range manifest.Modes {
		repoPath, err := primitives.ParseRepoPath(value)
		if err != nil {
			return nil, fmt.Errorf("checkpoint mode manifest path %q: %w", value, err)
		}
		if rawMode > 0o777 {
			return nil, fmt.Errorf("checkpoint mode manifest path %s has invalid mode %o", repoPath, rawMode)
		}
		modes[repoPath.String()] = fs.FileMode(rawMode)
	}
	return modes, nil
}

func (repo *Repo) secretDenyGlobs() ([]string, error) {
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, configFileName), agentconfig.Overrides{})
	if err != nil {
		return nil, err
	}
	return effective.Secrets.SnapshotDenyGlobs, nil
}

func (repo *Repo) gitignoredPath(indexPath string, repoPath string) (bool, error) {
	cmd := exec.Command("git", "check-ignore", "--quiet", "--no-index", "--", repoPath)
	cmd.Dir = repo.WorkspaceRoot.String()
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.WorkspaceRoot.String(),
		"GIT_INDEX_FILE="+indexPath,
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git check-ignore --quiet --no-index -- %s: %w\n%s", repoPath, err, strings.TrimSpace(string(output)))
}

func excludedPath(repoPath string) bool {
	for _, segment := range strings.Split(repoPath, "/") {
		if strings.EqualFold(segment, ".git") || strings.EqualFold(segment, metadataDirName) {
			return true
		}
	}
	return false
}

func secretDeniedPath(repoPath string, patterns []string) bool {
	return snapshotpolicy.Denied(repoPath, patterns)
}

func gitRegularFileMode(mode fs.FileMode) string {
	if mode&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

func phaseRank(phase primitives.CheckpointPhase) int {
	switch phase {
	case primitives.CheckpointPhasePre:
		return 0
	case primitives.CheckpointPhasePost:
		return 1
	default:
		return 2
	}
}

func paths(root primitives.WorkspaceRoot) *Repo {
	metadataDir := filepath.Join(root.String(), metadataDirName)
	return pathsAt(root, metadataDir)
}

func pathsAt(root primitives.WorkspaceRoot, metadataDir string) *Repo {
	return &Repo{
		WorkspaceRoot: root,
		MetadataDir:   filepath.Clean(metadataDir),
		GitDir:        filepath.Join(metadataDir, gitDirName),
		TmpDir:        filepath.Join(metadataDir, tmpDirName),
	}
}

func (repo *Repo) tempIndex() (string, func(), error) {
	if err := os.MkdirAll(repo.TmpDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}

	file, err := os.CreateTemp(repo.TmpDir, "index-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp index: %w", err)
	}
	indexPath := file.Name()
	if err := file.Close(); err != nil {
		return "", nil, fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", nil, fmt.Errorf("prepare temp index: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(indexPath)
		_ = os.Remove(indexPath + ".lock")
	}
	return indexPath, cleanup, nil
}

func writeFileIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func runGitNoRepo(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = cleanGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func configureHiddenGit(repo *Repo) error {
	if _, err := runGitNoRepo(repo.WorkspaceRoot.String(), "--git-dir="+repo.GitDir, "config", "core.longpaths", "true"); err != nil {
		return fmt.Errorf("configure hidden git long-path support: %w", err)
	}
	return nil
}

func runHiddenGit(repo *Repo, indexPath string, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, nil, args...)
}

func runHiddenGitWithInput(repo *Repo, indexPath string, stdin io.Reader, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, stdin, args...)
}

func runHiddenGitReadOnly(repo *Repo, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo.WorkspaceRoot.String()
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.WorkspaceRoot.String(),
		"GIT_OPTIONAL_LOCKS=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func runHiddenGitCommand(repo *Repo, indexPath string, stdin io.Reader, args ...string) (string, error) {
	if indexPath == "" {
		var cleanup func()
		var err error
		indexPath, cleanup, err = repo.tempIndex()
		if err != nil {
			return "", err
		}
		defer cleanup()
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repo.WorkspaceRoot.String()
	cmd.Stdin = stdin
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.WorkspaceRoot.String(),
		"GIT_INDEX_FILE="+indexPath,
		"GIT_AUTHOR_NAME=turnal",
		"GIT_AUTHOR_EMAIL=turnal@localhost",
		"GIT_COMMITTER_NAME=turnal",
		"GIT_COMMITTER_EMAIL=turnal@localhost",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func cleanGitEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
