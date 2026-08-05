package viewer

import "time"

// ProjectView is one recorded project in the global index. Present is false
// when the store directory is gone; the project stays listed because its
// recorded history outlives the working tree.
type ProjectView struct {
	StoreID      string                `json:"store_id"`
	RepoID       string                `json:"repo_id,omitempty"`
	Name         string                `json:"name"`
	Root         string                `json:"root"`
	Branch       string                `json:"branch,omitempty"`
	Present      bool                  `json:"present"`
	IndexState   string                `json:"index_state,omitempty"`
	HistoryState string                `json:"history_state,omitempty"`
	SessionCount int                   `json:"session_count"`
	TurnCount    int                   `json:"turn_count"`
	Additions    int                   `json:"additions"`
	Deletions    int                   `json:"deletions"`
	LastActivity time.Time             `json:"last_activity,omitempty"`
	LastPrompt   string                `json:"last_prompt,omitempty"`
	LastAdapter  string                `json:"last_adapter,omitempty"`
	AddedAt      time.Time             `json:"added_at,omitempty"`
	Worktrees    []ProjectWorktreeView `json:"worktrees,omitempty"`
}

type ProjectWorktreeView struct {
	Root       string `json:"root"`
	GitDir     string `json:"git_dir,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

// ActivityView is one session in the cross-project feed. It carries the owning
// store so a click can route back into that project.
type ActivityView struct {
	StoreID     string    `json:"store_id"`
	ProjectName string    `json:"project_name"`
	SessionKey  string    `json:"session_key"`
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title,omitempty"`
	Adapter     string    `json:"adapter,omitempty"`
	Model       string    `json:"model,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Status      string    `json:"status,omitempty"`
	TurnCount   int       `json:"turn_count"`
	FileCount   int       `json:"file_count"`
	Additions   int       `json:"additions"`
	Deletions   int       `json:"deletions"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
}

type ActivityPageView struct {
	Items     []ActivityView `json:"items"`
	Truncated bool           `json:"truncated"`
}

// IndexView is the payload for the global landing page.
type IndexView struct {
	Projects        []ProjectView `json:"projects"`
	ReadOnly        bool          `json:"read_only"`
	NetworkSilent   bool          `json:"network_silent"`
	ViewerStartedAt time.Time     `json:"viewer_started_at"`
	CurrentStoreID  string        `json:"current_store_id,omitempty"`
}

// AddProjectRequest initializes recording in a directory. This mirrors the
// flags turnal init accepts.
type AddProjectRequest struct {
	Directory       string `json:"directory"`
	Agent           string `json:"agent,omitempty"`
	UpdateGitignore bool   `json:"update_gitignore"`
	GitSync         bool   `json:"git_sync"`
}

// AddProjectResult reports what the add flow changed on disk.
type AddProjectResult struct {
	StoreID          string   `json:"store_id"`
	Root             string   `json:"root"`
	StorePath        string   `json:"-"`
	Attached         bool     `json:"attached"`
	GitignoreUpdated bool     `json:"gitignore_updated"`
	Hooks            []string `json:"hooks,omitempty"`
	Warning          string   `json:"warning,omitempty"`
}

type WorkspaceView struct {
	Name            string    `json:"name"`
	Root            string    `json:"root"`
	RepoID          string    `json:"repo_id"`
	StoreID         string    `json:"store_id"`
	WorktreeID      string    `json:"worktree_id"`
	SessionCount    int       `json:"session_count"`
	TurnCount       int       `json:"turn_count"`
	IndexState      string    `json:"index_state"`
	HistoryState    string    `json:"history_state"`
	Problems        []string  `json:"problems,omitempty"`
	LastActivity    time.Time `json:"last_activity,omitempty"`
	ReadOnly        bool      `json:"read_only"`
	NetworkSilent   bool      `json:"network_silent"`
	ViewerStartedAt time.Time `json:"viewer_started_at"`
}

type SessionSummaryView struct {
	Key           string    `json:"key"`
	ID            string    `json:"id"`
	StreamID      string    `json:"stream_id"`
	WorktreeID    string    `json:"worktree_id,omitempty"`
	Adapter       string    `json:"adapter,omitempty"`
	Model         string    `json:"model,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	EventCount    int       `json:"event_count"`
	TurnCount     int       `json:"turn_count"`
	CompleteTurns int       `json:"complete_turns"`
	ErrorCount    int       `json:"error_count"`
	FileCount     int       `json:"file_count"`
	Additions     int       `json:"additions"`
	Deletions     int       `json:"deletions"`
	Status        string    `json:"status"`
	PromptPreview string    `json:"prompt_preview,omitempty"`
	runID         string
	captureKind   string
}

// ManualSaveView is a standalone folder snapshot, intentionally separate from
// agent sessions and turns.
type ManualSaveView struct {
	ID       string    `json:"id"`
	Message  string    `json:"message,omitempty"`
	Time     time.Time `json:"time,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

type TurnSummaryView struct {
	Key          string     `json:"key"`
	ID           uint64     `json:"id"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at,omitempty"`
	FinishedAt   time.Time  `json:"finished_at,omitempty"`
	Adapter      string     `json:"adapter,omitempty"`
	Prompt       string     `json:"prompt,omitempty"`
	Assistant    string     `json:"assistant,omitempty"`
	ToolNames    []string   `json:"tool_names,omitempty"`
	EventCount   int        `json:"event_count"`
	ErrorCount   int        `json:"error_count"`
	Files        []FileView `json:"files,omitempty"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	PreCommit    string     `json:"pre_commit,omitempty"`
	PostCommit   string     `json:"post_commit,omitempty"`
	Checkpointed bool       `json:"checkpointed"`
}

type FileView struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

type SessionTurnsView struct {
	Session SessionSummaryView `json:"session"`
	Turns   []TurnSummaryView  `json:"turns"`
}

type TurnDetailView struct {
	TurnSummaryView
	SessionID string            `json:"session_id"`
	StreamID  string            `json:"stream_id"`
	Events    []EventView       `json:"events"`
	Warnings  []string          `json:"warnings,omitempty"`
	Truncated bool              `json:"truncated"`
	Identity  map[string]string `json:"identity"`
}

type EventView struct {
	Sequence  uint64    `json:"sequence"`
	Type      string    `json:"type"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	Time      time.Time `json:"time"`
	Sensitive bool      `json:"sensitive"`
}

type DiffSummaryView struct {
	TurnKey     string     `json:"turn_key"`
	Files       []FileView `json:"files"`
	Additions   int        `json:"additions"`
	Deletions   int        `json:"deletions"`
	BinaryFiles int        `json:"binary_files"`
	PreCommit   string     `json:"pre_commit"`
	PostCommit  string     `json:"post_commit"`
	TruthSource string     `json:"truth_source"`
}

type FilePatchView struct {
	Path       string `json:"path"`
	Patch      string `json:"patch"`
	Truncated  bool   `json:"truncated"`
	ByteCount  int    `json:"byte_count"`
	LineCount  int    `json:"line_count"`
	LimitBytes int    `json:"limit_bytes"`
	LimitLines int    `json:"limit_lines"`
}

type BlameView struct {
	Path          string          `json:"path"`
	LatestCommit  string          `json:"latest_commit"`
	LatestTime    time.Time       `json:"latest_time"`
	CompleteTurns int             `json:"complete_turns"`
	Lines         []BlameLineView `json:"lines"`
	Warnings      []string        `json:"warnings,omitempty"`
	Truncated     bool            `json:"truncated"`
	TruthSource   string          `json:"truth_source"`
}

type BlameLineView struct {
	Line      int       `json:"line"`
	Text      string    `json:"text"`
	Kind      string    `json:"kind"`
	TurnKey   string    `json:"turn_key,omitempty"`
	TurnID    uint64    `json:"turn_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Adapter   string    `json:"adapter,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	ToolNames []string  `json:"tool_names,omitempty"`
	Time      time.Time `json:"time,omitempty"`
}
