package blame

import (
	"errors"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

var (
	ErrNoHistory    = errors.New("no completed turn history")
	ErrInvalidLine  = errors.New("line number must be greater than zero")
	ErrLineNotFound = errors.New("line not found")
	ErrFileNotFound = errors.New("file not found in completed turn history")
	ErrBinaryFile   = errors.New("binary file cannot be blamed")
)

type Query struct {
	Path          primitives.RepoPath
	Line          int
	SessionID     primitives.SessionID
	WorktreeID    primitives.WorktreeID
	StreamID      primitives.EventStreamID
	ThroughTurnID primitives.TurnID
}

type Result struct {
	Path          primitives.RepoPath      `json:"path"`
	LatestRef     primitives.CheckpointRef `json:"latest_ref"`
	LatestCommit  primitives.CommitSHA     `json:"latest_commit"`
	LatestTime    time.Time                `json:"latest_time"`
	Sessions      []SessionSummary         `json:"sessions,omitempty"`
	Entries       []Entry                  `json:"entries"`
	Warnings      []string                 `json:"warnings,omitempty"`
	CompleteTurns int                      `json:"complete_turns"`
	// Notes are reviewer comments about this file. They are joined to the result
	// by their own anchor rather than attached to a line's origin: the turn that
	// last changed a line is not necessarily the turn a reviewer commented on.
	Notes []FileNote `json:"notes,omitempty"`
}

// FileNote is one reviewer comment that names this file, carrying the anchor
// state observed against the blamed checkpoint.
type FileNote struct {
	Note notes.Note `json:"note"`
	// Line is the anchored start line, or zero for a file-scoped note.
	Line int `json:"line,omitempty"`
	// LineEnd is the inclusive end of an anchored range, or zero.
	LineEnd int `json:"line_end,omitempty"`
	// Drift reports whether the anchored text still matches. An unchecked drift
	// is reported as unknown rather than as agreement.
	Drift notes.AnchorDrift `json:"drift"`
}

type SessionSummary struct {
	ID        primitives.SessionID `json:"id"`
	Adapter   string               `json:"adapter,omitempty"`
	StartedAt time.Time            `json:"started_at,omitempty"`
}

type Entry struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Origin Origin `json:"origin"`
}

type Origin struct {
	Kind            string                   `json:"kind"`
	SessionID       primitives.SessionID     `json:"session_id,omitempty"`
	TurnID          primitives.TurnID        `json:"turn_id,omitempty"`
	CheckpointRef   primitives.CheckpointRef `json:"checkpoint_ref,omitempty"`
	Commit          primitives.CommitSHA     `json:"commit,omitempty"`
	Time            time.Time                `json:"time,omitempty"`
	Adapter         string                   `json:"adapter,omitempty"`
	Prompt          string                   `json:"prompt,omitempty"`
	ToolNames       []string                 `json:"tool_names,omitempty"`
	ActionTool      string                   `json:"action_tool,omitempty"`
	ActionAgentID   string                   `json:"action_agent_id,omitempty"`
	ActionAgentType string                   `json:"action_agent_type,omitempty"`
	Intent          *provenance.Attribution  `json:"intent,omitempty"`
}

type Engine struct {
	Repo     *checkpoint.Repo
	ReadOnly bool
}

func New(repo *checkpoint.Repo) Engine {
	return Engine{Repo: repo}
}
