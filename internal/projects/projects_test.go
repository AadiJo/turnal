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

	feed, _, err := db.Activity(context.Background(), 10)
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
	lastActivity := time.Now().UTC().Add(-time.Minute)
	if err := db.Refresh(context.Background(), staticSummary(Summary{
		TurnCount: 3, Additions: 7, LastPrompt: "keep me", LastActivity: lastActivity,
		Sessions: []Activity{{SessionKey: "kept", SessionID: "session"}},
	})); err != nil {
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
	if list[0].HistoryState != "absent" || list[0].TurnCount != 3 || list[0].Additions != 7 || list[0].LastPrompt != "keep me" {
		t.Fatalf("missing store lost its last readable summary: %#v", list[0])
	}
	activity, _, err := db.Activity(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 1 || activity[0].SessionKey != "kept" {
		t.Fatalf("missing store lost its last readable activity: %#v", activity)
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

func TestRefreshKeepsLastGoodSummaryWhenStoreIsTemporarilyUnreadable(t *testing.T) {
	db := openIsolated(t)
	repo := registerStore(t, filepath.Join(t.TempDir(), "temporarily-unreadable"))
	lastActivity := time.Now().UTC().Add(-time.Minute)
	good := Summary{
		Branch: "main", HistoryState: "ready", TurnCount: 4, Additions: 9,
		LastActivity: lastActivity, LastPrompt: "keep this", LastAdapter: "codex",
		Sessions: []Activity{{SessionKey: "kept", SessionID: "session", Title: "keep this"}},
	}
	if err := db.Refresh(context.Background(), staticSummary(good)); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), func(context.Context, checkpoint.RegisteredStore) (Summary, error) {
		return Summary{}, os.ErrPermission
	}); err != nil {
		t.Fatal(err)
	}
	project, err := db.Project(context.Background(), repo.StoreID.String())
	if err != nil {
		t.Fatal(err)
	}
	if project.HistoryState != "attention" || project.TurnCount != 4 || project.Additions != 9 || project.LastPrompt != "keep this" {
		t.Fatalf("summary was erased after transient failure: %#v", project)
	}
	activity, _, err := db.Activity(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 1 || activity[0].SessionKey != "kept" {
		t.Fatalf("activity was erased after transient failure: %#v", activity)
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
	// Retained hooks and ordinary writable opens refresh identities. That must
	// not silently put a project back into the viewer after the user removed it.
	if _, err := checkpoint.Open(repo.WorkspaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	list, err = db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("automatic open re-added removed project: %#v", list)
	}
	// Re-adding is explicit and clears the durable hidden marker.
	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}
	if err := db.Refresh(context.Background(), staticSummary(Summary{})); err != nil {
		t.Fatal(err)
	}
	list, err = db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].StoreID != repo.StoreID.String() {
		t.Fatalf("explicit re-add projects = %#v", list)
	}
}

func TestDeregisterCascadesAfterReopeningIndex(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TURNAL_STATE_DIR", stateDir)
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	repo := registerStore(t, filepath.Join(t.TempDir(), "reopened"))
	if err := db.Refresh(context.Background(), staticSummary(Summary{
		Sessions: []Activity{{SessionKey: "session", SessionID: "session"}},
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Deregister(context.Background(), repo.StoreID.String()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"activity", "project_worktrees"} {
		var rows int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s retained %d orphaned rows after reopen", table, rows)
		}
	}
}

func TestActivityReportsWhenOlderSessionsAreOmitted(t *testing.T) {
	db := openIsolated(t)
	registerStore(t, filepath.Join(t.TempDir(), "activity-limit"))
	now := time.Now().UTC()
	if err := db.Refresh(context.Background(), staticSummary(Summary{Sessions: []Activity{
		{SessionKey: "one", SessionID: "one", FinishedAt: now},
		{SessionKey: "two", SessionID: "two", FinishedAt: now.Add(-time.Minute)},
		{SessionKey: "three", SessionID: "three", FinishedAt: now.Add(-2 * time.Minute)},
	}})); err != nil {
		t.Fatal(err)
	}
	activity, truncated, err := db.Activity(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 2 || !truncated {
		t.Fatalf("activity len=%d truncated=%v, want 2 and true", len(activity), truncated)
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

func TestDeregisterWaitsForInFlightRefresh(t *testing.T) {
	db := openIsolated(t)
	otherDB, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherDB.Close() })
	repo := registerStore(t, filepath.Join(t.TempDir(), "racing"))
	started := make(chan struct{})
	release := make(chan struct{})
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- db.Refresh(context.Background(), func(context.Context, checkpoint.RegisteredStore) (Summary, error) {
			close(started)
			<-release
			return Summary{TurnCount: 1}, nil
		})
	}()
	<-started

	deregisterDone := make(chan error, 1)
	go func() {
		deregisterDone <- otherDB.Deregister(context.Background(), repo.StoreID.String())
	}()
	select {
	case err := <-deregisterDone:
		t.Fatalf("Deregister returned before the in-flight refresh completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := <-deregisterDone; err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	list, err := db.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("removed project was resurrected by refresh: %#v", list)
	}
}

func TestPreferredRootSkipsDeletedLinkedWorktreeAndKeepsPrimary(t *testing.T) {
	base := t.TempDir()
	deleted := filepath.Join(base, "a-deleted-link")
	primary := filepath.Join(base, "z-primary")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	store := checkpoint.RegisteredStore{
		StorePath: filepath.Join(primary, ".turnal"),
		Worktrees: []checkpoint.RegisteredWorktree{
			{Root: deleted},
			{Root: primary, Primary: true},
		},
	}
	if got := store.PreferredRoot(); got != primary {
		t.Fatalf("PreferredRoot = %q, want live primary %q", got, primary)
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

func TestConcurrentOpenSerializesCorruptIndexRebuild(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	results := make(chan *DB, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			db, openErr := Open()
			if openErr != nil {
				errors <- openErr
				return
			}
			results <- db
		}()
	}
	close(start)
	var opened []*DB
	for range 2 {
		select {
		case openErr := <-errors:
			t.Fatalf("concurrent Open: %v", openErr)
		case db := <-results:
			opened = append(opened, db)
		}
	}
	for _, db := range opened {
		healthy, healthErr := db.healthy()
		if healthErr != nil || !healthy {
			t.Fatalf("opened index is unhealthy: healthy=%v err=%v", healthy, healthErr)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
