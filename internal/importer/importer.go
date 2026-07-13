package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fsidentity"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	journalVersion  = 1
	manifestVersion = 2
	maxStreamCount  = 10000
	maxStreamBytes  = int64(512 << 20)
)

type Options struct {
	DryRun                   bool
	AdoptSourceAsCurrentRepo bool
}

type StreamPlan struct {
	SessionID  primitives.SessionID     `json:"session_id"`
	StreamID   primitives.EventStreamID `json:"stream_id"`
	WorktreeID primitives.WorktreeID    `json:"worktree_id,omitempty"`
	ByteSHA256 string                   `json:"byte_sha256"`
	Bytes      int64                    `json:"bytes"`
	Events     int                      `json:"events"`
	Legacy     bool                     `json:"legacy"`
	Status     string                   `json:"status"`
	Workspace  bool                     `json:"workspace,omitempty"`
}

type Plan struct {
	ImportID        primitives.ImportID `json:"import_id"`
	SourcePath      string              `json:"source_path"`
	SourceRepoID    primitives.RepoID   `json:"source_repo_id"`
	EffectiveRepoID primitives.RepoID   `json:"effective_repo_id"`
	SourceStoreID   primitives.StoreID  `json:"source_store_id"`
	DestinationID   primitives.StoreID  `json:"destination_store_id"`
	AdoptedRepo     bool                `json:"repo_adoption_asserted"`
	Streams         []StreamPlan        `json:"streams"`
	Refs            int                 `json:"refs"`
	Checkpoints     int                 `json:"checkpoints"`
	Duplicates      int                 `json:"duplicates"`
	Bytes           int64               `json:"bytes"`
	DryRun          bool                `json:"dry_run"`
}

type Result struct {
	Plan       Plan
	Manifest   string
	IndexError error
}

type Journal struct {
	Version                  int                 `json:"version"`
	State                    string              `json:"state"`
	ImportID                 primitives.ImportID `json:"import_id"`
	SourcePath               string              `json:"source_path"`
	AdoptSourceAsCurrentRepo bool                `json:"adopt_source_as_current_repo"`
	StartedAt                string              `json:"started_at"`
	UpdatedAt                string              `json:"updated_at"`
}

type Manifest struct {
	Version         int                 `json:"version"`
	ImportID        primitives.ImportID `json:"import_id"`
	SourceStoreID   primitives.StoreID  `json:"source_store_id"`
	SourceRepoID    primitives.RepoID   `json:"source_repo_id"`
	EffectiveRepoID primitives.RepoID   `json:"effective_repo_id"`
	RepoAdopted     bool                `json:"repo_adoption_asserted"`
	ObjectFormat    string              `json:"git_object_format"`
	Streams         []StreamPlan        `json:"streams"`
	RefMappings     map[string]string   `json:"ref_mappings"`
	CompletedAt     string              `json:"completed_at"`
}

type checkpointPayload struct {
	Origin       string `json:"origin"`
	CheckpointID string `json:"checkpoint_id"`
	WorktreeID   string `json:"worktree_id"`
	StreamID     string `json:"stream_id"`
	Phase        string `json:"phase"`
	CommitSHA    string `json:"commit_sha"`
	Ref          string `json:"ref"`
}

type checkpointAlias struct {
	Ref    primitives.CheckpointRef
	Commit primitives.CommitSHA
}

func Run(repo *checkpoint.Repo, sourcePath string, options Options) (Result, error) {
	importID, err := primitives.NewImportID()
	if err != nil {
		return Result{}, err
	}
	return runWithID(repo, sourcePath, importID, options, false)
}

func Recover(repo *checkpoint.Repo) (Result, error) {
	journal, err := pendingJournal(repo)
	if err != nil {
		return Result{}, err
	}
	return runWithID(repo, journal.SourcePath, journal.ImportID, Options{AdoptSourceAsCurrentRepo: journal.AdoptSourceAsCurrentRepo}, true)
}

func Abort(repo *checkpoint.Repo) error {
	journal, err := pendingJournal(repo)
	if err != nil {
		return err
	}
	if err := repo.ClearImportStaging(journal.ImportID); err != nil {
		return err
	}
	return os.RemoveAll(importTmpDir(repo, journal.ImportID))
}

func Pending(repo *checkpoint.Repo) ([]Journal, error) {
	dir := filepath.Join(repo.TmpDir, "imports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var journals []Journal
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		journal, err := readJournal(filepath.Join(dir, entry.Name(), "journal.json"))
		if err != nil {
			return nil, err
		}
		journals = append(journals, journal)
	}
	sort.Slice(journals, func(i, j int) bool { return journals[i].StartedAt < journals[j].StartedAt })
	return journals, nil
}

func runWithID(repo *checkpoint.Repo, sourcePath string, importID primitives.ImportID, options Options, recovering bool) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("merge requires destination repo")
	}
	sourceMetadata, err := resolveSourceMetadata(sourcePath)
	if err != nil {
		return Result{}, err
	}
	if samePath(sourceMetadata, repo.MetadataDir) {
		return Result{}, fmt.Errorf("source and destination Turnal stores are the same: %s", sourceMetadata)
	}
	sourceIdentity, err := checkpoint.ReadStoreIdentity(sourceMetadata)
	if err != nil {
		return Result{}, err
	}
	if sourceIdentity.StoreID == repo.StoreID {
		return Result{}, fmt.Errorf("source store id %s matches destination; copied stores must be rekeyed before divergent import", sourceIdentity.StoreID)
	}
	if sourceIdentity.GitObjectFormat != repo.GitObjectFormat {
		return Result{}, fmt.Errorf("hidden git object format mismatch: source=%s destination=%s", sourceIdentity.GitObjectFormat, repo.GitObjectFormat)
	}
	adopted := sourceIdentity.RepoID != repo.RepoID
	if adopted && !options.AdoptSourceAsCurrentRepo {
		return Result{}, fmt.Errorf("repo identity mismatch: source=%s destination=%s; rerun with --adopt-source-as-current-repo only if these stores are the same logical project", sourceIdentity.RepoID, repo.RepoID)
	}

	streams, err := eventlog.ListDurableStreams(sourceMetadata)
	if err != nil {
		return Result{}, err
	}
	if len(streams) > maxStreamCount {
		return Result{}, fmt.Errorf("import stream count %d exceeds limit %d", len(streams), maxStreamCount)
	}
	worktrees, err := checkpoint.ReadWorktreeIdentities(sourceMetadata)
	if err != nil {
		return Result{}, err
	}
	primaryWorktree := primaryWorktreeIdentity(worktrees)

	refs, err := checkpoint.ListSourcePrivateRefs(filepath.Join(sourceMetadata, "git"), sourceIdentity.StoreID)
	if err != nil {
		return Result{}, err
	}
	refByOriginal := make(map[string]checkpoint.ImportedRef, len(refs))
	for _, ref := range refs {
		refByOriginal[ref.OriginalRef] = ref
	}

	plan := Plan{
		ImportID:        importID,
		SourcePath:      sourceMetadata,
		SourceRepoID:    sourceIdentity.RepoID,
		EffectiveRepoID: repo.RepoID,
		SourceStoreID:   sourceIdentity.StoreID,
		DestinationID:   repo.StoreID,
		AdoptedRepo:     adopted,
		Refs:            len(refs),
		DryRun:          options.DryRun,
	}
	aliases, err := inspectCheckpoints(streams, refByOriginal, primaryWorktree, &plan)
	if err != nil {
		return Result{}, err
	}
	for _, stream := range streams {
		if stream.ByteCount > maxStreamBytes {
			return Result{}, fmt.Errorf("event stream %s is %d bytes; limit is %d", stream.StreamID, stream.ByteCount, maxStreamBytes)
		}
		status, err := destinationStreamStatus(repo.MetadataDir, stream)
		if err != nil {
			return Result{}, err
		}
		if status == "duplicate" {
			plan.Duplicates++
		}
		plan.Bytes += stream.ByteCount
		worktreeID := stream.WorktreeID
		if worktreeID == "" {
			worktreeID = primaryWorktree.WorktreeID
		}
		plan.Streams = append(plan.Streams, StreamPlan{
			SessionID:  stream.SessionID,
			StreamID:   stream.StreamID,
			WorktreeID: worktreeID,
			ByteSHA256: stream.ByteSHA256,
			Bytes:      stream.ByteCount,
			Events:     len(stream.Events),
			Legacy:     stream.Legacy,
			Status:     status,
			Workspace:  stream.Workspace,
		})
	}
	if options.DryRun {
		return Result{Plan: plan}, nil
	}

	var result Result
	err = repo.WithWorkspaceLock("merge store", func() error {
		if !recovering {
			journal := Journal{
				Version:                  journalVersion,
				State:                    "intent",
				ImportID:                 importID,
				SourcePath:               sourceMetadata,
				AdoptSourceAsCurrentRepo: options.AdoptSourceAsCurrentRepo,
				StartedAt:                time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := writeJournalForRepo(repo, journal); err != nil {
				return err
			}
		}
		stagedRefs, err := repo.StageImportRefs(filepath.Join(sourceMetadata, "git"), sourceIdentity.StoreID, importID)
		if err != nil {
			return err
		}
		stagedByOriginal := make(map[string]checkpoint.ImportedRef, len(stagedRefs))
		for _, ref := range stagedRefs {
			stagedByOriginal[ref.OriginalRef] = ref
		}
		if _, err := inspectCheckpoints(streams, stagedByOriginal, primaryWorktree, nil); err != nil {
			return err
		}
		if err := stageStreams(repo, importID, streams); err != nil {
			return err
		}
		if err := updateJournalState(repo, importID, "staged"); err != nil {
			return err
		}
		if err := repo.PromoteImportedRefs(stagedRefs); err != nil {
			return err
		}
		for _, alias := range aliases {
			if err := repo.EnsureImportedCheckpointAlias(alias.Ref, alias.Commit); err != nil {
				return err
			}
		}
		if err := installStreams(repo, importID, streams, primaryWorktree); err != nil {
			return err
		}
		manifestPath, err := writeManifest(repo, sourceIdentity, plan, stagedRefs)
		if err != nil {
			return err
		}
		if err := repo.ClearImportStaging(importID); err != nil {
			return err
		}
		if err := os.RemoveAll(importTmpDir(repo, importID)); err != nil {
			return fmt.Errorf("clear import staging directory: %w", err)
		}
		result = Result{Plan: plan, Manifest: manifestPath}
		return nil
	})
	if err != nil {
		return Result{Plan: plan}, err
	}
	if _, indexErr := queryindex.Rebuild(repo); indexErr != nil {
		result.IndexError = indexErr
	}
	return result, nil
}

func inspectCheckpoints(streams []eventlog.DurableStream, refs map[string]checkpoint.ImportedRef, primary checkpoint.WorktreeIdentity, plan *Plan) ([]checkpointAlias, error) {
	var aliases []checkpointAlias
	for _, stream := range streams {
		for _, event := range stream.Events {
			if stream.Workspace && event.Type == primitives.EventTypeRollback {
				rollback, err := manualcheckpoints.ParseRollbackEvent(event)
				if err != nil {
					return nil, err
				}
				checkpointRef, ok := refs[rollback.Ref.String()]
				if !ok || checkpointRef.Commit != rollback.Target {
					return nil, fmt.Errorf("workspace rollback event %s:%s checkpoint ref does not match target commit", stream.StreamID, event.Seq)
				}
				safetyRef, ok := refs[rollback.Payload.SafetyRef]
				if !ok || safetyRef.Commit != rollback.SafetyCommit {
					return nil, fmt.Errorf("workspace rollback event %s:%s safety ref does not match safety commit", stream.StreamID, event.Seq)
				}
				continue
			}
			if event.Type != primitives.EventTypeCheckpoint {
				continue
			}
			var payload checkpointPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("checkpoint event %s:%s payload is malformed: %w", stream.StreamID, event.Seq, err)
			}
			commit, err := primitives.ParseCommitSHA(payload.CommitSHA)
			if err != nil {
				return nil, err
			}
			importedRef, ok := refs[payload.Ref]
			if !ok {
				return nil, fmt.Errorf("checkpoint event %s:%s references missing source ref %s", stream.StreamID, event.Seq, payload.Ref)
			}
			if importedRef.Commit != commit {
				return nil, fmt.Errorf("checkpoint event %s:%s commit mismatch: ref=%s payload=%s", stream.StreamID, event.Seq, importedRef.Commit, commit)
			}
			if stream.Workspace {
				if payload.Origin != "manual" || event.TurnID != nil {
					return nil, fmt.Errorf("workspace checkpoint event %s:%s must be a turnless manual checkpoint", stream.StreamID, event.Seq)
				}
				checkpointID, err := primitives.ParseCheckpointID(payload.CheckpointID)
				if err != nil {
					return nil, err
				}
				worktreeID, err := primitives.ParseWorktreeID(payload.WorktreeID)
				if err != nil {
					return nil, err
				}
				if stream.WorktreeID != "" && stream.WorktreeID != worktreeID {
					return nil, fmt.Errorf("workspace checkpoint event %s:%s worktree mismatch", stream.StreamID, event.Seq)
				}
				ref, err := primitives.ParseCheckpointRef(payload.Ref)
				if err != nil {
					return nil, err
				}
				parts, err := ref.Parts()
				if err != nil || !parts.Manual || parts.WorktreeID != worktreeID || parts.CheckpointID != checkpointID {
					return nil, fmt.Errorf("workspace checkpoint event %s:%s manual ref identity mismatch", stream.StreamID, event.Seq)
				}
				alias, err := primitives.NewManualCheckpointRef(worktreeID, checkpointID)
				if err != nil {
					return nil, err
				}
				canonicalRef, err := primitives.NewCheckpointIDRef(checkpointID)
				if err != nil {
					return nil, err
				}
				aliases = append(aliases, checkpointAlias{Ref: alias, Commit: commit}, checkpointAlias{Ref: canonicalRef, Commit: commit})
				if plan != nil {
					plan.Checkpoints++
				}
				continue
			}
			if payload.Origin == "manual" {
				return nil, fmt.Errorf("session checkpoint event %s:%s cannot have manual origin", stream.StreamID, event.Seq)
			}
			worktreeID := stream.WorktreeID
			if payload.WorktreeID != "" {
				worktreeID, err = primitives.ParseWorktreeID(payload.WorktreeID)
				if err != nil {
					return nil, err
				}
			}
			if worktreeID == "" {
				worktreeID = primary.WorktreeID
			}
			if worktreeID == "" || event.TurnID == nil {
				return nil, fmt.Errorf("checkpoint event %s:%s lacks worktree or turn identity", stream.StreamID, event.Seq)
			}
			phase, err := primitives.ParseCheckpointPhase(payload.Phase)
			if err != nil {
				return nil, err
			}
			alias, err := primitives.NewScopedCheckpointRef(worktreeID, stream.StreamID, stream.SessionID, *event.TurnID, phase)
			if err != nil {
				return nil, err
			}
			aliases = append(aliases, checkpointAlias{Ref: alias, Commit: commit})
			if payload.CheckpointID != "" {
				checkpointID, err := primitives.ParseCheckpointID(payload.CheckpointID)
				if err != nil {
					return nil, err
				}
				canonicalRef, err := primitives.NewCheckpointIDRef(checkpointID)
				if err != nil {
					return nil, err
				}
				aliases = append(aliases, checkpointAlias{Ref: canonicalRef, Commit: commit})
			}
			if plan != nil {
				plan.Checkpoints++
			}
		}
	}
	return aliases, nil
}

func stageStreams(repo *checkpoint.Repo, importID primitives.ImportID, streams []eventlog.DurableStream) error {
	for _, stream := range streams {
		destination := stagedStreamPath(repo, importID, stream)
		if err := copyExactFile(stream.Path, destination); err != nil {
			return err
		}
	}
	return nil
}

func installStreams(repo *checkpoint.Repo, importID primitives.ImportID, streams []eventlog.DurableStream, primary checkpoint.WorktreeIdentity) error {
	for _, stream := range streams {
		finalPath := destinationStreamPath(repo.MetadataDir, stream)
		status, err := destinationStreamStatus(repo.MetadataDir, stream)
		if err != nil {
			return err
		}
		if status != "duplicate" {
			if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
				return err
			}
			stagedPath := stagedStreamPath(repo, importID, stream)
			if err := os.Rename(stagedPath, finalPath); err != nil {
				return fmt.Errorf("install event stream %s: %w", stream.StreamID, err)
			}
		}
		worktreeID := stream.WorktreeID
		producerID := stream.ProducerID
		if worktreeID == "" {
			worktreeID = primary.WorktreeID
		}
		if producerID == "" {
			producerID = primary.ProducerID
		}
		metadata := eventlog.StreamMetadata{
			Version:    1,
			StreamID:   stream.StreamID,
			ProducerID: producerID,
			RepoID:     stream.RepoID,
			WorktreeID: worktreeID,
			SessionID:  stream.SessionID,
		}
		writeMetadata := eventlog.WriteStreamMetadata
		if stream.Workspace {
			writeMetadata = eventlog.WriteWorkspaceStreamMetadata
		}
		if err := writeMetadata(repo.MetadataDir, metadata); err != nil {
			return err
		}
	}
	return nil
}

func destinationStreamStatus(metadataDir string, stream eventlog.DurableStream) (string, error) {
	path := destinationStreamPath(metadataDir, stream)
	digest, exists, err := fileDigest(path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "import", nil
	}
	if digest == stream.ByteSHA256 {
		return "duplicate", nil
	}
	return "", fmt.Errorf("divergent stream conflict: %s exists with digest %s, source has %s", stream.StreamID, digest, stream.ByteSHA256)
}

func stagedStreamPath(repo *checkpoint.Repo, importID primitives.ImportID, stream eventlog.DurableStream) string {
	if stream.Workspace {
		return filepath.Join(importTmpDir(repo, importID), "workspace-streams", stream.WorktreeID.String(), stream.StreamID.String()+".jsonl")
	}
	return filepath.Join(importTmpDir(repo, importID), "streams", stream.SessionID.String(), stream.StreamID.String()+".jsonl")
}

func destinationStreamPath(metadataDir string, stream eventlog.DurableStream) string {
	if stream.Workspace {
		return eventlog.WorkspaceStreamPath(metadataDir, stream.WorktreeID, stream.StreamID)
	}
	return eventlog.StreamPath(metadataDir, stream.SessionID, stream.StreamID)
}

func writeManifest(repo *checkpoint.Repo, source checkpoint.StoreIdentity, plan Plan, refs []checkpoint.ImportedRef) (string, error) {
	mappings := make(map[string]string, len(refs))
	for _, ref := range refs {
		mappings[ref.OriginalRef] = ref.FinalRef
	}
	manifest := Manifest{
		Version:         manifestVersion,
		ImportID:        plan.ImportID,
		SourceStoreID:   source.StoreID,
		SourceRepoID:    source.RepoID,
		EffectiveRepoID: plan.EffectiveRepoID,
		RepoAdopted:     plan.AdoptedRepo,
		ObjectFormat:    source.GitObjectFormat,
		Streams:         plan.Streams,
		RefMappings:     mappings,
		CompletedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	path := filepath.Join(repo.MetadataDir, "imports", plan.ImportID.String()+".json")
	if err := writeJSONAtomic(path, manifest); err != nil {
		return "", err
	}
	return path, nil
}

func resolveSourceMetadata(sourcePath string) (string, error) {
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve source store path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if info, err := os.Stat(filepath.Join(absolute, "git")); err == nil && info.IsDir() {
		return absolute, nil
	}
	candidate := filepath.Join(absolute, ".turnal")
	if info, err := os.Stat(filepath.Join(candidate, "git")); err == nil && info.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("source is not a Turnal store: expected hidden git at %s/git", absolute)
}

func primaryWorktreeIdentity(worktrees []checkpoint.WorktreeIdentity) checkpoint.WorktreeIdentity {
	for _, worktree := range worktrees {
		if worktree.Primary {
			return worktree
		}
	}
	if len(worktrees) > 0 {
		return worktrees[0]
	}
	return checkpoint.WorktreeIdentity{}
}

func pendingJournal(repo *checkpoint.Repo) (Journal, error) {
	journals, err := Pending(repo)
	if err != nil {
		return Journal{}, err
	}
	if len(journals) == 0 {
		return Journal{}, fmt.Errorf("no pending import journal")
	}
	if len(journals) > 1 {
		return Journal{}, fmt.Errorf("multiple pending import journals; inspect %s", filepath.Join(repo.TmpDir, "imports"))
	}
	return journals[0], nil
}

func importTmpDir(repo *checkpoint.Repo, importID primitives.ImportID) string {
	return filepath.Join(repo.TmpDir, "imports", importID.String())
}

func writeJournalForRepo(repo *checkpoint.Repo, journal Journal) error {
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeJSONAtomic(filepath.Join(importTmpDir(repo, journal.ImportID), "journal.json"), journal)
}

func updateJournalState(repo *checkpoint.Repo, importID primitives.ImportID, state string) error {
	path := filepath.Join(importTmpDir(repo, importID), "journal.json")
	journal, err := readJournal(path)
	if err != nil {
		return err
	}
	journal.State = state
	return writeJournalForRepo(repo, journal)
}

func readJournal(path string) (Journal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return Journal{}, fmt.Errorf("import journal invariant failed at %s: %w", path, err)
	}
	if journal.Version != journalVersion {
		return Journal{}, fmt.Errorf("import journal invariant failed: unsupported version %d", journal.Version)
	}
	if _, err := primitives.ParseImportID(journal.ImportID.String()); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".import-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func copyExactFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("import source must be a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileDigest(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", false, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), true, nil
}

func samePath(left, right string) bool {
	return fsidentity.Same(left, right)
}
