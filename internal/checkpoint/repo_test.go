package checkpoint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/primitives"
)

func requireGit(t *testing.T) {
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
	writeFile(t, root, ".agent-vcs/tmp/internal.txt", "tool metadata\n")
	writeFile(t, root, "nested/.git/config", "nested git metadata\n")
	writeFile(t, root, "nested/.AGENT-VCS/tmp/internal.txt", "nested tool metadata\n")

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
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":.agent-vcs/tmp/internal.txt"); err == nil {
		t.Fatal(".agent-vcs metadata was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.git/config"); err == nil {
		t.Fatal("nested .git/config was captured, want excluded")
	}
	if _, err := runHiddenGit(repo, "", "show", checkpoint.Commit.String()+":nested/.AGENT-VCS/tmp/internal.txt"); err == nil {
		t.Fatal("nested .AGENT-VCS metadata was captured, want excluded")
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

	root := workspaceRoot(t)
	repo, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	rawBytes := []byte{'h', 'i', 0, '\r', '\n', 0xff}
	writeBytes(t, root, "raw.bin", rawBytes, 0o644)
	writeBytes(t, root, "script.sh", []byte("#!/bin/sh\n"), 0o755)
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
	if err := os.Chmod(filepath.Join(root.String(), "script.sh"), 0o644); err != nil {
		t.Fatalf("chmod script.sh: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), "link.bin")); err != nil {
		t.Fatalf("remove link.bin: %v", err)
	}
	writeFile(t, root, "extra.txt", "remove me\n")
	writeFile(t, root, ".agent-vcs/tmp/keep.txt", "metadata\n")

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
	if _, err := os.Stat(filepath.Join(root.String(), ".agent-vcs/tmp/keep.txt")); err != nil {
		t.Fatalf("metadata file was not preserved: %v", err)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
