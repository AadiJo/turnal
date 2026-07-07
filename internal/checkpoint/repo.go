package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentconfig "agent-vcs-again/internal/config"
	"agent-vcs-again/internal/primitives"
)

const (
	metadataDirName = ".agent-vcs"
	gitDirName      = "git"
	tmpDirName      = "tmp"
	logDirName      = "log"
	indexDirName    = "index"
	versionFileName = "VERSION"
	configFileName  = "config.toml"
)

const workspaceConfigTemplate = `# agent-vcs workspace configuration
version = 1

# Workspace-specific overrides go here. Global defaults live in:
#   ~/.config/agent-vcs/config.toml
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
# command = "agent-vcs"
#
# [bootstrap]
# init_workspace_git = true
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
# agent-vcs session drop, agent-vcs retention prune, then explicit
# agent-vcs maintenance gc to delete refs first and garbage-collect later.
#
# [secrets]
# agent-vcs stores local snapshots byte-exact unless paths are denied here.
# Metadata stays local to this workspace unless you copy or sync .agent-vcs.
# store_prompts = true
# store_tool_io = true
# snapshot_deny_globs = [".env", ".env.*", "**/.env", "**/.env.*", "**/credentials.*"]
`

type Repo struct {
	WorkspaceRoot primitives.WorkspaceRoot
	MetadataDir   string
	GitDir        string
	TmpDir        string
}

type Checkpoint struct {
	Ref    primitives.CheckpointRef
	Commit primitives.CommitSHA
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
	Ref       primitives.CheckpointRef
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	Phase     primitives.CheckpointPhase
	HasPhase  bool
	Commit    primitives.CommitSHA
	Time      time.Time
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
	repo := paths(root)

	for _, dir := range []string{repo.MetadataDir, repo.TmpDir, filepath.Join(repo.MetadataDir, logDirName), filepath.Join(repo.MetadataDir, indexDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
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

	bare, err := repo.HiddenGitBare()
	if err != nil {
		return nil, fmt.Errorf("verify hidden git repo: %w", err)
	}
	if !bare {
		return nil, fmt.Errorf("hidden git repo is not bare: %s", repo.GitDir)
	}

	return repo, nil
}

func Open(root primitives.WorkspaceRoot) (*Repo, error) {
	repo := paths(root)
	if _, err := os.Stat(repo.GitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hidden git repo not initialized at %s", repo.GitDir)
		}
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
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

	return "", fmt.Errorf("not an agent-vcs workspace: run agent-vcs init")
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
	ref, err := primitives.NewCheckpointRef(sessionID, turnID, phase)
	if err != nil {
		return Checkpoint{}, err
	}

	message := fmt.Sprintf("agent-vcs checkpoint %s turn %s", sessionID, turnID)
	if phase != "" {
		message += " " + phase.String()
	}
	commit, err := repo.createSnapshotCommit(message)
	if err != nil {
		return Checkpoint{}, err
	}

	if _, err := runHiddenGit(repo, "", "update-ref", ref.String(), commit.String()); err != nil {
		return Checkpoint{}, err
	}

	return Checkpoint{Ref: ref, Commit: commit}, nil
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
		message = "agent-vcs snapshot"
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
		message = "agent-vcs snapshot"
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

	if err := repo.snapshotWorktree(indexPath); err != nil {
		return "", err
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
		return "", fmt.Errorf("parse checkpoint commit: %w", err)
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
	preRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return nil, err
	}
	postRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return nil, err
	}
	return repo.DiffRefs(preRef, postRef)
}

func (repo *Repo) ListCheckpointRefs(sessionID primitives.SessionID) ([]primitives.CheckpointRef, error) {
	refPrefix, err := primitives.CheckpointSessionRefPrefix(sessionID)
	if err != nil {
		return nil, err
	}

	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)", refPrefix)
	if err != nil {
		return nil, err
	}

	var refs []primitives.CheckpointRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref, err := primitives.ParseCheckpointRef(line)
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", line, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (repo *Repo) ListAllCheckpointRefInfos() ([]CheckpointRefInfo, error) {
	return repo.listCheckpointRefInfos(primitives.CheckpointRefsPrefix())
}

func (repo *Repo) ListCheckpointRefInfos(sessionID primitives.SessionID) ([]CheckpointRefInfo, error) {
	refPrefix, err := primitives.CheckpointSessionRefPrefix(sessionID)
	if err != nil {
		return nil, err
	}
	return repo.listCheckpointRefInfos(refPrefix)
}

func (repo *Repo) listCheckpointRefInfos(refPrefix string) ([]CheckpointRefInfo, error) {
	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)%09%(objectname)%09%(committerdate:iso-strict)", refPrefix)
	if err != nil {
		return nil, err
	}

	var infos []CheckpointRefInfo
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

		infos = append(infos, CheckpointRefInfo{
			Ref:       ref,
			SessionID: refParts.SessionID,
			TurnID:    refParts.TurnID,
			Phase:     refParts.Phase,
			HasPhase:  refParts.HasPhase,
			Commit:    commit,
			Time:      createdAt,
		})
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
	preRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return DiffSummary{}, err
	}
	postRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePost)
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
	if _, err := runHiddenGit(repo, "", "reflog", "expire", "--expire=all", "--all"); err != nil {
		return err
	}
	if _, err := runHiddenGit(repo, "", "gc", "--prune=now"); err != nil {
		return err
	}
	return nil
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
	output, err := runHiddenGit(repo, "", "rev-parse", parsedRef+"^{commit}")
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
	preservePaths, err := materializePreserveSet(options.PreservePaths)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := repo.restoreTreeEntryAtRoot(materializeRoot, entry); err != nil {
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
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if secretDeniedPath(entry.Path, denyGlobs) {
			continue
		}
		if err := repo.restoreTreeEntry(entry); err != nil {
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
	output, err := runHiddenGit(repo, "", "ls-tree", "-r", "-z", "--full-tree", parsedCommit.String())
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
	if err := repo.snapshotWorktree(indexPath); err != nil {
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

func (repo *Repo) restoreTreeEntryAtRoot(root string, entry TreeEntry) error {
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
		return repo.restoreRegularFile(absPath, entry)
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

func (repo *Repo) restoreRegularFile(absPath string, entry TreeEntry) error {
	content, err := repo.blobBytes(entry.ObjectID)
	if err != nil {
		return fmt.Errorf("read blob for %s: %w", entry.Path, err)
	}

	dir := filepath.Dir(absPath)
	tmpFile, err := os.CreateTemp(dir, ".agent-vcs-restore-*")
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

	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp file for %s: %w", entry.Path, err)
	}
	mode := fs.FileMode(0o644)
	if entry.Mode == "100755" {
		mode = 0o755
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
	target, err := repo.blobBytes(entry.ObjectID)
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
	if _, err := runHiddenGit(repo, "", "check-ref-format", ref); err != nil {
		return "", err
	}
	return ref, nil
}

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

func (repo *Repo) snapshotWorktree(indexPath string) error {
	root := repo.WorkspaceRoot.String()
	denyGlobs, err := repo.secretDenyGlobs()
	if err != nil {
		return err
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
}

func (repo *Repo) secretDenyGlobs() ([]string, error) {
	effective, _, err := agentconfig.Resolve(repo.WorkspaceRoot.String(), agentconfig.Overrides{})
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
	for _, pattern := range patterns {
		if globMatchesRepoPath(filepath.ToSlash(pattern), repoPath) {
			return true
		}
	}
	return false
}

func globMatchesRepoPath(pattern string, repoPath string) bool {
	if matched, _ := path.Match(pattern, repoPath); matched {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if matched, _ := path.Match(pattern, path.Base(repoPath)); matched {
			return true
		}
	}
	if suffix, ok := strings.CutPrefix(pattern, "**/"); ok {
		if matched, _ := path.Match(suffix, repoPath); matched {
			return true
		}
		parts := strings.Split(repoPath, "/")
		for i := 1; i < len(parts); i++ {
			if matched, _ := path.Match(suffix, strings.Join(parts[i:], "/")); matched {
				return true
			}
		}
	}
	return false
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
	return &Repo{
		WorkspaceRoot: root,
		MetadataDir:   metadataDir,
		GitDir:        filepath.Join(metadataDir, gitDirName),
		TmpDir:        filepath.Join(metadataDir, tmpDirName),
	}
}

func (repo *Repo) tempIndex() (string, func(), error) {
	if err := os.MkdirAll(repo.TmpDir, 0o755); err != nil {
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
	if err := os.WriteFile(path, content, 0o644); err != nil {
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

func runHiddenGit(repo *Repo, indexPath string, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, nil, args...)
}

func runHiddenGitWithInput(repo *Repo, indexPath string, stdin io.Reader, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, stdin, args...)
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
		"GIT_AUTHOR_NAME=agent-vcs",
		"GIT_AUTHOR_EMAIL=agent-vcs@localhost",
		"GIT_COMMITTER_NAME=agent-vcs",
		"GIT_COMMITTER_EMAIL=agent-vcs@localhost",
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
