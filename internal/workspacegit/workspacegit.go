package workspacegit

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/primitives"
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

func (git Git) Capture() (gitsync.Capture, error) {
	if err := git.ensureSupportedWorktree(); err != nil {
		return gitsync.Capture{}, err
	}
	head, err := git.currentHead()
	if err != nil {
		return gitsync.Capture{}, err
	}

	stagedPatch, err := git.runBytes("diff", "--binary", "--full-index", "--cached", head.Commit.String(), "--")
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture staged patch: %w", err)
	}
	unstagedPatch, err := git.runBytes("diff", "--binary", "--full-index", "--")
	if err != nil {
		return gitsync.Capture{}, fmt.Errorf("capture unstaged patch: %w", err)
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

func (git Git) Restore(target gitsync.Capture) error {
	if err := git.ensureSupportedWorktree(); err != nil {
		return err
	}
	if err := git.ensureNoOperationInProgress(); err != nil {
		return err
	}
	if err := git.run("cat-file", "-e", target.State.Head.Commit.String()+"^{commit}"); err != nil {
		return fmt.Errorf("target workspace git commit is not available: %w", err)
	}

	if err := git.run("reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("normalize current worktree: %w", err)
	}
	if err := git.run("clean", "-fd", "-e", ".turnal", "-e", ".turnal/", "--", "."); err != nil {
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
	root := filepath.Clean(git.Root.String())
	got := filepath.Clean(strings.TrimSpace(toplevel))
	if got != root {
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
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("workspace git operation in progress (%s); finish or abort it before workspace-git rollback", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect workspace git state %s: %w", name, err)
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
