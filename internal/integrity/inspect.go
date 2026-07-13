package integrity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/adapters"
	caseengine "github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
)

type Report struct {
	Problems []string
}

func Inspect(repo *checkpoint.Repo) Report {
	var report Report
	if repo == nil {
		report.Problems = append(report.Problems, "integrity check requires checkpoint repo")
		return report
	}
	held, lockErr := repo.WorkspaceLockStatus()
	if lockErr != nil {
		report.Problems = append(report.Problems, fmt.Sprintf("workspace lock inspection failed: %v", lockErr))
	} else if held {
		report.Problems = append(report.Problems, fmt.Sprintf("workspace lock held: %s", repo.WorkspaceLockPath()))
	}
	report.Problems = append(report.Problems, inspectEventLogs(repo)...)
	if _, err := manualcheckpoints.Read(repo, true); err != nil {
		report.Problems = append(report.Problems, fmt.Sprintf("manual checkpoint event inspection failed: %v", err))
	}
	if _, err := caseengine.Rebuild(repo); err != nil {
		report.Problems = append(report.Problems, fmt.Sprintf("task/case projection failed: %v", err))
	}
	report.Problems = append(report.Problems, inspectCheckpointRefs(repo)...)
	report.Problems = append(report.Problems, inspectCheckpointJournals(repo)...)
	report.Problems = append(report.Problems, rollbackengine.InspectJournal(repo)...)
	report.Problems = append(report.Problems, inspectHookFailures(repo)...)
	report.Problems = append(report.Problems, inspectCaptureFiles(repo)...)
	report.Problems = append(report.Problems, inspectIndex(repo)...)
	return report
}

func inspectCaptureFiles(repo *checkpoint.Repo) []string {
	var problems []string
	roots := []string{
		filepath.Join(repo.MetadataDir, "log", "adapter"),
		filepath.Join(repo.MetadataDir, "log", "raw"),
		filepath.Join(repo.MetadataDir, "log", "events"),
		filepath.Join(repo.MetadataDir, "log", "manual-checkpoints"),
		filepath.Join(repo.TmpDir, "hooks"),
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if !os.IsNotExist(err) {
					problems = append(problems, fmt.Sprintf("capture path inspection failed for %s: %v", path, err))
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(entry.Name(), ".lock") {
				_, lockErr := filelock.Held(path)
				if lockErr != nil {
					problems = append(problems, fmt.Sprintf("capture lock inspection failed for %s: %v", path, lockErr))
				}
				return nil
			}
			if filepath.Ext(entry.Name()) != ".jsonl" {
				return nil
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				problems = append(problems, fmt.Sprintf("capture log inspection failed for %s: %v", path, openErr))
				return nil
			}
			info, statErr := file.Stat()
			if statErr == nil && info.Size() > 0 {
				last := []byte{0}
				if _, readErr := file.ReadAt(last, info.Size()-1); readErr != nil {
					problems = append(problems, fmt.Sprintf("capture log tail inspection failed for %s: %v", path, readErr))
				} else if last[0] != '\n' {
					problems = append(problems, fmt.Sprintf("capture log has trailing partial record: %s", path))
				}
			}
			_ = file.Close()
			return nil
		})
	}
	return problems
}

func inspectIndex(repo *checkpoint.Repo) []string {
	exists, err := queryindex.Exists(repo.MetadataDir)
	if err != nil {
		return []string{fmt.Sprintf("query index inspection failed: %v", err)}
	}
	if !exists {
		return nil
	}
	store, err := queryindex.Open(repo.MetadataDir)
	if err != nil {
		return []string{fmt.Sprintf("query index inspection failed: %v", err)}
	}
	defer func() { _ = store.Close() }()
	healthy, err := store.StructurallyHealthy()
	if err != nil {
		return []string{fmt.Sprintf("query index inspection failed: %v", err)}
	}
	if !healthy {
		return []string{"query index is corrupt or incompatible; run turnal reindex"}
	}
	return nil
}

func inspectHookFailures(repo *checkpoint.Repo) []string {
	failures, err := adapters.ReadHookFailures(repo.MetadataDir)
	if err != nil {
		return []string{fmt.Sprintf("hook failure inspection failed: %v", err)}
	}
	if len(failures) == 0 {
		return nil
	}
	latest := failures[len(failures)-1]
	return []string{fmt.Sprintf(
		"%d hook capture failure(s) require acknowledgement; latest=%s adapter=%s hook=%s error=%s (run turnal maintenance clear-hook-failures --yes after review)",
		len(failures),
		latest.Time,
		latest.Adapter,
		latest.Hook,
		latest.Error,
	)}
}

func inspectEventLogs(repo *checkpoint.Repo) []string {
	var problems []string
	log := eventlog.Open(repo.MetadataDir)
	sessions, err := log.ListSessions()
	if err != nil {
		return []string{fmt.Sprintf("event log listing failed: %v", err)}
	}
	for _, sessionID := range sessions {
		events, err := log.Read(sessionID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("event log verification failed for session %s: %v", sessionID, err))
			continue
		}
		for _, event := range events {
			if event.Type != primitives.EventTypeCheckpoint {
				continue
			}
			if problem := inspectCheckpointEvent(repo, sessionID, event); problem != "" {
				problems = append(problems, problem)
			}
		}
	}
	return problems
}

func inspectCheckpointEvent(repo *checkpoint.Repo, sessionID primitives.SessionID, event eventlog.Event) string {
	var payload struct {
		Turn      uint64 `json:"turn"`
		Phase     string `json:"phase"`
		CommitSHA string `json:"commit_sha"`
		Ref       string `json:"ref"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Sprintf("checkpoint event payload malformed for session %s seq %s: %v", sessionID, event.Seq, err)
	}
	turnID, err := primitives.NewTurnID(payload.Turn)
	if err != nil {
		return fmt.Sprintf("checkpoint event payload invalid for session %s seq %s: %v", sessionID, event.Seq, err)
	}
	phase, err := primitives.ParseCheckpointPhase(payload.Phase)
	if err != nil {
		return fmt.Sprintf("checkpoint event payload invalid for session %s turn %s seq %s: %v", sessionID, turnID, event.Seq, err)
	}
	ref, err := primitives.ParseCheckpointRef(payload.Ref)
	if err != nil {
		return fmt.Sprintf("checkpoint event payload invalid for session %s turn %s seq %s: %v", sessionID, turnID, event.Seq, err)
	}
	parts, err := ref.Parts()
	if err != nil {
		return fmt.Sprintf("checkpoint event ref invalid for session %s turn %s seq %s: %v", sessionID, turnID, event.Seq, err)
	}
	if parts.SessionID != sessionID || parts.TurnID != turnID || parts.Phase != phase || !parts.HasPhase {
		return fmt.Sprintf("checkpoint event ref mismatch for session %s turn %s seq %s: ref=%s phase=%s", sessionID, turnID, event.Seq, ref, phase)
	}
	commit, err := primitives.ParseCommitSHA(payload.CommitSHA)
	if err != nil {
		return fmt.Sprintf("checkpoint event payload invalid for session %s turn %s seq %s: %v", sessionID, turnID, event.Seq, err)
	}
	refCommit, err := repo.CheckpointCommit(ref)
	if err != nil {
		matches, findErr := repo.FindCheckpointTargets(sessionID, turnID, phase)
		if findErr != nil {
			return fmt.Sprintf("checkpoint event references missing checkpoint ref for session %s turn %s seq %s: %s: %v", sessionID, turnID, event.Seq, ref, err)
		}
		found := false
		for _, match := range matches {
			if match.StreamID == event.StreamID && match.Commit == commit {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("checkpoint event references missing checkpoint ref for session %s turn %s seq %s: %s: %v", sessionID, turnID, event.Seq, ref, err)
		}
		return ""
	}
	if refCommit != commit {
		return fmt.Sprintf("checkpoint event commit mismatch for session %s turn %s seq %s: ref %s points to %s, payload has %s", sessionID, turnID, event.Seq, ref, refCommit, commit)
	}
	return ""
}

func inspectCheckpointRefs(repo *checkpoint.Repo) []string {
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return []string{fmt.Sprintf("checkpoint ref listing failed: %v", err)}
	}
	var problems []string
	for _, info := range infos {
		commit, err := repo.CheckpointCommit(info.Ref)
		if err != nil {
			problems = append(problems, fmt.Sprintf("checkpoint ref %s is unreadable: %v", info.Ref, err))
			continue
		}
		if commit != info.Commit {
			problems = append(problems, fmt.Sprintf("checkpoint ref %s commit mismatch: listed %s resolved %s", info.Ref, info.Commit, commit))
		}
	}
	return problems
}

func inspectCheckpointJournals(repo *checkpoint.Repo) []string {
	journals, err := repo.ListCheckpointJournals()
	if err != nil {
		return []string{fmt.Sprintf("checkpoint journal inspection failed: %v", err)}
	}
	var problems []string
	for _, journal := range journals {
		switch journal.State {
		case "intent":
			problems = append(problems, fmt.Sprintf("checkpoint journal intent pending for session %s turn %s %s", journal.SessionID, journal.TurnID, journal.Phase))
		case "committed":
			if journal.Ref == "" || journal.CommitSHA == "" {
				problems = append(problems, fmt.Sprintf("checkpoint journal committed without ref/commit for session %s turn %s %s", journal.SessionID, journal.TurnID, journal.Phase))
				continue
			}
			commit, err := repo.CheckpointCommit(journal.Ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("checkpoint journal ref %s is unreadable: %v", journal.Ref, err))
				continue
			}
			if commit != journal.CommitSHA {
				problems = append(problems, fmt.Sprintf("checkpoint journal commit mismatch for %s: ref points to %s, journal has %s", journal.Ref, commit, journal.CommitSHA))
			}
		case "finalized":
			problems = append(problems, fmt.Sprintf("checkpoint journal finalized but not cleared for session %s turn %s %s", journal.SessionID, journal.TurnID, journal.Phase))
		}
	}
	return problems
}
