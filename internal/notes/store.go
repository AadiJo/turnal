package notes

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

// LogDirName is the durable note log root beneath the metadata log directory.
// It is exported so index freshness and retention can observe note writes
// without duplicating the layout.
const LogDirName = "notes"

// SessionID names the author's commentary stream. It is a fixed non-agent
// session id, parallel to the workspace session used for manual checkpoints.
func SessionID() primitives.SessionID {
	sessionID, err := primitives.ParseSessionID(noteSessionText)
	if err != nil {
		// noteSessionText is a compile-time constant that must parse.
		panic(err)
	}
	return sessionID
}

// Root is the note log root for one store.
func Root(metadataDir string) string {
	return filepath.Join(metadataDir, "log", LogDirName)
}

// authorLog returns the append target for notes written by this worktree. Notes
// are authored, so a note is always written to the current worktree's own
// producer-derived stream and never to the stream that recorded the target turn.
func authorLog(repo *checkpoint.Repo) (eventlog.Log, primitives.SessionID, error) {
	if repo == nil {
		return eventlog.Log{}, "", fmt.Errorf("note log requires checkpoint repo")
	}
	if repo.EventProducerID == "" {
		return eventlog.Log{}, "", fmt.Errorf("note log invariant failed: repo has no event producer id")
	}
	return eventlog.Log{
		Dir:           filepath.Join(Root(repo.MetadataDir), repo.WorktreeID.String(), "events"),
		WorkspaceRoot: repo.WorkspaceRoot.String(),
		RepoID:        repo.RepoID,
		StoreID:       repo.StoreID,
		WorktreeID:    repo.WorktreeID,
		ProducerID:    repo.EventProducerID,
	}, SessionID(), nil
}

// readerLog returns an aggregating reader over one worktree's note streams.
func readerLog(metadataDir string, worktreeID primitives.WorktreeID) (eventlog.Log, primitives.SessionID) {
	return eventlog.Log{
		Dir:        filepath.Join(Root(metadataDir), worktreeID.String(), "events"),
		WorktreeID: worktreeID,
		Aggregate:  true,
	}, SessionID()
}

// noteWorktrees lists worktrees that have written notes. A worktree directory
// that has disappeared still has readable notes, so absence of the worktree is
// not treated as absence of its commentary.
func noteWorktrees(metadataDir string) ([]primitives.WorktreeID, error) {
	entries, err := os.ReadDir(Root(metadataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read note log directory: %w", err)
	}
	var worktrees []primitives.WorktreeID
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("note log path invariant failed: symlink is not allowed: %s", filepath.Join(Root(metadataDir), entry.Name()))
		}
		if !entry.IsDir() {
			continue
		}
		worktreeID, err := primitives.ParseWorktreeID(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("note log directory invariant failed for %s: %w", entry.Name(), err)
		}
		worktrees = append(worktrees, worktreeID)
	}
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].String() < worktrees[j].String() })
	return worktrees, nil
}

// ReadEvents returns every durable note event in the store, hash-chain verified
// by the event log reader.
func ReadEvents(repo *checkpoint.Repo) ([]eventlog.Event, error) {
	if repo == nil {
		return nil, fmt.Errorf("read note events: repo is required")
	}
	return ReadEventsFromMetadata(repo.MetadataDir)
}

// ReadEventsFromMetadata reads note events using only the metadata directory, so
// readers that never open a checkpoint repo can still surface commentary.
func ReadEventsFromMetadata(metadataDir string) ([]eventlog.Event, error) {
	worktrees, err := noteWorktrees(metadataDir)
	if err != nil {
		return nil, err
	}
	var result []eventlog.Event
	for _, worktreeID := range worktrees {
		log, sessionID := readerLog(metadataDir, worktreeID)
		events, err := log.Read(sessionID)
		if err != nil {
			return nil, fmt.Errorf("read note events for worktree %s: %w", worktreeID, err)
		}
		result = append(result, events...)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].Time.Time.Equal(result[j].Time.Time) {
			return result[i].Time.Time.Before(result[j].Time.Time)
		}
		if result[i].StreamID != result[j].StreamID {
			return result[i].StreamID.String() < result[j].StreamID.String()
		}
		return result[i].Seq.Uint64() < result[j].Seq.Uint64()
	})
	return result, nil
}
