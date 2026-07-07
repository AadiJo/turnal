package index

import (
	"path/filepath"
	"time"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

const (
	SchemaVersion = 2
	DBFileName    = "index.sqlite"
)

type Paths struct {
	Dir    string
	DBPath string
}

func PathsForMetadata(metadataDir string) Paths {
	dir := filepath.Join(metadataDir, "index")
	return Paths{
		Dir:    dir,
		DBPath: filepath.Join(dir, DBFileName),
	}
}

type RebuildStats struct {
	DBPath      string
	Sessions    int
	Turns       int
	Events      int
	Checkpoints int
	FileTouches int
}

type GraphQuery struct {
	Session primitives.SessionID
	Limit   int
}

type GraphSession struct {
	ID         primitives.SessionID
	Turns      []GraphTurn
	TotalTurns int
	Warnings   []string
}

type GraphTurn struct {
	TurnID     primitives.TurnID
	Pre        *checkpoint.CheckpointRefInfo
	Post       *checkpoint.CheckpointRefInfo
	Diff       checkpoint.DiffSummary
	DiffLoaded bool
	Events     TurnEventSummary
	Warnings   []string
}

type TurnEventSummary struct {
	Count      int
	Adapter    string
	Prompt     string
	Assistant  string
	ToolNames  []string
	TypeCounts map[primitives.EventType]int
	First      time.Time
	Last       time.Time
}

type BlameCacheQuery struct {
	ScopeSession  primitives.SessionID
	Path          primitives.RepoPath
	HistoryKey    string
	LatestRef     primitives.CheckpointRef
	LatestCommit  primitives.CommitSHA
	CompleteTurns int
	Line          int
}

type BlameCacheSnapshot struct {
	ScopeSession  primitives.SessionID
	Path          primitives.RepoPath
	HistoryKey    string
	LatestRef     primitives.CheckpointRef
	LatestCommit  primitives.CommitSHA
	LatestTime    time.Time
	CompleteTurns int
	LineCount     int
	Entries       []BlameCacheEntry
	Warnings      []string
}

type BlameCacheEntry struct {
	Line   int
	Text   string
	Origin BlameCacheOrigin
}

type BlameCacheOrigin struct {
	Kind          string
	SessionID     primitives.SessionID
	TurnID        primitives.TurnID
	CheckpointRef primitives.CheckpointRef
	Commit        primitives.CommitSHA
	Time          time.Time
	Adapter       string
	Prompt        string
	ToolNames     []string
}
