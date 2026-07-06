package blame

import (
	"errors"
	"time"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

var (
	ErrNoHistory    = errors.New("no completed turn history")
	ErrInvalidLine  = errors.New("line number must be greater than zero")
	ErrLineNotFound = errors.New("line not found")
	ErrFileNotFound = errors.New("file not found in completed turn history")
	ErrBinaryFile   = errors.New("binary file cannot be blamed")
)

type Query struct {
	Path      primitives.RepoPath
	Line      int
	SessionID primitives.SessionID
}

type Result struct {
	Path          primitives.RepoPath      `json:"path"`
	LatestRef     primitives.CheckpointRef `json:"latest_ref"`
	LatestCommit  primitives.CommitSHA     `json:"latest_commit"`
	LatestTime    time.Time                `json:"latest_time"`
	Entries       []Entry                  `json:"entries"`
	Warnings      []string                 `json:"warnings,omitempty"`
	CompleteTurns int                      `json:"complete_turns"`
}

type Entry struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Origin Origin `json:"origin"`
}

type Origin struct {
	Kind          string                   `json:"kind"`
	SessionID     primitives.SessionID     `json:"session_id,omitempty"`
	TurnID        primitives.TurnID        `json:"turn_id,omitempty"`
	CheckpointRef primitives.CheckpointRef `json:"checkpoint_ref,omitempty"`
	Commit        primitives.CommitSHA     `json:"commit,omitempty"`
	Time          time.Time                `json:"time,omitempty"`
	Adapter       string                   `json:"adapter,omitempty"`
	Prompt        string                   `json:"prompt,omitempty"`
	ToolNames     []string                 `json:"tool_names,omitempty"`
}

type Engine struct {
	Repo *checkpoint.Repo
}

func New(repo *checkpoint.Repo) Engine {
	return Engine{Repo: repo}
}
