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
)

type Result struct {
	DryRun        bool
	DeletedRefs   []string
	DeletedFiles  []string
	RedactedFiles []string
	Residuals     []string
}

func DropSession(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("retention repo is required")
	}
	unlockSession, err := adapters.AcquireSessionLock(repo, sessionID)
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
		for _, ref := range result.DeletedRefs {
			if err := repo.DeletePrivateRef(ref); err != nil {
				return err
			}
		}
		for _, path := range result.DeletedFiles {
			if err := removeRetentionPath(path); err != nil {
				return fmt.Errorf("remove %s: %w", path, err)
			}
		}
		if _, err := repo.PruneModeManifests(); err != nil {
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
			if err := repo.DeletePrivateRef(ref); err != nil {
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
		if definition.Source.SessionID == sessionID {
			return fmt.Errorf("cannot drop session %s because case %s preserves it as the immutable source; case deletion is required before removing referenced history", sessionID, definition.ID)
		}
		for _, attempt := range definition.AttemptLinks {
			if attempt.Execution.SessionID == sessionID {
				return fmt.Errorf("cannot drop session %s because case %s attempt %s preserves its result checkpoints; case deletion is required before removing referenced history", sessionID, definition.ID, attempt.AttemptID)
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
	path := rollbackengine.JournalPath(repo)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read rollback journal before session drop: %w", err)
	}
	var journal struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("cannot drop session while rollback journal is unreadable at %s: %w", path, err)
	}
	target, err := primitives.ParseTargetRef(journal.Target)
	if err != nil {
		return fmt.Errorf("cannot drop session while rollback journal target is invalid at %s: %w", path, err)
	}
	if target.SessionID() == sessionID {
		return fmt.Errorf("cannot drop session %s while rollback recovery is pending; run turnal recovery status", sessionID)
	}
	return nil
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
	collectRollbackJournalRefs(repo, referenced)
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

func collectRollbackJournalRefs(repo *checkpoint.Repo, referenced map[string]struct{}) {
	data, err := os.ReadFile(rollbackengine.JournalPath(repo))
	if err != nil {
		return
	}
	collectPrivateRefs(referenced, json.RawMessage(data))
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
