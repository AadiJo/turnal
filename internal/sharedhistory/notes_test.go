package sharedhistory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
)

// Enabling note sharing must not disturb the turn-context policy hash, or every
// existing publisher would be forced to re-approve something they never enabled.
func TestEnablingNotesDoesNotChangeTurnContextApproval(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	before, err := New(repo).Status(context.Background())
	if err != nil || !before.Approved {
		t.Fatalf("turn context approval before = %#v, %v", before, err)
	}

	if _, err := ConfigureNotes(repo, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	after, err := New(repo).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.PolicyHash != before.PolicyHash {
		t.Fatalf("turn context policy hash changed from %s to %s", before.PolicyHash, after.PolicyHash)
	}
	if !after.Approved {
		t.Fatal("enabling note sharing revoked the existing turn context approval")
	}
}

// A note must never ride a turn bundle. A turn published before its note was
// written could not carry it, so including it would make the wire form depend on
// publication timing.
func TestTurnBundlesNeverCarryNotes(t *testing.T) {
	testRoot := t.TempDir()
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	recordTestNote(t, repo, sessionID, turnID, "this broke auth", "")

	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(testRoot, "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range plan.Events {
		if event.Type == primitives.EventTypeNoteCreate || event.Type == primitives.EventTypeNoteDelete {
			t.Fatalf("turn bundle carries note event %s", event.Type)
		}
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "this broke auth") {
		t.Fatalf("turn bundle projection leaked note text:\n%s", encoded)
	}
}

func TestNotePushAndPullRoundTrip(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)

	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	note := recordTestNote(t, publisher, sessionID, turnID, "the intent statement was wrong", "")

	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(publisher, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(publisher).PreviewNote(context.Background(), NotePreviewOptions{NoteID: note.NoteID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Note.Text == nil || plan.Note.Text.Text != "the intent statement was wrong" {
		t.Fatalf("note projection text = %#v", plan.Note.Text)
	}
	result, err := New(publisher).SyncNotes(context.Background(), DirectionPush)
	if err != nil {
		t.Fatalf("push notes: %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("published = %d, want 1", result.Published)
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(receiver, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	pull, err := New(receiver).SyncNotes(context.Background(), DirectionPull)
	if err != nil {
		t.Fatalf("pull notes: %v", err)
	}
	if pull.Pulled != 1 {
		t.Fatalf("pulled = %d, want 1", pull.Pulled)
	}
	listed, err := New(receiver).ListNotes(context.Background(), NoteListOptions{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	if listed[0].Text != "the intent statement was wrong" {
		t.Fatalf("pulled note text = %q", listed[0].Text)
	}
	if listed[0].SessionID != sessionID || listed[0].TurnID != turnID {
		t.Fatalf("pulled note target = %s:%s, want %s:%s", listed[0].SessionID, listed[0].TurnID, sessionID, turnID)
	}
}

// Create and delete are separate immutable publications. A removal published
// after its create must hide the note on the receiver without rewriting the
// already-published create.
func TestPublishedRemovalHidesNoteOnReceiver(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)

	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	note := recordTestNote(t, publisher, sessionID, turnID, "retracted later", "")
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(publisher, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).PreviewNote(context.Background(), NotePreviewOptions{NoteID: note.NoteID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).SyncNotes(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(receiver, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(receiver).SyncNotes(context.Background(), DirectionPull); err != nil {
		t.Fatal(err)
	}
	if listed, err := New(receiver).ListNotes(context.Background(), NoteListOptions{}); err != nil || len(listed) != 1 {
		t.Fatalf("receiver before removal = %#v, %v", listed, err)
	}

	// Hide it locally, then publish the removal.
	if err := notes.Delete(publisher, note.NoteID); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).SyncNotes(context.Background(), DirectionPush); err != nil {
		t.Fatalf("push removal: %v", err)
	}
	if _, err := New(receiver).SyncNotes(context.Background(), DirectionPull); err != nil {
		t.Fatalf("pull removal: %v", err)
	}
	listed, err := New(receiver).ListNotes(context.Background(), NoteListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("receiver still lists %d note(s) after a published removal", len(listed))
	}
}

// A metadata_only publisher must not be able to ship note text, even with a
// consistent signature. The receiver enforces the policy the manifest declares.
func TestReceiverRejectsNoteTextUnderMetadataOnly(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	manifest := NoteManifest{
		SchemaVersion: NotesSchemaVersion, PromptMode: PromptModeMetadataOnly,
		Operation: NoteOperationCreate, NoteID: testNoteID(t),
	}
	projection := NoteProjection{
		SchemaVersion: NotesSchemaVersion, Operation: NoteOperationCreate, NoteID: manifest.NoteID,
		Text: &TextProjection{Text: "should not be here"},
	}
	if err := validateNoteProjection(manifest, projection); err == nil {
		t.Fatal("metadata_only bundle carrying note text was accepted")
	}
	_ = repo
}

// A removal carries no commentary of its own. Text or an anchor on a tombstone
// would be a channel for publishing content under a removal's policy.
func TestRemovalBundleCarriesNoContent(t *testing.T) {
	manifest := NoteManifest{
		SchemaVersion: NotesSchemaVersion, PromptMode: PromptModeRedactedText,
		Operation: NoteOperationDelete, NoteID: testNoteID(t),
	}
	projection := NoteProjection{
		SchemaVersion: NotesSchemaVersion, Operation: NoteOperationDelete, NoteID: manifest.NoteID,
		Text: &TextProjection{Text: "smuggled"},
	}
	if err := validateNoteProjection(manifest, projection); err == nil {
		t.Fatal("note removal carrying text was accepted")
	}
}

// The reference is inside the signed payload, so retargeting a signed note at a
// different turn must fail verification.
func TestTamperingWithNoteReferenceFailsVerification(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	identity, err := loadOrCreateDevice(repo)
	if err != nil {
		t.Fatal(err)
	}
	bundleID, err := deriveNoteBundleID(repo.RepoID, testNoteID(t), NoteOperationCreate)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := primitives.ParseSessionID("shared-history-test")
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := primitives.NewTurnID(1)
	if err != nil {
		t.Fatal(err)
	}
	streamID, err := primitives.ParseEventStreamID("stream_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	reference := "v1:" + strings.Repeat("a", 32) + ":bundle_0123456789abcdef0123456789abcdef"
	manifest := NoteManifest{
		SchemaVersion: NotesSchemaVersion, BundleID: bundleID, RepoID: repo.RepoID, DeviceID: identity.DeviceID,
		Operation: NoteOperationCreate, NoteID: testNoteID(t),
		References: reference,
		Target: NoteTargetProjection{
			RepoID: repo.RepoID, StreamID: streamID, SessionID: sessionID, TurnID: turnID, Locator: reference,
		},
		PromptMode: PromptModeRedactedText, EvidenceClass: EvidencePublisherClaim,
	}
	signed, err := signNoteManifest(identity, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNoteManifest(identity.public, signed); err != nil {
		t.Fatalf("freshly signed note manifest failed verification: %v", err)
	}
	tampered := signed
	tampered.References = "v1:" + strings.Repeat("b", 32) + ":bundle_0123456789abcdef0123456789abcdef"
	if err := verifyNoteManifest(identity.public, tampered); err == nil {
		t.Fatal("retargeted note reference passed verification")
	}
}

// Create and delete must occupy different immutable paths, or a removal could
// never be published after its create.
func TestNoteBundleIDsDifferByOperation(t *testing.T) {
	repoID, err := primitives.ParseRepoID("repo_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	noteID := testNoteID(t)
	create, err := deriveNoteBundleID(repoID, noteID, NoteOperationCreate)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := deriveNoteBundleID(repoID, noteID, NoteOperationDelete)
	if err != nil {
		t.Fatal(err)
	}
	if create == remove {
		t.Fatal("create and delete share a bundle id, so a removal could never be published")
	}
	if noteBundlePath(create) == noteBundlePath(remove) {
		t.Fatal("create and delete share an immutable path")
	}
}

// The anchor digest binds file content, so it must stay local. Publishing it
// would let a receiver confirm guessed line contents by comparison.
func TestPublishedNoteOmitsAnchorDigestAndAuthor(t *testing.T) {
	testRoot := t.TempDir()
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	// The file has to exist before the turn is captured: an anchor is verified
	// against the turn's post checkpoint, not against the current workspace.
	if err := os.WriteFile(filepath.Join(repo.WorkspaceRoot.String(), "app.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionID, turnID := recordSharedHistoryTurn(t, repo)

	resolved, err := notes.ResolveLocalTurn(repo, sessionID, turnID, "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatal(err)
	}
	note, err := notes.Record(repo, notes.RecordInput{
		Target: resolved.Target, Text: "beta is wrong", Author: "someone@example.com",
		Path: path, LineStart: 2, AnchorCommit: resolved.PostCommit,
	})
	if err != nil {
		t.Fatalf("record anchored note: %v", err)
	}
	if note.Anchor == nil || note.Anchor.LineSHA == "" {
		t.Fatalf("local note has no anchor digest: %#v", note.Anchor)
	}

	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(testRoot, "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(repo, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).PreviewNote(context.Background(), NotePreviewOptions{NoteID: note.NoteID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), note.Anchor.LineSHA) {
		t.Fatalf("published projection leaked the anchor digest:\n%s", encoded)
	}
	if strings.Contains(string(encoded), "example.com") {
		t.Fatalf("published projection leaked the self-asserted author:\n%s", encoded)
	}
	if plan.Note.Anchor == nil || plan.Note.Anchor.Path != "app.txt" || plan.Note.Anchor.LineStart != 2 {
		t.Fatalf("published anchor = %#v, want the path and line only", plan.Note.Anchor)
	}
	if plan.Manifest.Omissions["note_anchor_digest"] == 0 || plan.Manifest.Omissions["note_author"] == 0 {
		t.Fatalf("omissions do not disclose the withheld fields: %#v", plan.Manifest.Omissions)
	}
}

// A note about a turn published by a teammate resolves back to their bundle, so
// the original author can find the reply from their own locator.
func TestNotesForLocatorFindsReplies(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)

	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	turnPlan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	note := recordTestNote(t, publisher, sessionID, turnID, "reviewed from the shared bundle", turnPlan.Locator)
	if _, err := ConfigureNotes(publisher, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).PreviewNote(context.Background(), NotePreviewOptions{NoteID: note.NoteID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).SyncNotes(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureNotes(receiver, NotesConfigureOptions{PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(receiver).SyncNotes(context.Background(), DirectionPull); err != nil {
		t.Fatal(err)
	}

	replies, err := New(receiver).NotesForLocator(context.Background(), turnPlan.Locator)
	if err != nil {
		t.Fatalf("NotesForLocator: %v", err)
	}
	if len(replies) != 1 || replies[0].Text != "reviewed from the shared bundle" {
		t.Fatalf("replies = %#v, want the note replying to %s", replies, turnPlan.Locator)
	}

	// A locator nobody replied to resolves to nothing rather than erroring.
	other := "v1:" + strings.Repeat("c", 32) + ":bundle_0123456789abcdef0123456789abcdef"
	if replies, err := New(receiver).NotesForLocator(context.Background(), other); err != nil || len(replies) != 0 {
		t.Fatalf("unreferenced locator = %#v, %v", replies, err)
	}
}

// A note whose body the local secrets policy withheld has nothing publishable.
func TestRedactedNotesAreNotPublishable(t *testing.T) {
	testRoot := t.TempDir()
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte("[secrets]\nstore_prompts = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	note := recordTestNote(t, repo, sessionID, turnID, "withheld locally", "")
	if !note.Redacted {
		t.Fatal("note was not redacted under store_prompts = false")
	}
	operations, err := listPublishableNotes(repo, repo.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Note.NoteID == note.NoteID {
			t.Fatal("a locally redacted note was offered for publication")
		}
	}
}

func recordTestNote(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, text, locator string) notes.Note {
	t.Helper()
	resolved, err := notes.ResolveLocalTurn(repo, sessionID, turnID, "")
	if err != nil {
		t.Fatalf("ResolveLocalTurn: %v", err)
	}
	resolved.Target.Locator = locator
	note, err := notes.Record(repo, notes.RecordInput{Target: resolved.Target, Text: text})
	if err != nil {
		t.Fatalf("Record note: %v", err)
	}
	return note
}

func testNoteID(t *testing.T) primitives.NoteID {
	t.Helper()
	noteID, err := primitives.ParseNoteID("note_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	return noteID
}
