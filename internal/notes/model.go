// Package notes records human commentary about recorded turns.
//
// A note is a reviewer's statement, not recorded evidence. It never changes the
// workspace, never participates in checkpoint or attempt identity, and is never
// treated as proof that a turn was right or wrong.
//
// Notes live in the author's own event stream rather than in the stream of the
// turn they discuss. That keeps one storage model for two cases that would
// otherwise diverge: a note about a turn this machine recorded, and a note about
// a turn a teammate published. It also keeps commentary out of the agent's
// lifecycle stream, where a late note would otherwise extend the recorded turn's
// apparent duration.
package notes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/primitives"
)

// SchemaVersion is the durable note payload version.
const SchemaVersion = 1

// MaxTextBytes bounds one note body. Notes are commentary, so a reviewer who
// needs more room should be writing a document, not a note.
const MaxTextBytes = 16 << 10

// MaxAuthorBytes bounds the self-asserted author label.
const MaxAuthorBytes = 256

// noteSessionText is the session under which every note stream is written. It
// is not an agent session; it names the author's own commentary stream.
const noteSessionText = "notes"

// Target is the canonical reference to the turn a note discusses.
//
// The target is carried explicitly in the payload rather than implied by the
// stream the note lives in, because the author's stream is never the target
// turn's stream. RepoID, StreamID, SessionID, and TurnID together identify the
// turn across machines. Locator records the shared-history bundle the author
// actually read, when the note came from published context rather than from
// local history.
type Target struct {
	RepoID    primitives.RepoID        `json:"repo_id"`
	StreamID  primitives.EventStreamID `json:"stream_id"`
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    primitives.TurnID        `json:"turn_id"`
	Locator   string                   `json:"locator,omitempty"`
}

func (target Target) key() string {
	return target.RepoID.String() + "\x00" + target.StreamID.String() + "\x00" +
		target.SessionID.String() + "\x00" + target.TurnID.String()
}

// Anchor optionally binds a note to a line range as it existed at the target
// turn's post checkpoint.
//
// LineSHA is a domain-separated digest of the anchored text, not a bare hash of
// the line. A bare hash of a single line is a membership oracle for predictable
// content, so the digest binds the path, the range, and the commit as well.
// Turnal records the anchor and reports drift; it never guesses where a line
// moved to.
type Anchor struct {
	Path      primitives.RepoPath  `json:"path"`
	LineStart int                  `json:"line_start"`
	LineEnd   int                  `json:"line_end"`
	Commit    primitives.CommitSHA `json:"commit"`
	LineSHA   string               `json:"line_sha"`
}

// CreatePayload is the durable note.create event body.
type CreatePayload struct {
	SchemaVersion int               `json:"schema_version"`
	NoteID        primitives.NoteID `json:"note_id"`
	Target        Target            `json:"target"`
	Text          string            `json:"text"`
	Anchor        *Anchor           `json:"anchor,omitempty"`
	// Author is self-asserted. It authenticates nothing on its own; only a
	// shared-history device signature attests to a publisher.
	Author string `json:"author,omitempty"`
	// Redacted records that the workspace secrets policy withheld the note body
	// and its anchor metadata at capture time.
	Redacted bool `json:"redacted,omitempty"`
}

// DeletePayload is the durable note.delete tombstone body.
//
// A tombstone hides a note from projections. It does not erase it: the original
// note.create event remains in the append-only log, and any published copy
// remains recoverable from the shared-history Git ref forever.
type DeletePayload struct {
	SchemaVersion int               `json:"schema_version"`
	NoteID        primitives.NoteID `json:"note_id"`
	Target        Target            `json:"target"`
}

// Note is the projected view of a surviving note.
type Note struct {
	NoteID    primitives.NoteID        `json:"note_id"`
	Target    Target                   `json:"target"`
	Text      string                   `json:"text"`
	Anchor    *Anchor                  `json:"anchor,omitempty"`
	Author    string                   `json:"author,omitempty"`
	Redacted  bool                     `json:"redacted,omitempty"`
	CreatedAt primitives.Timestamp     `json:"created_at"`
	StreamID  primitives.EventStreamID `json:"stream_id"`
	Seq       primitives.EventSeq      `json:"seq"`
}

// AnchorDrift reports whether an anchored note still describes the text it was
// written against.
type AnchorDrift struct {
	Checked bool   `json:"checked"`
	Drifted bool   `json:"drifted"`
	Reason  string `json:"reason,omitempty"`
}

func ValidateText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("note text is required")
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("note text must be valid UTF-8")
	}
	if len(text) > MaxTextBytes {
		return fmt.Errorf("note text must be at most %d bytes", MaxTextBytes)
	}
	return nil
}

// containsControl reports whether a value carries characters that a terminal
// would interpret rather than display.
func containsControl(value string) bool {
	for _, character := range value {
		if character == '\n' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return true
		}
	}
	return false
}

func validateAuthor(author string) error {
	if author == "" {
		return nil
	}
	if !utf8.ValidString(author) {
		return fmt.Errorf("note author must be valid UTF-8")
	}
	if len(author) > MaxAuthorBytes {
		return fmt.Errorf("note author must be at most %d bytes", MaxAuthorBytes)
	}
	if strings.ContainsAny(author, "\r\n\x00") {
		return fmt.Errorf("note author must not contain control characters")
	}
	return nil
}

// anchorLineSHA domain-separates the anchored text with the path, range, and
// commit so the digest cannot be reused as a bare content oracle.
func anchorLineSHA(path primitives.RepoPath, lineStart, lineEnd int, commit primitives.CommitSHA, text string) string {
	hash := sha256.New()
	for _, field := range []string{
		"turnal-note-anchor-v1",
		path.String(),
		fmt.Sprintf("%d-%d", lineStart, lineEnd),
		commit.String(),
		text,
	} {
		_, _ = hash.Write([]byte(field))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
