package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
)

type Result struct {
	DryRun       bool
	DeletedRefs  []string
	DeletedFiles []string
}

func DropSession(repo *checkpoint.Repo, sessionID primitives.SessionID, dryRun bool) (Result, error) {
	if repo == nil {
		return Result{}, fmt.Errorf("retention repo is required")
	}
	var result Result
	err := repo.WithWorkspaceLock("drop session", func() error {
		var err error
		result, err = planDropSession(repo, sessionID, dryRun)
		if err != nil || dryRun {
			return err
		}
		for _, ref := range result.DeletedRefs {
			if err := repo.DeletePrivateRef(ref); err != nil {
				return err
			}
		}
		for _, path := range result.DeletedFiles {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", path, err)
			}
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
	refPrefixes := []string{
		fmt.Sprintf("%s/%s/turn", primitives.CheckpointRefsPrefix(), sessionID),
		fmt.Sprintf("%s/%s/turn", primitives.GitSyncRefsPrefix(), sessionID),
		fmt.Sprintf("refs/agent-vcs/rollback-safety/%s", sessionID),
		fmt.Sprintf("refs/agent-vcs/git-sync-safety/rollback/%s", sessionID),
	}
	result := Result{DryRun: dryRun}
	seenRefs := map[string]struct{}{}
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
			result.DeletedRefs = append(result.DeletedRefs, ref)
		}
	}
	sort.Strings(result.DeletedRefs)

	for _, path := range sessionFiles(repo, sessionID) {
		if _, err := os.Stat(path); err == nil {
			result.DeletedFiles = append(result.DeletedFiles, path)
		} else if err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("stat %s: %w", path, err)
		}
	}
	sort.Strings(result.DeletedFiles)
	return result, nil
}

func sessionFiles(repo *checkpoint.Repo, sessionID primitives.SessionID) []string {
	files := []string{
		filepath.Join(repo.MetadataDir, "log", "events", sessionID.String()+".jsonl"),
		filepath.Join(repo.TmpDir, "turns", sessionID.String()+".json"),
		filepath.Join(repo.TmpDir, "hooks", sessionID.String()+".lock"),
	}
	if matches, err := filepath.Glob(filepath.Join(repo.TmpDir, "checkpoints", sessionID.String()+"-turn-*.json")); err == nil {
		files = append(files, matches...)
	}
	return files
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
	return referenced, nil
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
