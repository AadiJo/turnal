package notes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestRecordAndProjectNote(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\nbeta\n")

	resolved := resolveTurn(t, repo, sessionID, 1)
	if !resolved.Local || resolved.PostCommit == "" {
		t.Fatalf("resolved = %#v, want a local target with a post commit", resolved)
	}

	note, err := Record(repo, RecordInput{Target: resolved.Target, Text: "this broke auth"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if note.Target.SessionID != sessionID || note.Target.TurnID.Uint64() != 1 {
		t.Fatalf("note target = %#v, want demo turn 1", note.Target)
	}

	listed, err := ForTurn(repo, sessionID, resolved.Target.TurnID)
	if err != nil {
		t.Fatalf("ForTurn: %v", err)
	}
	if len(listed) != 1 || listed[0].Text != "this broke auth" {
		t.Fatalf("listed = %#v, want one note", listed)
	}
}

// A note must not land in the agent's own stream. Doing so would make a
// completed turn look like it was still running when the note was written.
func TestNoteDoesNotTouchTheAgentStream(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")

	before, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read agent stream: %v", err)
	}

	resolved := resolveTurn(t, repo, sessionID, 1)
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "late commentary"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	after, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("reread agent stream: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("agent stream grew from %d to %d events; notes must not enter the lifecycle stream", len(before), len(after))
	}

	events, err := ReadEvents(repo)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("note events len = %d, want 1", len(events))
	}
	if events[0].TurnID != nil {
		t.Fatalf("note event carries turn id %s; it must stay empty so recall does not treat the note stream as a turn source", events[0].TurnID)
	}
}

// A caller that retries after a reported failure must not record a second copy
// of a note that is already durable. Reusing the note id is what makes the
// retry idempotent; without it each attempt mints a fresh identity.
func TestRecordWithReusedNoteIDIsIdempotent(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	noteID, err := primitives.NewNoteID()
	if err != nil {
		t.Fatal(err)
	}
	input := RecordInput{Target: resolved.Target, Text: "recorded once", NoteID: noteID}
	first, err := Record(repo, input)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	// The caller saw a failure from a later step and retried the same command.
	second, err := Record(repo, input)
	if err != nil {
		t.Fatalf("Record retry: %v", err)
	}
	if second.NoteID != first.NoteID || second.Seq != first.Seq {
		t.Fatalf("retry recorded a different note: %#v, want %#v", second, first)
	}
	listed, err := ForTurn(repo, sessionID, resolved.Target.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("retry produced %d notes, want 1", len(listed))
	}
}

// The anchor path is rendered beside note text, and ParseRepoPath permits
// control characters other than NUL, so a terminal escape must be refused when
// the note is recorded rather than only escaped at each render site.
func TestRecordRejectsControlCharactersInAnchorPath(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	evil, err := primitives.ParseRepoPath("src/\x1b[31mevil.go")
	if err != nil {
		t.Fatalf("ParseRepoPath accepted the path under test: %v", err)
	}
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "x", Path: evil}); err == nil {
		t.Fatal("anchor path containing a terminal escape was accepted")
	}
}

func TestRecordRejectsUnknownTurn(t *testing.T) {
	_, repo := newNoteRepo(t)
	if _, err := ResolveLocalTurn(repo, noteSessionID(t, "demo"), noteTurnID(t, 9), ""); err == nil {
		t.Fatal("ResolveLocalTurn for an unrecorded turn succeeded, want error")
	}
}

func TestDeleteHidesOnlyItsOwnAuthorsNote(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	note, err := Record(repo, RecordInput{Target: resolved.Target, Text: "mine"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := Delete(repo, note.NoteID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	remaining, err := ForTurn(repo, sessionID, resolved.Target.TurnID)
	if err != nil {
		t.Fatalf("ForTurn: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %#v, want the note hidden", remaining)
	}

	// The create event survives: removal hides a note, it does not erase it.
	events, err := ReadEvents(repo)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	creates := 0
	for _, event := range events {
		if event.Type == primitives.EventTypeNoteCreate {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("note.create events = %d, want the original create preserved", creates)
	}

	if err := Delete(repo, note.NoteID); err != nil {
		t.Fatalf("Delete is not idempotent: %v", err)
	}
}

// A tombstone written by a different author's stream must not hide the note.
func TestForeignTombstoneDoesNotHideNote(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	note, err := Record(repo, RecordInput{Target: resolved.Target, Text: "mine"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	events, err := ReadEvents(repo)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	foreignStream, err := primitives.ParseEventStreamID("stream_ffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatalf("ParseEventStreamID: %v", err)
	}
	tombstone := eventlog.Event{
		Type:     primitives.EventTypeNoteDelete,
		StreamID: foreignStream,
		Payload:  mustJSON(t, DeletePayload{SchemaVersion: SchemaVersion, NoteID: note.NoteID, Target: resolved.Target}),
	}
	surviving := Project(append(events, tombstone), Query{})
	if len(surviving) != 1 {
		t.Fatalf("surviving = %#v, want the note to survive a foreign tombstone", surviving)
	}
}

func TestAnchorDriftIsReportedNotGuessed(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\nbeta\ngamma\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	note, err := Record(repo, RecordInput{
		Target: resolved.Target, Text: "beta is wrong",
		Path: path, LineStart: 2, AnchorCommit: resolved.PostCommit,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if note.Anchor == nil || note.Anchor.LineSHA == "" {
		t.Fatalf("anchor = %#v, want a digest", note.Anchor)
	}

	if drift := CheckAnchor(repo, note, resolved.PostCommit); drift.Drifted {
		t.Fatalf("drift = %#v, want no drift against the anchored commit", drift)
	}

	// A later turn rewrites the anchored line.
	captureTurn(t, repo, root, sessionID, 2, "app.txt", "alpha\nbeta rewritten\ngamma\n")
	later := resolveTurn(t, repo, sessionID, 2)
	drift := CheckAnchor(repo, note, later.PostCommit)
	if !drift.Checked || !drift.Drifted {
		t.Fatalf("drift = %#v, want drift reported after the line changed", drift)
	}

	// A moved line drifts rather than silently re-anchoring.
	captureTurn(t, repo, root, sessionID, 3, "app.txt", "inserted\nalpha\nbeta\ngamma\n")
	moved := resolveTurn(t, repo, sessionID, 3)
	if drift := CheckAnchor(repo, note, moved.PostCommit); !drift.Drifted {
		t.Fatalf("drift = %#v, want drift when the anchored line moved", drift)
	}
}

func TestAnchorRejectsMissingLineAndCommit(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)
	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}

	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "x", Path: path, LineStart: 99, AnchorCommit: resolved.PostCommit}); err == nil {
		t.Fatal("anchoring past the end of the file succeeded, want error")
	}
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "x", Path: path, LineStart: 1}); err == nil {
		t.Fatal("line anchor without a commit succeeded, want error")
	}
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "x", LineStart: 1}); err == nil {
		t.Fatal("line anchor without a path succeeded, want error")
	}
}

// The secrets policy must withhold the anchor and author too, not only the body:
// path, line range, and digest all describe workspace content.
func TestRedactionCoversAnchorAndAuthor(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\nbeta\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	writeConfig(t, repo, "[secrets]\nstore_prompts = false\n")

	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	note, err := Record(repo, RecordInput{
		Target: resolved.Target, Text: "secret detail", Author: "someone@example.com",
		Path: path, LineStart: 2, AnchorCommit: resolved.PostCommit,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !note.Redacted {
		t.Fatal("note was not marked redacted under store_prompts = false")
	}
	if note.Text != primitives.SecretsRedactionText {
		t.Fatalf("text = %q, want the redaction marker", note.Text)
	}
	if note.Anchor != nil {
		t.Fatalf("anchor = %#v, want anchor metadata withheld", note.Anchor)
	}
	if note.Author != "" {
		t.Fatalf("author = %q, want author withheld", note.Author)
	}

	events, err := ReadEvents(repo)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "secret detail") || strings.Contains(string(event.Payload), "example.com") {
			t.Fatalf("durable payload retained redacted material: %s", event.Payload)
		}
	}
}

func TestRecordValidatesTextAndAuthor(t *testing.T) {
	root, repo := newNoteRepo(t)
	sessionID := noteSessionID(t, "demo")
	captureTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\n")
	resolved := resolveTurn(t, repo, sessionID, 1)

	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "   "}); err == nil {
		t.Fatal("blank note text succeeded, want error")
	}
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: strings.Repeat("a", MaxTextBytes+1)}); err == nil {
		t.Fatal("oversize note text succeeded, want error")
	}
	if _, err := Record(repo, RecordInput{Target: resolved.Target, Text: "ok", Author: "bad\nauthor"}); err == nil {
		t.Fatal("author with a newline succeeded, want error")
	}
}

func newNoteRepo(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return root, repo
}

func captureTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, sessionID primitives.SessionID, turn uint64, path, content string) {
	t.Helper()
	turnID := noteTurnID(t, turn)
	gitSync := false
	manager := turns.NewManager(repo)
	manager.GitSyncEnabled = &gitSync
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: manager, Adapter: primitives.AdapterCodex}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("turn %d start: %v", turn, err)
	}
	writeWorkspaceFile(t, root, path, content)
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("turn %d finish: %v", turn, err)
	}
}

func resolveTurn(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turn uint64) Resolved {
	t.Helper()
	resolved, err := ResolveLocalTurn(repo, sessionID, noteTurnID(t, turn), "")
	if err != nil {
		t.Fatalf("ResolveLocalTurn turn %d: %v", turn, err)
	}
	return resolved
}

func writeWorkspaceFile(t *testing.T, root primitives.WorkspaceRoot, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root.String(), name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func writeConfig(t *testing.T, repo *checkpoint.Repo, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func noteSessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func noteTurnID(t *testing.T, value uint64) primitives.TurnID {
	t.Helper()
	turnID, err := primitives.NewTurnID(value)
	if err != nil {
		t.Fatalf("NewTurnID: %v", err)
	}
	return turnID
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
