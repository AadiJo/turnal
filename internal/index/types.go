package index

import (
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	SchemaVersion = 4
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
	DBPath            string
	Sessions          int
	Turns             int
	Events            int
	Checkpoints       int
	FileTouches       int
	SearchDocuments   int
	SourceFingerprint string
}

type GraphQuery struct {
	Session    primitives.SessionID
	WorktreeID primitives.WorktreeID
	Limit      int
}

type GraphSession struct {
	ID         primitives.SessionID
	Turns      []GraphTurn
	TotalTurns int
	Warnings   []string
}

type GraphTurn struct {
	WorktreeID primitives.WorktreeID
	StreamID   primitives.EventStreamID
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

type SearchQuery struct {
	Query      string
	Session    primitives.SessionID
	WorktreeID primitives.WorktreeID
	Limit      int
}

type SearchResult struct {
	SessionID  primitives.SessionID     `json:"session_id"`
	WorktreeID primitives.WorktreeID    `json:"worktree_id,omitempty"`
	StreamID   primitives.EventStreamID `json:"stream_id,omitempty"`
	TurnID     primitives.TurnID        `json:"turn_id"`
	First      time.Time                `json:"first,omitempty"`
	Last       time.Time                `json:"last,omitempty"`
	Adapter    string                   `json:"adapter,omitempty"`
	Prompt     string                   `json:"prompt,omitempty"`
	Assistant  string                   `json:"assistant,omitempty"`
	ToolNames  []string                 `json:"tool_names,omitempty"`
	Paths      []string                 `json:"paths,omitempty"`
	Snippet    string                   `json:"snippet,omitempty"`
	Rank       float64                  `json:"rank"`
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
