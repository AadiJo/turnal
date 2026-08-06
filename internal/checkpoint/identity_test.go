package checkpoint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fsidentity"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestLinkedWorktreeDiscoversSharedStoreAndKeepsIndependentHistory(t *testing.T) {
	requireGit(t)
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())

	parent := t.TempDir()
	mainPath := filepath.Join(parent, "main")
	linkedPath := filepath.Join(parent, "linked")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("mkdir main: %v", err)
	}
	runUserGit(t, mainPath, "init")
	runUserGit(t, mainPath, "config", "user.email", "turnal@example.test")
	runUserGit(t, mainPath, "config", "user.name", "Turnal Test")
	if err := os.WriteFile(filepath.Join(mainPath, "app.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write app: %v", err)
	}
	runUserGit(t, mainPath, "add", "app.txt")
	runUserGit(t, mainPath, "commit", "-m", "initial")

	mainRoot, err := primitives.ParseWorkspaceRoot(mainPath)
	if err != nil {
		t.Fatalf("main root: %v", err)
	}
	mainRepo, err := Init(mainRoot)
	if err != nil {
		t.Fatalf("Init main: %v", err)
	}
	runUserGit(t, mainPath, "worktree", "add", "-b", "linked-test", linkedPath)

	// Hidden and discovery Git commands must ignore provider-inherited Git routing.
	t.Setenv("GIT_DIR", filepath.Join(parent, "wrong-git-dir"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(parent, "wrong-work-tree"))
	linkedRoot, err := primitives.ParseWorkspaceRoot(linkedPath)
	if err != nil {
		t.Fatalf("linked root: %v", err)
	}
	linkedRepo, err := Open(linkedRoot)
	if err != nil {
		t.Fatalf("Open linked through registry: %v", err)
	}

	if !fsidentity.Same(linkedRepo.MetadataDir, mainRepo.MetadataDir) || linkedRepo.StoreID != mainRepo.StoreID || linkedRepo.RepoID != mainRepo.RepoID {
		t.Fatalf("linked repo did not reuse store: main=%#v linked=%#v", mainRepo, linkedRepo)
	}
	if linkedRepo.WorktreeID == mainRepo.WorktreeID || linkedRepo.EventProducerID == mainRepo.EventProducerID {
		t.Fatalf("linked worktree identity was not independent: main=%#v linked=%#v", mainRepo.WorktreeIdentity(), linkedRepo.WorktreeIdentity())
	}
	if !linkedRepo.ScopedRefs || linkedRepo.PrimaryWorktree {
		t.Fatalf("linked repo scope = scoped:%v primary:%v, want scoped non-primary", linkedRepo.ScopedRefs, linkedRepo.PrimaryWorktree)
	}

	sessionID, _ := primitives.ParseSessionID("same-session")
	turnID, _ := primitives.NewTurnID(1)
	mainCheckpoint, err := mainRepo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("main checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linkedPath, "app.txt"), []byte("linked\n"), 0o644); err != nil {
		t.Fatalf("write linked app: %v", err)
	}
	linkedCheckpoint, err := linkedRepo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("linked checkpoint with same session/turn: %v", err)
	}
	if mainCheckpoint.Ref == linkedCheckpoint.Ref || mainCheckpoint.Commit == linkedCheckpoint.Commit {
		t.Fatalf("worktree checkpoints collided: main=%#v linked=%#v", mainCheckpoint, linkedCheckpoint)
	}
	if !strings.Contains(linkedCheckpoint.Ref.String(), "/by-worktree/"+linkedRepo.WorktreeID.String()+"/") {
		t.Fatalf("linked ref is not worktree scoped: %s", linkedCheckpoint.Ref)
	}

	for _, repo := range []*Repo{mainRepo, linkedRepo} {
		payload, _ := json.Marshal(map[string]string{"root": repo.WorkspaceRoot.String()})
		if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser, Payload: payload}); err != nil {
			t.Fatalf("append event for %s: %v", repo.WorktreeID, err)
		}
	}
	streams, err := eventlog.ListDurableStreams(mainRepo.MetadataDir)
	if err != nil {
		t.Fatalf("ListDurableStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("streams = %d, want 2: %#v", len(streams), streams)
	}
	bindings, err := mainRepo.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("worktree bindings = %d, want 2: %#v", len(bindings), bindings)
	}

	output := runUserGit(t, mainPath, "for-each-ref", "--format=%(refname)", "refs/agent-vcs")
	if strings.TrimSpace(output) != "" {
		t.Fatalf("user Git was mutated with Turnal refs:\n%s", output)
	}
}

func TestRekeyStorePreservesEventsAndCommits(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	if err := os.WriteFile(filepath.Join(root.String(), "bytes.bin"), []byte{'a', 0, '\r', '\n'}, 0o644); err != nil {
		t.Fatalf("write bytes: %v", err)
	}
	created, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser, Payload: json.RawMessage(`{"text":"before rekey"}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil || len(streams) != 1 {
		t.Fatalf("source streams = %#v, err=%v", streams, err)
	}
	beforeBytes, err := os.ReadFile(streams[0].Path)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	oldStoreID := repo.StoreID
	oldWorktreeID := repo.WorktreeID

	result, err := repo.RekeyStore()
	if err != nil {
		t.Fatalf("RekeyStore: %v", err)
	}
	if result.OldStoreID != oldStoreID || result.NewStoreID == oldStoreID || repo.StoreID != result.NewStoreID || repo.WorktreeID == oldWorktreeID {
		t.Fatalf("unexpected rekey result: result=%#v repo=%#v", result, repo)
	}
	afterBytes, err := os.ReadFile(streams[0].Path)
	if err != nil {
		t.Fatalf("read stream after rekey: %v", err)
	}
	if string(afterBytes) != string(beforeBytes) {
		t.Fatal("rekey changed durable event bytes")
	}
	if commit, err := repo.RefCommit(created.CanonicalRef.String()); err != nil || commit != created.Commit {
		t.Fatalf("canonical checkpoint changed after rekey: commit=%s err=%v", commit, err)
	}
}

func TestValidateReadOnlyWorktreeIdentityRejectsStaleGitBinding(t *testing.T) {
	root := t.TempDir()
	storedGit := filepath.Join(t.TempDir(), "stored.git")
	currentGit := filepath.Join(t.TempDir(), "current.git")
	for _, path := range []string{storedGit, currentGit} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	binding := WorktreeIdentity{
		Root:         root,
		GitTopLevel:  root,
		GitCommonDir: storedGit,
		GitDir:       storedGit,
		Primary:      true,
	}
	identity := &UserGitIdentity{
		TopLevel:     root,
		GitCommonDir: currentGit,
		GitDir:       currentGit,
		PrimaryRoot:  root,
	}

	err := validateReadOnlyWorktreeIdentity(binding, root, identity)
	if err == nil || !strings.Contains(err.Error(), "Git common directory") {
		t.Fatalf("validateReadOnlyWorktreeIdentity error = %v", err)
	}
}

func TestListRegisteredStoresInfersLegacyPrimaryWorktree(t *testing.T) {
	requireGit(t)
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root := workspaceRoot(t)
	runUserGit(t, root.String(), "init")
	repo, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}

	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	value, err := readRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	for storeIndex := range value.Stores {
		for worktreeID, worktree := range value.Stores[storeIndex].Worktrees {
			worktree.Primary = false
			value.Stores[storeIndex].Worktrees[worktreeID] = worktree
		}
	}
	if err := writeJSONAtomic(path, value, 0o600); err != nil {
		t.Fatal(err)
	}

	stores, err := ListRegisteredStores()
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || len(stores[0].Worktrees) != 1 || !stores[0].Worktrees[0].Primary {
		t.Fatalf("legacy registry primary was not inferred: %#v", stores)
	}
}

func TestRegisterStoreRemovesStaleWorktreeWithSameRoot(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}

	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	value, err := readRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	currentGitDir := value.Stores[0].Worktrees[repo.WorktreeID.String()].GitDir
	staleID, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	value.Stores[0].Worktrees[staleID.String()] = registryWorktree{
		Root: root.String(), LastSeen: "2026-08-05T01:00:00Z", Primary: true,
	}
	if err := writeJSONAtomic(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	stores, err := ListRegisteredStores()
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || len(stores[0].Worktrees) != 1 || stores[0].Worktrees[0].GitDir != currentGitDir {
		t.Fatalf("registered duplicate roots were not reconciled in memory: %#v", stores)
	}

	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}
	value, err = readRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	worktrees := value.Stores[0].Worktrees
	if len(worktrees) != 1 {
		t.Fatalf("registered worktrees = %#v, want only the current binding", worktrees)
	}
	if _, ok := worktrees[repo.WorktreeID.String()]; !ok {
		t.Fatalf("current worktree %s is not registered: %#v", repo.WorktreeID, worktrees)
	}
}

func runUserGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanTestGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func cleanTestGitEnv(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}
