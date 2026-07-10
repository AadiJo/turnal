package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/blame"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
)

const (
	maxEventsPerTurn = 500
	maxPatchBytes    = 512 << 10
	maxPatchLines    = 6000
	maxBlameLines    = 1500
)

type Service struct {
	Repo      *checkpoint.Repo
	codec     keyCodec
	startedAt time.Time
	diffMu    sync.Mutex
	diffCache map[string]checkpoint.DiffSummary
}

func NewService(repo *checkpoint.Repo) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("viewer service requires checkpoint repo")
	}
	return &Service{
		Repo:      repo,
		codec:     newKeyCodec(repo),
		startedAt: time.Now().UTC(),
		diffCache: make(map[string]checkpoint.DiffSummary),
	}, nil
}

type sessionRecord struct {
	stream eventlog.DurableStream
	turns  []turnRecord
	model  string
	branch string
}

type turnRecord struct {
	id      primitives.TurnID
	summary queryindex.TurnEventSummary
	pre     *checkpoint.CheckpointRefInfo
	post    *checkpoint.CheckpointRefInfo
	diff    checkpoint.DiffSummary
}

func (service *Service) Workspace(ctx context.Context) (WorkspaceView, error) {
	streams, err := service.listDurableStreams(ctx)
	if err != nil {
		return WorkspaceView{}, err
	}
	infos, err := service.Repo.ListAllCheckpointRefInfos()
	if err != nil {
		return WorkspaceView{}, err
	}
	status := checkpoint.Inspect(service.Repo.WorkspaceRoot)
	view := WorkspaceView{
		Name:            filepath.Base(service.Repo.WorkspaceRoot.String()),
		Root:            service.Repo.WorkspaceRoot.String(),
		RepoID:          service.Repo.RepoID.String(),
		StoreID:         service.Repo.StoreID.String(),
		WorktreeID:      service.Repo.WorktreeID.String(),
		IndexState:      service.indexState(),
		HistoryState:    "ready",
		Problems:        status.Problems,
		ReadOnly:        true,
		NetworkSilent:   true,
		ViewerStartedAt: service.startedAt,
	}
	if len(status.Problems) > 0 {
		view.HistoryState = "attention"
	}
	type sessionKey struct{ session, stream string }
	type turnKey struct {
		session, stream string
		turn            uint64
	}
	sessionKeys := make(map[sessionKey]struct{})
	turnKeys := make(map[turnKey]struct{})
	for _, stream := range streams {
		sessionKeys[sessionKey{session: stream.SessionID.String(), stream: stream.StreamID.String()}] = struct{}{}
		for _, event := range stream.Events {
			if event.Time.Time.After(view.LastActivity) {
				view.LastActivity = event.Time.Time
			}
			if event.TurnID != nil {
				turnKeys[turnKey{session: stream.SessionID.String(), stream: stream.StreamID.String(), turn: event.TurnID.Uint64()}] = struct{}{}
			}
		}
	}
	for _, info := range infos {
		sessionKeys[sessionKey{session: info.SessionID.String(), stream: info.StreamID.String()}] = struct{}{}
		turnKeys[turnKey{session: info.SessionID.String(), stream: info.StreamID.String(), turn: info.TurnID.Uint64()}] = struct{}{}
	}
	view.SessionCount = len(sessionKeys)
	view.TurnCount = len(turnKeys)
	return view, nil
}

func (service *Service) Sessions(ctx context.Context) ([]SessionSummaryView, error) {
	records, _, err := service.loadRecords(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]SessionSummaryView, 0, len(records))
	for _, record := range records {
		view, err := service.sessionView(record)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if !views[i].StartedAt.Equal(views[j].StartedAt) {
			return views[i].StartedAt.After(views[j].StartedAt)
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}

func (service *Service) SessionTurns(ctx context.Context, key string) (SessionTurnsView, error) {
	identity, err := service.codec.decode(key, resourceSession)
	if err != nil {
		return SessionTurnsView{}, err
	}
	record, err := service.recordForIdentity(ctx, identity)
	if err != nil {
		return SessionTurnsView{}, err
	}
	session, err := service.sessionView(record)
	if err != nil {
		return SessionTurnsView{}, err
	}
	turns := make([]TurnSummaryView, 0, len(record.turns))
	for _, turn := range record.turns {
		view, err := service.turnView(record.stream, turn)
		if err != nil {
			return SessionTurnsView{}, err
		}
		turns = append(turns, view)
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].ID > turns[j].ID })
	return SessionTurnsView{Session: session, Turns: turns}, nil
}

func (service *Service) Turn(ctx context.Context, key string) (TurnDetailView, error) {
	identity, err := service.codec.decode(key, resourceTurn)
	if err != nil {
		return TurnDetailView{}, err
	}
	sessionID, _ := primitives.ParseSessionID(identity.SessionID)
	turnID, _ := primitives.NewTurnID(identity.TurnID)
	streamID, _ := primitives.ParseEventStreamID(identity.StreamID)
	var worktreeID primitives.WorktreeID
	if identity.WorktreeID != "" {
		worktreeID, _ = primitives.ParseWorktreeID(identity.WorktreeID)
	}
	if err := ctx.Err(); err != nil {
		return TurnDetailView{}, err
	}
	recalled, err := recall.NewReader(service.Repo.MetadataDir).RecallTurn(sessionID, turnID, recall.Options{
		WorktreeID: worktreeID,
		StreamID:   streamID,
	})
	if err != nil {
		return TurnDetailView{}, err
	}
	record, err := service.recordForIdentity(ctx, resourceIdentity{
		Version: identity.Version, Kind: string(resourceSession), StoreID: identity.StoreID,
		WorktreeID: identity.WorktreeID, StreamID: identity.StreamID, SessionID: identity.SessionID,
	})
	if err != nil {
		return TurnDetailView{}, err
	}
	var summary TurnSummaryView
	for _, turn := range record.turns {
		if turn.id == turnID {
			summary, err = service.turnView(record.stream, turn)
			break
		}
	}
	if err != nil {
		return TurnDetailView{}, err
	}
	if summary.Key == "" {
		return TurnDetailView{}, fmt.Errorf("turn %d is not present in its canonical stream", identity.TurnID)
	}
	events := recalled.Events
	truncated := len(events) > maxEventsPerTurn
	if truncated {
		events = events[:maxEventsPerTurn]
	}
	eventViews := make([]EventView, 0, len(events))
	for _, event := range events {
		eventViews = append(eventViews, eventView(event))
	}
	return TurnDetailView{
		TurnSummaryView: summary,
		SessionID:       identity.SessionID,
		StreamID:        identity.StreamID,
		Events:          eventViews,
		Truncated:       truncated,
		Identity: map[string]string{
			"worktree": identity.WorktreeID,
			"stream":   identity.StreamID,
			"session":  identity.SessionID,
		},
	}, nil
}

func (service *Service) Diff(ctx context.Context, key string) (DiffSummaryView, error) {
	identity, turn, err := service.turnRecordForKey(ctx, key)
	if err != nil {
		return DiffSummaryView{}, err
	}
	if turn.pre == nil || turn.post == nil {
		return DiffSummaryView{}, fmt.Errorf("turn %d has no complete checkpoint pair", identity.TurnID)
	}
	return DiffSummaryView{
		TurnKey:     key,
		Files:       fileViews(turn.diff.Files),
		Additions:   turn.diff.Additions,
		Deletions:   turn.diff.Deletions,
		BinaryFiles: turn.diff.BinaryFiles,
		PreCommit:   turn.pre.Commit.String(),
		PostCommit:  turn.post.Commit.String(),
		TruthSource: "private checkpoint Git commits",
	}, nil
}

func (service *Service) Patch(ctx context.Context, key, path string) (FilePatchView, error) {
	_, turn, err := service.turnRecordForKey(ctx, key)
	if err != nil {
		return FilePatchView{}, err
	}
	if turn.pre == nil || turn.post == nil {
		return FilePatchView{}, fmt.Errorf("turn has no complete checkpoint pair")
	}
	parsedPath, err := primitives.ParseRepoPath(path)
	if err != nil {
		return FilePatchView{}, err
	}
	if err := ctx.Err(); err != nil {
		return FilePatchView{}, err
	}
	patch, err := service.Repo.DiffRefsPath(turn.pre.Ref, turn.post.Ref, parsedPath.String())
	if err != nil {
		return FilePatchView{}, err
	}
	originalBytes := len(patch)
	originalLines := strings.Count(string(patch), "\n")
	truncated := false
	if len(patch) > maxPatchBytes {
		patch = patch[:maxPatchBytes]
		for !utf8.Valid(patch) && len(patch) > 0 {
			patch = patch[:len(patch)-1]
		}
		truncated = true
	}
	lines := strings.Split(string(patch), "\n")
	if len(lines) > maxPatchLines {
		patch = []byte(strings.Join(lines[:maxPatchLines], "\n"))
		truncated = true
	}
	return FilePatchView{
		Path: parsedPath.String(), Patch: string(patch), Truncated: truncated,
		ByteCount: originalBytes, LineCount: originalLines, LimitBytes: maxPatchBytes,
	}, nil
}

func (service *Service) Blame(ctx context.Context, key, path string, line int) (BlameView, error) {
	identity, err := service.codec.decode(key, resourceTurn)
	if err != nil {
		return BlameView{}, err
	}
	sessionID, _ := primitives.ParseSessionID(identity.SessionID)
	parsedPath, err := primitives.ParseRepoPath(path)
	if err != nil {
		return BlameView{}, err
	}
	if line < 0 {
		return BlameView{}, fmt.Errorf("line must be zero or greater")
	}
	if err := ctx.Err(); err != nil {
		return BlameView{}, err
	}
	result, err := (blame.Engine{Repo: service.Repo, ReadOnly: true}).Compute(blame.Query{
		Path: parsedPath, Line: line, SessionID: sessionID,
	})
	if err != nil {
		return BlameView{}, err
	}
	entries := result.Entries
	truncated := len(entries) > maxBlameLines
	if truncated {
		entries = entries[:maxBlameLines]
	}
	lines := make([]BlameLineView, 0, len(entries))
	for _, entry := range entries {
		lineView := BlameLineView{
			Line: entry.Line, Text: entry.Text, Kind: entry.Origin.Kind,
			TurnID: entry.Origin.TurnID.Uint64(), SessionID: entry.Origin.SessionID.String(),
			Adapter: entry.Origin.Adapter, Prompt: entry.Origin.Prompt,
			ToolNames: entry.Origin.ToolNames, Time: entry.Origin.Time,
		}
		if entry.Origin.Kind == "turn" && entry.Origin.TurnID != 0 {
			streamID, streamErr := service.streamForOrigin(ctx, entry.Origin.SessionID, entry.Origin.TurnID)
			if streamErr == nil {
				lineView.TurnKey, _ = service.codec.encode(resourceTurn, service.Repo.WorktreeID, streamID, entry.Origin.SessionID, entry.Origin.TurnID)
			}
		}
		lines = append(lines, lineView)
	}
	return BlameView{
		Path: result.Path.String(), LatestCommit: result.LatestCommit.String(), LatestTime: result.LatestTime,
		CompleteTurns: result.CompleteTurns, Lines: lines, Warnings: result.Warnings,
		Truncated: truncated, TruthSource: "checkpoint replay with recorded turn associations",
	}, nil
}

func (service *Service) loadRecords(ctx context.Context) ([]sessionRecord, string, error) {
	streams, err := service.listDurableStreams(ctx)
	if err != nil {
		return nil, "unavailable", err
	}
	infos, err := service.Repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, "unavailable", err
	}
	indexState := service.indexState()
	type recordKey struct{ session, stream string }
	records := make(map[recordKey]*sessionRecord)
	ensure := func(sessionID primitives.SessionID, streamID primitives.EventStreamID, worktreeID primitives.WorktreeID) *sessionRecord {
		key := recordKey{session: sessionID.String(), stream: streamID.String()}
		record := records[key]
		if record == nil {
			record = &sessionRecord{stream: eventlog.DurableStream{SessionID: sessionID, StreamID: streamID, WorktreeID: worktreeID}}
			records[key] = record
		}
		return record
	}
	for _, stream := range streams {
		if err := ctx.Err(); err != nil {
			return nil, indexState, err
		}
		record := ensure(stream.SessionID, stream.StreamID, stream.WorktreeID)
		record.stream = stream
		for _, event := range stream.Events {
			if event.Type == primitives.EventTypeSessionStart {
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(event.Payload, &payload)
				if record.model == "" {
					record.model = payload.Model
				}
			}
			if event.Type == primitives.EventTypeCheckpoint {
				var payload struct {
					UserGit struct {
						Branch string `json:"branch"`
					} `json:"user_git"`
				}
				_ = json.Unmarshal(event.Payload, &payload)
				if payload.UserGit.Branch != "" {
					record.branch = strings.TrimPrefix(payload.UserGit.Branch, "refs/heads/")
				}
			}
		}
	}
	for _, info := range infos {
		ensure(info.SessionID, info.StreamID, info.WorktreeID)
	}
	for _, record := range records {
		summaries := queryindex.SummarizeTurnEvents(record.stream.Events)
		turns := make(map[uint64]*turnRecord)
		ensureTurn := func(turnID primitives.TurnID) *turnRecord {
			turn := turns[turnID.Uint64()]
			if turn == nil {
				turn = &turnRecord{id: turnID}
				turns[turnID.Uint64()] = turn
			}
			return turn
		}
		for turnNumber, summary := range summaries {
			turnID, turnErr := primitives.NewTurnID(turnNumber)
			if turnErr == nil {
				ensureTurn(turnID).summary = summary
			}
		}
		for _, info := range infos {
			if info.SessionID != record.stream.SessionID || info.StreamID != record.stream.StreamID {
				continue
			}
			turn := ensureTurn(info.TurnID)
			copyInfo := info
			switch info.Phase {
			case primitives.CheckpointPhasePre:
				turn.pre = &copyInfo
			case primitives.CheckpointPhasePost:
				turn.post = &copyInfo
			}
		}
		for _, turn := range turns {
			if turn.pre != nil && turn.post != nil {
				diff, diffErr := service.cachedDiff(turn.pre.Ref, turn.post.Ref)
				if diffErr != nil {
					return nil, indexState, fmt.Errorf("summarize turn %s:%s diff: %w", record.stream.SessionID, turn.id, diffErr)
				}
				turn.diff = diff
			}
			record.turns = append(record.turns, *turn)
		}
		sort.Slice(record.turns, func(i, j int) bool { return record.turns[i].id < record.turns[j].id })
	}
	result := make([]sessionRecord, 0, len(records))
	for _, record := range records {
		result = append(result, *record)
	}
	return result, indexState, nil
}

func (service *Service) listDurableStreams(ctx context.Context) ([]eventlog.DurableStream, error) {
	const attempts = 5
	for attempt := 0; attempt < attempts; attempt++ {
		streams, err := eventlog.ListDurableStreams(service.Repo.MetadataDir)
		if err == nil {
			return streams, nil
		}
		if !strings.Contains(err.Error(), "trailing partial line") {
			return nil, err
		}
		held, lockErr := activeEventWriter(service.Repo.MetadataDir)
		if lockErr != nil {
			return nil, lockErr
		}
		if !held {
			return nil, err
		}
		timer := time.NewTimer(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("recording is still appending history; retry the viewer read")
}

func activeEventWriter(metadataDir string) (bool, error) {
	root := filepath.Join(metadataDir, "log", "events")
	active := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl.lock") {
			return nil
		}
		held, err := filelock.Held(path)
		if err != nil {
			return err
		}
		if held {
			active = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect active event writer: %w", err)
	}
	return active, nil
}

func (service *Service) indexState() string {
	exists, err := queryindex.Exists(service.Repo.MetadataDir)
	if err != nil || !exists {
		return "missing"
	}
	store, err := queryindex.Open(service.Repo.MetadataDir)
	if err != nil {
		return "unavailable"
	}
	defer store.Close()
	healthy, err := store.Healthy()
	if err != nil {
		return "unavailable"
	}
	if !healthy {
		return "stale"
	}
	return "healthy"
}

func (service *Service) cachedDiff(pre, post primitives.CheckpointRef) (checkpoint.DiffSummary, error) {
	key := pre.String() + "\x00" + post.String()
	service.diffMu.Lock()
	if cached, ok := service.diffCache[key]; ok {
		service.diffMu.Unlock()
		return cached, nil
	}
	service.diffMu.Unlock()
	summary, err := service.Repo.DiffStatRefs(pre, post)
	if err != nil {
		return checkpoint.DiffSummary{}, err
	}
	service.diffMu.Lock()
	service.diffCache[key] = summary
	service.diffMu.Unlock()
	return summary, nil
}

func (service *Service) sessionView(record sessionRecord) (SessionSummaryView, error) {
	key, err := service.codec.encode(resourceSession, record.stream.WorktreeID, record.stream.StreamID, record.stream.SessionID, 0)
	if err != nil {
		return SessionSummaryView{}, err
	}
	view := SessionSummaryView{
		Key: key, ID: record.stream.SessionID.String(), StreamID: record.stream.StreamID.String(),
		WorktreeID: record.stream.WorktreeID.String(), Model: record.model, Branch: record.branch,
		EventCount: len(record.stream.Events), TurnCount: len(record.turns), Status: "complete",
	}
	seenFiles := make(map[string]struct{})
	for _, event := range record.stream.Events {
		if view.Adapter == "" && event.Adapter != "" {
			view.Adapter = event.Adapter.String()
		}
		if view.StartedAt.IsZero() || event.Time.Time.Before(view.StartedAt) {
			view.StartedAt = event.Time.Time
		}
		if event.Time.Time.After(view.FinishedAt) {
			view.FinishedAt = event.Time.Time
		}
		if event.Type == primitives.EventTypeError {
			view.ErrorCount++
		}
	}
	for _, turn := range record.turns {
		if turn.pre != nil && turn.post != nil {
			view.CompleteTurns++
		} else {
			view.Status = "active"
		}
		if view.PromptPreview == "" {
			view.PromptPreview = turn.summary.Prompt
		}
		view.Additions += turn.diff.Additions
		view.Deletions += turn.diff.Deletions
		for _, file := range turn.diff.Files {
			seenFiles[file.Path] = struct{}{}
		}
	}
	view.FileCount = len(seenFiles)
	if view.ErrorCount > 0 {
		view.Status = "attention"
	}
	return view, nil
}

func (service *Service) turnView(stream eventlog.DurableStream, turn turnRecord) (TurnSummaryView, error) {
	key, err := service.codec.encode(resourceTurn, stream.WorktreeID, stream.StreamID, stream.SessionID, turn.id)
	if err != nil {
		return TurnSummaryView{}, err
	}
	view := TurnSummaryView{
		Key: key, ID: turn.id.Uint64(), Status: "active", Adapter: turn.summary.Adapter,
		StartedAt: turn.summary.First, FinishedAt: turn.summary.Last,
		Prompt: turn.summary.Prompt, Assistant: turn.summary.Assistant, ToolNames: turn.summary.ToolNames,
		EventCount: turn.summary.Count, Files: fileViews(turn.diff.Files),
		Additions: turn.diff.Additions, Deletions: turn.diff.Deletions,
	}
	view.ErrorCount = turn.summary.TypeCounts[primitives.EventTypeError]
	if turn.pre != nil {
		view.PreCommit = turn.pre.Commit.String()
	}
	if turn.post != nil {
		view.PostCommit = turn.post.Commit.String()
	}
	view.Checkpointed = turn.pre != nil && turn.post != nil
	if view.Checkpointed {
		view.Status = "complete"
	}
	if view.ErrorCount > 0 {
		view.Status = "attention"
	}
	return view, nil
}

func (service *Service) recordForIdentity(ctx context.Context, identity resourceIdentity) (sessionRecord, error) {
	records, _, err := service.loadRecords(ctx)
	if err != nil {
		return sessionRecord{}, err
	}
	for _, record := range records {
		if record.stream.SessionID.String() == identity.SessionID && record.stream.StreamID.String() == identity.StreamID &&
			(identity.WorktreeID == "" || record.stream.WorktreeID.String() == identity.WorktreeID) {
			return record, nil
		}
	}
	return sessionRecord{}, fmt.Errorf("resource no longer exists in this Turnal store")
}

func (service *Service) turnRecordForKey(ctx context.Context, key string) (resourceIdentity, turnRecord, error) {
	identity, err := service.codec.decode(key, resourceTurn)
	if err != nil {
		return resourceIdentity{}, turnRecord{}, err
	}
	record, err := service.recordForIdentity(ctx, resourceIdentity{
		Version: identity.Version, Kind: string(resourceSession), StoreID: identity.StoreID,
		WorktreeID: identity.WorktreeID, StreamID: identity.StreamID, SessionID: identity.SessionID,
	})
	if err != nil {
		return resourceIdentity{}, turnRecord{}, err
	}
	for _, turn := range record.turns {
		if turn.id.Uint64() == identity.TurnID {
			return identity, turn, nil
		}
	}
	return resourceIdentity{}, turnRecord{}, fmt.Errorf("turn no longer exists in this canonical stream")
}

func (service *Service) streamForOrigin(ctx context.Context, sessionID primitives.SessionID, turnID primitives.TurnID) (primitives.EventStreamID, error) {
	records, _, err := service.loadRecords(ctx)
	if err != nil {
		return "", err
	}
	var match primitives.EventStreamID
	for _, record := range records {
		if record.stream.SessionID != sessionID {
			continue
		}
		for _, turn := range record.turns {
			if turn.id == turnID {
				if match != "" && match != record.stream.StreamID {
					return "", fmt.Errorf("turn origin is ambiguous across streams")
				}
				match = record.stream.StreamID
			}
		}
	}
	if match == "" {
		return "", fmt.Errorf("turn origin stream not found")
	}
	return match, nil
}

func fileViews(files []checkpoint.DiffFileStat) []FileView {
	views := make([]FileView, 0, len(files))
	for _, file := range files {
		views = append(views, FileView{Path: file.Path, Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary})
	}
	return views
}

func eventView(event eventlog.Event) EventView {
	view := EventView{
		Sequence: event.Seq.Uint64(), Type: event.Type.String(), Time: event.Time.Time,
		Kind: "system", Title: eventTitle(event.Type),
	}
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	switch event.Type {
	case primitives.EventTypePromptUser:
		view.Kind, view.Body = "prompt", firstPayloadString(payload, "text", "prompt", "message")
	case primitives.EventTypeAssistantMessage:
		view.Kind, view.Body = "assistant", firstPayloadString(payload, "text", "message", "content")
	case primitives.EventTypeToolCall:
		view.Kind = "tool"
		view.ToolName = firstPayloadString(payload, "tool_name", "name", "tool")
		view.Title = view.ToolName
		if view.Title == "" {
			view.Title = "Tool call"
		}
		view.Body = compactPayloadPreview(payload, "tool_name", "name", "tool")
		view.Sensitive = true
	case primitives.EventTypeToolResult:
		view.Kind = "result"
		view.ToolName = firstPayloadString(payload, "tool_name", "name", "tool")
		view.Body = firstPayloadString(payload, "result", "output", "text", "content")
		if view.Body == "" {
			view.Body = compactPayloadPreview(payload)
		}
		view.Sensitive = true
	case primitives.EventTypeError:
		view.Kind, view.Body = "error", firstPayloadString(payload, "error", "message", "text")
	case primitives.EventTypeCheckpoint:
		view.Kind = "checkpoint"
		phase := firstPayloadString(payload, "phase")
		commit := firstPayloadString(payload, "commit_sha")
		view.Title = strings.Title(phase) + " checkpoint"
		if len(commit) > 10 {
			commit = commit[:10]
		}
		view.Body = commit
	}
	view.Body = truncateText(view.Body, 12000)
	return view
}

func eventTitle(eventType primitives.EventType) string {
	switch eventType {
	case primitives.EventTypeSessionStart:
		return "Session started"
	case primitives.EventTypeTurnStart:
		return "Turn started"
	case primitives.EventTypePromptUser:
		return "User prompt"
	case primitives.EventTypeAssistantMessage:
		return "Assistant response"
	case primitives.EventTypeToolCall:
		return "Tool call"
	case primitives.EventTypeToolResult:
		return "Tool result"
	case primitives.EventTypeTurnFinish:
		return "Turn finished"
	case primitives.EventTypeCheckpoint:
		return "Checkpoint"
	case primitives.EventTypeError:
		return "Captured error"
	default:
		return strings.ReplaceAll(eventType.String(), ".", " ")
	}
}

func firstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		default:
			data, _ := json.Marshal(typed)
			if len(data) > 0 && string(data) != "null" {
				return string(data)
			}
		}
	}
	return ""
}

func compactPayloadPreview(payload map[string]any, excluded ...string) string {
	copyPayload := make(map[string]any, len(payload))
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		excludedSet[key] = struct{}{}
	}
	for key, value := range payload {
		if _, skip := excludedSet[key]; !skip {
			copyPayload[key] = value
		}
	}
	data, _ := json.MarshalIndent(copyPayload, "", "  ")
	return string(data)
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value + "\n[content truncated]"
}
