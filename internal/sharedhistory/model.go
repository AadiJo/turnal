package sharedhistory

import (
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	SchemaVersion = 1
	// AllowlistVersion changes whenever the set of publishable fields widens.
	// v2 added the source branch, which leaves the machine only after the
	// publisher approves the resulting policy hash again.
	AllowlistVersion = "turnal-context-v2"
	// v3 replaces the fixed regex list with the detector pipeline. Any detector
	// or threshold change must bump this version and require fresh approval.
	ScannerVersion         = "turnal-secrets-v3"
	EvidencePublisherClaim = "publisher_attested_projection"
	DefaultFieldLimit      = 64 << 10
	DefaultBundleLimit     = 2 << 20
	MaxMaterializedLimit   = 8 << 20
	MaxBatchBytes          = 16 << 20
	MaxBundlesPerBatch     = 16
	DefaultNetworkTimeout  = 2 * time.Minute
)

type PromptMode string

const (
	PromptModeRedactedText PromptMode = "redacted_text"
	PromptModeOmit         PromptMode = "omit"
	PromptModeMetadataOnly PromptMode = "metadata_only"
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
	Enabled          bool              `json:"enabled"`
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
	Quarantined      map[string]string `json:"quarantined,omitempty"`
	Retired          map[string]string `json:"retired,omitempty"`
	RemoteChecked    bool              `json:"remote_checked"`
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
	Direction   Direction         `json:"direction"`
	Published   int               `json:"published,omitempty"`
	Pulled      int               `json:"pulled,omitempty"`
	Blocked     int               `json:"blocked,omitempty"`
	Remaining   int               `json:"remaining,omitempty"`
	Head        string            `json:"head,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	Quarantined map[string]string `json:"quarantined,omitempty"`
}

type PendingBundle struct {
	Locator   string                   `json:"locator"`
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    primitives.TurnID        `json:"turn_id"`
	StreamID  primitives.EventStreamID `json:"stream_id"`
	Bytes     int                      `json:"bytes,omitempty"`
	Blocked   string                   `json:"blocked,omitempty"`
	Queued    bool                     `json:"queued,omitempty"`
}

type PushPlan struct {
	PolicyHash        string          `json:"policy_hash"`
	ApprovalRequired  bool            `json:"approval_required"`
	MigrationRequired bool            `json:"migration_required"`
	Pending           []PendingBundle `json:"pending"`
	Publishable       int             `json:"publishable"`
	Queued            int             `json:"queued"`
	Blocked           int             `json:"blocked"`
	BatchSize         int             `json:"batch_size"`
	Remaining         int             `json:"remaining"`
}

type BundleSummary struct {
	Locator      string                   `json:"locator"`
	SessionID    primitives.SessionID     `json:"session_id"`
	TurnID       primitives.TurnID        `json:"turn_id"`
	StreamID     primitives.EventStreamID `json:"stream_id"`
	DeviceID     string                   `json:"device_id"`
	CreatedAt    time.Time                `json:"created_at"`
	PromptMode   PromptMode               `json:"prompt_mode"`
	EventCount   int                      `json:"event_count"`
	SourceCommit string                   `json:"source_commit,omitempty"`
	Branch       string                   `json:"branch,omitempty"`
	Local        bool                     `json:"local"`
	Error        string                   `json:"error,omitempty"`
}

type ListOptions struct {
	SessionID primitives.SessionID
	DeviceID  string
	// CommitSHA matches a bundle whose source links reference this commit. A
	// prefix is accepted so it can be used with abbreviated Git SHAs.
	CommitSHA string
}

type SourceRef struct {
	StreamID primitives.EventStreamID `json:"stream_id"`
	Seq      primitives.EventSeq      `json:"seq"`
	Hash     primitives.EventHash     `json:"hash"`
}

// SourceLink connects a bundle to the source history it describes. Branch is a
// short ref name such as "main"; it is omitted for detached HEAD and under
// metadata_only, which publishes no source naming.
type SourceLink struct {
	CommitSHA  string `json:"commit_sha,omitempty"`
	Checkpoint string `json:"checkpoint_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
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
	// The policy hash is opaque to a receiver, so these name the projection
	// that produced the bundle. They let a reader tell which allowlist and
	// secret scanner applied without asking the publisher to re-derive it.
	AllowlistVersion string            `json:"allowlist_version,omitempty"`
	ScannerVersion   string            `json:"scanner_version,omitempty"`
	ProducerVersion  string            `json:"producer_version,omitempty"`
	SourceLinks      []SourceLink      `json:"source_links,omitempty"`
	Omissions        map[string]int    `json:"omissions"`
	Redactions       map[string]int    `json:"redactions,omitempty"`
	Truncations      Truncations       `json:"truncations"`
	ContentHashes    map[string]string `json:"content_hashes"`
	CreatedAt        time.Time         `json:"created_at"`
	Signature        string            `json:"signature"`
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
	Disabled         bool              `json:"disabled,omitempty"`
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
	Version     int               `json:"version"`
	Remote      string            `json:"remote,omitempty"`
	RepoID      primitives.RepoID `json:"repo_id,omitempty"`
	Committed   map[string]string `json:"committed,omitempty"`
	Published   map[string]string `json:"published,omitempty"`
	Blocked     map[string]string `json:"blocked,omitempty"`
	LastSeen    map[string]string `json:"last_seen,omitempty"`
	Quarantined map[string]string `json:"quarantined,omitempty"`
	Retired     map[string]string `json:"retired,omitempty"`
}

type unsignedManifest Manifest
type unsignedBatch Batch
