package replay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestCheckoutMaterializesCheckpointWithoutMutatingSource(t *testing.T) {
	repo, sessionID := replayFixture(t)
	writeReplayFile(t, repo.WorkspaceRoot.String(), "app.txt", "working copy\n")
	writeReplayFile(t, repo.WorkspaceRoot.String(), "extra.txt", "source only\n")

	destination := filepath.Join(t.TempDir(), "checkout")
	result, err := New(repo).Checkout(sessionID.String()+":turn:1:pre", destination)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	if result.Current.Phase != primitives.CheckpointPhasePre.String() {
		t.Fatalf("checkout phase = %q, want pre", result.Current.Phase)
	}
	assertReplayFile(t, destination, "app.txt", "before\n")
	assertReplayFile(t, repo.WorkspaceRoot.String(), "app.txt", "working copy\n")
	assertReplayFile(t, repo.WorkspaceRoot.String(), "extra.txt", "source only\n")
	if _, err := os.Stat(filepath.Join(destination, ".turnal")); !os.IsNotExist(err) {
		t.Fatalf("checkout contains Turnal metadata or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, MarkerFileName())); err != nil {
		t.Fatalf("checkout marker missing: %v", err)
	}
}

func TestCheckoutRejectsSourceWorkspaceAndNestedPaths(t *testing.T) {
	repo, sessionID := replayFixture(t)
	manager := New(repo)

	for _, destination := range []string{
		repo.WorkspaceRoot.String(),
		filepath.Join(repo.WorkspaceRoot.String(), "nested"),
	} {
		_, err := manager.Checkout(sessionID.String()+":turn:1:pre", destination)
		if err == nil {
			t.Fatalf("Checkout(%s) succeeded", destination)
		}
		if !strings.Contains(err.Error(), "source workspace") {
			t.Fatalf("Checkout(%s) error = %v", destination, err)
		}
	}

	assertReplayFile(t, repo.WorkspaceRoot.String(), "app.txt", "after\n")
}

func TestCheckoutRejectsSymlinkIntoSourceWorkspace(t *testing.T) {
	repo, sessionID := replayFixture(t)
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(repo.WorkspaceRoot.String(), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	destination := filepath.Join(link, "nested")
	_, err := New(repo).Checkout(sessionID.String()+":turn:1:pre", destination)
	if err == nil {
		t.Fatalf("Checkout through source symlink succeeded")
	}
	if !strings.Contains(err.Error(), "source workspace") {
		t.Fatalf("Checkout error = %v", err)
	}
	assertReplayFile(t, repo.WorkspaceRoot.String(), "app.txt", "after\n")
}

func TestStopRefusesWorktreeWithReplacedMarker(t *testing.T) {
	repo, sessionID := replayFixture(t)
	destination := filepath.Join(t.TempDir(), "checkout")
	manager := New(repo)
	if _, err := manager.Checkout(sessionID.String()+":turn:1:pre", destination); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, MarkerFileName()), []byte(`{"id":"different"}`), 0o644); err != nil {
		t.Fatalf("replace marker: %v", err)
	}

	_, _, err := manager.Stop()
	if err == nil {
		t.Fatal("Stop with replaced marker succeeded")
	}
	if !strings.Contains(err.Error(), "belongs to session different") {
		t.Fatalf("Stop error = %v", err)
	}
	assertReplayFile(t, destination, "app.txt", "before\n")
}

func replayFixture(t *testing.T) (*checkpoint.Repo, primitives.SessionID) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID, err := primitives.ParseSessionID("replay-direct")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	turnID, err := primitives.NewTurnID(1)
	if err != nil {
		t.Fatalf("NewTurnID: %v", err)
	}
	writeReplayFile(t, root.String(), "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("CreateCheckpoint(pre): %v", err)
	}
	writeReplayFile(t, root.String(), "app.txt", "after\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("CreateCheckpoint(post): %v", err)
	}
	return repo, sessionID
}

func writeReplayFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertReplayFile(t *testing.T, root, relative, want string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
