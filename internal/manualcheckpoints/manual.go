package manualcheckpoints

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

const eventSessionText = "workspace"

type Payload struct {
	Origin       string `json:"origin"`
	Message      string `json:"message,omitempty"`
	CheckpointID string `json:"checkpoint_id"`
	WorktreeID   string `json:"worktree_id"`
	CommitSHA    string `json:"commit_sha"`
	Ref          string `json:"ref"`
	CanonicalRef string `json:"canonical_ref"`
}

type Save struct {
	Event      eventlog.Event
	Message    string
	Checkpoint checkpoint.CheckpointRefInfo
	Warnings   []string
}

func Append(repo *checkpoint.Repo, created checkpoint.Checkpoint, message string) (eventlog.Event, error) {
	if repo == nil {
		return eventlog.Event{}, fmt.Errorf("append manual checkpoint event: repo is required")
	}
	parts, err := created.Ref.Parts()
	if err != nil || !parts.Manual {
		return eventlog.Event{}, fmt.Errorf("append manual checkpoint event: manual checkpoint ref required")
	}
	if parts.CheckpointID != created.ID || parts.WorktreeID != created.WorktreeID || created.WorktreeID != repo.WorktreeID {
		return eventlog.Event{}, fmt.Errorf("append manual checkpoint event: checkpoint identity mismatch")
	}
	payload, err := json.Marshal(Payload{
		Origin:       "manual",
		Message:      message,
		CheckpointID: created.ID.String(),
		WorktreeID:   created.WorktreeID.String(),
		CommitSHA:    created.Commit.String(),
		Ref:          created.Ref.String(),
		CanonicalRef: created.CanonicalRef.String(),
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("marshal manual checkpoint event: %w", err)
	}

	sourceID := "turnal:checkpoint:manual:" + created.ID.String()
	event, err := AppendEvent(repo, primitives.EventTypeCheckpoint, sourceID, created.Ref.String(), payload)
	if err != nil {
		return eventlog.Event{}, err
	}
	var recorded Payload
	if err := json.Unmarshal(event.Payload, &recorded); err != nil || recorded != (Payload{
		Origin: "manual", Message: message, CheckpointID: created.ID.String(), WorktreeID: created.WorktreeID.String(),
		CommitSHA: created.Commit.String(), Ref: created.Ref.String(), CanonicalRef: created.CanonicalRef.String(),
	}) {
		return eventlog.Event{}, fmt.Errorf("manual checkpoint event source collision for %s", created.ID)
	}
	return event, nil
}

// AppendEvent appends a workspace-scoped event to the same integrity-checked
// stream used for manual checkpoints. It is intentionally separate from agent
// session streams.
func AppendEvent(repo *checkpoint.Repo, eventType primitives.EventType, sourceID string, rawRef string, payload json.RawMessage) (eventlog.Event, error) {
	if repo == nil {
		return eventlog.Event{}, fmt.Errorf("append workspace event: repo is required")
	}
	log, sessionID := eventLog(repo, repo.WorktreeID, repo.EventProducerID, false)
	if existing, ok, err := log.FindSourceID(sessionID, sourceID); err != nil {
		return eventlog.Event{}, err
	} else if ok {
		return existing, nil
	}
	return log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      eventType,
		Adapter:   primitives.AdapterManual,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload:   payload,
	})
}

func ReadEvents(repo *checkpoint.Repo, allWorktrees bool) ([]eventlog.Event, error) {
	if repo == nil {
		return nil, fmt.Errorf("read workspace events: repo is required")
	}
	worktrees := []primitives.WorktreeID{repo.WorktreeID}
	if allWorktrees {
		var err error
		worktrees, err = eventWorktrees(repo)
		if err != nil {
			return nil, err
		}
	}
	var result []eventlog.Event
	for _, worktreeID := range worktrees {
		log, sessionID := eventLog(repo, worktreeID, "", true)
		events, err := log.Read(sessionID)
		if err != nil {
			return nil, fmt.Errorf("read workspace events for worktree %s: %w", worktreeID, err)
		}
		result = append(result, events...)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].Time.Time.Equal(result[j].Time.Time) {
			return result[i].Time.Time.Before(result[j].Time.Time)
		}
		if result[i].StreamID != result[j].StreamID {
			return result[i].StreamID.String() < result[j].StreamID.String()
		}
		return result[i].Seq.Uint64() < result[j].Seq.Uint64()
	})
	return result, nil
}

func Read(repo *checkpoint.Repo, allWorktrees bool) ([]Save, error) {
	if repo == nil {
		return nil, fmt.Errorf("read manual checkpoints: repo is required")
	}
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	byRef := make(map[string]checkpoint.CheckpointRefInfo)
	for _, info := range infos {
		if info.Manual {
			byRef[info.Ref.String()] = info
		}
	}

	var saves []Save
	seenRefs := make(map[string]struct{})
	events, err := ReadEvents(repo, allWorktrees)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		save, err := saveFromEvent(event, byRef)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenRefs[save.Checkpoint.Ref.String()]; duplicate {
			return nil, fmt.Errorf("duplicate manual checkpoint event for %s", save.Checkpoint.Ref)
		}
		seenRefs[save.Checkpoint.Ref.String()] = struct{}{}
		saves = append(saves, save)
	}
	for ref, info := range byRef {
		if _, ok := seenRefs[ref]; ok {
			continue
		}
		if !allWorktrees && info.WorktreeID != repo.WorktreeID {
			continue
		}
		saves = append(saves, Save{
			Checkpoint: info,
			Warnings:   []string{"manual checkpoint event is missing; the checkpoint remains recoverable by hash"},
		})
	}
	sort.SliceStable(saves, func(i, j int) bool {
		left, right := saves[i].Checkpoint.Time, saves[j].Checkpoint.Time
		if !saves[i].Event.Time.Time.IsZero() {
			left = saves[i].Event.Time.Time
		}
		if !saves[j].Event.Time.Time.IsZero() {
			right = saves[j].Event.Time.Time
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return saves[i].Checkpoint.Ref.String() < saves[j].Checkpoint.Ref.String()
	})
	return saves, nil
}

func saveFromEvent(event eventlog.Event, byRef map[string]checkpoint.CheckpointRefInfo) (Save, error) {
	var payload Payload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s payload malformed: %w", event.Seq, err)
	}
	if payload.Origin != "manual" {
		return Save{}, fmt.Errorf("manual checkpoint event %s origin invariant failed: %q", event.Seq, payload.Origin)
	}
	if event.TurnID != nil || event.Adapter != primitives.AdapterManual {
		return Save{}, fmt.Errorf("manual checkpoint event %s provenance invariant failed", event.Seq)
	}
	if !utf8.ValidString(payload.Message) || len(payload.Message) > 4096 {
		return Save{}, fmt.Errorf("manual checkpoint event %s message invariant failed", event.Seq)
	}
	checkpointID, err := primitives.ParseCheckpointID(payload.CheckpointID)
	if err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s checkpoint id invariant failed: %w", event.Seq, err)
	}
	worktreeID, err := primitives.ParseWorktreeID(payload.WorktreeID)
	if err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s worktree invariant failed: %w", event.Seq, err)
	}
	commit, err := primitives.ParseCommitSHA(payload.CommitSHA)
	if err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s commit invariant failed: %w", event.Seq, err)
	}
	ref, err := primitives.ParseCheckpointRef(payload.Ref)
	if err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s ref invariant failed: %w", event.Seq, err)
	}
	parts, err := ref.Parts()
	if err != nil || !parts.Manual || parts.CheckpointID != checkpointID || parts.WorktreeID != worktreeID {
		return Save{}, fmt.Errorf("manual checkpoint event %s ref identity mismatch: %s", event.Seq, ref)
	}
	canonicalRef, err := primitives.ParseCheckpointRef(payload.CanonicalRef)
	if err != nil {
		return Save{}, fmt.Errorf("manual checkpoint event %s canonical ref invariant failed: %w", event.Seq, err)
	}
	canonicalParts, err := canonicalRef.Parts()
	if err != nil || !canonicalParts.Canonical || canonicalParts.CheckpointID != checkpointID {
		return Save{}, fmt.Errorf("manual checkpoint event %s canonical ref identity mismatch: %s", event.Seq, canonicalRef)
	}
	info, ok := byRef[ref.String()]
	if !ok {
		return Save{}, fmt.Errorf("manual checkpoint event %s references missing checkpoint %s", event.Seq, ref)
	}
	if info.ID != checkpointID || info.WorktreeID != worktreeID || info.Commit != commit || info.CanonicalRef != canonicalRef {
		return Save{}, fmt.Errorf("manual checkpoint event %s checkpoint metadata mismatch for %s", event.Seq, ref)
	}
	if event.WorktreeID != "" && event.WorktreeID != worktreeID {
		return Save{}, fmt.Errorf("manual checkpoint event %s stream worktree mismatch: event=%s payload=%s", event.Seq, event.WorktreeID, worktreeID)
	}
	if event.RawRef != ref.String() || event.SourceID != "turnal:checkpoint:manual:"+checkpointID.String() {
		return Save{}, fmt.Errorf("manual checkpoint event %s source invariant failed", event.Seq)
	}
	return Save{Event: event, Message: payload.Message, Checkpoint: info}, nil
}

func eventLog(repo *checkpoint.Repo, worktreeID primitives.WorktreeID, producerID primitives.EventProducerID, aggregate bool) (eventlog.Log, primitives.SessionID) {
	sessionID, err := primitives.ParseSessionID(eventSessionText)
	if err != nil {
		panic(err)
	}
	return eventlog.Log{
		Dir:           filepath.Join(repo.MetadataDir, "log", "manual-checkpoints", worktreeID.String(), "events"),
		WorkspaceRoot: repo.WorkspaceRoot.String(),
		RepoID:        repo.RepoID,
		StoreID:       repo.StoreID,
		WorktreeID:    worktreeID,
		ProducerID:    producerID,
		Aggregate:     aggregate,
	}, sessionID
}

func eventWorktrees(repo *checkpoint.Repo) ([]primitives.WorktreeID, error) {
	dir := filepath.Join(repo.MetadataDir, "log", "manual-checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read manual checkpoint event directory: %w", err)
	}
	var worktrees []primitives.WorktreeID
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "streams" {
			continue
		}
		worktreeID, err := primitives.ParseWorktreeID(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("manual checkpoint event directory invariant failed for %s: %w", entry.Name(), err)
		}
		worktrees = append(worktrees, worktreeID)
	}
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].String() < worktrees[j].String() })
	return worktrees, nil
}
