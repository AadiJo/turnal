package sharedhistory

import (
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	SchemaVersion          = 1
	AllowlistVersion       = "turnal-context-v1"
	ScannerVersion         = "turnal-secrets-v1"
	EvidencePublisherClaim = "publisher_attested_projection"
	DefaultFieldLimit      = 64 << 10
	DefaultBundleLimit     = 2 << 20
	MaxMaterializedLimit   = 8 << 20
	MaxBatchBytes          = 16 << 20
	MaxBundlesPerBatch     = 16
)

type PromptMode string

const (
	PromptModeRedactedText PromptMode = "redacted_text"
	PromptModeOmit         PromptMode = "omit"
)

type Direction string

const (
	DirectionPush Direction = "push"
	DirectionPull Direction = "pull"
)

type PreviewOptions struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	StreamID  primitives.EventStreamID
	Approve   bool
}

type Status struct {
	Configured       bool              `json:"configured"`
	Remote           string            `json:"remote,omitempty"`
	RepoID           primitives.RepoID `json:"repo_id,omitempty"`
	PromptMode       PromptMode        `json:"prompt_mode,omitempty"`
	PolicyHash       string            `json:"policy_hash,omitempty"`
	Approved         bool              `json:"approved"`
	DeviceID         string            `json:"device_id,omitempty"`
	Pending          int               `json:"pending"`
	Blocked          map[string]string `json:"blocked,omitempty"`
	Published        int               `json:"published"`
	Pulled           int               `json:"pulled"`
	LastSeen         map[string]string `json:"last_seen,omitempty"`
	UnpushedLocalTip bool              `json:"unpushed_local_tip"`
	RemoteError      string            `json:"remote_error,omitempty"`
}

type Plan struct {
	Locator          string         `json:"locator"`
	PolicyHash       string         `json:"policy_hash"`
	ApprovalRequired bool           `json:"approval_required"`
	Bytes            int            `json:"bytes"`
	Manifest         Manifest       `json:"manifest"`
	Events           []ContextEvent `json:"events"`
}

type Result struct {
	Direction Direction `json:"direction"`
	Published int       `json:"published,omitempty"`
	Pulled    int       `json:"pulled,omitempty"`
	Blocked   int       `json:"blocked,omitempty"`
	Head      string    `json:"head,omitempty"`
}

type SourceRef struct {
	StreamID primitives.EventStreamID `json:"stream_id"`
	Seq      primitives.EventSeq      `json:"seq"`
	Hash     primitives.EventHash     `json:"hash"`
}

type SourceLink struct {
	CommitSHA  string `json:"commit_sha,omitempty"`
	Checkpoint string `json:"checkpoint_id,omitempty"`
}

type SequenceRange struct {
	First primitives.EventSeq `json:"first"`
	Last  primitives.EventSeq `json:"last"`
}

type Manifest struct {
	SchemaVersion  int                        `json:"schema_version"`
	BundleID       primitives.BundleID        `json:"bundle_id"`
	RepoID         primitives.RepoID          `json:"repo_id"`
	DeviceID       string                     `json:"device_id"`
	ProducerID     primitives.EventProducerID `json:"producer_id"`
	StoreID        primitives.StoreID         `json:"store_id"`
	WorktreeID     primitives.WorktreeID      `json:"worktree_id"`
	StreamID       primitives.EventStreamID   `json:"stream_id"`
	SessionID      primitives.SessionID       `json:"session_id"`
	TurnID         primitives.TurnID          `json:"turn_id"`
	SourceSequence SequenceRange              `json:"source_sequence_range"`
	SourceRefs     []SourceRef                `json:"source_ref"`
	PolicyHash     string                     `json:"policy_hash"`
	PromptMode     PromptMode                 `json:"prompt_mode"`
	EvidenceClass  string                     `json:"evidence_class"`
	SourceLinks    []SourceLink               `json:"source_links,omitempty"`
	Omissions      map[string]int             `json:"omissions"`
	Truncations    Truncations                `json:"truncations"`
	ContentHashes  map[string]string          `json:"content_hashes"`
	CreatedAt      time.Time                  `json:"created_at"`
	Signature      string                     `json:"signature"`
}

type Truncations struct {
	Count         int `json:"count"`
	OriginalBytes int `json:"original_bytes"`
}

// ContextEvent is intentionally a closed projection. It has no field capable
// of carrying a patch, snapshot, raw provider payload, command input, or tool
// output. Each event sets exactly one typed payload below.
type ContextEvent struct {
	SchemaVersion int                     `json:"schema_version"`
	Type          primitives.EventType    `json:"type"`
	Seq           primitives.EventSeq     `json:"seq"`
	Time          primitives.Timestamp    `json:"time"`
	Adapter       primitives.AdapterName  `json:"adapter,omitempty"`
	Source        SourceRef               `json:"source_ref"`
	Lifecycle     *LifecycleProjection    `json:"lifecycle,omitempty"`
	Prompt        *PromptProjection       `json:"prompt,omitempty"`
	Intent        *IntentProjection       `json:"intent,omitempty"`
	Assistant     *TextProjection         `json:"assistant,omitempty"`
	Tool          *ToolProjection         `json:"tool,omitempty"`
	Checkpoint    *CheckpointProjection   `json:"checkpoint,omitempty"`
	CaptureError  *CaptureErrorProjection `json:"capture_error,omitempty"`
}

type LifecycleProjection struct {
	State string `json:"state"`
}

type PromptProjection struct {
	Text      string `json:"text,omitempty"`
	Omitted   bool   `json:"omitted,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Bytes     int    `json:"original_bytes,omitempty"`
}

type TextProjection struct {
	Text      string `json:"text"`
	Redacted  bool   `json:"redacted,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Bytes     int    `json:"original_bytes,omitempty"`
}

type IntentProjection struct {
	Problem   TextProjection `json:"problem"`
	Scope     []string       `json:"scope,omitempty"`
	Evidence  []string       `json:"evidence,omitempty"`
	Redacted  bool           `json:"redacted,omitempty"`
	AgentType string         `json:"agent_type,omitempty"`
}

type ToolProjection struct {
	Name              string `json:"name"`
	Category          string `json:"category"`
	Status            string `json:"status"`
	MutationCandidate bool   `json:"mutation_candidate,omitempty"`
}

type CheckpointProjection struct {
	Phase        string `json:"phase"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`
	Dirty        bool   `json:"dirty"`
}

type CaptureErrorProjection struct {
	Kind string `json:"kind"`
}

type Batch struct {
	SchemaVersion int           `json:"schema_version"`
	DeviceID      string        `json:"device_id"`
	PublicKey     string        `json:"public_key"`
	PreviousHead  string        `json:"previous_head,omitempty"`
	Bundles       []BatchBundle `json:"bundles"`
	CreatedAt     time.Time     `json:"created_at"`
	Signature     string        `json:"signature"`
}

type BatchBundle struct {
	BundleID  primitives.BundleID  `json:"bundle_id"`
	Path      string               `json:"path"`
	RepoID    primitives.RepoID    `json:"repo_id"`
	SessionID primitives.SessionID `json:"session_id"`
	TurnID    primitives.TurnID    `json:"turn_id"`
	Sequence  SequenceRange        `json:"sequence_range"`
}

type StoredBundle struct {
	Manifest  Manifest       `json:"manifest"`
	Events    []ContextEvent `json:"events"`
	PublicKey string         `json:"public_key"`
}

type policyFile struct {
	Version          int               `json:"version"`
	Remote           string            `json:"remote"`
	RepoID           primitives.RepoID `json:"repo_id"`
	PromptMode       PromptMode        `json:"prompt_mode"`
	AllowlistVersion string            `json:"allowlist_version"`
	ScannerVersion   string            `json:"scanner_version"`
	FieldLimit       int               `json:"field_limit"`
	BundleLimit      int               `json:"bundle_limit"`
	ApprovedHash     string            `json:"approved_hash,omitempty"`
}

type stateFile struct {
	Version   int               `json:"version"`
	Remote    string            `json:"remote,omitempty"`
	RepoID    primitives.RepoID `json:"repo_id,omitempty"`
	Committed map[string]string `json:"committed,omitempty"`
	Published map[string]string `json:"published,omitempty"`
	Blocked   map[string]string `json:"blocked,omitempty"`
	LastSeen  map[string]string `json:"last_seen,omitempty"`
}

type unsignedManifest Manifest
type unsignedBatch Batch
