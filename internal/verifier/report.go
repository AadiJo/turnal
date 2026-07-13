package verifier

import "time"

const (
	SchemaVersion      = 1
	DefaultOutputLimit = 1 << 20
)

type TargetKind string

const (
	TargetLiveWorkspace TargetKind = "live_workspace"
	TargetCheckpoint    TargetKind = "checkpoint"
)

type Status string

const (
	StatusPassed      Status = "passed"
	StatusFailed      Status = "failed"
	StatusTimedOut    Status = "timed_out"
	StatusLaunchError Status = "launch_error"
)

type Target struct {
	Kind          TargetKind `json:"kind"`
	Display       string     `json:"display"`
	WorkspaceRoot string     `json:"workspace_root,omitempty"`
	WorktreeID    string     `json:"worktree_id,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	Turn          uint64     `json:"turn,omitempty"`
	Phase         string     `json:"phase,omitempty"`
	CheckpointRef string     `json:"checkpoint_ref,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	Mutable       bool       `json:"mutable"`
	Reproducible  bool       `json:"reproducible"`
	Environment   string     `json:"environment"`
	Limitations   []string   `json:"limitations"`
}

type Check struct {
	Name                 string                `json:"name"`
	Command              string                `json:"command"`
	Args                 []string              `json:"args"`
	Status               Status                `json:"status"`
	StartedAt            time.Time             `json:"started_at"`
	FinishedAt           time.Time             `json:"finished_at"`
	DurationMS           int64                 `json:"duration_ms"`
	Timeout              string                `json:"timeout"`
	TimedOut             bool                  `json:"timed_out"`
	ExitCode             *int                  `json:"exit_code,omitempty"`
	LaunchError          string                `json:"launch_error,omitempty"`
	InfrastructureErrors []InfrastructureError `json:"infrastructure_errors,omitempty"`
	Stdout               string                `json:"stdout"`
	Stderr               string                `json:"stderr"`
	StdoutTruncated      bool                  `json:"stdout_truncated"`
	StderrTruncated      bool                  `json:"stderr_truncated"`
}

type InfrastructureError struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type Summary struct {
	Outcome              string `json:"outcome"`
	Total                int    `json:"total"`
	Passed               int    `json:"passed"`
	Failed               int    `json:"failed"`
	TimedOut             int    `json:"timed_out"`
	LaunchError          int    `json:"launch_error"`
	InfrastructureErrors int    `json:"infrastructure_errors"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Target        Target    `json:"target"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	DurationMS    int64     `json:"duration_ms"`
	Checks        []Check   `json:"checks"`
	Summary       Summary   `json:"summary"`
}

func (report Report) Successful() bool {
	return report.Summary.Outcome == "passed"
}
