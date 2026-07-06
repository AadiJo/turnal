package blame

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"agent-vcs-again/internal/checkpoint"
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
	origins, currentExists, warnings, err := engine.replayPath(path, turns)
	if err != nil {
		return Result{}, err
	}

	latest := turns[len(turns)-1].Post
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

	entries := make([]Entry, 0, len(finalLines))
	for index, text := range finalLines {
		lineNumber := index + 1
		if query.Line != 0 && query.Line != lineNumber {
			continue
		}
		entries = append(entries, Entry{
			Line:   lineNumber,
			Text:   text,
			Origin: origins[index],
		})
	}

	return Result{
		Path:          query.Path,
		LatestRef:     latest.Ref,
		LatestCommit:  latest.Commit,
		LatestTime:    latest.Time,
		Entries:       entries,
		Warnings:      warnings,
		CompleteTurns: len(turns),
	}, nil
}

func (engine Engine) replayPath(path string, turns []completeTurn) ([]Origin, bool, []string, error) {
	baseline := baselineOrigin(turns[0].Pre)
	initialBytes, exists, err := engine.Repo.CommitFileBytesIfExists(turns[0].Pre.Commit, path)
	if err != nil {
		return nil, false, nil, err
	}
	if exists && looksBinary(initialBytes) {
		return nil, false, nil, fmt.Errorf("%w: %s", ErrBinaryFile, path)
	}

	origins := originsForLines(splitLines(initialBytes), baseline)
	currentExists := exists
	var warnings []string

	for _, turn := range turns {
		stat, touched, err := engine.touchStat(turn, path)
		if err != nil {
			return nil, false, warnings, err
		}
		if !touched {
			continue
		}
		if stat.Binary {
			return nil, false, warnings, fmt.Errorf("%w: %s touched by %s:turn:%s", ErrBinaryFile, path, turn.SessionID, turn.TurnID)
		}

		preBytes, preExists, err := engine.Repo.CommitFileBytesIfExists(turn.Pre.Commit, path)
		if err != nil {
			return nil, false, warnings, err
		}
		postBytes, postExists, err := engine.Repo.CommitFileBytesIfExists(turn.Post.Commit, path)
		if err != nil {
			return nil, false, warnings, err
		}
		if looksBinary(preBytes) || looksBinary(postBytes) {
			return nil, false, warnings, fmt.Errorf("%w: %s touched by %s:turn:%s", ErrBinaryFile, path, turn.SessionID, turn.TurnID)
		}

		preLines := splitLines(preBytes)
		if currentExists != preExists || len(origins) != len(preLines) {
			warnings = append(warnings, fmt.Sprintf("history for %s was resynchronized at %s:turn:%s pre checkpoint", path, turn.SessionID, turn.TurnID))
			origins = originsForLines(preLines, baselineOrigin(turn.Pre))
			currentExists = preExists
		}

		patch, err := engine.Repo.DiffRefsPath(turn.Pre.Ref, turn.Post.Ref, path)
		if err != nil {
			return nil, false, warnings, err
		}
		if patchLooksBinary(patch) {
			return nil, false, warnings, fmt.Errorf("%w: %s touched by %s:turn:%s", ErrBinaryFile, path, turn.SessionID, turn.TurnID)
		}
		hunks, err := parseUnifiedHunks(patch)
		if err != nil {
			return nil, false, warnings, err
		}
		origins, err = applyHunks(origins, hunks, turnOrigin(turn))
		if err != nil {
			return nil, false, warnings, fmt.Errorf("apply blame diff for %s at %s:turn:%s: %w", path, turn.SessionID, turn.TurnID, err)
		}

		postLines := splitLines(postBytes)
		if len(origins) != len(postLines) {
			return nil, false, warnings, fmt.Errorf("blame diff for %s at %s:turn:%s produced %d lines, post checkpoint has %d", path, turn.SessionID, turn.TurnID, len(origins), len(postLines))
		}
		currentExists = postExists
	}

	return origins, currentExists, warnings, nil
}

func (engine Engine) touchStat(turn completeTurn, path string) (checkpoint.DiffFileStat, bool, error) {
	summary, err := engine.Repo.DiffStatRefs(turn.Pre.Ref, turn.Post.Ref)
	if err != nil {
		return checkpoint.DiffFileStat{}, false, err
	}
	for _, file := range summary.Files {
		if file.Path == path {
			return file, true, nil
		}
	}
	return checkpoint.DiffFileStat{}, false, nil
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
