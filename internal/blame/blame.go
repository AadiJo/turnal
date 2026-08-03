package blame

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

func (engine Engine) Compute(query Query) (Result, error) {
	if engine.Repo == nil {
		return Result{}, fmt.Errorf("blame requires checkpoint repo")
	}
	if query.Path == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	if query.Line < 0 {
		return Result{}, ErrInvalidLine
	}

	history, err := engine.observeHistory("")
	if err != nil {
		return Result{}, err
	}
	turns := history.Complete
	if query.SessionID != "" {
		turns = make([]completeTurn, 0, len(history.Complete))
		for _, turn := range history.Complete {
			if turn.SessionID == query.SessionID {
				turns = append(turns, turn)
			}
		}
	}
	if len(turns) == 0 {
		return Result{}, ErrNoHistory
	}

	path := query.Path.String()
	latest := turns[len(turns)-1].Post
	historyKey := blameHistoryKey(history.Complete, history.Incomplete)
	concurrent := concurrentTurnAttributions(history.Complete, history.Incomplete)

	store := engine.openBlameCache()
	if store != nil {
		defer store.Close()
		cached, found, err := store.LoadBlameCache(queryindex.BlameCacheQuery{
			ScopeSession:  query.SessionID,
			Path:          query.Path,
			HistoryKey:    historyKey,
			LatestRef:     latest.Ref,
			LatestCommit:  latest.Commit,
			CompleteTurns: len(turns),
			Line:          query.Line,
		})
		if err == nil && found {
			if err := engine.validateCachedEvidence(turns, concurrent); err != nil {
				return Result{}, err
			}
			return engine.resultFromCache(query, cached, turns)
		}
	}

	origins, replayedBytes, currentExists, warnings, err := engine.replayPath(path, turns, concurrent, query.SessionID == "")
	if err != nil {
		return Result{}, err
	}

	finalBytes, finalExists, err := engine.Repo.CommitFileBytesIfExists(latest.Commit, path)
	if err != nil {
		return Result{}, err
	}
	if !finalExists || !currentExists {
		return Result{}, fmt.Errorf("%w: %s", ErrFileNotFound, path)
	}
	if !bytes.Equal(replayedBytes, finalBytes) {
		return Result{}, fmt.Errorf("blame reconstruction for %s does not match the latest checkpoint", path)
	}
	if looksBinary(finalBytes) {
		return Result{}, fmt.Errorf("%w: %s", ErrBinaryFile, path)
	}

	finalLines := splitLines(finalBytes)
	if len(finalLines) != len(origins) {
		return Result{}, fmt.Errorf("blame reconstruction for %s has %d lines, latest checkpoint has %d", path, len(origins), len(finalLines))
	}
	if query.Line > len(finalLines) {
		return Result{}, fmt.Errorf("%w: %s:%d", ErrLineNotFound, path, query.Line)
	}

	allEntries := entriesForLines(finalLines, origins, 0)
	if store != nil {
		_ = store.SaveBlameCache(queryindex.BlameCacheSnapshot{
			ScopeSession:  query.SessionID,
			Path:          query.Path,
			HistoryKey:    historyKey,
			LatestRef:     latest.Ref,
			LatestCommit:  latest.Commit,
			LatestTime:    latest.Time,
			CompleteTurns: len(turns),
			LineCount:     len(finalLines),
			Entries:       cacheEntriesFromBlame(allEntries),
			Warnings:      warnings,
		})
	}

	return Result{
		Path:          query.Path,
		LatestRef:     latest.Ref,
		LatestCommit:  latest.Commit,
		LatestTime:    latest.Time,
		Sessions:      sessionSummariesForTurns(turns),
		Entries:       filterEntries(allEntries, query.Line),
		Warnings:      warnings,
		CompleteTurns: len(turns),
	}, nil
}

func (engine Engine) validateCachedEvidence(turns []completeTurn, concurrent concurrentTurnAttribution) error {
	seen := make(map[primitives.CommitSHA]struct{})
	validate := func(commit primitives.CommitSHA, description string) error {
		if commit == "" {
			return nil
		}
		if _, ok := seen[commit]; ok {
			return nil
		}
		seen[commit] = struct{}{}
		if err := engine.Repo.ValidateCommit(commit); err != nil {
			return fmt.Errorf("validate cached %s at %s: %w", description, commit, err)
		}
		return nil
	}
	for _, turn := range turns {
		turnLabel := fmt.Sprintf("checkpoint for %s:turn:%s", turn.SessionID, turn.TurnID)
		if err := validate(turn.Pre.Commit, "pre "+turnLabel); err != nil {
			return err
		}
		if err := validate(turn.Post.Commit, "post "+turnLabel); err != nil {
			return err
		}
		if fact, ok := concurrent[completeTurnIdentity(turn)]; ok && fact.Baseline != nil {
			if err := validate(fact.Baseline.Commit, "concurrent baseline checkpoint "+fact.Baseline.Ref.String()); err != nil {
				return err
			}
		}
		for _, event := range turn.Records {
			switch event.Type {
			case primitives.EventTypeToolCall:
				var payload capturedToolCall
				if json.Unmarshal(event.Payload, &payload) == nil {
					if payload.PreSnapshot != nil {
						description := fmt.Sprintf("action snapshot %s", payload.PreSnapshot.Ref)
						if err := validate(payload.PreSnapshot.Commit, description); err != nil {
							return err
						}
					}
				}
			case primitives.EventTypeToolResult:
				var payload capturedToolResult
				if json.Unmarshal(event.Payload, &payload) == nil {
					if payload.PostSnapshot != nil {
						description := fmt.Sprintf("action snapshot %s", payload.PostSnapshot.Ref)
						if err := validate(payload.PostSnapshot.Commit, description); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func (engine Engine) openBlameCache() *queryindex.Store {
	exists, err := queryindex.Exists(engine.Repo.MetadataDir)
	if err != nil || !exists {
		return nil
	}
	store, err := queryindex.Open(engine.Repo.MetadataDir)
	if err != nil {
		return nil
	}
	healthy, err := store.Healthy()
	if err != nil || !healthy {
		_ = store.Close()
		return nil
	}
	return store
}

func (engine Engine) resultFromCache(query Query, cached queryindex.BlameCacheSnapshot, turns []completeTurn) (Result, error) {
	if query.Line > cached.LineCount {
		return Result{}, fmt.Errorf("%w: %s:%d", ErrLineNotFound, query.Path, query.Line)
	}

	entries := make([]Entry, 0, len(cached.Entries))
	for _, cachedEntry := range cached.Entries {
		entries = append(entries, Entry{
			Line:   cachedEntry.Line,
			Text:   cachedEntry.Text,
			Origin: engine.hydrateCachedOrigin(originFromCache(cachedEntry.Origin), turns),
		})
	}

	return Result{
		Path:          query.Path,
		LatestRef:     cached.LatestRef,
		LatestCommit:  cached.LatestCommit,
		LatestTime:    cached.LatestTime,
		Sessions:      sessionSummariesForTurns(turns),
		Entries:       entries,
		Warnings:      cached.Warnings,
		CompleteTurns: cached.CompleteTurns,
	}, nil
}

func sessionSummariesForTurns(turns []completeTurn) []SessionSummary {
	type builder struct {
		id          primitives.SessionID
		adapter     string
		adapterTurn uint64
		startedAt   time.Time
	}

	builders := make(map[string]*builder)
	for _, turn := range turns {
		key := turn.SessionID.String()
		current := builders[key]
		if current == nil {
			current = &builder{id: turn.SessionID}
			builders[key] = current
		}

		at := completeTurnDisplayTime(turn)
		if !at.IsZero() && (current.startedAt.IsZero() || at.Before(current.startedAt)) {
			current.startedAt = at
		}
		if turn.Events.Adapter != "" && turn.TurnID.Uint64() >= current.adapterTurn {
			current.adapter = turn.Events.Adapter
			current.adapterTurn = turn.TurnID.Uint64()
		}
	}

	summaries := make([]SessionSummary, 0, len(builders))
	for _, current := range builders {
		summaries = append(summaries, SessionSummary{
			ID:        current.id,
			Adapter:   current.adapter,
			StartedAt: current.startedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ID.String() < summaries[j].ID.String()
	})
	return summaries
}

func completeTurnDisplayTime(turn completeTurn) time.Time {
	switch {
	case !turn.Post.Time.IsZero():
		return turn.Post.Time
	case !turn.Pre.Time.IsZero():
		return turn.Pre.Time
	case !turn.Events.Last.IsZero():
		return turn.Events.Last
	case !turn.Events.First.IsZero():
		return turn.Events.First
	default:
		return time.Time{}
	}
}

func (engine Engine) hydrateCachedOrigin(origin Origin, turns []completeTurn) Origin {
	if origin.Kind != "turn" || origin.SessionID == "" || origin.TurnID == 0 {
		return origin
	}
	for _, turn := range turns {
		if turn.SessionID == origin.SessionID && turn.TurnID == origin.TurnID {
			hydrated := turnOrigin(turn)
			hydrated.ActionTool = origin.ActionTool
			hydrated.ActionAgentID = origin.ActionAgentID
			hydrated.ActionAgentType = origin.ActionAgentType
			hydrated.Intent = origin.Intent
			if !origin.Time.IsZero() {
				hydrated.Time = origin.Time
			}
			return hydrated
		}
	}
	return origin
}

func entriesForLines(lines []string, origins []Origin, line int) []Entry {
	entries := make([]Entry, 0, len(lines))
	for index, text := range lines {
		lineNumber := index + 1
		if line != 0 && line != lineNumber {
			continue
		}
		entries = append(entries, Entry{
			Line:   lineNumber,
			Text:   text,
			Origin: origins[index],
		})
	}
	return entries
}

func filterEntries(entries []Entry, line int) []Entry {
	if line == 0 {
		return entries
	}
	for _, entry := range entries {
		if entry.Line == line {
			return []Entry{entry}
		}
	}
	return nil
}

func cacheEntriesFromBlame(entries []Entry) []queryindex.BlameCacheEntry {
	cached := make([]queryindex.BlameCacheEntry, 0, len(entries))
	for _, entry := range entries {
		cached = append(cached, queryindex.BlameCacheEntry{
			Line:   entry.Line,
			Text:   entry.Text,
			Origin: originToCache(entry.Origin),
		})
	}
	return cached
}

func originToCache(origin Origin) queryindex.BlameCacheOrigin {
	return queryindex.BlameCacheOrigin{
		Kind:            origin.Kind,
		SessionID:       origin.SessionID,
		TurnID:          origin.TurnID,
		CheckpointRef:   origin.CheckpointRef,
		Commit:          origin.Commit,
		Time:            origin.Time,
		Adapter:         origin.Adapter,
		Prompt:          origin.Prompt,
		ToolNames:       append([]string(nil), origin.ToolNames...),
		ActionTool:      origin.ActionTool,
		ActionAgentID:   origin.ActionAgentID,
		ActionAgentType: origin.ActionAgentType,
		Intent:          origin.Intent,
	}
}

func originFromCache(origin queryindex.BlameCacheOrigin) Origin {
	return Origin{
		Kind:            origin.Kind,
		SessionID:       origin.SessionID,
		TurnID:          origin.TurnID,
		CheckpointRef:   origin.CheckpointRef,
		Commit:          origin.Commit,
		Time:            origin.Time,
		Adapter:         origin.Adapter,
		Prompt:          origin.Prompt,
		ToolNames:       append([]string(nil), origin.ToolNames...),
		ActionTool:      origin.ActionTool,
		ActionAgentID:   origin.ActionAgentID,
		ActionAgentType: origin.ActionAgentType,
		Intent:          origin.Intent,
	}
}

func blameHistoryKey(turns []completeTurn, incomplete []incompleteTurn) string {
	hash := sha256.New()
	for _, turn := range turns {
		writeHistoryField(hash, turn.SessionID.String())
		writeHistoryField(hash, turn.TurnID.String())
		writeHistoryField(hash, turn.Pre.Ref.String())
		writeHistoryField(hash, turn.Pre.Commit.String())
		writeHistoryField(hash, turn.Post.Ref.String())
		writeHistoryField(hash, turn.Post.Commit.String())
		writeHistoryField(hash, turn.Post.Time.UTC().Format(time.RFC3339Nano))
		for _, event := range turn.Records {
			writeHistoryField(hash, event.Hash.String())
		}
	}
	for _, turn := range incomplete {
		writeHistoryField(hash, "incomplete")
		writeHistoryField(hash, turn.SessionID.String())
		writeHistoryField(hash, turn.TurnID.String())
		writeHistoryField(hash, turn.Pre.Ref.String())
		writeHistoryField(hash, turn.Pre.Commit.String())
		for _, event := range turn.Records {
			writeHistoryField(hash, event.Hash.String())
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeHistoryField(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

func (engine Engine) replayPath(path string, turns []completeTurn, concurrent concurrentTurnAttribution, collapseConcurrent bool) ([]Origin, []byte, bool, []string, error) {
	segments, warnings, err := engine.changeSegments(turns, concurrent, collapseConcurrent)
	if err != nil {
		return nil, nil, false, warnings, err
	}
	steps, err := engine.replaySteps(path, segments)
	if err != nil {
		return nil, nil, false, warnings, err
	}

	baselineInfo := segments[0].baselineInfo()
	baseline := baselineOrigin(baselineInfo)
	initialBytes, exists, err := engine.Repo.CommitFileBytesIfExists(segments[0].PreCommit, steps[0].PrePath)
	if err != nil {
		return nil, nil, false, warnings, err
	}
	if exists && looksBinary(initialBytes) {
		return nil, nil, false, warnings, fmt.Errorf("%w: %s", ErrBinaryFile, steps[0].PrePath)
	}

	origins := originsForLines(splitLines(initialBytes), baseline)
	currentExists := exists
	currentBytes := initialBytes

	for _, step := range steps {
		segment := step.Segment
		preBytes, preExists, err := engine.Repo.CommitFileBytesIfExists(segment.PreCommit, step.PrePath)
		if err != nil {
			return nil, nil, false, warnings, err
		}
		if looksBinary(preBytes) {
			return nil, nil, false, warnings, fmt.Errorf("%w: %s before %s", ErrBinaryFile, step.PrePath, segmentLabel(segment))
		}

		preLines := splitLines(preBytes)
		if currentExists != preExists || !bytes.Equal(currentBytes, preBytes) {
			warnings = append(warnings, fmt.Sprintf("history for %s was resynchronized before %s", step.PrePath, segmentLabel(segment)))
			origins = originsForLines(preLines, baselineOrigin(segment.baselineInfo()))
			currentExists = preExists
			currentBytes = preBytes
		}
		if !step.Touched {
			continue
		}

		postBytes, postExists, err := engine.Repo.CommitFileBytesIfExists(segment.PostCommit, step.PostPath)
		if err != nil {
			return nil, nil, false, warnings, err
		}
		if looksBinary(postBytes) {
			return nil, nil, false, warnings, fmt.Errorf("%w: %s touched by %s", ErrBinaryFile, step.PostPath, segmentLabel(segment))
		}

		patch, err := engine.Repo.DiffCommitsPathWithRenames(segment.PreCommit, segment.PostCommit, step.PrePath, step.PostPath)
		if err != nil {
			return nil, nil, false, warnings, err
		}
		if patchLooksBinary(patch) {
			return nil, nil, false, warnings, fmt.Errorf("%w: %s touched by %s", ErrBinaryFile, step.PostPath, segmentLabel(segment))
		}
		hunks, err := parseUnifiedHunks(patch)
		if err != nil {
			return nil, nil, false, warnings, err
		}
		origins, err = applyHunks(origins, hunks, segment.origin(step.PrePath, step.PostPath))
		if err != nil {
			return nil, nil, false, warnings, fmt.Errorf("apply blame diff for %s at %s: %w", step.PostPath, segmentLabel(segment), err)
		}

		postLines := splitLines(postBytes)
		if len(origins) != len(postLines) {
			return nil, nil, false, warnings, fmt.Errorf("blame diff for %s at %s produced %d lines, post snapshot has %d", step.PostPath, segmentLabel(segment), len(origins), len(postLines))
		}
		currentExists = postExists
		currentBytes = postBytes
	}

	return origins, currentBytes, currentExists, warnings, nil
}

type replayStep struct {
	Segment  changeSegment
	PrePath  string
	PostPath string
	Touched  bool
}

func (engine Engine) replaySteps(finalPath string, segments []changeSegment) ([]replayStep, error) {
	steps := make([]replayStep, len(segments))
	postPath := finalPath
	for index := len(segments) - 1; index >= 0; index-- {
		segment := segments[index]
		prePath, touched, err := engine.prePathForSegment(segment, postPath)
		if err != nil {
			return nil, err
		}
		steps[index] = replayStep{
			Segment:  segment,
			PrePath:  prePath,
			PostPath: postPath,
			Touched:  touched,
		}
		postPath = prePath
	}
	return steps, nil
}

func (engine Engine) prePathForSegment(segment changeSegment, postPath string) (string, bool, error) {
	changes, err := engine.Repo.DiffNameStatusCommits(segment.PreCommit, segment.PostCommit)
	if err != nil {
		return "", false, err
	}

	for _, change := range changes {
		switch change.Status {
		case "R":
			if change.Path == postPath {
				return change.OldPath, true, nil
			}
			if change.OldPath == postPath {
				return postPath, true, nil
			}
		case "C":
			if change.Path == postPath {
				return postPath, true, nil
			}
		default:
			if change.Path == postPath {
				return postPath, true, nil
			}
		}
	}
	return postPath, false, nil
}

func segmentLabel(segment changeSegment) string {
	if segment.Concurrent {
		return "concurrent agent turns"
	}
	label := fmt.Sprintf("%s:turn:%s", segment.Turn.SessionID, segment.Turn.TurnID)
	if segment.Action != nil && segment.Action.ToolName != "" {
		return label + " " + segment.Action.ToolName
	}
	return label
}

func baselineOrigin(info checkpoint.CheckpointRefInfo) Origin {
	return Origin{
		Kind:          "baseline",
		CheckpointRef: info.Ref,
		Commit:        info.Commit,
		Time:          info.Time,
	}
}

func turnOrigin(turn completeTurn) Origin {
	return Origin{
		Kind:          "turn",
		SessionID:     turn.SessionID,
		TurnID:        turn.TurnID,
		CheckpointRef: turn.Post.Ref,
		Commit:        turn.Post.Commit,
		Time:          turn.Post.Time,
		Adapter:       turn.Events.Adapter,
		Prompt:        turn.Events.Prompt,
		ToolNames:     turn.Events.ToolNames,
	}
}

func originsForLines(lines []string, origin Origin) []Origin {
	origins := make([]Origin, len(lines))
	for index := range origins {
		origins[index] = origin
	}
	return origins
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func looksBinary(data []byte) bool {
	return bytes.Contains(data, []byte{0})
}

func patchLooksBinary(patch []byte) bool {
	return bytes.Contains(patch, []byte("Binary files ")) || bytes.Contains(patch, []byte("GIT binary patch"))
}

func ParsePathLine(value string) (primitives.RepoPath, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, fmt.Errorf("path is required")
	}

	pathText := value
	line := 0
	if colon := strings.LastIndex(value, ":"); colon > 0 && colon < len(value)-1 && allDigits(value[colon+1:]) {
		parsedLine, err := strconv.Atoi(value[colon+1:])
		if err != nil || parsedLine <= 0 {
			return "", 0, fmt.Errorf("%w: %s", ErrInvalidLine, value[colon+1:])
		}
		pathText = value[:colon]
		line = parsedLine
	}

	path, err := primitives.ParseRepoPath(pathText)
	if err != nil {
		return "", 0, err
	}
	return path, line, nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
