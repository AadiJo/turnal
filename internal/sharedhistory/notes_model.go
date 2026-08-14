package sharedhistory

import (
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

// The notes channel is deliberately separate from the turn-context channel.
//
// Turn bundles are immutable and turn-shaped: a manifest names one session,
// turn, and source sequence range, and receivers reject anything else on that
// ref. Notes are neither. A note may discuss a turn this device never recorded,
// it has no lifecycle or checkpoint range of its own, and hiding one has to be a
// second immutable publication rather than a rewrite of the first.
//
// Publishing notes on the existing history ref would therefore force every
// publisher to re-approve a widened allowlist, and would make any receiver that
// predates notes quarantine the publisher for sending an event it cannot decode.
// A separate ref namespace and a separate policy avoid both: older receivers
// enumerate only refs/turnal/v1/history/, so they never see this channel, and a
// publisher who never shares notes keeps their existing approval untouched.
const (
	NotesSchemaVersion = 1
	// NotesAllowlistVersion changes whenever the set of publishable note fields
	// widens. It is approved independently of the turn-context allowlist.
	NotesAllowlistVersion = "turnal-notes-v1"
	NotesRefPrefix        = "refs/turnal/v1/notes/"
)

const (
	NoteOperationCreate = "create"
	NoteOperationDelete = "delete"
)

// NoteTargetProjection names the turn a published note discusses.
//
// It carries no local path or worktree identity: the target is meaningful to a
// receiver only as a turn coordinate plus, optionally, the bundle locator the
// author actually read.
type NoteTargetProjection struct {
	RepoID    primitives.RepoID        `json:"repo_id"`
	StreamID  primitives.EventStreamID `json:"stream_id"`
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    primitives.TurnID        `json:"turn_id"`
	// Locator is the turn-context bundle the author reviewed, when the note came
	// from published context rather than from local history. It is the reverse
	// reference a reader follows back to the turn.
	Locator string `json:"locator,omitempty"`
}

// NoteAnchorProjection publishes where a note was anchored, without publishing
// the anchored text or its digest.
//
// The local digest binds the anchored line content. Publishing it would let a
// receiver confirm guessed file contents by comparison, so the wire form keeps
// only the path and range and reports drift as a local question.
type NoteAnchorProjection struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

// NoteProjection is the closed publishable form of one note operation.
type NoteProjection struct {
	SchemaVersion int                  `json:"schema_version"`
	Operation     string               `json:"operation"`
	NoteID        primitives.NoteID    `json:"note_id"`
	Target        NoteTargetProjection `json:"target"`
	// Text is present only for a create operation under a text-publishing policy.
	Text   *TextProjection       `json:"text,omitempty"`
	Anchor *NoteAnchorProjection `json:"anchor,omitempty"`
	// Author is intentionally absent. A note's author label is self-asserted and
	// often a personal email address; the manifest's device signature is the only
	// attestation this channel can honestly make.
	CreatedAt time.Time `json:"created_at"`
}

// NoteManifest is the signed envelope for one published note operation.
type NoteManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	BundleID      primitives.BundleID `json:"bundle_id"`
	RepoID        primitives.RepoID   `json:"repo_id"`
	DeviceID      string              `json:"device_id"`
	Operation     string              `json:"operation"`
	NoteID        primitives.NoteID   `json:"note_id"`
	// References names the turn-context bundle this note replies to, when known.
	// It is inside the signed payload: leaving it unsigned would let anyone with
	// write access to the remote retarget a signed note at a different turn.
	References       string               `json:"references,omitempty"`
	Target           NoteTargetProjection `json:"target"`
	PolicyHash       string               `json:"policy_hash"`
	PromptMode       PromptMode           `json:"prompt_mode"`
	EvidenceClass    string               `json:"evidence_class"`
	AllowlistVersion string               `json:"allowlist_version,omitempty"`
	ScannerVersion   string               `json:"scanner_version,omitempty"`
	ProducerVersion  string               `json:"producer_version,omitempty"`
	Omissions        map[string]int       `json:"omissions,omitempty"`
	Redactions       map[string]int       `json:"redactions,omitempty"`
	Truncations      Truncations          `json:"truncations"`
	ContentHashes    map[string]string    `json:"content_hashes"`
	CreatedAt        time.Time            `json:"created_at"`
	Signature        string               `json:"signature"`
}

// StoredNoteBundle is the materialized form of one published note operation.
type StoredNoteBundle struct {
	Manifest  NoteManifest   `json:"manifest"`
	Note      NoteProjection `json:"note"`
	PublicKey string         `json:"public_key"`
}

// NoteBatch is one signed publication of note bundles.
type NoteBatch struct {
	SchemaVersion int               `json:"schema_version"`
	DeviceID      string            `json:"device_id"`
	PublicKey     string            `json:"public_key"`
	PreviousHead  string            `json:"previous_head,omitempty"`
	Bundles       []NoteBatchBundle `json:"bundles"`
	CreatedAt     time.Time         `json:"created_at"`
	Signature     string            `json:"signature"`
}

type NoteBatchBundle struct {
	BundleID  primitives.BundleID `json:"bundle_id"`
	Path      string              `json:"path"`
	RepoID    primitives.RepoID   `json:"repo_id"`
	NoteID    primitives.NoteID   `json:"note_id"`
	Operation string              `json:"operation"`
}

// NoteBundleSummary describes a local or pulled note bundle for listing.
type NoteBundleSummary struct {
	Locator    string               `json:"locator"`
	NoteID     primitives.NoteID    `json:"note_id"`
	Operation  string               `json:"operation"`
	DeviceID   string               `json:"device_id"`
	SessionID  primitives.SessionID `json:"session_id"`
	TurnID     primitives.TurnID    `json:"turn_id"`
	References string               `json:"references,omitempty"`
	Text       string               `json:"text,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	Local      bool                 `json:"local"`
	Error      string               `json:"error,omitempty"`
}

// NotesStatus reports note-channel consent and synchronization state.
type NotesStatus struct {
	Configured  bool              `json:"configured"`
	Enabled     bool              `json:"enabled"`
	Remote      string            `json:"remote,omitempty"`
	RepoID      primitives.RepoID `json:"repo_id,omitempty"`
	PromptMode  PromptMode        `json:"prompt_mode,omitempty"`
	PolicyHash  string            `json:"policy_hash,omitempty"`
	Approved    bool              `json:"approved"`
	DeviceID    string            `json:"device_id,omitempty"`
	Pending     int               `json:"pending"`
	Published   int               `json:"published"`
	Pulled      int               `json:"pulled"`
	Quarantined map[string]string `json:"quarantined,omitempty"`
}

type notesPolicyFile struct {
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

type notesStateFile struct {
	Version     int               `json:"version"`
	Remote      string            `json:"remote,omitempty"`
	RepoID      primitives.RepoID `json:"repo_id,omitempty"`
	Committed   map[string]string `json:"committed,omitempty"`
	Published   map[string]string `json:"published,omitempty"`
	Blocked     map[string]string `json:"blocked,omitempty"`
	LastSeen    map[string]string `json:"last_seen,omitempty"`
	Quarantined map[string]string `json:"quarantined,omitempty"`
}

type unsignedNoteManifest NoteManifest
type unsignedNoteBatch NoteBatch
