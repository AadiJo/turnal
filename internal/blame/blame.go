package blame

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agent-vcs-again/internal/checkpoint"
	queryindex "agent-vcs-again/internal/index"
	"agent-vcs-again/internal/primitives"
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

	turns, err := engine.completeTurns(query.SessionID)
	if err != nil {
		return Result{}, err
	}
	if len(turns) == 0 {
		return Result{}, ErrNoHistory
	}

	path := query.Path.String()
	latest := turns[len(turns)-1].Post
	historyKey := blameHistoryKey(turns)

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
			return engine.resultFromCache(query, cached, turns)
		}
	}

	origins, currentExists, warnings, err := engine.replayPath(path, turns)
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
		Entries:       filterEntries(allEntries, query.Line),
		Warnings:      warnings,
		CompleteTurns: len(turns),
	}, nil
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
		Entries:       entries,
		Warnings:      cached.Warnings,
		CompleteTurns: cached.CompleteTurns,
	}, nil
}

func (engine Engine) hydrateCachedOrigin(origin Origin, turns []completeTurn) Origin {
	if origin.Kind != "turn" || origin.SessionID == "" || origin.TurnID == 0 {
		return origin
	}
	for _, turn := range turns {
		if turn.SessionID == origin.SessionID && turn.TurnID == origin.TurnID {
			return turnOrigin(turn)
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
		Kind:          origin.Kind,
		SessionID:     origin.SessionID,
		TurnID:        origin.TurnID,
		CheckpointRef: origin.CheckpointRef,
		Commit:        origin.Commit,
		Time:          origin.Time,
		Adapter:       origin.Adapter,
		Prompt:        origin.Prompt,
		ToolNames:     append([]string(nil), origin.ToolNames...),
	}
}

func originFromCache(origin queryindex.BlameCacheOrigin) Origin {
	return Origin{
		Kind:          origin.Kind,
		SessionID:     origin.SessionID,
		TurnID:        origin.TurnID,
		CheckpointRef: origin.CheckpointRef,
		Commit:        origin.Commit,
		Time:          origin.Time,
		Adapter:       origin.Adapter,
		Prompt:        origin.Prompt,
		ToolNames:     append([]string(nil), origin.ToolNames...),
	}
}

func blameHistoryKey(turns []completeTurn) string {
	hash := sha256.New()
	for _, turn := range turns {
		writeHistoryField(hash, turn.SessionID.String())
		writeHistoryField(hash, turn.TurnID.String())
		writeHistoryField(hash, turn.Pre.Ref.String())
		writeHistoryField(hash, turn.Pre.Commit.String())
		writeHistoryField(hash, turn.Post.Ref.String())
		writeHistoryField(hash, turn.Post.Commit.String())
		writeHistoryField(hash, turn.Post.Time.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeHistoryField(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

func (engine Engine) replayPath(path string, turns []completeTurn) ([]Origin, bool, []string, error) {
	steps, err := engine.replaySteps(path, turns)
	if err != nil {
		return nil, false, nil, err
	}

	baseline := baselineOrigin(turns[0].Pre)
	initialBytes, exists, err := engine.Repo.CommitFileBytesIfExists(turns[0].Pre.Commit, steps[0].PrePath)
	if err != nil {
		return nil, false, nil, err
	}
	if exists && looksBinary(initialBytes) {
		return nil, false, nil, fmt.Errorf("%w: %s", ErrBinaryFile, steps[0].PrePath)
	}

	origins := originsForLines(splitLines(initialBytes), baseline)
	currentExists := exists
	var warnings []string

	for _, step := range steps {
		turn := step.Turn
		if !step.Touched {
			continue
		}

		preBytes, preExists, err := engine.Repo.CommitFileBytesIfExists(turn.Pre.Commit, step.PrePath)
		if err != nil {
			return nil, false, warnings, err
		}
		postBytes, postExists, err := engine.Repo.CommitFileBytesIfExists(turn.Post.Commit, step.PostPath)
		if err != nil {
			return nil, false, warnings, err
		}
		if looksBinary(preBytes) || looksBinary(postBytes) {
			return nil, false, warnings, fmt.Errorf("%w: %s touched by %s:turn:%s", ErrBinaryFile, step.PostPath, turn.SessionID, turn.TurnID)
		}

		preLines := splitLines(preBytes)
		if currentExists != preExists || len(origins) != len(preLines) {
			warnings = append(warnings, fmt.Sprintf("history for %s was resynchronized at %s:turn:%s pre checkpoint", step.PrePath, turn.SessionID, turn.TurnID))
			origins = originsForLines(preLines, baselineOrigin(turn.Pre))
			currentExists = preExists
		}

		patch, err := engine.Repo.DiffRefsPathWithRenames(turn.Pre.Ref, turn.Post.Ref, step.PrePath, step.PostPath)
		if err != nil {
			return nil, false, warnings, err
		}
		if patchLooksBinary(patch) {
			return nil, false, warnings, fmt.Errorf("%w: %s touched by %s:turn:%s", ErrBinaryFile, step.PostPath, turn.SessionID, turn.TurnID)
		}
		hunks, err := parseUnifiedHunks(patch)
		if err != nil {
			return nil, false, warnings, err
		}
		origins, err = applyHunks(origins, hunks, turnOrigin(turn))
		if err != nil {
			return nil, false, warnings, fmt.Errorf("apply blame diff for %s at %s:turn:%s: %w", step.PostPath, turn.SessionID, turn.TurnID, err)
		}

		postLines := splitLines(postBytes)
		if len(origins) != len(postLines) {
			return nil, false, warnings, fmt.Errorf("blame diff for %s at %s:turn:%s produced %d lines, post checkpoint has %d", step.PostPath, turn.SessionID, turn.TurnID, len(origins), len(postLines))
		}
		currentExists = postExists
	}

	return origins, currentExists, warnings, nil
}

type replayStep struct {
	Turn     completeTurn
	PrePath  string
	PostPath string
	Touched  bool
}

func (engine Engine) replaySteps(finalPath string, turns []completeTurn) ([]replayStep, error) {
	steps := make([]replayStep, len(turns))
	postPath := finalPath
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		prePath, touched, err := engine.prePathForTurn(turn, postPath)
		if err != nil {
			return nil, err
		}
		steps[index] = replayStep{
			Turn:     turn,
			PrePath:  prePath,
			PostPath: postPath,
			Touched:  touched,
		}
		postPath = prePath
	}
	return steps, nil
}

func (engine Engine) prePathForTurn(turn completeTurn, postPath string) (string, bool, error) {
	changes, err := engine.Repo.DiffNameStatusRefs(turn.Pre.Ref, turn.Post.Ref)
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
