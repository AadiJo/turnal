package verifier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestPrepareCheckpointMaterializesPreAndPostInIsolation(t *testing.T) {
	repo, sessionID, turnID := recordedCheckpointFixture(t)
	writeVerifierFixtureFile(t, repo.WorkspaceRoot.String(), "app.txt", "live workspace\n")
	writeVerifierFixtureFile(t, repo.WorkspaceRoot.String(), "live-only.txt", "untouched\n")

	for _, test := range []struct {
		phase primitives.CheckpointPhase
		want  string
	}{
		{phase: primitives.CheckpointPhasePre, want: "before\n"},
		{phase: primitives.CheckpointPhasePost, want: "after\n"},
	} {
		t.Run(test.phase.String(), func(t *testing.T) {
			prepared, err := PrepareCheckpoint(repo, sessionID, turnID, test.phase)
			if err != nil {
				t.Fatalf("PrepareCheckpoint: %v", err)
			}
			ownedPath := prepared.ownedPath
			if prepared.Target.Kind != TargetCheckpoint || prepared.Target.Phase != test.phase.String() || prepared.Target.CheckpointRef == "" || prepared.Target.Commit == "" {
				t.Fatalf("target = %#v", prepared.Target)
			}
			assertVerifierFixtureFile(t, prepared.Root, "app.txt", test.want)
			for _, absent := range []string{".git", ".turnal", "ignored/cache.txt", ".env", "live-only.txt"} {
				if _, err := os.Lstat(filepath.Join(prepared.Root, filepath.FromSlash(absent))); !os.IsNotExist(err) {
					t.Fatalf("%s present in evaluation or stat failed: %v", absent, err)
				}
			}
			assertVerifierFixtureFile(t, repo.WorkspaceRoot.String(), "app.txt", "live workspace\n")
			assertVerifierFixtureFile(t, repo.WorkspaceRoot.String(), "live-only.txt", "untouched\n")
			if err := prepared.Cleanup(); err != nil {
				t.Fatalf("Cleanup: %v", err)
			}
			if _, err := os.Stat(ownedPath); !os.IsNotExist(err) {
				t.Fatalf("owned temp directory remains: %v", err)
			}
		})
	}
}

func TestPrepareCheckpointAppliesCurrentSecretPolicyToOlderCapture(t *testing.T) {
	repo, sessionID, _ := recordedCheckpointFixture(t)
	root := repo.WorkspaceRoot.String()
	writeVerifierFixtureFile(t, root, "historical-secret.txt", "captured before policy tightened\n")
	writeVerifierFixtureFile(t, root, "secret-dir/key.txt", "captured below directory before policy tightened\n")
	turnID, _ := primitives.NewTurnID(2)
	log := eventlog.OpenFor(repo.MetadataDir, root, repo.RepoID, repo.StoreID, repo.WorktreeID, repo.EventProducerID)
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start recorded turn: %v", err)
	}
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("finish recorded turn: %v", err)
	}

	writeVerifierFixtureFile(t, root, ".turnal/config.toml", `version = 1
[secrets]
snapshot_deny_globs = ["historical-secret.txt", "secret-dir"]
`)
	ref, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CheckpointRefFor: %v", err)
	}
	commit, err := repo.CheckpointCommit(ref)
	if err != nil {
		t.Fatalf("CheckpointCommit: %v", err)
	}
	exactRoot := filepath.Join(t.TempDir(), "exact-replay")
	if err := repo.MaterializeCommit(commit, exactRoot, checkpoint.MaterializeOptions{}); err != nil {
		t.Fatalf("exact MaterializeCommit: %v", err)
	}
	assertVerifierFixtureFile(t, exactRoot, "historical-secret.txt", "captured before policy tightened\n")
	assertVerifierFixtureFile(t, exactRoot, "secret-dir/key.txt", "captured below directory before policy tightened\n")

	prepared, err := PrepareCheckpoint(repo, sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("PrepareCheckpoint: %v", err)
	}
	defer func() {
		if err := prepared.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	}()
	for _, denied := range []string{"historical-secret.txt", "secret-dir/key.txt"} {
		if _, err := os.Lstat(filepath.Join(prepared.Root, filepath.FromSlash(denied))); !os.IsNotExist(err) {
			t.Fatalf("historical secret %s present in verifier evaluation: %v", denied, err)
		}
	}
	assertVerifierFixtureFile(t, prepared.Root, "app.txt", "after\n")
}

func TestPreparedCheckpointCleansAfterVerifierFailure(t *testing.T) {
	repo, sessionID, turnID := recordedCheckpointFixture(t)
	prepared, err := PrepareCheckpoint(repo, sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("PrepareCheckpoint: %v", err)
	}
	ownedPath := prepared.ownedPath
	report, err := Run(context.Background(), Request{
		Root: prepared.Root, Target: prepared.Target,
		Verifiers: []config.Verifier{helperVerifier("failure", "fail")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Successful() {
		t.Fatal("failing verifier reported success")
	}
	if err := prepared.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(ownedPath); !os.IsNotExist(err) {
		t.Fatalf("owned temp directory remains: %v", err)
	}
}

func TestPrepareCheckpointRejectsMissingAndMismatchedTargets(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		repo, sessionID, _ := recordedCheckpointFixture(t)
		missingTurn, _ := primitives.NewTurnID(2)
		_, err := PrepareCheckpoint(repo, sessionID, missingTurn, primitives.CheckpointPhasePre)
		if err == nil || !strings.Contains(err.Error(), "no events found") {
			t.Fatalf("PrepareCheckpoint error = %v", err)
		}
	})

	t.Run("mismatched ref", func(t *testing.T) {
		repo, sessionID, turnID := recordedCheckpointFixture(t)
		preRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
		if err != nil {
			t.Fatalf("CheckpointRefFor pre: %v", err)
		}
		postRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePost)
		if err != nil {
			t.Fatalf("CheckpointRefFor post: %v", err)
		}
		postCommit, err := repo.CheckpointCommit(postRef)
		if err != nil {
			t.Fatalf("CheckpointCommit post: %v", err)
		}
		command := exec.Command("git", "--git-dir="+repo.GitDir, "update-ref", preRef.String(), postCommit.String())
		command.Env = verifierCleanGitEnv(os.Environ())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("move pre ref: %v: %s", err, output)
		}
		_, err = PrepareCheckpoint(repo, sessionID, turnID, primitives.CheckpointPhasePre)
		if err == nil || !strings.Contains(err.Error(), "integrity failed") {
			t.Fatalf("PrepareCheckpoint error = %v", err)
		}
	})
}

func TestPrepareCheckpointRejectsCorruptMaterialization(t *testing.T) {
	repo, sessionID, turnID := recordedCheckpointFixture(t)
	ref, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CheckpointRefFor: %v", err)
	}
	commit, err := repo.CheckpointCommit(ref)
	if err != nil {
		t.Fatalf("CheckpointCommit: %v", err)
	}
	manifest := filepath.Join(repo.MetadataDir, "manifests", "modes", commit.String()+".json")
	if err := os.WriteFile(manifest, []byte(`{"version":1,"modes":{}}`), 0o600); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}
	_, err = PrepareCheckpoint(repo, sessionID, turnID, primitives.CheckpointPhasePre)
	if err == nil || !strings.Contains(err.Error(), "mode manifest hash") {
		t.Fatalf("PrepareCheckpoint error = %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(repo.TmpDir, evaluationDirName))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read verifier temp parent: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary evaluations remain after preparation failure: %#v", entries)
	}
}

func TestCleanupRefusesDirectoryWithoutMatchingOwnershipProof(t *testing.T) {
	parent := t.TempDir()
	path, err := os.MkdirTemp(parent, evaluationPrefix)
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	writeVerifierFixtureFile(t, path, "keep.txt", "keep\n")
	if err := cleanupOwnedEvaluation(parent, path, "unknown-token"); err == nil || !strings.Contains(err.Error(), "refuse verifier cleanup") {
		t.Fatalf("cleanupOwnedEvaluation error = %v", err)
	}
	assertVerifierFixtureFile(t, path, "keep.txt", "keep\n")
}

func TestLiveTargetUsesWorkspaceDirectly(t *testing.T) {
	repo, _, _ := recordedCheckpointFixture(t)
	prepared, err := LiveTarget(repo)
	if err != nil {
		t.Fatalf("LiveTarget: %v", err)
	}
	if prepared.Root != repo.WorkspaceRoot.String() || prepared.Target.Kind != TargetLiveWorkspace || !prepared.Target.Mutable || prepared.Target.Reproducible {
		t.Fatalf("prepared = %#v", prepared)
	}
	if err := prepared.Cleanup(); err != nil {
		t.Fatalf("live cleanup: %v", err)
	}
}

func recordedCheckpointFixture(t *testing.T) (*checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	workspaceRoot, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(workspaceRoot)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	writeVerifierFixtureFile(t, workspaceRoot.String(), ".gitignore", "ignored/\n")
	writeVerifierFixtureFile(t, workspaceRoot.String(), "ignored/cache.txt", "ignored\n")
	writeVerifierFixtureFile(t, workspaceRoot.String(), ".env", "SECRET=value\n")
	writeVerifierFixtureFile(t, workspaceRoot.String(), "app.txt", "before\n")
	sessionID, _ := primitives.ParseSessionID("verify-fixture")
	turnID, _ := primitives.NewTurnID(1)
	log := eventlog.OpenFor(repo.MetadataDir, workspaceRoot.String(), repo.RepoID, repo.StoreID, repo.WorktreeID, repo.EventProducerID)
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start recorded turn: %v", err)
	}
	writeVerifierFixtureFile(t, workspaceRoot.String(), "app.txt", "after\n")
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("finish recorded turn: %v", err)
	}
	return repo, sessionID, turnID
}

func writeVerifierFixtureFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertVerifierFixtureFile(t *testing.T, root, relative, want string) {
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

func verifierCleanGitEnv(environment []string) []string {
	cleaned := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(key, "GIT_") {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}
