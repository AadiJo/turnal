package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
)

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func TestInitCreatesHiddenBareRepo(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	output, err := runGitNoRepo(root.String(), "--git-dir", repo.GitDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		t.Fatalf("verify bare repo: %v", err)
	}
	if strings.TrimSpace(output) != "true" {
		t.Fatalf("is-bare-repository = %q, want true", output)
	}
	output, err = runGitNoRepo(root.String(), "--git-dir", repo.GitDir, "config", "--bool", "--get", "core.longpaths")
	if err != nil {
		t.Fatalf("read core.longpaths: %v", err)
	}
	if strings.TrimSpace(output) != "true" {
		t.Fatalf("core.longpaths = %q, want true", output)
	}
}

func TestForCaptureRootSnapshotsIsolatedFilesIntoOriginStore(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "live.txt"), []byte("live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isolated := t.TempDir()
	if err := os.WriteFile(filepath.Join(isolated, "result.txt"), []byte("forked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(isolated, ".turnal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(isolated, ".turnal", "must-not-capture"), []byte("metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := repo.ForCaptureRoot(isolated)
	if err != nil {
		t.Fatalf("ForCaptureRoot: %v", err)
	}
	snapshot, err := view.CreateSnapshotRef("refs/agent-vcs/test/isolated", "isolated")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	entries, err := repo.ListCommitTree(snapshot.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "result.txt" {
		t.Fatalf("captured entries = %#v", entries)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("isolated capture changed source workspace: %v", err)
	}
	if _, err := repo.ForCaptureRoot(repo.MetadataDir); err == nil || !strings.Contains(err.Error(), "outside Turnal metadata") {
		t.Fatalf("metadata capture root error = %v", err)
	}
}

func TestOpenUpgradesLegacyMetadataPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	legacyDir := filepath.Join(repo.MetadataDir, "log", "raw", "legacy")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "payload.jsonl")
	if err := os.WriteFile(legacyFile, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(legacyDir, 0o755); err != nil {
		t.Fatalf("Chmod dir: %v", err)
	}
	if err := os.Chmod(legacyFile, 0o644); err != nil {
		t.Fatalf("Chmod file: %v", err)
	}
	if err := os.Remove(filepath.Join(repo.MetadataDir, permissionsVersionFileName)); err != nil {
		t.Fatalf("remove permission marker: %v", err)
	}

	if _, err := Open(root); err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := os.Stat(legacyDir)
	if err != nil {
		t.Fatalf("stat legacy dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("legacy dir mode = %v, want 0700", info.Mode().Perm())
	}
	info, err = os.Stat(legacyFile)
	if err != nil {
		t.Fatalf("stat legacy file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("legacy file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWorkspaceLockBlocksCheckpointMutation(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	lock, err := filelock.Acquire(repo.WorkspaceLockPath(), time.Second)
	if err != nil {
		t.Fatalf("create workspace lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	repo.LockTimeout = 20 * time.Millisecond

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	_, err = repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err == nil {
		t.Fatal("CreateCheckpoint succeeded while workspace lock was held")
	}
	if !strings.Contains(err.Error(), "workspace lock busy") {
		t.Fatalf("CreateCheckpoint error = %v, want workspace lock busy", err)
	}
}

func TestInstallCheckpointRefsAtomic(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "content\n")
	commit, err := repo.createSnapshotCommit("atomic ref test")
	if err != nil {
		t.Fatalf("createSnapshotCommit: %v", err)
	}
	checkpointID, _ := primitives.NewCheckpointID()
	canonicalRef, _ := primitives.NewCheckpointIDRef(checkpointID)
	friendlyRef, _ := primitives.NewManualCheckpointRef(repo.WorktreeID, checkpointID)
	if _, err := runHiddenGit(repo, "", "update-ref", canonicalRef.String(), commit.String()); err != nil {
		t.Fatalf("seed canonical ref: %v", err)
	}

	if err := repo.installCheckpointRefsAtomic(canonicalRef, friendlyRef, commit); err == nil {
		t.Fatal("atomic install succeeded despite canonical ref collision")
	}
	if _, err := repo.RefCommit(friendlyRef.String()); err == nil {
		t.Fatal("failed transaction installed the friendly ref")
	}
	if got, err := repo.RefCommit(canonicalRef.String()); err != nil || got != commit {
		t.Fatalf("failed transaction changed canonical ref: got=%s err=%v", got, err)
	}

	if _, err := runHiddenGit(repo, "", "update-ref", "-d", canonicalRef.String()); err != nil {
		t.Fatalf("remove seeded canonical ref: %v", err)
	}
	if err := repo.installCheckpointRefsAtomic(canonicalRef, friendlyRef, commit); err != nil {
		t.Fatalf("atomic install: %v", err)
	}
	for _, ref := range []primitives.CheckpointRef{canonicalRef, friendlyRef} {
		if got, err := repo.RefCommit(ref.String()); err != nil || got != commit {
			t.Fatalf("ref %s = %s, err=%v; want %s", ref, got, err, commit)
		}
	}
}

func TestWorkspaceLockExcludesConcurrentGoroutines(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- repo.WithWorkspaceLock("first", func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- repo.WithWorkspaceLock("second", func() error {
			close(secondEntered)
			return nil
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("second goroutine entered while the first held the workspace lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first workspace lock: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second workspace lock: %v", err)
	}
}

func TestWorkspaceLockTimesOutOnInProcessContention(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	repo.LockTimeout = 50 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- repo.WithWorkspaceLock("holder", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	started := time.Now()
	err = repo.WithWorkspaceLock("contender", func() error {
		t.Fatal("contender entered while workspace lock was held")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "workspace lock busy") {
		t.Fatalf("contender error = %v, want workspace lock busy", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("in-process lock timeout took %s, want bounded wait", elapsed)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder lock: %v", err)
	}
}

func TestCreateCheckpointSnapshotsWorktreeAndExcludesMetadata(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, "src/app.txt", "hello\n")
	writeFile(t, root, ".git/config", "user git metadata\n")
	writeFile(t, root, ".turnal/tmp/internal.txt", "tool metadata\n")
	writeFile(t, root, "nested/.git/config", "nested git metadata\n")
	writeFile(t, root, "nested/.TURNAL/tmp/internal.txt", "nested tool metadata\n")

	sessionID, _ := primitives.ParseSessionID("Demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":src/app.txt")
	if err != nil {
		t.Fatalf("show captured file: %v", err)
	}
	if content != "hello\n" {
		t.Fatalf("captured content = %q, want hello", content)
	}

	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":.git/config"); err == nil {
		t.Fatal(".git/config was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":.turnal/tmp/internal.txt"); err == nil {
		t.Fatal(".turnal metadata was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.git/config"); err == nil {
		t.Fatal("nested .git/config was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.TURNAL/tmp/internal.txt"); err == nil {
		t.Fatal("nested .TURNAL metadata was captured, want excluded")
	}
}

func TestCreateCheckpointHonorsGitignore(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, ".gitignore", "dist/\n*.log\n!keep.log\n")
	writeFile(t, root, "nested/.gitignore", "local.txt\n")
	writeFile(t, root, "src/app.txt", "hello\n")
	writeFile(t, root, "dist/bundle.js", "generated\n")
	writeFile(t, root, "debug.log", "ignored\n")
	writeFile(t, root, "keep.log", "kept\n")
	writeFile(t, root, "nested/local.txt", "ignored by nested gitignore\n")
	writeFile(t, root, "nested/keep.txt", "kept nested\n")

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	for _, path := range []string{".gitignore", "nested/.gitignore", "src/app.txt", "keep.log", "nested/keep.txt"} {
		if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+path); err != nil {
			t.Fatalf("%s was not captured: %v", path, err)
		}
	}
	for _, path := range []string{"dist/bundle.js", "debug.log", "nested/local.txt"} {
		if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+path); err == nil {
			t.Fatalf("%s was captured, want gitignored", path)
		}
	}
}

func TestCreateCheckpointHonorsSecretsSnapshotDenyGlobs(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, "src/app.txt", "hello\n")
	writeFile(t, root, ".env", "SECRET=root\n")
	writeFile(t, root, ".env.local", "SECRET=local\n")
	writeFile(t, root, "nested/.env", "SECRET=nested\n")
	writeFile(t, root, "config/credentials.json", `{"secret":true}`)

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":src/app.txt"); err != nil {
		t.Fatalf("app.txt missing from checkpoint: %v", err)
	}
	for _, denied := range []string{".env", ".env.local", "nested/.env", "config/credentials.json"} {
		if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+denied); err == nil {
			t.Fatalf("%s was captured, want denied by secrets snapshot policy", denied)
		}
	}
}

func TestCreateCheckpointBypassesGitFilters(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, ".gitattributes", "*.txt text eol=lf\n")
	writeFile(t, root, "crlf.txt", "hello\r\n")

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":crlf.txt")
	if err != nil {
		t.Fatalf("show crlf file: %v", err)
	}
	if content != "hello\r\n" {
		t.Fatalf("captured content = %q, want raw CRLF bytes", content)
	}
}

func TestCreateCheckpointStoresSymlinkWithoutFollowing(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, "target.txt", "target content\n")
	if err := os.Symlink("target.txt", filepath.Join(root.String(), "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	tree, err := runHiddenGit(repo, "", "ls-tree", checkpoint.Commit.String(), "link.txt")
	if err != nil {
		t.Fatalf("ls-tree symlink: %v", err)
	}
	if !strings.Contains(tree, "120000 blob") {
		t.Fatalf("symlink tree entry = %q, want mode 120000", tree)
	}

	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":link.txt")
	if err != nil {
		t.Fatalf("show symlink: %v", err)
	}
	if content != "target.txt" {
		t.Fatalf("symlink blob = %q, want link target", content)
	}
}

func TestSnapshotIndexReaderStreamsEntries(t *testing.T) {
	firstBlob, err := primitives.ParseGitObjectID(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	secondBlob, err := primitives.ParseGitObjectID(strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	reader := &snapshotIndexReader{entries: []snapshotIndexEntry{
		{mode: primitives.GitFileModeRegular, blob: firstBlob, path: snapshotPath("first.txt")},
		{mode: primitives.GitFileModeSymlink, blob: secondBlob, path: snapshotPath("line\nbreak")},
	}}

	var output bytes.Buffer
	buffer := make([]byte, 7)
	for {
		count, readErr := reader.Read(buffer)
		output.Write(buffer[:count])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read index input: %v", readErr)
		}
	}
	want := "100644 " + firstBlob.String() + "\tfirst.txt\x00" +
		"120000 " + secondBlob.String() + "\tline\nbreak\x00"
	if output.String() != want {
		t.Fatalf("index input = %q, want %q", output.String(), want)
	}
}

func TestCreateCheckpointPreservesLeadingDashPath(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	const name = "-leading.txt"
	const want = "leading dash\n"
	writeFile(t, root, name, want)

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+name)
	if err != nil {
		t.Fatalf("show %q: %v", name, err)
	}
	if content != want {
		t.Fatalf("content for %q = %q, want %q", name, content, want)
	}
}

func TestCreateCheckpointPreservesContentsAcrossHashBatches(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	const fileCount = 240
	wantContents := make(map[string]string, fileCount)
	argumentBytes := 0
	for index := range fileCount {
		name := fmt.Sprintf("files/file-%03d-%s.txt", index, strings.Repeat("x", 80))
		content := fmt.Sprintf("unique content %03d\n", index)
		writeFile(t, root, name, content)
		wantContents[name] = content
		argumentBytes += len(name) + 1
	}
	if argumentBytes <= 2*maxSnapshotPathArgumentBytes {
		t.Fatalf("fixture uses %d path argument bytes, want more than two %d-byte batches", argumentBytes, maxSnapshotPathArgumentBytes)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	entries, err := repo.ListCommitTree(checkpoint.Commit)
	if err != nil {
		t.Fatalf("ListCommitTree: %v", err)
	}
	if len(entries) != len(wantContents) {
		t.Fatalf("tree entries = %d, want %d", len(entries), len(wantContents))
	}
	for _, entry := range entries {
		content, ok := wantContents[entry.Path]
		if !ok {
			t.Fatalf("unexpected tree path %q", entry.Path)
		}
		wantObjectID := gitBlobObjectID(t, []byte(content), len(entry.ObjectID))
		if entry.ObjectID != wantObjectID {
			t.Fatalf("object id for %q = %s, want %s", entry.Path, entry.ObjectID, wantObjectID)
		}
	}
}

func TestCreateCheckpointPreservesUnusualPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("control characters are not portable Windows path names")
	}
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, ".gitignore", "*.tmp\n")
	files := map[string]string{
		"\"quoted.txt\"":      "quoted\n",
		"ends-carriage.txt\r": "carriage return\n",
		"line\nbreak.txt":     "newline\n",
		"tab\tseparated.txt":  "tab\n",
	}
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	ignored := "ignored\noutput.tmp"
	writeFile(t, root, ignored, "ignored\n")

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	for name, want := range files {
		content, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+name)
		if err != nil {
			t.Fatalf("show %q: %v", name, err)
		}
		if content != want {
			t.Fatalf("content for %q = %q, want %q", name, content, want)
		}
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":"+ignored); err == nil {
		t.Fatalf("%q was captured, want gitignored", ignored)
	}
}

func BenchmarkSnapshotWorktree(b *testing.B) {
	requireGit(b)

	for _, fileCount := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("files=%d", fileCount), func(b *testing.B) {
			b.Setenv("TURNAL_STATE_DIR", b.TempDir())
			root, err := primitives.ParseWorkspaceRoot(b.TempDir())
			if err != nil {
				b.Fatalf("ParseWorkspaceRoot: %v", err)
			}
			repo, err := Init(root)
			if err != nil {
				b.Fatalf("Init: %v", err)
			}
			filesDir := filepath.Join(root.String(), "files")
			if err := os.Mkdir(filesDir, 0o755); err != nil {
				b.Fatalf("create files dir: %v", err)
			}
			for index := range fileCount {
				path := filepath.Join(filesDir, fmt.Sprintf("file-%06d", index))
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					b.Fatalf("write fixture: %v", err)
				}
			}

			b.ResetTimer()
			for range b.N {
				_, cleanup, err := repo.snapshotWorktreeTree()
				if err != nil {
					b.Fatalf("snapshotWorktreeTree: %v", err)
				}
				cleanup()
			}
		})
	}
}

func TestCleanGitEnvDropsInheritedGitVariables(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"GIT_DIR=/bad",
		"GIT_WORK_TREE=/bad",
		"GIT_INDEX_FILE=/bad",
		"GIT_OBJECT_DIRECTORY=/bad",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/bad",
		"HOME=/home/test",
	}

	cleaned := cleanGitEnv(env)
	for _, entry := range cleaned {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Fatalf("cleaned env still contains %s in %v", key, cleaned)
		}
	}
	for _, want := range []string{"PATH=/bin", "HOME=/home/test"} {
		if !containsString(cleaned, want) {
			t.Fatalf("cleaned env missing %s: %v", want, cleaned)
		}
	}
}

func TestDiffTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "src/app.txt", "hello\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}

	writeFile(t, root, "src/app.txt", "hello world\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	diffText := string(diff)
	for _, want := range []string{"diff --git a/src/app.txt b/src/app.txt", "-hello", "+hello world"} {
		if !strings.Contains(diffText, want) {
			t.Fatalf("diff missing %q:\n%s", want, diffText)
		}
	}
}

func TestDiffRefsPathFiltersToPathAndUsesZeroContext(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "a.txt", "old\nkeep\n")
	writeFile(t, root, "b.txt", "old\n")
	pre, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}

	writeFile(t, root, "a.txt", "new\nkeep\n")
	writeFile(t, root, "b.txt", "new\n")
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	diff, err := repo.DiffRefsPath(pre.Ref, post.Ref, "a.txt")
	if err != nil {
		t.Fatalf("DiffRefsPath: %v", err)
	}
	diffText := string(diff)
	for _, want := range []string{"diff --git a/a.txt b/a.txt", "@@ -1 +1 @@", "-old", "+new"} {
		if !strings.Contains(diffText, want) {
			t.Fatalf("path diff missing %q:\n%s", want, diffText)
		}
	}
	for _, notWant := range []string{"b.txt", " keep"} {
		if strings.Contains(diffText, notWant) {
			t.Fatalf("path diff unexpectedly contains %q:\n%s", notWant, diffText)
		}
	}
}

func TestDiffRefsPathTreatsWildcardFilenameLiterally(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := primitives.ParseSessionID("literal-path")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "[ab].txt", "literal old\n")
	writeFile(t, root, "a.txt", "matched old\n")
	pre, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "[ab].txt", "literal new\n")
	writeFile(t, root, "a.txt", "matched new\n")
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := repo.DiffRefsPath(pre.Ref, post.Ref, "[ab].txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff), "literal new") || strings.Contains(string(diff), "a.txt") {
		t.Fatalf("literal wildcard path diff =\n%s", diff)
	}
}

func TestDiffRefsPathLimitedBoundsBufferedOutput(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := primitives.ParseSessionID("bounded-diff")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "large.txt", strings.Repeat("old line\n", 4000))
	pre, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "large.txt", strings.Repeat("new line\n", 4000))
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatal(err)
	}

	const limit = 1024
	result, err := repo.DiffRefsPathLimited(context.Background(), pre.Ref, post.Ref, "large.txt", limit)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Patch) != limit {
		t.Fatalf("limited diff = %d bytes, truncated=%v", len(result.Patch), result.Truncated)
	}
	if result.ByteCount <= len(result.Patch) || result.LineCount < 8000 {
		t.Fatalf("complete output counts = %d bytes, %d lines", result.ByteCount, result.LineCount)
	}
}

func TestCommitFileBytesIfExists(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "dir/app.txt", "hello\n")
	checkpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	content, ok, err := repo.CommitFileBytesIfExists(checkpoint.Commit, "dir/app.txt")
	if err != nil {
		t.Fatalf("CommitFileBytesIfExists existing: %v", err)
	}
	if !ok || string(content) != "hello\n" {
		t.Fatalf("existing content ok=%t content=%q, want hello", ok, content)
	}

	if _, ok, err := repo.CommitFileBytesIfExists(checkpoint.Commit, "missing.txt"); err != nil || ok {
		t.Fatalf("missing file ok=%t err=%v, want false nil", ok, err)
	}

	if _, ok, err := repo.CommitFileBytesIfExists(checkpoint.Commit, "dir"); err == nil || ok {
		t.Fatalf("tree path ok=%t err=%v, want non-blob error", ok, err)
	}
}

func TestListCheckpointRefsFiltersSessionAndDropsInheritedGitEnv(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	otherSessionID, _ := primitives.ParseSessionID("other")
	turnID, _ := primitives.NewTurnID(1)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("demo pre checkpoint: %v", err)
	}
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("demo post checkpoint: %v", err)
	}
	if _, err := repo.CreateCheckpoint(otherSessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("other pre checkpoint: %v", err)
	}

	t.Setenv("GIT_DIR", "/bad/git-dir")
	t.Setenv("GIT_WORK_TREE", "/bad/work-tree")
	t.Setenv("GIT_INDEX_FILE", "/bad/index")

	refs, err := repo.ListCheckpointRefs(sessionID)
	if err != nil {
		t.Fatalf("ListCheckpointRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs len = %d, want 2: %v", len(refs), refs)
	}
	for _, ref := range refs {
		parts, err := ref.Parts()
		if err != nil {
			t.Fatalf("ref parts: %v", err)
		}
		if parts.SessionID != sessionID {
			t.Fatalf("listed ref for session %s, want %s: %s", parts.SessionID, sessionID, ref)
		}
	}
}

func TestListCheckpointRefInfosAndDiffStat(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	pre, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		t.Fatalf("ListAllCheckpointRefInfos: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos len = %d, want 2: %#v", len(infos), infos)
	}
	if infos[0].SessionID != sessionID || infos[0].TurnID != turnID || infos[0].Phase != primitives.CheckpointPhasePre {
		t.Fatalf("first ref info = %#v, want demo turn 1 pre", infos[0])
	}
	if infos[0].Commit != pre.Commit || infos[1].Commit != post.Commit {
		t.Fatalf("commits = %s %s, want %s %s", infos[0].Commit, infos[1].Commit, pre.Commit, post.Commit)
	}
	if infos[0].Time.IsZero() || infos[1].Time.IsZero() {
		t.Fatalf("ref info times must be populated: %#v", infos)
	}

	sessionInfos, err := repo.ListCheckpointRefInfos(sessionID)
	if err != nil {
		t.Fatalf("ListCheckpointRefInfos: %v", err)
	}
	if len(sessionInfos) != 2 {
		t.Fatalf("session infos len = %d, want 2", len(sessionInfos))
	}

	summary, err := repo.DiffStatTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffStatTurn: %v", err)
	}
	if len(summary.Files) != 1 {
		t.Fatalf("diff files len = %d, want 1: %#v", len(summary.Files), summary)
	}
	if summary.Files[0].Path != "app.txt" || summary.Additions != 1 || summary.Deletions != 1 {
		t.Fatalf("summary = %#v, want app.txt +1 -1", summary)
	}
}

func TestPlanRestoreCommitClassifiesChanges(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "add.txt", "target\n")
	writeFile(t, root, "modify.txt", "target\n")
	writeFile(t, root, "mode.sh", "same\n")
	if err := os.Chmod(filepath.Join(root.String(), "mode.sh"), 0o755); err != nil {
		t.Fatalf("chmod mode.sh: %v", err)
	}
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	if err := os.Remove(filepath.Join(root.String(), "add.txt")); err != nil {
		t.Fatalf("remove add.txt: %v", err)
	}
	writeFile(t, root, "modify.txt", "current\n")
	writeFile(t, root, "delete.txt", "current only\n")
	if err := os.Chmod(filepath.Join(root.String(), "mode.sh"), 0o644); err != nil {
		t.Fatalf("chmod mode.sh current: %v", err)
	}

	plan, err := repo.PlanRestoreCommit(target.Commit)
	if err != nil {
		t.Fatalf("PlanRestoreCommit: %v", err)
	}
	actions := map[string]RestoreAction{}
	for _, change := range plan.Changes {
		actions[change.Path] = change.Action
	}
	want := map[string]RestoreAction{
		"add.txt":    RestoreActionAdded,
		"modify.txt": RestoreActionModified,
		"delete.txt": RestoreActionDeleted,
		"mode.sh":    RestoreActionModeChanged,
	}
	if runtime.GOOS == "windows" {
		delete(want, "mode.sh")
	}
	for path, action := range want {
		if actions[path] != action {
			t.Fatalf("action for %s = %s, want %s; all actions=%#v", path, actions[path], action, actions)
		}
	}
	if len(actions) != len(want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
}

func TestRestoreCommitPreservesBytesModesSymlinksAndMetadata(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("exact POSIX mode and symlink round-trip is not supported on Windows")
	}

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	rawBytes := []byte{'h', 'i', 0, '\r', '\n', 0xff}
	writeBytes(t, root, "raw.bin", rawBytes, 0o644)
	writeBytes(t, root, "script.sh", []byte("#!/bin/sh\n"), 0o755)
	writeBytes(t, root, "private.key", []byte("checkpoint secret\n"), 0o600)
	if err := os.Symlink("raw.bin", filepath.Join(root.String(), "link.bin")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	writeBytes(t, root, "raw.bin", []byte("changed\n"), 0o644)
	writeBytes(t, root, "private.key", []byte("changed secret\n"), 0o644)
	if err := os.Chmod(filepath.Join(root.String(), "script.sh"), 0o644); err != nil {
		t.Fatalf("chmod script.sh: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), "link.bin")); err != nil {
		t.Fatalf("remove link.bin: %v", err)
	}
	writeFile(t, root, "extra.txt", "remove me\n")
	writeFile(t, root, ".turnal/tmp/keep.txt", "metadata\n")

	if err := repo.RestoreCommit(target.Commit); err != nil {
		t.Fatalf("RestoreCommit: %v", err)
	}

	restoredRaw, err := os.ReadFile(filepath.Join(root.String(), "raw.bin"))
	if err != nil {
		t.Fatalf("read raw.bin: %v", err)
	}
	if !bytes.Equal(restoredRaw, rawBytes) {
		t.Fatalf("raw.bin = %v, want %v", restoredRaw, rawBytes)
	}

	scriptInfo, err := os.Stat(filepath.Join(root.String(), "script.sh"))
	if err != nil {
		t.Fatalf("stat script.sh: %v", err)
	}
	if scriptInfo.Mode().Perm() != 0o755 {
		t.Fatalf("script.sh mode = %o, want 755", scriptInfo.Mode().Perm())
	}
	privateInfo, err := os.Stat(filepath.Join(root.String(), "private.key"))
	if err != nil {
		t.Fatalf("stat private.key: %v", err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private.key mode = %o, want 600", privateInfo.Mode().Perm())
	}

	linkInfo, err := os.Lstat(filepath.Join(root.String(), "link.bin"))
	if err != nil {
		t.Fatalf("lstat link.bin: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link.bin mode = %s, want symlink", linkInfo.Mode())
	}
	linkTarget, err := os.Readlink(filepath.Join(root.String(), "link.bin"))
	if err != nil {
		t.Fatalf("readlink link.bin: %v", err)
	}
	if linkTarget != "raw.bin" {
		t.Fatalf("link.bin target = %q, want raw.bin", linkTarget)
	}

	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal/tmp/keep.txt")); err != nil {
		t.Fatalf("metadata file was not preserved: %v", err)
	}
}

func TestRestoreRejectsTamperedModeManifest(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "content\n")
	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if err := os.WriteFile(repo.modeManifestPath(target.Commit), []byte(`{"version":1,"modes":{"app.txt":384}}`), 0o600); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}
	if err := repo.PreflightRestoreCommit(target.Commit); err == nil || !strings.Contains(err.Error(), "does not match commit trailer") {
		t.Fatalf("PreflightRestoreCommit error = %v, want manifest hash mismatch", err)
	}
}

func TestPruneModeManifestsRemovesOnlyUnreachableSidecars(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "one\n")
	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	first, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "two\n")
	second, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	refs, err := repo.ListAllPrivateRefs()
	if err != nil {
		t.Fatalf("ListAllPrivateRefs: %v", err)
	}
	for _, ref := range refs {
		commit, resolveErr := repo.RefCommit(ref)
		if resolveErr == nil && commit == first.Commit {
			if _, err := runHiddenGit(repo, "", "update-ref", "-d", ref); err != nil {
				t.Fatalf("delete first commit ref %s: %v", ref, err)
			}
		}
	}
	removed, err := repo.PruneModeManifests()
	if err != nil {
		t.Fatalf("PruneModeManifests: %v", err)
	}
	if len(removed) != 1 || removed[0] != repo.modeManifestPath(first.Commit) {
		t.Fatalf("removed = %#v, want first manifest", removed)
	}
	if _, err := os.Stat(repo.modeManifestPath(second.Commit)); err != nil {
		t.Fatalf("reachable manifest removed: %v", err)
	}
}

func TestRestoreCommitPreservesGitignoredFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	writeFile(t, root, ".gitignore", "ignored/\n*.tmp\n")
	writeFile(t, root, "app.txt", "target\n")
	writeFile(t, root, "ignored/cache.tmp", "before checkpoint\n")

	sessionID, _ := primitives.ParseSessionID("demo")
	turnID, _ := primitives.NewTurnID(1)
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	writeFile(t, root, "app.txt", "current\n")
	writeFile(t, root, "extra.txt", "remove me\n")
	writeFile(t, root, "ignored/cache.tmp", "preserve me\n")
	writeFile(t, root, "ignored/new.tmp", "preserve me too\n")
	writeFile(t, root, "scratch.tmp", "preserve ignored file\n")

	if err := repo.RestoreCommit(target.Commit); err != nil {
		t.Fatalf("RestoreCommit: %v", err)
	}

	app, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read app.txt: %v", err)
	}
	if string(app) != "target\n" {
		t.Fatalf("app.txt = %q, want target", app)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt still exists or stat failed: %v", err)
	}
	for path, want := range map[string]string{
		"ignored/cache.tmp": "preserve me\n",
		"ignored/new.tmp":   "preserve me too\n",
		"scratch.tmp":       "preserve ignored file\n",
	} {
		content, err := os.ReadFile(filepath.Join(root.String(), filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(content) != want {
			t.Fatalf("%s = %q, want %q", path, content, want)
		}
	}
}

func workspaceRoot(t *testing.T) primitives.WorkspaceRoot {
	t.Helper()
	// Redirect machine-wide state: initializing a store registers it, and a
	// throwaway temp workspace must not land in the developer's real registry.
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func writeFile(t *testing.T, root primitives.WorkspaceRoot, relPath, content string) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func writeBytes(t *testing.T, root primitives.WorkspaceRoot, relPath string, content []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", relPath, err)
	}
}

func gitBlobObjectID(t *testing.T, content []byte, encodedLength int) string {
	t.Helper()
	object := append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...)
	switch encodedLength {
	case sha1.Size * 2:
		digest := sha1.Sum(object) // Git object identity, not a security primitive.
		return hex.EncodeToString(digest[:])
	case sha256.Size * 2:
		digest := sha256.Sum256(object)
		return hex.EncodeToString(digest[:])
	default:
		t.Fatalf("unsupported Git object id length %d", encodedLength)
		return ""
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestParseDiffNumstatPreservesUnusualPaths(t *testing.T) {
	summary, err := parseDiffNumstat("2\t1\tline\nbreak\tand-tab.txt\x00-\t-\tbinary.dat\x00")
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 2 {
		t.Fatalf("files = %#v", summary.Files)
	}
	if got := summary.Files[0]; got.Path != "line\nbreak\tand-tab.txt" || got.Additions != 2 || got.Deletions != 1 {
		t.Fatalf("text stat = %#v", got)
	}
	if got := summary.Files[1]; got.Path != "binary.dat" || !got.Binary {
		t.Fatalf("binary stat = %#v", got)
	}
}
