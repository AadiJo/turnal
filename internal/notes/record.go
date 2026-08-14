package notes

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

type RecordInput struct {
	Target Target
	Text   string
	// NoteID makes a retry idempotent. Record generates one when it is empty,
	// which means a caller that retries after a reported failure would otherwise
	// record a second copy of a note that is already durable. A caller that can
	// retry should generate the id once with primitives.NewNoteID and reuse it.
	NoteID primitives.NoteID
	// Path, LineStart, and LineEnd optionally anchor the note to a line range as
	// it existed at the target turn's post checkpoint.
	Path      primitives.RepoPath
	LineStart int
	LineEnd   int
	// AnchorCommit is the target turn's post checkpoint commit. Anchoring
	// requires it, because an anchor without a commit cannot be checked for drift.
	AnchorCommit primitives.CommitSHA
	Author       string
}

// Record appends one note about a recorded turn.
//
// The note is written to this worktree's own note stream. The target turn is
// carried in the payload, so a note can discuss a turn recorded by another
// worktree or published by a teammate.
func Record(repo *checkpoint.Repo, input RecordInput) (Note, error) {
	if repo == nil {
		return Note{}, fmt.Errorf("note requires checkpoint repo")
	}
	if err := validateTarget(input.Target); err != nil {
		return Note{}, err
	}
	if err := ValidateText(input.Text); err != nil {
		return Note{}, err
	}
	if err := validateAuthor(input.Author); err != nil {
		return Note{}, err
	}

	anchor, err := buildAnchor(repo, input)
	if err != nil {
		return Note{}, err
	}

	noteID := input.NoteID
	if noteID == "" {
		if noteID, err = primitives.NewNoteID(); err != nil {
			return Note{}, err
		}
	} else if noteID, err = primitives.ParseNoteID(noteID.String()); err != nil {
		return Note{}, err
	}

	text := input.Text
	author := input.Author
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return Note{}, err
	}
	// The secrets policy withholds the note body and everything derived from
	// workspace content. Path, line range, and anchor digest all describe the
	// working tree, so a policy that refuses to store prompt text must not keep
	// them either.
	redacted := !effective.Secrets.StorePrompts
	if redacted {
		text = primitives.SecretsRedactionText
		author = ""
		anchor = nil
	}

	payload, err := json.Marshal(CreatePayload{
		SchemaVersion: SchemaVersion,
		NoteID:        noteID,
		Target:        input.Target,
		Text:          text,
		Anchor:        anchor,
		Author:        author,
		Redacted:      redacted,
	})
	if err != nil {
		return Note{}, fmt.Errorf("marshal note: %w", err)
	}

	log, sessionID, err := authorLog(repo)
	if err != nil {
		return Note{}, err
	}
	event, appended, err := log.AppendOnce(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeNoteCreate,
		Adapter:   primitives.AdapterManual,
		SourceID:  createSourceID(noteID),
		Payload:   payload,
	})
	if err != nil {
		return Note{}, err
	}
	if !appended {
		// A retry of an already durable note. Report what is recorded rather than
		// what this attempt would have written; the two can differ if the
		// workspace secrets policy changed between attempts.
		existing, err := ParseCreatePayload(event.Payload)
		if err != nil {
			return Note{}, err
		}
		return noteFromCreate(event, existing), nil
	}
	return noteFromCreate(event, CreatePayload{
		SchemaVersion: SchemaVersion, NoteID: noteID, Target: input.Target,
		Text: text, Anchor: anchor, Author: author, Redacted: redacted,
	}), nil
}

// Delete tombstones a note this worktree authored.
//
// A note is hidden, not erased: the original note.create event remains in the
// append-only log, and a published copy remains recoverable from shared history.
// Only the authoring stream may tombstone its own note, so one reviewer cannot
// retract another reviewer's note by supplying its id.
func Delete(repo *checkpoint.Repo, noteID primitives.NoteID) error {
	if repo == nil {
		return fmt.Errorf("note removal requires checkpoint repo")
	}
	parsedNoteID, err := primitives.ParseNoteID(noteID.String())
	if err != nil {
		return err
	}
	log, sessionID, err := authorLog(repo)
	if err != nil {
		return err
	}
	streamID, err := primitives.DeriveEventStreamID(repo.EventProducerID, sessionID)
	if err != nil {
		return err
	}

	events, err := ReadEvents(repo)
	if err != nil {
		return err
	}
	var target Target
	found := false
	for _, event := range events {
		if event.Type != primitives.EventTypeNoteCreate {
			continue
		}
		payload, err := ParseCreatePayload(event.Payload)
		if err != nil || payload.NoteID != parsedNoteID {
			continue
		}
		if event.StreamID != streamID {
			return fmt.Errorf("note %s was authored by another stream; only its author can remove it", parsedNoteID)
		}
		target = payload.Target
		found = true
	}
	if !found {
		return fmt.Errorf("note %s does not exist in this Turnal store", parsedNoteID)
	}

	payload, err := json.Marshal(DeletePayload{
		SchemaVersion: SchemaVersion,
		NoteID:        parsedNoteID,
		Target:        target,
	})
	if err != nil {
		return fmt.Errorf("marshal note removal: %w", err)
	}
	_, _, err = log.AppendOnce(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeNoteDelete,
		Adapter:   primitives.AdapterManual,
		SourceID:  deleteSourceID(parsedNoteID),
		Payload:   payload,
	})
	return err
}

func buildAnchor(repo *checkpoint.Repo, input RecordInput) (*Anchor, error) {
	if input.Path == "" {
		if input.LineStart != 0 || input.LineEnd != 0 {
			return nil, fmt.Errorf("note line anchor requires a path")
		}
		return nil, nil
	}
	path, err := primitives.ParseRepoPath(input.Path.String())
	if err != nil {
		return nil, err
	}
	// ParseRepoPath rejects NUL but permits other control characters, and an
	// anchor path is rendered next to note text in blame, show, and note list. A
	// path carrying a terminal escape would inject it into those outputs, so it
	// is refused at the boundary rather than only escaped at each render site.
	if containsControl(path.String()) {
		return nil, fmt.Errorf("note anchor path must not contain control characters")
	}
	if input.LineStart == 0 && input.LineEnd == 0 {
		// A path without a line range is a file-scoped note. There is no line
		// text to digest, so there is nothing to drift.
		return &Anchor{Path: path}, nil
	}
	if input.LineStart <= 0 {
		return nil, fmt.Errorf("note anchor line must be greater than zero")
	}
	lineEnd := input.LineEnd
	if lineEnd == 0 {
		lineEnd = input.LineStart
	}
	if lineEnd < input.LineStart {
		return nil, fmt.Errorf("note anchor line range %d-%d is inverted", input.LineStart, lineEnd)
	}
	if input.AnchorCommit == "" {
		return nil, fmt.Errorf("note line anchor requires the target turn's post checkpoint commit")
	}
	commit, err := primitives.ParseCommitSHA(input.AnchorCommit.String())
	if err != nil {
		return nil, err
	}
	text, ok, err := anchoredText(repo, commit, path, input.LineStart, lineEnd)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("note anchor %s:%d-%d does not exist at commit %s", path, input.LineStart, lineEnd, commit)
	}
	return &Anchor{
		Path:      path,
		LineStart: input.LineStart,
		LineEnd:   lineEnd,
		Commit:    commit,
		LineSHA:   anchorLineSHA(path, input.LineStart, lineEnd, commit, text),
	}, nil
}

// anchoredText returns the exact anchored line range at one commit.
//
// Lines are split on "\n" and a trailing "\r" is preserved rather than
// normalized, so a CRLF file digests the bytes it actually stores. A file with
// no final newline still yields its last line.
func anchoredText(repo *checkpoint.Repo, commit primitives.CommitSHA, path primitives.RepoPath, lineStart, lineEnd int) (string, bool, error) {
	data, ok, err := repo.CommitFileBytesIfExists(commit, path.String())
	if err != nil || !ok {
		return "", false, err
	}
	if isBinary(data) {
		return "", false, fmt.Errorf("note anchor %s at %s is a binary file", path, commit)
	}
	content := string(data)
	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	if lineStart > len(lines) || lineEnd > len(lines) {
		return "", false, nil
	}
	return strings.Join(lines[lineStart-1:lineEnd], "\n"), true, nil
}

func isBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for _, value := range data[:limit] {
		if value == 0 {
			return true
		}
	}
	return false
}

// CheckAnchor reports whether an anchored note still describes the text it was
// written against. Turnal reports drift rather than searching for a new home for
// the note: guessing where a line moved would assert a fact it cannot verify.
func CheckAnchor(repo *checkpoint.Repo, note Note, commit primitives.CommitSHA) AnchorDrift {
	if note.Anchor == nil || note.Anchor.LineSHA == "" || commit == "" {
		return AnchorDrift{}
	}
	text, ok, err := anchoredText(repo, commit, note.Anchor.Path, note.Anchor.LineStart, note.Anchor.LineEnd)
	if err != nil {
		return AnchorDrift{Checked: true, Drifted: true, Reason: "anchored text could not be read"}
	}
	if !ok {
		return AnchorDrift{Checked: true, Drifted: true, Reason: "anchored lines no longer exist"}
	}
	if anchorLineSHA(note.Anchor.Path, note.Anchor.LineStart, note.Anchor.LineEnd, commit, text) == note.Anchor.LineSHA {
		return AnchorDrift{Checked: true}
	}
	return AnchorDrift{Checked: true, Drifted: true, Reason: "anchored text changed"}
}

func validateTarget(target Target) error {
	if _, err := primitives.ParseRepoID(target.RepoID.String()); err != nil {
		return fmt.Errorf("note target repository: %w", err)
	}
	if _, err := primitives.ParseEventStreamID(target.StreamID.String()); err != nil {
		return fmt.Errorf("note target stream: %w", err)
	}
	if _, err := primitives.ParseSessionID(target.SessionID.String()); err != nil {
		return fmt.Errorf("note target session: %w", err)
	}
	if _, err := primitives.NewTurnID(target.TurnID.Uint64()); err != nil {
		return fmt.Errorf("note target turn: %w", err)
	}
	if strings.ContainsAny(target.Locator, "\r\n\x00") {
		return fmt.Errorf("note target locator must not contain control characters")
	}
	return nil
}

func createSourceID(noteID primitives.NoteID) string {
	return "turnal:note:" + noteID.String() + ":create"
}

func deleteSourceID(noteID primitives.NoteID) string {
	return "turnal:note:" + noteID.String() + ":delete"
}

func ParseCreatePayload(data json.RawMessage) (CreatePayload, error) {
	var payload CreatePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return CreatePayload{}, fmt.Errorf("parse note payload: %w", err)
	}
	if payload.SchemaVersion != SchemaVersion {
		return CreatePayload{}, fmt.Errorf("unsupported note schema version %d", payload.SchemaVersion)
	}
	if _, err := primitives.ParseNoteID(payload.NoteID.String()); err != nil {
		return CreatePayload{}, err
	}
	if err := validateTarget(payload.Target); err != nil {
		return CreatePayload{}, err
	}
	if strings.TrimSpace(payload.Text) == "" {
		return CreatePayload{}, fmt.Errorf("note text is required")
	}
	return payload, nil
}

func ParseDeletePayload(data json.RawMessage) (DeletePayload, error) {
	var payload DeletePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return DeletePayload{}, fmt.Errorf("parse note removal payload: %w", err)
	}
	if payload.SchemaVersion != SchemaVersion {
		return DeletePayload{}, fmt.Errorf("unsupported note schema version %d", payload.SchemaVersion)
	}
	if _, err := primitives.ParseNoteID(payload.NoteID.String()); err != nil {
		return DeletePayload{}, err
	}
	return payload, nil
}

func noteFromCreate(event eventlog.Event, payload CreatePayload) Note {
	return Note{
		NoteID:    payload.NoteID,
		Target:    payload.Target,
		Text:      payload.Text,
		Anchor:    payload.Anchor,
		Author:    payload.Author,
		Redacted:  payload.Redacted,
		CreatedAt: event.Time,
		StreamID:  event.StreamID,
		Seq:       event.Seq,
	}
}
