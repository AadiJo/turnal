package projects

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

// openIsolated redirects machine-wide state so a test never touches the real
// registry or project index.
func openIsolated(t *testing.T) *DB {
	t.Helper()
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// registerStore creates a real store and puts it in the registry, which is what
// Refresh reconciles against.
func registerStore(t *testing.T, root string) *checkpoint.Repo {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := primitives.ParseWorkspaceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func staticSummary(summary Summary) Summarizer {
	return func(context.Context, checkpoint.RegisteredStore) (Summary, error) {
		return summary, nil
	}
}

func TestRefreshIndexesRegisteredStores(t *testing.T) {
	db := openIsolated(t)
	repo := registerStore(t, filepath.Join(t.TempDir(), "alpha"))

	activity := time.Now().UTC().Add(-2 * time.Minute)
	summarize := staticSummary(Summary{
		Branch: "main", IndexState: "healthy", HistoryState: "ready",
		SessionCount: 2, TurnCount: 7, Additions: 40, Deletions: 5,
		LastActivity: activity, LastPrompt: "Add the thing", LastAdapter: "codex",
		Sessions: []Activity{{
			SessionKey: "key-1", SessionID: "sess", Title: "Add the thing",
			Adapter: "codex", TurnCount: 7, Additions: 40, Deletions: 5, FinishedAt: activity,
		}},
	})
	if err := db.Refresh(context.Background(), summarize); err != nil {
		t.Fatal(err)
	}

	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("projects = %d, want 1", len(list))
	}
	project := list[0]
	if project.StoreID != repo.StoreID.String() {
		t.Fatalf("store id = %q, want %q", project.StoreID, repo.StoreID)
	}
	if project.Name != "alpha" {
		t.Fatalf("name = %q, want alpha", project.Name)
	}
	if !project.Present {
		t.Fatal("freshly registered store reported as absent")
	}
	if project.TurnCount != 7 || project.Additions != 40 {
		t.Fatalf("summary not stored: turns=%d additions=%d", project.TurnCount, project.Additions)
	}

	feed, err := db.Activity(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed) != 1 || feed[0].ProjectName != "alpha" || feed[0].Title != "Add the thing" {
		t.Fatalf("activity = %#v", feed)
	}
}

// A store whose directory disappeared must stay listed. Recorded history
// outlives the working tree, and dropping the row silently would look like the
// project was never recorded.
func TestRefreshKeepsStoresWhoseDirectoryIsGone(t *testing.T) {
	db := openIsolated(t)
	root := filepath.Join(t.TempDir(), "vanishing")
	repo := registerStore(t, root)
	if err := db.Refresh(context.Background(), staticSummary(Summary{TurnCount: 3})); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo.MetadataDir); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("projects = %d, want the absent project to remain listed", len(list))
	}
	if list[0].Present {
		t.Fatal("missing store directory was still reported present")
	}
}

// A failure summarizing one store must not blank the index.
func TestRefreshToleratesUnreadableStore(t *testing.T) {
	db := openIsolated(t)
	registerStore(t, filepath.Join(t.TempDir(), "broken"))
	failing := func(context.Context, checkpoint.RegisteredStore) (Summary, error) {
		return Summary{}, os.ErrPermission
	}
	if err := db.Refresh(context.Background(), failing); err != nil {
		t.Fatalf("refresh aborted on a single unreadable store: %v", err)
	}
	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].HistoryState != "attention" {
		t.Fatalf("projects = %#v, want one project flagged for attention", list)
	}
}

// Deregistering removes the project from the registry and index but leaves the
// store on disk, so the history can be recovered by re-adding it.
func TestDeregisterKeepsStoreOnDisk(t *testing.T) {
	db := openIsolated(t)
	root := filepath.Join(t.TempDir(), "leaving")
	repo := registerStore(t, root)
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	if err := db.Deregister(context.Background(), repo.StoreID.String()); err != nil {
		t.Fatal(err)
	}
	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("projects = %d, want 0 after deregistering", len(list))
	}
	if _, err := os.Stat(filepath.Join(repo.MetadataDir, "git")); err != nil {
		t.Fatalf("deregister removed the store from disk: %v", err)
	}
	stores, err := checkpoint.ListRegisteredStores()
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range stores {
		if store.StoreID == repo.StoreID {
			t.Fatal("store is still registered after deregistering")
		}
	}
}

// A store removed from the registry outside the viewer disappears on refresh.
func TestRefreshDropsDeregisteredStores(t *testing.T) {
	db := openIsolated(t)
	repo := registerStore(t, filepath.Join(t.TempDir(), "gone"))
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.DeregisterStore(repo.StoreID); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("projects = %d, want 0", len(list))
	}
}

// The index is derived state, so deleting the file must be safe.
func TestOpenRebuildsAfterDeletionAndCorruption(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	repo := registerStore(t, filepath.Join(t.TempDir(), "rebuilt"))

	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), staticSummary(Summary{TurnCount: 4})); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open()
	if err != nil {
		t.Fatalf("open after deleting the index: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Refresh(context.Background(), staticSummary(Summary{TurnCount: 4})); err != nil {
		t.Fatal(err)
	}
	list, err := reopened.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].StoreID != repo.StoreID.String() {
		t.Fatalf("index did not rebuild from the registry: %#v", list)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	// A corrupt file is discarded rather than reported as an error.
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open()
	if err != nil {
		t.Fatalf("open after corrupting the index: %v", err)
	}
	defer recovered.Close()
	if err := recovered.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatalf("refresh after corruption: %v", err)
	}
}
