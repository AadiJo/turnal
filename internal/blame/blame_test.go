package blame

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	queryindex "agent-vcs-again/internal/index"
	"agent-vcs-again/internal/primitives"
)

func TestComputeBlameOverlappingEdits(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	writeBlameFile(t, root, "app.txt", "alpha\nbeta\ngamma\n")
	captureBlameTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\nbeta v1\ngamma\n", "first beta")
	captureBlameTurn(t, repo, root, sessionID, 2, "app.txt", "alpha\nbeta v2\ngamma\n", "second beta")

	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Text != "beta v2" {
		t.Fatalf("line text = %q, want beta v2", entry.Text)
	}
	if entry.Origin.Kind != "turn" || entry.Origin.SessionID != sessionID || entry.Origin.TurnID.Uint64() != 2 {
		t.Fatalf("origin = %#v, want demo turn 2", entry.Origin)
	}
	if entry.Origin.Prompt != "second beta" {
		t.Fatalf("prompt = %q, want second beta", entry.Origin.Prompt)
	}
}

func TestComputeBlameDeletedLineShiftsOlderOrigin(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	turn1 := blameTurnID(t, 1)
	turn2 := blameTurnID(t, 2)

	pre1, err := repo.CreateCheckpoint(sessionID, turn1, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("turn 1 pre: %v", err)
	}
	writeBlameFile(t, root, "notes.txt", "one\ntwo\nthree\n")
	post1, err := repo.CreateCheckpoint(sessionID, turn1, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("turn 1 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn1, "add notes")
	_ = pre1
	_ = post1

	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	writeBlameFile(t, root, "notes.txt", "one\nthree\n")
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn2, "delete two")

	path, _ := primitives.ParseRepoPath("notes.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	entry := result.Entries[0]
	if entry.Text != "three" {
		t.Fatalf("line 2 text = %q, want three", entry.Text)
	}
	if entry.Origin.Kind != "turn" || entry.Origin.TurnID.Uint64() != 1 {
		t.Fatalf("line 2 origin = %#v, want turn 1", entry.Origin)
	}
}

func TestComputeBlameSessionScopeUsesSessionEndpoint(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionA := blameSessionID(t, "session-a")
	sessionB := blameSessionID(t, "session-b")

	captureBlameTurn(t, repo, root, sessionA, 1, "shared.txt", "from a\n", "write from a")
	captureBlameTurn(t, repo, root, sessionB, 1, "shared.txt", "from b\n", "write from b")

	path, _ := primitives.ParseRepoPath("shared.txt")
	global, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("global Compute: %v", err)
	}
	if global.Entries[0].Text != "from b" || global.Entries[0].Origin.SessionID != sessionB {
		t.Fatalf("global blame = %#v, want session-b", global.Entries[0])
	}

	scoped, err := New(repo).Compute(Query{Path: path, Line: 1, SessionID: sessionA})
	if err != nil {
		t.Fatalf("scoped Compute: %v", err)
	}
	if scoped.Entries[0].Text != "from a" || scoped.Entries[0].Origin.SessionID != sessionA {
		t.Fatalf("scoped blame = %#v, want session-a endpoint", scoped.Entries[0])
	}
}

func TestComputeBlameFileDeletedAtLatest(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "gone.txt", "temporary\n", "add file")

	turn2 := blameTurnID(t, 2)
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}

	path, _ := primitives.ParseRepoPath("gone.txt")
	_, err := New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("Compute error = %v, want ErrFileNotFound", err)
	}
}

func TestComputeBlameMovedLineUsesCurrentDiffSemantics(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	writeBlameFile(t, root, "move.txt", "one\ntwo\nthree\n")
	captureBlameTurn(t, repo, root, sessionID, 1, "move.txt", "two\none\nthree\n", "swap first two")

	path, _ := primitives.ParseRepoPath("move.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	entry := result.Entries[0]
	if entry.Text != "one" {
		t.Fatalf("line 2 text = %q, want one", entry.Text)
	}
	if entry.Origin.Kind != "baseline" {
		t.Fatalf("moved unchanged line origin = %#v, want baseline until explicit move detection exists", entry.Origin)
	}
}

func TestComputeBlameRejectsBinaryFile(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	turnID := blameTurnID(t, 1)

	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeBlameBytes(t, root, "raw.bin", []byte{'a', 0, 'b'})
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	path, _ := primitives.ParseRepoPath("raw.bin")
	_, err := New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("Compute error = %v, want ErrBinaryFile", err)
	}
}

func TestComputeBlameLineNumberEdges(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "single.txt", "hello", "write single")

	path, _ := primitives.ParseRepoPath("single.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Compute line 1: %v", err)
	}
	if result.Entries[0].Text != "hello" {
		t.Fatalf("line 1 text = %q, want hello", result.Entries[0].Text)
	}

	_, err = New(repo).Compute(Query{Path: path, Line: 2})
	if !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("line 2 error = %v, want ErrLineNotFound", err)
	}

	if _, _, err := ParsePathLine("single.txt:0"); !errors.Is(err, ErrInvalidLine) {
		t.Fatalf("ParsePathLine line 0 error = %v, want ErrInvalidLine", err)
	}
}

func TestComputeBlameEmptyFileHasNoEntries(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "empty.txt", "", "write empty")

	path, _ := primitives.ParseRepoPath("empty.txt")
	result, err := New(repo).Compute(Query{Path: path})
	if err != nil {
		t.Fatalf("Compute all: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries len = %d, want 0", len(result.Entries))
	}

	_, err = New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("line 1 error = %v, want ErrLineNotFound", err)
	}
}

func TestComputeBlameUsesSQLiteCacheAndInvalidatesOnHistoryChange(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "cache.txt", "first\nsecond\n", "cache fill")

	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	path, _ := primitives.ParseRepoPath("cache.txt")
	first, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("initial Compute: %v", err)
	}
	if first.Entries[0].Text != "first" {
		t.Fatalf("initial line text = %q, want first", first.Entries[0].Text)
	}

	db, err := sql.Open("sqlite", queryindex.PathsForMetadata(repo.MetadataDir).DBPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blame_cache WHERE path = ?`, "cache.txt").Scan(&rows); err != nil {
		t.Fatalf("count blame cache: %v", err)
	}
	if rows != 3 {
		t.Fatalf("blame cache rows = %d, want sentinel + 2 lines", rows)
	}
	if _, err := db.Exec(`UPDATE blame_cache SET line_text = ?, origin_prompt = ? WHERE path = ? AND line_no = 1`, "from cache", "stale prompt", "cache.txt"); err != nil {
		t.Fatalf("mutate blame cache: %v", err)
	}

	cached, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("cached Compute: %v", err)
	}
	if cached.Entries[0].Text != "from cache" {
		t.Fatalf("cached line text = %q, want from cache", cached.Entries[0].Text)
	}
	if cached.Entries[0].Origin.Prompt != "cache fill" {
		t.Fatalf("cached origin prompt = %q, want current event summary", cached.Entries[0].Origin.Prompt)
	}

	captureBlameTurn(t, repo, root, sessionID, 2, "cache.txt", "fresh\nsecond\n", "new history")
	updated, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("updated Compute: %v", err)
	}
	if updated.Entries[0].Text != "fresh" {
		t.Fatalf("updated line text = %q, want fresh after history change", updated.Entries[0].Text)
	}
	if updated.Entries[0].Origin.TurnID.Uint64() != 2 {
		t.Fatalf("updated origin = %#v, want turn 2", updated.Entries[0].Origin)
	}
}

func TestParseAndApplyUnifiedHunks(t *testing.T) {
	patch := []byte(`diff --git a/app.txt b/app.txt
--- a/app.txt
+++ b/app.txt
@@ -1,2 +1,3 @@
-old one
-old two
+new one
+new two
+new three
`)
	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatalf("parseUnifiedHunks: %v", err)
	}
	if len(hunks) != 1 || hunks[0].OldStart != 1 || hunks[0].OldCount != 2 || hunks[0].NewStart != 1 || hunks[0].NewCount != 3 {
		t.Fatalf("hunks = %#v, want one 2-line replacement with 3 new lines", hunks)
	}

	origin := Origin{Kind: "turn"}
	applied, err := applyHunks([]Origin{{Kind: "baseline"}, {Kind: "baseline"}}, hunks, origin)
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("applied len = %d, want 3", len(applied))
	}
	for index, got := range applied {
		if got.Kind != "turn" {
			t.Fatalf("origin %d = %#v, want turn", index, got)
		}
	}
}

func TestApplyUnifiedHunksPreservesContextOrigins(t *testing.T) {
	patch := []byte(`diff --git a/app.txt b/app.txt
--- a/app.txt
+++ b/app.txt
@@ -1,3 +1,3 @@
 keep
-old
+new
 keep too
`)
	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatalf("parseUnifiedHunks: %v", err)
	}
	applied, err := applyHunks([]Origin{{Kind: "first"}, {Kind: "second"}, {Kind: "third"}}, hunks, Origin{Kind: "turn"})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	gotKinds := []string{applied[0].Kind, applied[1].Kind, applied[2].Kind}
	wantKinds := []string{"first", "turn", "third"}
	for index := range wantKinds {
		if gotKinds[index] != wantKinds[index] {
			t.Fatalf("origin kinds = %#v, want %#v", gotKinds, wantKinds)
		}
	}
}

func newBlameRepo(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo) {
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

func captureBlameTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, sessionID primitives.SessionID, turn uint64, path string, postContent string, prompt string) {
	t.Helper()
	turnID := blameTurnID(t, turn)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn %d pre: %v", turn, err)
	}
	writeBlameFile(t, root, path, postContent)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn %d post: %v", turn, err)
	}
	appendBlamePrompt(t, repo, sessionID, turnID, prompt)
}

func appendBlamePrompt(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, prompt string) {
	t.Helper()
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   mustBlameJSON(t, map[string]string{"text": prompt}),
	}); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
}

func mustBlameJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func blameSessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func blameTurnID(t *testing.T, value uint64) primitives.TurnID {
	t.Helper()
	turnID, err := primitives.NewTurnID(value)
	if err != nil {
		t.Fatalf("NewTurnID: %v", err)
	}
	return turnID
}

func writeBlameFile(t *testing.T, root primitives.WorkspaceRoot, relPath string, content string) {
	t.Helper()
	writeBlameBytes(t, root, relPath, []byte(content))
}

func writeBlameBytes(t *testing.T, root primitives.WorkspaceRoot, relPath string, content []byte) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}
