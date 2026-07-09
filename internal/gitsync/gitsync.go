package gitsync

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	StateVersion      = 1
	statePath         = "state.json"
	stagedPatchPath   = "staged.patch"
	unstagedPatchPath = "unstaged.patch"
	untrackedPrefix   = "untracked"
)

type Head struct {
	Commit      primitives.CommitSHA `json:"commit"`
	SymbolicRef string               `json:"symbolic_ref,omitempty"`
	Detached    bool                 `json:"detached"`
}

type Patch struct {
	Paths []primitives.RepoPath `json:"paths,omitempty"`
}

type UntrackedFile struct {
	Path        primitives.RepoPath    `json:"path"`
	Mode        primitives.GitFileMode `json:"mode"`
	StoragePath string                 `json:"storage_path"`
}

type State struct {
	Version    int             `json:"version"`
	CapturedAt string          `json:"captured_at"`
	Head       Head            `json:"head"`
	Staged     Patch           `json:"staged"`
	Unstaged   Patch           `json:"unstaged"`
	Untracked  []UntrackedFile `json:"untracked,omitempty"`
}

type Capture struct {
	State            State
	StagedPatch      []byte
	UnstagedPatch    []byte
	UntrackedContent map[string][]byte
}

func NewState(head Head) State {
	return State{
		Version:    StateVersion,
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Head:       head,
	}
}

func Save(repo *checkpoint.Repo, ref primitives.GitSyncRef, capture Capture, message string) (checkpoint.Snapshot, error) {
	parsedRef, err := primitives.ParseGitSyncRef(ref.String())
	if err != nil {
		return checkpoint.Snapshot{}, err
	}
	return SavePrivate(repo, parsedRef.String(), capture, message)
}

func SavePrivate(repo *checkpoint.Repo, ref string, capture Capture, message string) (checkpoint.Snapshot, error) {
	if repo == nil {
		return checkpoint.Snapshot{}, fmt.Errorf("git-sync repo is required")
	}
	capture.State.Version = StateVersion
	if capture.State.CapturedAt == "" {
		capture.State.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err := time.Parse(time.RFC3339Nano, capture.State.CapturedAt); err != nil {
		return checkpoint.Snapshot{}, fmt.Errorf("git-sync state captured_at is invalid: %w", err)
	}
	if _, err := primitives.ParseCommitSHA(capture.State.Head.Commit.String()); err != nil {
		return checkpoint.Snapshot{}, err
	}

	stateJSON, err := json.MarshalIndent(capture.State, "", "  ")
	if err != nil {
		return checkpoint.Snapshot{}, fmt.Errorf("marshal git-sync state: %w", err)
	}
	stateJSON = append(stateJSON, '\n')

	entries := []checkpoint.SyntheticTreeEntry{
		{Path: statePath, Mode: primitives.GitFileModeRegular, Content: stateJSON},
		{Path: stagedPatchPath, Mode: primitives.GitFileModeRegular, Content: capture.StagedPatch},
		{Path: unstagedPatchPath, Mode: primitives.GitFileModeRegular, Content: capture.UnstagedPatch},
	}
	for _, file := range capture.State.Untracked {
		if _, err := primitives.ParseRepoPath(file.Path.String()); err != nil {
			return checkpoint.Snapshot{}, err
		}
		if _, err := primitives.ParseGitFileMode(file.Mode.String()); err != nil {
			return checkpoint.Snapshot{}, err
		}
		if _, err := primitives.ParseRepoPath(file.StoragePath); err != nil {
			return checkpoint.Snapshot{}, err
		}
		content, ok := capture.UntrackedContent[file.Path.String()]
		if !ok {
			return checkpoint.Snapshot{}, fmt.Errorf("missing content for untracked path %s", file.Path)
		}
		entries = append(entries, checkpoint.SyntheticTreeEntry{
			Path:    file.StoragePath,
			Mode:    file.Mode,
			Content: content,
		})
	}
	if message == "" {
		message = "turnal git-sync state"
	}
	return repo.CreateSyntheticSnapshotRef(ref, message, entries)
}

func Load(repo *checkpoint.Repo, ref primitives.GitSyncRef) (Capture, error) {
	if repo == nil {
		return Capture{}, fmt.Errorf("git-sync repo is required")
	}
	parsedRef, err := primitives.ParseGitSyncRef(ref.String())
	if err != nil {
		return Capture{}, err
	}
	return LoadPrivate(repo, parsedRef.String())
}

func LoadPrivate(repo *checkpoint.Repo, ref string) (Capture, error) {
	if repo == nil {
		return Capture{}, fmt.Errorf("git-sync repo is required")
	}
	commit, err := repo.RefCommit(ref)
	if err != nil {
		return Capture{}, fmt.Errorf("load git-sync ref %s: %w", ref, err)
	}

	stateBytes, err := repo.CommitFileBytes(commit, statePath)
	if err != nil {
		return Capture{}, fmt.Errorf("read git-sync state: %w", err)
	}
	var state State
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		return Capture{}, fmt.Errorf("parse git-sync state: %w", err)
	}
	if state.Version != StateVersion {
		return Capture{}, fmt.Errorf("unsupported git-sync state version %d", state.Version)
	}
	if _, err := time.Parse(time.RFC3339Nano, state.CapturedAt); err != nil {
		return Capture{}, fmt.Errorf("git-sync state captured_at is invalid: %w", err)
	}
	if _, err := primitives.ParseCommitSHA(state.Head.Commit.String()); err != nil {
		return Capture{}, err
	}

	stagedPatch, err := repo.CommitFileBytes(commit, stagedPatchPath)
	if err != nil {
		return Capture{}, fmt.Errorf("read staged git-sync patch: %w", err)
	}
	unstagedPatch, err := repo.CommitFileBytes(commit, unstagedPatchPath)
	if err != nil {
		return Capture{}, fmt.Errorf("read unstaged git-sync patch: %w", err)
	}

	capture := Capture{
		State:            state,
		StagedPatch:      stagedPatch,
		UnstagedPatch:    unstagedPatch,
		UntrackedContent: make(map[string][]byte, len(state.Untracked)),
	}
	for _, file := range state.Untracked {
		if _, err := primitives.ParseRepoPath(file.Path.String()); err != nil {
			return Capture{}, err
		}
		if _, err := primitives.ParseGitFileMode(file.Mode.String()); err != nil {
			return Capture{}, err
		}
		content, err := repo.CommitFileBytes(commit, file.StoragePath)
		if err != nil {
			return Capture{}, fmt.Errorf("read untracked git-sync file %s: %w", file.Path, err)
		}
		capture.UntrackedContent[file.Path.String()] = content
	}
	return capture, nil
}

func Ref(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (primitives.GitSyncRef, error) {
	return primitives.NewGitSyncRef(sessionID, turnID, phase)
}

func UntrackedStoragePath(repoPath primitives.RepoPath) string {
	return untrackedPrefix + "/" + repoPath.String()
}
