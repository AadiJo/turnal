package viewer

import (
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestResourceKeysRoundTripAndRejectTampering(t *testing.T) {
	repo := newViewerTestRepo(t)
	codec := newKeyCodec(repo)
	sessionID, err := primitives.ParseSessionID("viewer-test")
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := primitives.NewTurnID(7)
	if err != nil {
		t.Fatal(err)
	}
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := codec.encode(resourceTurn, repo.WorktreeID, streamID, sessionID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := codec.decode(key, resourceTurn)
	if err != nil {
		t.Fatal(err)
	}
	if identity.SessionID != sessionID.String() || identity.TurnID != 7 || identity.StreamID != streamID.String() {
		t.Fatalf("decoded identity = %#v", identity)
	}

	replacement := "A"
	if key[1] == 'A' {
		replacement = "B"
	}
	tampered := key[:1] + replacement + key[2:]
	if tampered == key {
		t.Fatal("failed to construct a tampered key")
	}
	if _, err := codec.decode(tampered, resourceTurn); err == nil {
		t.Fatal("tampered key was accepted")
	}
	if _, err := codec.decode(key, resourceSession); err == nil {
		t.Fatal("turn key was accepted as a session key")
	}

	other := newViewerTestRepo(t)
	if _, err := newKeyCodec(other).decode(key, resourceTurn); err == nil {
		t.Fatal("resource key was accepted by a different store")
	}
}

func newViewerTestRepo(t *testing.T) *checkpoint.Repo {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
