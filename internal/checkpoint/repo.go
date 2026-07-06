package checkpoint

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-vcs-again/internal/primitives"
)

const (
	metadataDirName = ".agent-vcs"
	gitDirName      = "git"
	tmpDirName      = "tmp"
	logDirName      = "log"
	indexDirName    = "index"
	versionFileName = "VERSION"
	configFileName  = "config.toml"
)

type Repo struct {
	WorkspaceRoot primitives.WorkspaceRoot
	MetadataDir   string
	GitDir        string
	TmpDir        string
}

type Checkpoint struct {
	Ref    primitives.CheckpointRef
	Commit primitives.CommitSHA
}

type CheckpointRefInfo struct {
	Ref       primitives.CheckpointRef
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	Phase     primitives.CheckpointPhase
	HasPhase  bool
	Commit    primitives.CommitSHA
	Time      time.Time
}

type DiffFileStat struct {
	Path      string
	Additions int
	Deletions int
	Binary    bool
}

type DiffSummary struct {
	Files       []DiffFileStat
	Additions   int
	Deletions   int
	BinaryFiles int
}

func Init(root primitives.WorkspaceRoot) (*Repo, error) {
	repo := paths(root)

	for _, dir := range []string{repo.MetadataDir, repo.TmpDir, filepath.Join(repo.MetadataDir, logDirName), filepath.Join(repo.MetadataDir, indexDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if _, err := os.Stat(repo.GitDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat hidden git repo: %w", err)
		}
		if _, err := runGitNoRepo(root.String(), "init", "--bare", repo.GitDir); err != nil {
			return nil, err
		}
	}

	if err := writeFileIfMissing(filepath.Join(repo.MetadataDir, versionFileName), []byte("1\n")); err != nil {
		return nil, err
	}
	if err := writeFileIfMissing(filepath.Join(repo.MetadataDir, configFileName), []byte("# agent-vcs configuration\n")); err != nil {
		return nil, err
	}

	bare, err := repo.HiddenGitBare()
	if err != nil {
		return nil, fmt.Errorf("verify hidden git repo: %w", err)
	}
	if !bare {
		return nil, fmt.Errorf("hidden git repo is not bare: %s", repo.GitDir)
	}

	return repo, nil
}

func Open(root primitives.WorkspaceRoot) (*Repo, error) {
	repo := paths(root)
	if _, err := os.Stat(repo.GitDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hidden git repo not initialized at %s", repo.GitDir)
		}
		return nil, fmt.Errorf("stat hidden git repo: %w", err)
	}
	return repo, nil
}

func FindRoot(start string) (primitives.WorkspaceRoot, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve start path: %w", err)
	}

	for {
		candidate := filepath.Join(abs, metadataDirName, gitDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return primitives.ParseWorkspaceRoot(abs)
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}

	return "", fmt.Errorf("not an agent-vcs workspace: run agent-vcs init")
}

func (repo *Repo) CreateCheckpoint(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (Checkpoint, error) {
	ref, err := primitives.NewCheckpointRef(sessionID, turnID, phase)
	if err != nil {
		return Checkpoint{}, err
	}

	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return Checkpoint{}, err
	}
	defer cleanup()

	if _, err := runHiddenGit(repo, indexPath, "read-tree", "--empty"); err != nil {
		return Checkpoint{}, err
	}

	if err := repo.snapshotWorktree(indexPath); err != nil {
		return Checkpoint{}, err
	}

	tree, err := runHiddenGit(repo, indexPath, "write-tree")
	if err != nil {
		return Checkpoint{}, err
	}

	message := fmt.Sprintf("agent-vcs checkpoint %s turn %s", sessionID, turnID)
	if phase != "" {
		message += " " + phase.String()
	}
	commitOutput, err := runHiddenGit(repo, indexPath, "commit-tree", strings.TrimSpace(tree), "-m", message)
	if err != nil {
		return Checkpoint{}, err
	}

	commit, err := primitives.ParseCommitSHA(strings.TrimSpace(commitOutput))
	if err != nil {
		return Checkpoint{}, fmt.Errorf("parse checkpoint commit: %w", err)
	}

	if _, err := runHiddenGit(repo, indexPath, "update-ref", ref.String(), commit.String()); err != nil {
		return Checkpoint{}, err
	}

	return Checkpoint{Ref: ref, Commit: commit}, nil
}

func (repo *Repo) DiffRefs(preRef, postRef primitives.CheckpointRef) ([]byte, error) {
	indexPath, cleanup, err := repo.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	output, err := runHiddenGit(repo, indexPath,
		"diff",
		"--patch",
		"--no-color",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
	)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func (repo *Repo) DiffTurn(sessionID primitives.SessionID, turnID primitives.TurnID) ([]byte, error) {
	preRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return nil, err
	}
	postRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return nil, err
	}
	return repo.DiffRefs(preRef, postRef)
}

func (repo *Repo) ListCheckpointRefs(sessionID primitives.SessionID) ([]primitives.CheckpointRef, error) {
	refPrefix, err := primitives.CheckpointSessionRefPrefix(sessionID)
	if err != nil {
		return nil, err
	}

	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)", refPrefix)
	if err != nil {
		return nil, err
	}

	var refs []primitives.CheckpointRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref, err := primitives.ParseCheckpointRef(line)
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", line, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (repo *Repo) ListAllCheckpointRefInfos() ([]CheckpointRefInfo, error) {
	return repo.listCheckpointRefInfos(primitives.CheckpointRefsPrefix())
}

func (repo *Repo) ListCheckpointRefInfos(sessionID primitives.SessionID) ([]CheckpointRefInfo, error) {
	refPrefix, err := primitives.CheckpointSessionRefPrefix(sessionID)
	if err != nil {
		return nil, err
	}
	return repo.listCheckpointRefInfos(refPrefix)
}

func (repo *Repo) listCheckpointRefInfos(refPrefix string) ([]CheckpointRefInfo, error) {
	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)%09%(objectname)%09%(committerdate:iso-strict)", refPrefix)
	if err != nil {
		return nil, err
	}

	var infos []CheckpointRefInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("checkpoint ref listing invariant failed for %q: expected ref, commit and time", line)
		}

		ref, err := primitives.ParseCheckpointRef(fields[0])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", fields[0], err)
		}
		refParts, err := ref.Parts()
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref invariant failed for %q: %w", ref, err)
		}
		commit, err := primitives.ParseCommitSHA(fields[1])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref %s commit invariant failed: %w", ref, err)
		}
		createdAt, err := time.Parse(time.RFC3339, fields[2])
		if err != nil {
			return nil, fmt.Errorf("checkpoint ref %s time invariant failed: %w", ref, err)
		}

		infos = append(infos, CheckpointRefInfo{
			Ref:       ref,
			SessionID: refParts.SessionID,
			TurnID:    refParts.TurnID,
			Phase:     refParts.Phase,
			HasPhase:  refParts.HasPhase,
			Commit:    commit,
			Time:      createdAt,
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		left, right := infos[i], infos[j]
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		if left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		if phaseRank(left.Phase) != phaseRank(right.Phase) {
			return phaseRank(left.Phase) < phaseRank(right.Phase)
		}
		return left.Ref.String() < right.Ref.String()
	})

	return infos, nil
}

func (repo *Repo) DiffStatRefs(preRef, postRef primitives.CheckpointRef) (DiffSummary, error) {
	output, err := runHiddenGit(repo, "",
		"-c",
		"core.quotePath=false",
		"diff",
		"--numstat",
		"--no-renames",
		"--no-ext-diff",
		"--no-textconv",
		preRef.String()+"^{commit}",
		postRef.String()+"^{commit}",
	)
	if err != nil {
		return DiffSummary{}, err
	}

	var summary DiffSummary
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return DiffSummary{}, fmt.Errorf("diff stat invariant failed for %q: expected additions, deletions and path", line)
		}

		stat := DiffFileStat{Path: fields[2]}
		if fields[0] == "-" || fields[1] == "-" {
			stat.Binary = true
			summary.BinaryFiles++
		} else {
			additions, err := strconv.Atoi(fields[0])
			if err != nil {
				return DiffSummary{}, fmt.Errorf("parse additions for %q: %w", line, err)
			}
			deletions, err := strconv.Atoi(fields[1])
			if err != nil {
				return DiffSummary{}, fmt.Errorf("parse deletions for %q: %w", line, err)
			}
			stat.Additions = additions
			stat.Deletions = deletions
			summary.Additions += additions
			summary.Deletions += deletions
		}
		summary.Files = append(summary.Files, stat)
	}
	return summary, nil
}

func (repo *Repo) DiffStatTurn(sessionID primitives.SessionID, turnID primitives.TurnID) (DiffSummary, error) {
	preRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return DiffSummary{}, err
	}
	postRef, err := primitives.NewCheckpointRef(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return DiffSummary{}, err
	}
	return repo.DiffStatRefs(preRef, postRef)
}

func (repo *Repo) DeleteCheckpointRef(ref primitives.CheckpointRef) error {
	parsedRef, err := primitives.ParseCheckpointRef(ref.String())
	if err != nil {
		return err
	}
	if _, err := runHiddenGit(repo, "", "update-ref", "-d", parsedRef.String()); err != nil {
		return err
	}
	return nil
}

func (repo *Repo) snapshotWorktree(indexPath string) error {
	root := repo.WorkspaceRoot.String()
	return filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", absPath, err)
		}
		if relPath == "." {
			return nil
		}

		repoPath := filepath.ToSlash(relPath)
		if excludedPath(repoPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", repoPath, err)
		}

		var mode string
		var blob string
		switch {
		case info.Mode().IsRegular():
			mode = gitRegularFileMode(info.Mode())
			output, err := runHiddenGit(repo, indexPath, "hash-object", "-w", "--no-filters", "--", repoPath)
			if err != nil {
				return err
			}
			blob = strings.TrimSpace(output)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(absPath)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", repoPath, err)
			}
			mode = "120000"
			output, err := runHiddenGitWithInput(repo, indexPath, strings.NewReader(target), "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			blob = strings.TrimSpace(output)
		default:
			return nil
		}

		if _, err := runHiddenGit(repo, indexPath, "update-index", "--add", "--cacheinfo", mode, blob, repoPath); err != nil {
			return err
		}
		return nil
	})
}

func excludedPath(repoPath string) bool {
	for _, segment := range strings.Split(repoPath, "/") {
		if strings.EqualFold(segment, ".git") || strings.EqualFold(segment, metadataDirName) {
			return true
		}
	}
	return false
}

func gitRegularFileMode(mode fs.FileMode) string {
	if mode&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

func phaseRank(phase primitives.CheckpointPhase) int {
	switch phase {
	case primitives.CheckpointPhasePre:
		return 0
	case primitives.CheckpointPhasePost:
		return 1
	default:
		return 2
	}
}

func paths(root primitives.WorkspaceRoot) *Repo {
	metadataDir := filepath.Join(root.String(), metadataDirName)
	return &Repo{
		WorkspaceRoot: root,
		MetadataDir:   metadataDir,
		GitDir:        filepath.Join(metadataDir, gitDirName),
		TmpDir:        filepath.Join(metadataDir, tmpDirName),
	}
}

func (repo *Repo) tempIndex() (string, func(), error) {
	if err := os.MkdirAll(repo.TmpDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}

	file, err := os.CreateTemp(repo.TmpDir, "index-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp index: %w", err)
	}
	indexPath := file.Name()
	if err := file.Close(); err != nil {
		return "", nil, fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", nil, fmt.Errorf("prepare temp index: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(indexPath)
		_ = os.Remove(indexPath + ".lock")
	}
	return indexPath, cleanup, nil
}

func writeFileIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func runGitNoRepo(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = cleanGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func runHiddenGit(repo *Repo, indexPath string, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, nil, args...)
}

func runHiddenGitWithInput(repo *Repo, indexPath string, stdin io.Reader, args ...string) (string, error) {
	return runHiddenGitCommand(repo, indexPath, stdin, args...)
}

func runHiddenGitCommand(repo *Repo, indexPath string, stdin io.Reader, args ...string) (string, error) {
	if indexPath == "" {
		var cleanup func()
		var err error
		indexPath, cleanup, err = repo.tempIndex()
		if err != nil {
			return "", err
		}
		defer cleanup()
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repo.WorkspaceRoot.String()
	cmd.Stdin = stdin
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.WorkspaceRoot.String(),
		"GIT_INDEX_FILE="+indexPath,
		"GIT_AUTHOR_NAME=agent-vcs",
		"GIT_AUTHOR_EMAIL=agent-vcs@localhost",
		"GIT_COMMITTER_NAME=agent-vcs",
		"GIT_COMMITTER_EMAIL=agent-vcs@localhost",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
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
