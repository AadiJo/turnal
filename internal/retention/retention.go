package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/adapters"
	caseengine "github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/AadiJo/turnal/internal/turns"
)

type Result struct {
	DryRun        bool
	DeletedRefs   []string
	DeletedFiles  []string
	RedactedFiles []string
	Residuals     []string
}

type sessionLockAcquirer func(*checkpoint.Repo, primitives.SessionID) (func(), error)

func DropSession(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool) (Result, error) {
	return dropSession(repo, sessionID, dryRun, removeRetentionPath)
}

func dropSession(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool, removePath func(string) error) (Result, error) {
	return dropSessionWithLock(repo, sessionID, dryRun, removePath, adapters.AcquireSessionLock)
}

func dropSessionWithLock(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool, removePath func(string) error, acquireSessionLock sessionLockAcquirer) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("retention repo is required")
	}
	if removePath == nil {
		return Result{}, fmt.Errorf("retention path remover is required")
	}
	if acquireSessionLock == nil {
		return Result{}, fmt.Errorf("retention session lock acquirer is required")
	}
	unlockSession, err := acquireSessionLock(repo, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("lock session %s for deletion: %w", sessionID, err)
	}
	defer unlockSession()
	var result Result
	err = repo.WithWorkspaceLock("drop session", func() error {
		var err error
		result, err = planDropSession(repo, sessionID, dryRun)
		if err != nil || dryRun {
			return err
		}
		result.RedactedFiles, err = adapters.RedactRawHookSession(repo.MetadataDir, sessionID, false)
		if err != nil {
			return err
		}
		// Remove durable records first. If this phase is interrupted, any refs
		// left behind are harmless orphans; deleting refs first would leave
		// surviving events pointing at missing durable workspace state.
		for _, path := range result.DeletedFiles {
			if err := removePath(path); err != nil {
				return fmt.Errorf("remove %s: %w", path, err)
			}
		}
		for _, ref := range result.DeletedRefs {
			if err := repo.DeletePrivateRefLocked(ref); err != nil {
				return err
			}
		}
		if _, err := repo.PruneModeManifestsLocked(); err != nil {
			return err
		}
		if err := index.Invalidate(repo.MetadataDir); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func PruneOrphanRefs(repo *checkpoint.Repo, dryRun bool) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("retention repo is required")
	}
	var result Result
	err := repo.WithWorkspaceLock("prune retention refs", func() error {
		refs, err := repo.ListAllPrivateRefs()
		if err != nil {
			return err
		}
		referenced, err := referencedPrivateRefs(repo)
		if err != nil {
			return err
		}
		result = Result{DryRun: dryRun}
		for _, ref := range refs {
			if _, ok := referenced[ref]; ok {
				continue
			}
			result.DeletedRefs = append(result.DeletedRefs, ref)
		}
		sort.Strings(result.DeletedRefs)
		if dryRun {
			return nil
		}
		for _, ref := range result.DeletedRefs {
			if err := repo.DeletePrivateRefLocked(ref); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func RunHiddenGitGC(repo *checkpoint.Repo, dryRun bool) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("retention repo is required")
	}
	result := Result{DryRun: dryRun}
	if dryRun {
		return result, nil
	}
	if err := repo.RunHiddenGitGC(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func planDropSession(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool) (Result, error) {
	sessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return Result{}, err
	}
	activeTurns, err := turns.ListActive(repo, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("inspect active turns before session drop: %w", err)
	}
	if len(activeTurns) > 0 {
		return Result{}, fmt.Errorf("cannot drop session %s while turn %s is active at %s; finish or recover the turn first", sessionID, activeTurns[0].TurnID, activeTurns[0].Path)
	}
	if err := ensureNoActiveRollbackForSession(repo, sessionID); err != nil {
		return Result{}, err
	}
	if err := ensureSessionNotCaseReferenced(repo, sessionID); err != nil {
		return Result{}, err
	}
	refPrefixes := []string{
		fmt.Sprintf("%s/%s/turn", primitives.CheckpointRefsPrefix(), sessionID),
		fmt.Sprintf("%s/%s/turn", primitives.GitSyncRefsPrefix(), sessionID),
		fmt.Sprintf("refs/agent-vcs/rollback-safety/%s", sessionID),
		fmt.Sprintf("refs/agent-vcs/git-sync-safety/rollback/%s", sessionID),
	}
	result := Result{DryRun: dryRun}
	seenRefs := map[string]struct{}{}
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return Result{}, err
	}
	for _, info := range infos {
		if info.SessionID != sessionID {
			continue
		}
		seenRefs[info.Ref.String()] = struct{}{}
		if info.CanonicalRef != "" {
			seenRefs[info.CanonicalRef.String()] = struct{}{}
		}
	}
	for _, prefix := range refPrefixes {
		refs, err := repo.ListPrivateRefs(prefix)
		if err != nil {
			return Result{}, err
		}
		for _, ref := range refs {
			if _, ok := seenRefs[ref]; ok {
				continue
			}
			seenRefs[ref] = struct{}{}
		}
	}
	allRefs, err := repo.ListAllPrivateRefs()
	if err != nil {
		return Result{}, err
	}
	needle := "/" + sessionID.String() + "/turn/"
	for _, ref := range allRefs {
		if strings.Contains(ref, needle) {
			seenRefs[ref] = struct{}{}
		}
	}
	for ref := range seenRefs {
		result.DeletedRefs = append(result.DeletedRefs, ref)
	}
	sort.Strings(result.DeletedRefs)

	paths, residuals := sessionFiles(repo, sessionID)
	result.Residuals = residuals
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			result.DeletedFiles = append(result.DeletedFiles, path)
		} else if err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("stat %s: %w", path, err)
		}
	}
	sort.Strings(result.DeletedFiles)
	result.RedactedFiles, err = adapters.RedactRawHookSession(repo.MetadataDir, sessionID, true)
	if err != nil {
		return Result{}, err
	}
	sort.Strings(result.RedactedFiles)
	return result, nil
}

func ensureSessionNotCaseReferenced(repo *checkpoint.Repo, sessionID primitives.SessionID) error {
	projection, err := caseengine.Rebuild(repo)
	if err != nil {
		return fmt.Errorf("inspect case references before session drop: %w", err)
	}
	for _, definition := range projection.Cases {
		task, ok := projection.Task(definition.TaskID)
		if !ok {
			return fmt.Errorf("cannot validate retention for case %s because task %s is missing", definition.ID, definition.TaskID)
		}
		if task.Created.SessionID == sessionID {
			return fmt.Errorf("cannot drop session %s because active case %s requires task %s creation history", sessionID, definition.ID, task.ID)
		}
		for _, revision := range task.Revisions {
			if revision.Created.SessionID == sessionID {
				return fmt.Errorf("cannot drop session %s because active case %s requires task %s revision %d history", sessionID, definition.ID, task.ID, revision.Number)
			}
		}
		if definition.Source.SessionID == sessionID {
			return fmt.Errorf("cannot drop session %s because case %s preserves it as the immutable source; case deletion is required before removing referenced history", sessionID, definition.ID)
		}
		for _, attempt := range definition.AttemptLinks {
			if attempt.Execution.SessionID == sessionID {
				return fmt.Errorf("cannot drop session %s because case %s attempt %s preserves its result checkpoints; case deletion is required before removing referenced history", sessionID, definition.ID, attempt.AttemptID)
			}
		}
	}
	for _, definition := range projection.TombstonedCases {
		// If this session owns the tombstoned Case creation, dropping it removes
		// the relationship itself. Otherwise the surviving Case record still
		// needs the shared Task creation and revision history to rebuild.
		if definition.Created.SessionID == sessionID {
			continue
		}
		task, ok := projection.Task(definition.TaskID)
		if !ok {
			return fmt.Errorf("cannot validate retention for tombstoned case %s because task %s is missing", definition.ID, definition.TaskID)
		}
		if task.Created.SessionID == sessionID {
			return fmt.Errorf("cannot drop session %s because tombstoned case %s still references task %s creation history; drop the case's own session first", sessionID, definition.ID, task.ID)
		}
		for _, revision := range task.Revisions {
			if revision.Number <= definition.TaskRevision && revision.Created.SessionID == sessionID {
				return fmt.Errorf("cannot drop session %s because tombstoned case %s still references task %s revision %d history; drop the case's own session first", sessionID, definition.ID, task.ID, revision.Number)
			}
		}
	}
	return nil
}

func sessionFiles(repo *checkpoint.Repo, sessionID primitives.SessionID) ([]string, []string) {
	files := []string{
		filepath.Join(repo.MetadataDir, "log", "events", sessionID.String()+".jsonl"),
		filepath.Join(repo.MetadataDir, "log", "events", sessionID.String()),
		filepath.Join(repo.MetadataDir, "log", "raw", sessionID.String()),
		filepath.Join(repo.MetadataDir, "log", "source", sessionID.String()),
		filepath.Join(repo.MetadataDir, "log", "tail", sessionID.String()+".json"),
		filepath.Join(repo.MetadataDir, "log", "tail", sessionID.String()),
		filepath.Join(repo.TmpDir, "turns", sessionID.String()+".json"),
	}
	if streams, err := eventlog.ListDurableStreams(repo.MetadataDir); err == nil {
		for _, stream := range streams {
			if stream.SessionID == sessionID {
				files = append(files, filepath.Join(repo.MetadataDir, "log", "streams", stream.StreamID.String()+".json"))
			}
		}
	}
	for _, pattern := range []string{
		filepath.Join(repo.TmpDir, "turns", "*-"+sessionID.String()+".json"),
		filepath.Join(repo.TmpDir, "hooks", "*-"+sessionID.String()+".lock"),
		filepath.Join(repo.TmpDir, "checkpoints", "*-"+sessionID.String()+"-turn-*.json"),
	} {
		if matches, err := filepath.Glob(pattern); err == nil {
			files = append(files, matches...)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(repo.TmpDir, "checkpoints", sessionID.String()+"-turn-*.json")); err == nil {
		files = append(files, matches...)
	}
	replayFiles, residuals := replaySessionFiles(repo, sessionID)
	files = append(files, replayFiles...)
	return files, residuals
}

func ensureNoActiveRollbackForSession(repo *checkpoint.Repo, sessionID primitives.SessionID) error {
	paths, err := allRollbackJournalPaths(repo)
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read rollback journal before session drop: %w", err)
		}
		var journal rollbackengine.Journal
		if err := json.Unmarshal(data, &journal); err != nil {
			return fmt.Errorf("cannot drop session while rollback journal is unreadable at %s: %w", path, err)
		}
		ownerRepo, err := rollbackJournalOwnerRepo(repo, path, journal)
		if err != nil {
			return fmt.Errorf("cannot drop session while rollback journal ownership is invalid at %s: %w", path, err)
		}
		target, err := rollbackengine.ValidateJournal(ownerRepo, journal)
		if err != nil {
			return fmt.Errorf("cannot drop session while rollback journal is invalid at %s: %w", path, err)
		}
		if target.Manual {
			continue
		}
		if target.SessionID == sessionID {
			return fmt.Errorf("cannot drop session %s while rollback recovery is pending in %s", sessionID, path)
		}
	}
	return nil
}

func rollbackJournalOwnerRepo(repo *checkpoint.Repo, path string, journal rollbackengine.Journal) (*checkpoint.Repo, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository is required")
	}
	if journal.RepoID == "" || journal.StoreID == "" || journal.WorktreeID == "" || journal.WorkspaceRoot == "" {
		return nil, fmt.Errorf("complete repository, store, worktree, and workspace identity is required")
	}
	if journal.RepoID != repo.RepoID || journal.StoreID != repo.StoreID {
		return nil, fmt.Errorf("repository or store identity mismatch")
	}
	worktreeID, err := primitives.ParseWorktreeID(journal.WorktreeID.String())
	if err != nil {
		return nil, fmt.Errorf("worktree identity: %w", err)
	}
	workspaceRoot, err := primitives.ParseWorkspaceRoot(journal.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	worktrees, err := repo.ListWorktrees()
	if err != nil {
		return nil, fmt.Errorf("list registered worktrees: %w", err)
	}
	registered := false
	for _, worktree := range worktrees {
		if worktree.WorktreeID != worktreeID {
			continue
		}
		registered = true
		if filepath.Clean(worktree.Root) != filepath.Clean(workspaceRoot.String()) {
			return nil, fmt.Errorf("workspace root does not match registered worktree %s", worktreeID)
		}
		break
	}
	if !registered {
		return nil, fmt.Errorf("worktree identity %s is not registered", worktreeID)
	}
	expectedPath := filepath.Join(repo.TmpDir, "rollback-journal-"+worktreeID.String()+".json")
	if filepath.Clean(path) != filepath.Clean(expectedPath) {
		return nil, fmt.Errorf("journal path does not match worktree identity %s", worktreeID)
	}
	ownerRepo := *repo
	ownerRepo.WorktreeID = worktreeID
	ownerRepo.WorkspaceRoot = workspaceRoot
	return &ownerRepo, nil
}

func replaySessionFiles(repo *checkpoint.Repo, sessionID primitives.SessionID) ([]string, []string) {
	dir := filepath.Join(repo.TmpDir, "replay", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	activeData, _ := os.ReadFile(filepath.Join(repo.TmpDir, "replay", "active"))
	activeID := strings.TrimSpace(string(activeData))
	managedRoot := filepath.Join(repo.TmpDir, "replay", "worktrees")
	var files []string
	var residuals []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		metadataPath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}
		var session struct {
			ID       string `json:"id"`
			Path     string `json:"path"`
			Sequence []struct {
				SessionID string `json:"session_id"`
			} `json:"sequence"`
		}
		if json.Unmarshal(data, &session) != nil {
			continue
		}
		matched := false
		for _, checkpoint := range session.Sequence {
			if strings.EqualFold(checkpoint.SessionID, sessionID.String()) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		files = append(files, metadataPath)
		if session.ID == activeID {
			files = append(files, filepath.Join(repo.TmpDir, "replay", "active"))
		}
		if pathWithin(session.Path, managedRoot) {
			files = append(files, session.Path)
		} else if session.Path != "" {
			residuals = append(residuals, fmt.Sprintf("kept replay worktree remains outside Turnal metadata: %s", session.Path))
		}
	}
	return files, residuals
}

func pathWithin(candidate, root string) bool {
	if candidate == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func removeRetentionPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func referencedPrivateRefs(repo *checkpoint.Repo) (map[string]struct{}, error) {
	referenced := map[string]struct{}{}
	log := eventlog.Open(repo.MetadataDir)
	sessions, err := log.ListSessions()
	if err != nil {
		return nil, err
	}
	for _, sessionID := range sessions {
		events, err := log.Read(sessionID)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			collectPrivateRefs(referenced, event.Payload)
		}
	}
	workspaceEvents, err := manualcheckpoints.ReadEvents(repo, true)
	if err != nil {
		return nil, err
	}
	for _, event := range workspaceEvents {
		collectPrivateRefs(referenced, event.Payload)
	}
	journals, err := repo.ListCheckpointJournals()
	if err != nil {
		return nil, err
	}
	for _, journal := range journals {
		if journal.Ref != "" {
			referenced[journal.Ref.String()] = struct{}{}
		}
		if journal.GitSyncRef != "" {
			referenced[journal.GitSyncRef] = struct{}{}
		}
	}
	collectActiveTurnRefs(repo, referenced)
	if err := collectRollbackJournalRefs(repo, referenced); err != nil {
		return nil, err
	}
	collectImportManifestRefs(repo, referenced)

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Manual {
			referenced[info.Ref.String()] = struct{}{}
			if info.CanonicalRef != "" {
				referenced[info.CanonicalRef.String()] = struct{}{}
			}
			continue
		}
		if _, ok := referenced[info.Ref.String()]; !ok {
			continue
		}
		if info.CanonicalRef != "" {
			referenced[info.CanonicalRef.String()] = struct{}{}
		}
	}
	return referenced, nil
}

func collectImportManifestRefs(repo *checkpoint.Repo, referenced map[string]struct{}) {
	dir := filepath.Join(repo.MetadataDir, "imports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var manifest struct {
			RefMappings map[string]string `json:"ref_mappings"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}
		for _, ref := range manifest.RefMappings {
			if strings.HasPrefix(ref, "refs/agent-vcs/") {
				referenced[ref] = struct{}{}
			}
		}
	}
}

func collectRollbackJournalRefs(repo *checkpoint.Repo, referenced map[string]struct{}) error {
	paths, err := allRollbackJournalPaths(repo)
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read rollback journal during retention scan: %w", err)
		}
		var journal rollbackengine.Journal
		if err := json.Unmarshal(data, &journal); err != nil {
			return fmt.Errorf("parse rollback journal during retention scan at %s: %w", path, err)
		}
		collectPrivateRefs(referenced, json.RawMessage(data))
	}
	return nil
}

func allRollbackJournalPaths(repo *checkpoint.Repo) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(repo.TmpDir, "rollback-journal-*.json"))
	if err != nil {
		return nil, err
	}
	legacy := filepath.Join(repo.TmpDir, "rollback-journal.json")
	if _, err := os.Lstat(legacy); err == nil {
		paths = append(paths, legacy)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("rollback journal path invariant failed: regular files only: %s", path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func collectActiveTurnRefs(repo *checkpoint.Repo, referenced map[string]struct{}) {
	dir := filepath.Join(repo.TmpDir, "turns")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var state struct {
			PreRef string `json:"pre_ref"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if strings.HasPrefix(state.PreRef, "refs/agent-vcs/") {
			referenced[state.PreRef] = struct{}{}
		}
	}
}

func collectPrivateRefs(out map[string]struct{}, data json.RawMessage) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return
	}
	walkJSON(value, func(text string) {
		if strings.HasPrefix(text, "refs/agent-vcs/") {
			out[text] = struct{}{}
		}
	})
}

func walkJSON(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkJSON(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkJSON(item, visit)
		}
	}
}
