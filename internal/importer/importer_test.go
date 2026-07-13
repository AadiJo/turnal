package importer

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestRunRequiresRepoAdoptionAndImportsExactStreamsAndRefs(t *testing.T) {
	requireGit(t)
	source := initRepo(t)
	destination := initRepo(t)
	sessionID, _ := primitives.ParseSessionID("shared")
	turnID, _ := primitives.NewTurnID(1)

	writeBytes(t, filepath.Join(source.WorkspaceRoot.String(), "payload.bin"), []byte{'b', 'e', 'f', 'o', 'r', 'e', 0, '\r', '\n'})
	recorder := turnevents.Recorder{Log: source.EventLog(), Manager: turns.NewManager(source), Adapter: primitives.AdapterManual}
	started, err := recorder.Start(sessionID, turnID)
	if err != nil {
		t.Fatalf("start source turn: %v", err)
	}
	writeBytes(t, filepath.Join(source.WorkspaceRoot.String(), "payload.bin"), []byte{'a', 'f', 't', 'e', 'r', 0, '\r', '\n'})
	if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
		t.Fatalf("finish source turn: %v", err)
	}
	sourceStreams, err := eventlog.ListDurableStreams(source.MetadataDir)
	if err != nil || len(sourceStreams) != 1 {
		t.Fatalf("source streams = %#v, err=%v", sourceStreams, err)
	}
	sourceBytes, err := os.ReadFile(sourceStreams[0].Path)
	if err != nil {
		t.Fatalf("read source stream: %v", err)
	}

	if _, err := Run(destination, source.MetadataDir, Options{DryRun: true}); err == nil || !strings.Contains(err.Error(), "repo identity mismatch") {
		t.Fatalf("merge without adoption error = %v, want repo identity mismatch", err)
	}
	dryRun, err := Run(destination, source.MetadataDir, Options{DryRun: true, AdoptSourceAsCurrentRepo: true})
	if err != nil {
		t.Fatalf("dry-run merge: %v", err)
	}
	if !dryRun.Plan.DryRun || dryRun.Plan.Checkpoints != 2 || len(dryRun.Plan.Streams) != 1 || dryRun.Plan.Refs < 4 {
		t.Fatalf("unexpected dry-run plan: %#v", dryRun.Plan)
	}
	if _, err := os.Stat(eventlog.StreamPath(destination.MetadataDir, sourceStreams[0].SessionID, sourceStreams[0].StreamID)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote destination stream, stat err=%v", err)
	}

	// Import must not inherit provider-supplied Git routing variables.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "wrong"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(t.TempDir(), "wrong"))
	result, err := Run(destination, source.MetadataDir, Options{AdoptSourceAsCurrentRepo: true})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.Manifest == "" || result.Plan.AdoptedRepo != true {
		t.Fatalf("unexpected merge result: %#v", result)
	}
	destinationBytes, err := os.ReadFile(eventlog.StreamPath(destination.MetadataDir, sourceStreams[0].SessionID, sourceStreams[0].StreamID))
	if err != nil {
		t.Fatalf("read imported stream: %v", err)
	}
	if !bytes.Equal(destinationBytes, sourceBytes) {
		t.Fatal("imported stream bytes differ from source")
	}
	importedPrefix := "refs/agent-vcs/imports/" + source.StoreID.String()
	refs, err := destination.ListPrivateRefs(importedPrefix)
	if err != nil || len(refs) != dryRun.Plan.Refs {
		t.Fatalf("imported refs = %d, want %d, err=%v: %#v", len(refs), dryRun.Plan.Refs, err, refs)
	}

	duplicate, err := Run(destination, source.MetadataDir, Options{DryRun: true, AdoptSourceAsCurrentRepo: true})
	if err != nil {
		t.Fatalf("duplicate dry-run: %v", err)
	}
	if duplicate.Plan.Duplicates != 1 || duplicate.Plan.Streams[0].Status != "duplicate" {
		t.Fatalf("duplicate plan = %#v", duplicate.Plan)
	}
}

func TestListDurableStreamsRejectsMalformedImportedStreamBeforeMutation(t *testing.T) {
	requireGit(t)
	source := initRepo(t)
	destination := initRepo(t)
	sessionID, _ := primitives.ParseSessionID("broken")
	if _, err := source.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser}); err != nil {
		t.Fatalf("append source event: %v", err)
	}
	streams, err := eventlog.ListDurableStreams(source.MetadataDir)
	if err != nil || len(streams) != 1 {
		t.Fatalf("source streams: %#v, err=%v", streams, err)
	}
	file, err := os.OpenFile(streams[0].Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open source stream: %v", err)
	}
	_, _ = file.WriteString("not-json\n")
	_ = file.Close()

	if _, err := Run(destination, source.MetadataDir, Options{AdoptSourceAsCurrentRepo: true}); err == nil {
		t.Fatal("merge succeeded with malformed source stream")
	}
	refs, err := destination.ListPrivateRefs("refs/agent-vcs/imports")
	if err != nil {
		t.Fatalf("list destination refs: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("failed preflight mutated destination refs: %#v", refs)
	}
	if pending, err := Pending(destination); err != nil || len(pending) != 0 {
		t.Fatalf("failed preflight left pending import: %#v, err=%v", pending, err)
	}
}

func TestRunImportsManualCheckpointStreamAndMessage(t *testing.T) {
	requireGit(t)
	source := initRepo(t)
	destination := initRepo(t)
	writeBytes(t, filepath.Join(source.WorkspaceRoot.String(), "app.txt"), []byte("known good\n"))
	created, err := source.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	if _, err := manualcheckpoints.Append(source, created, "before refactor"); err != nil {
		t.Fatalf("append manual checkpoint: %v", err)
	}
	streams, err := eventlog.ListDurableStreams(source.MetadataDir)
	if err != nil || len(streams) != 1 || !streams[0].Workspace {
		t.Fatalf("workspace streams = %#v, err=%v", streams, err)
	}

	result, err := Run(destination, source.MetadataDir, Options{AdoptSourceAsCurrentRepo: true})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.Plan.Checkpoints != 1 || len(result.Plan.Streams) != 1 || !result.Plan.Streams[0].Workspace {
		t.Fatalf("merge plan = %#v", result.Plan)
	}
	saves, err := manualcheckpoints.Read(destination, true)
	if err != nil {
		t.Fatalf("read imported manual checkpoints: %v", err)
	}
	if len(saves) != 1 || saves[0].Message != "before refactor" || saves[0].Checkpoint.Commit != created.Commit {
		t.Fatalf("imported saves = %#v", saves)
	}
	if _, err := destination.CheckpointCommit(saves[0].Checkpoint.Ref); err != nil {
		t.Fatalf("imported manual checkpoint ref is unreadable: %v", err)
	}
}

func initRepo(t *testing.T) *checkpoint.Repo {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return repo
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}
