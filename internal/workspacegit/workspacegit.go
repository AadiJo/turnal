package workspacegit

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/fsidentity"
	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/snapshotpolicy"
)

type Git struct {
	Root primitives.WorkspaceRoot
}

type RestorePlan struct {
	CurrentHead   gitsync.Head
	TargetHead    gitsync.Head
	StagedPaths   []primitives.RepoPath
	UnstagedPaths []primitives.RepoPath
	Untracked     []primitives.RepoPath
}

func Open(root primitives.WorkspaceRoot) Git {
	return Git{Root: root}
}

// ResolveCommit resolves a user-facing revision without changing the worktree.
func (git Git) ResolveCommit(revision string) (primitives.CommitSHA, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "", fmt.Errorf("Git revision must not be empty")
	}
	output, err := git.runOutput("rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve Git revision %q: %w", revision, err)
	}
	commit, err := primitives.ParseCommitSHA(strings.TrimSpace(output))
	if err != nil {
		return "", fmt.Errorf("resolve Git revision %q: %w", revision, err)
	}
	return commit, nil
}

func (git Git) Capture() (gitsync.Capture, error) {
	if err := git.ensureSupportedWorktree(); err != nil {
		return gitsync.Capture{}, err
	}
	head, err := git.currentHead()
	if err != nil {
		return gitsync.Capture{}, err
	}

	stagedPaths, err := git.diffPaths("--cached", head.Commit.String(), "--")
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture staged paths: %w", err)
	}
	unstagedPaths, err := git.diffPaths("--")
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture unstaged paths: %w", err)
	}
	untrackedPaths, err := git.nulPaths("ls-files", "-z", "--others", "--exclude-standard", "--")
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture untracked paths: %w", err)
	}
	effective, _, err := agentconfig.Resolve(git.Root.String(), agentconfig.Overrides{})
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("resolve snapshot deny policy: %w", err)
	}
	denyGlobs := effective.Secrets.SnapshotDenyGlobs
	stagedPaths = filterDeniedPaths(stagedPaths, denyGlobs)
	unstagedPaths = filterDeniedPaths(unstagedPaths, denyGlobs)
	untrackedPaths = filterDeniedPaths(untrackedPaths, denyGlobs)

	stagedPatch, err := git.diffPatch(denyGlobs, "--cached", head.Commit.String())
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture staged patch: %w", err)
	}
	unstagedPatch, err := git.diffPatch(denyGlobs)
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture unstaged patch: %w", err)
	}

	state := gitsync.NewState(head)
	state.Staged.Paths = stagedPaths
	state.Unstaged.Paths = unstagedPaths
	capture := gitsync.Capture{
		State:            state,
		StagedPatch:      stagedPatch,
		UnstagedPatch:    unstagedPatch,
		UntrackedContent: make(map[string][]byte, len(untrackedPaths)),
	}
	for _, repoPath := range untrackedPaths {
		mode, content, err := git.readUntracked(repoPath)
		if err != nil {
			return gitsync.Capture{}, err
		}
		capture.State.Untracked = append(capture.State.Untracked, gitsync.UntrackedFile{
			Path:        repoPath,
			Mode:        mode,
			StoragePath: gitsync.UntrackedStoragePath(repoPath),
		})
		capture.UntrackedContent[repoPath.String()] = content
	}
	return capture, nil
}

func (git Git) PlanRestore(target gitsync.Capture) (RestorePlan, error) {
	if err := git.ensureSupportedWorktree(); err != nil {
		return RestorePlan{}, err
	}
	current, err := git.currentHead()
	if err != nil {
		return RestorePlan{}, err
	}
	untracked := make([]primitives.RepoPath, 0, len(target.State.Untracked))
	for _, file := range target.State.Untracked {
		untracked = append(untracked, file.Path)
	}
	return RestorePlan{
		CurrentHead:   current,
		TargetHead:    target.State.Head,
		StagedPaths:   append([]primitives.RepoPath(nil), target.State.Staged.Paths...),
		UnstagedPaths: append([]primitives.RepoPath(nil), target.State.Unstaged.Paths...),
		Untracked:     untracked,
	}, nil
}

func (git Git) PreflightRestore(target gitsync.Capture) error {
	if err := git.ensureSupportedWorktree(); err != nil {
		return err
	}
	if err := git.ensureNoOperationInProgress(); err != nil {
		return err
	}
	if err := git.run("cat-file", "-e", target.State.Head.Commit.String()+"^{commit}"); err != nil {
		return fmt.Errorf("target workspace git commit is not available: %w", err)
	}
	return nil
}

func (git Git) Restore(target gitsync.Capture) (returnErr error) {
	if err := git.PreflightRestore(target); err != nil {
		return err
	}
	effective, _, err := agentconfig.Resolve(git.Root.String(), agentconfig.Overrides{})
	if err != nil {
		return fmt.Errorf("resolve snapshot deny policy: %w", err)
	}
	preserved, err := git.captureDeniedWorkspaceState(target.State.Head.Commit, effective.Secrets.SnapshotDenyGlobs)
	if err != nil {
		return err
	}
	defer func() {
		if err := git.restoreDeniedWorkspaceState(preserved); err != nil {
			if returnErr == nil {
				returnErr = err
			} else {
				returnErr = fmt.Errorf("%v; additionally failed to restore deny-listed paths: %w", returnErr, err)
			}
		}
	}()

	if err := git.run("reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("normalize current worktree: %w", err)
	}
	cleanArgs := []string{"clean", "-fd", "-e", ".turnal", "-e", ".turnal/"}
	for _, pattern := range effective.Secrets.SnapshotDenyGlobs {
		cleanArgs = append(cleanArgs, "-e", filepath.ToSlash(pattern))
	}
	cleanArgs = append(cleanArgs, "--", ".")
	if err := git.run(cleanArgs...); err != nil {
		return fmt.Errorf("clean current untracked files: %w", err)
	}
	if err := git.restoreHead(target.State.Head); err != nil {
		return err
	}
	if len(bytes.TrimSpace(target.StagedPatch)) > 0 {
		if err := git.runInput(target.StagedPatch, "apply", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("restore staged workspace git patch: %w", err)
		}
	}
	if len(bytes.TrimSpace(target.UnstagedPatch)) > 0 {
		if err := git.runInput(target.UnstagedPatch, "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("restore unstaged workspace git patch: %w", err)
		}
	}
	for _, file := range target.State.Untracked {
		content, ok := target.UntrackedContent[file.Path.String()]
		if !ok {
			return fmt.Errorf("missing captured untracked content for %s", file.Path)
		}
		if err := git.restoreUntracked(file, content); err != nil {
			return err
		}
	}
	return nil
}

func (git Git) ensureSupportedWorktree() error {
	toplevel, err := git.runOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("git-sync requires an initialized Git worktree at the turnal workspace root %s; run git init from that root and create an initial commit, or disable git-sync: %w", git.Root, err)
	}
	root := git.Root.String()
	got := strings.TrimSpace(toplevel)
	if !fsidentity.Same(root, got) {
		return fmt.Errorf("git-sync requires the turnal workspace root to be the Git worktree root; workspace=%s git_toplevel=%s. Run turnal init from the Git root, or disable git-sync for this workspace", root, got)
	}
	if _, err := git.currentHead(); err != nil {
		return err
	}
	return nil
}

func (git Git) ensureNoOperationInProgress() error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"} {
		path, err := git.runOutput("rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(git.Root.String(), path)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("workspace git operation in progress (%s); finish or abort it before workspace-git rollback", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect workspace git state %s: %w", name, err)
		}
	}
	return nil
}

func filterDeniedPaths(paths []primitives.RepoPath, patterns []string) []primitives.RepoPath {
	filtered := make([]primitives.RepoPath, 0, len(paths))
	for _, repoPath := range paths {
		if !snapshotpolicy.Denied(repoPath.String(), patterns) {
			filtered = append(filtered, repoPath)
		}
	}
	return filtered
}

func (git Git) diffPatch(denyGlobs []string, options ...string) ([]byte, error) {
	args := []string{"diff", "--binary", "--full-index"}
	args = append(args, options...)
	args = append(args, "--")
	for _, pattern := range denyGlobs {
		pattern = filepath.ToSlash(pattern)
		args = append(args, ":(exclude,glob)"+pattern)
		if !strings.Contains(pattern, "/") {
			args = append(args, ":(exclude,glob)**/"+pattern)
		}
	}
	return git.runBytes(args...)
}

type preservedDeniedPath struct {
	Path          primitives.RepoPath
	Exists        bool
	Mode          fs.FileMode
	Content       []byte
	SymlinkTarget string
	IndexChanged  bool
	IndexExists   bool
	IndexMode     string
	IndexObject   string
}

type preservedDeniedPaths []preservedDeniedPath

func (git Git) captureDeniedWorkspaceState(targetCommit primitives.CommitSHA, patterns []string) (preservedDeniedPaths, error) {
	candidates := map[string]primitives.RepoPath{}
	stagedPaths, err := git.diffPaths("--cached", "--")
	if err != nil {
		return nil, fmt.Errorf("list staged deny-policy candidates: %w", err)
	}
	staged := make(map[string]struct{}, len(stagedPaths))
	for _, repoPath := range stagedPaths {
		if snapshotpolicy.Denied(repoPath.String(), patterns) {
			staged[repoPath.String()] = struct{}{}
			candidates[repoPath.String()] = repoPath
		}
	}
	for _, args := range [][]string{
		{"ls-files", "-z", "--"},
		{"ls-tree", "-r", "-z", "--name-only", targetCommit.String()},
	} {
		paths, err := git.nulPaths(args...)
		if err != nil {
			return nil, fmt.Errorf("list deny-policy candidates: %w", err)
		}
		for _, repoPath := range paths {
			if snapshotpolicy.Denied(repoPath.String(), patterns) {
				candidates[repoPath.String()] = repoPath
			}
		}
	}
	root := git.Root.String()
	if err := filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, absPath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		repoText := filepath.ToSlash(relative)
		if entry.IsDir() {
			if repoText == ".git" || repoText == ".turnal" {
				return fs.SkipDir
			}
			return nil
		}
		if !snapshotpolicy.Denied(repoText, patterns) {
			return nil
		}
		repoPath, err := primitives.ParseRepoPath(repoText)
		if err != nil {
			return err
		}
		candidates[repoPath.String()] = repoPath
		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan deny-listed workspace paths: %w", err)
	}

	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := make([]preservedDeniedPath, 0, len(keys))
	for _, key := range keys {
		repoPath := candidates[key]
		entry := preservedDeniedPath{Path: repoPath}
		if _, ok := staged[repoPath.String()]; ok {
			entry.IndexChanged = true
			indexEntry, err := git.runBytes("ls-files", "-s", "-z", "--", repoPath.String())
			if err != nil {
				return nil, fmt.Errorf("read staged deny-listed path %s: %w", repoPath, err)
			}
			if len(indexEntry) > 0 {
				header, _, ok := bytes.Cut(indexEntry, []byte{'\t'})
				fields := strings.Fields(string(header))
				if !ok || len(fields) != 3 || fields[2] != "0" {
					return nil, fmt.Errorf("staged deny-listed path %s has unsupported index entry %q", repoPath, indexEntry)
				}
				entry.IndexExists = true
				entry.IndexMode = fields[0]
				entry.IndexObject = fields[1]
			}
		}
		absPath := git.Root.Join(repoPath)
		info, err := os.Lstat(absPath)
		if os.IsNotExist(err) {
			state = append(state, entry)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat deny-listed path %s: %w", repoPath, err)
		}
		entry.Exists = true
		entry.Mode = info.Mode()
		switch {
		case info.Mode().IsRegular():
			entry.Content, err = os.ReadFile(absPath)
		case info.Mode()&os.ModeSymlink != 0:
			entry.SymlinkTarget, err = os.Readlink(absPath)
		default:
			return nil, fmt.Errorf("cannot safely preserve deny-listed path %s with mode %s", repoPath, info.Mode())
		}
		if err != nil {
			return nil, fmt.Errorf("read deny-listed path %s: %w", repoPath, err)
		}
		state = append(state, entry)
	}
	return state, nil
}

func (git Git) restoreDeniedWorkspaceState(entries preservedDeniedPaths) error {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.Exists {
			continue
		}
		if err := os.RemoveAll(git.Root.Join(entry.Path)); err != nil {
			return fmt.Errorf("remove deny-listed path %s that was originally absent: %w", entry.Path, err)
		}
	}
	for _, entry := range entries {
		if !entry.Exists {
			continue
		}
		absPath := git.Root.Join(entry.Path)
		if err := os.RemoveAll(absPath); err != nil {
			return fmt.Errorf("replace deny-listed path %s: %w", entry.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return fmt.Errorf("create parent for deny-listed path %s: %w", entry.Path, err)
		}
		if entry.Mode&os.ModeSymlink != 0 {
			if err := os.Symlink(entry.SymlinkTarget, absPath); err != nil {
				return fmt.Errorf("restore deny-listed symlink %s: %w", entry.Path, err)
			}
			continue
		}
		if err := os.WriteFile(absPath, entry.Content, entry.Mode.Perm()); err != nil {
			return fmt.Errorf("restore deny-listed file %s: %w", entry.Path, err)
		}
		if err := os.Chmod(absPath, entry.Mode.Perm()); err != nil {
			return fmt.Errorf("restore deny-listed mode %s: %w", entry.Path, err)
		}
	}
	for _, entry := range entries {
		if !entry.IndexChanged {
			continue
		}
		if !entry.IndexExists {
			if err := git.run("update-index", "--force-remove", "--", entry.Path.String()); err != nil {
				return fmt.Errorf("restore staged deletion for deny-listed path %s: %w", entry.Path, err)
			}
			continue
		}
		if err := git.run("update-index", "--add", "--cacheinfo", entry.IndexMode, entry.IndexObject, entry.Path.String()); err != nil {
			return fmt.Errorf("restore staged deny-listed path %s: %w", entry.Path, err)
		}
	}
	return nil
}

func (git Git) currentHead() (gitsync.Head, error) {
	output, err := git.runOutput("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return gitsync.Head{}, fmt.Errorf("git-sync requires workspace Git to have an initial HEAD commit; create an initial commit before enabling git-sync capture or using --workspace-git rollback: %w", err)
	}
	commit, err := primitives.ParseCommitSHA(strings.TrimSpace(output))
	if err != nil {
		return gitsync.Head{}, err
	}

	symbolic, err := git.runOutput("symbolic-ref", "-q", "HEAD")
	if err != nil {
		return gitsync.Head{Commit: commit, Detached: true}, nil
	}
	symbolic = strings.TrimSpace(symbolic)
	if symbolic == "" {
		return gitsync.Head{Commit: commit, Detached: true}, nil
	}
	return gitsync.Head{Commit: commit, SymbolicRef: symbolic}, nil
}

func (git Git) restoreHead(head gitsync.Head) error {
	commit := head.Commit.String()
	if head.Detached || strings.TrimSpace(head.SymbolicRef) == "" {
		if err := git.run("checkout", "--detach", "--force", commit); err != nil {
			return fmt.Errorf("restore detached workspace git HEAD: %w", err)
		}
		return nil
	}
	if !strings.HasPrefix(head.SymbolicRef, "refs/heads/") {
		return fmt.Errorf("refusing to restore non-branch workspace git symbolic ref %s", head.SymbolicRef)
	}
	if err := git.run("check-ref-format", head.SymbolicRef); err != nil {
		return fmt.Errorf("invalid captured workspace git branch ref: %w", err)
	}
	if err := git.run("update-ref", head.SymbolicRef, commit); err != nil {
		return fmt.Errorf("restore workspace git branch %s: %w", head.SymbolicRef, err)
	}
	if err := git.run("symbolic-ref", "HEAD", head.SymbolicRef); err != nil {
		return fmt.Errorf("restore workspace git HEAD symbolic ref: %w", err)
	}
	if err := git.run("reset", "--hard", commit); err != nil {
		return fmt.Errorf("restore workspace git worktree to %s: %w", commit, err)
	}
	return nil
}

func (git Git) diffPaths(args ...string) ([]primitives.RepoPath, error) {
	fullArgs := append([]string{"diff", "--name-only", "-z"}, args...)
	return git.nulPaths(fullArgs...)
}

func (git Git) nulPaths(args ...string) ([]primitives.RepoPath, error) {
	output, err := git.runBytes(args...)
	if err != nil {
		return nil, err
	}
	var paths []primitives.RepoPath
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		repoPath, err := primitives.ParseRepoPath(string(raw))
		if err != nil {
			return nil, err
		}
		paths = append(paths, repoPath)
	}
	return paths, nil
}

func (git Git) readUntracked(repoPath primitives.RepoPath) (primitives.GitFileMode, []byte, error) {
	absPath := git.Root.Join(repoPath)
	info, err := os.Lstat(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("stat untracked path %s: %w", repoPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absPath)
		if err != nil {
			return "", nil, fmt.Errorf("read untracked symlink %s: %w", repoPath, err)
		}
		return primitives.GitFileModeSymlink, []byte(target), nil
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("unsupported untracked path %s mode %s", repoPath, info.Mode())
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("read untracked file %s: %w", repoPath, err)
	}
	mode := primitives.GitFileModeRegular
	if info.Mode().Perm()&0o111 != 0 {
		mode = primitives.GitFileModeExecutable
	}
	return mode, content, nil
}

func (git Git) restoreUntracked(file gitsync.UntrackedFile, content []byte) error {
	repoPath, err := primitives.ParseRepoPath(file.Path.String())
	if err != nil {
		return err
	}
	absPath := git.Root.Join(repoPath)
	if _, err := os.Lstat(absPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing path while restoring untracked file %s", repoPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat untracked restore path %s: %w", repoPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create parent directories for untracked file %s: %w", repoPath, err)
	}
	switch file.Mode {
	case primitives.GitFileModeRegular, primitives.GitFileModeExecutable:
		mode := fs.FileMode(0o644)
		if file.Mode == primitives.GitFileModeExecutable {
			mode = 0o755
		}
		if err := os.WriteFile(absPath, content, mode); err != nil {
			return fmt.Errorf("restore untracked file %s: %w", repoPath, err)
		}
	case primitives.GitFileModeSymlink:
		if strings.ContainsRune(string(content), 0) {
			return fmt.Errorf("restore untracked symlink %s: target contains NUL", repoPath)
		}
		if err := os.Symlink(string(content), absPath); err != nil {
			return fmt.Errorf("restore untracked symlink %s: %w", repoPath, err)
		}
	default:
		return fmt.Errorf("unsupported captured untracked mode %s for %s", file.Mode, repoPath)
	}
	return nil
}

func (git Git) run(args ...string) error {
	_, err := git.runBytes(args...)
	return err
}

func (git Git) runOutput(args ...string) (string, error) {
	output, err := git.runBytes(args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (git Git) runInput(input []byte, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = git.Root.String()
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = cleanGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (git Git) runBytes(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = git.Root.String()
	cmd.Env = cleanGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func cleanGitEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
