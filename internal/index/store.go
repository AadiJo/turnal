package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	paths Paths
}

type streamTurnDBKey struct {
	StreamID primitives.EventStreamID
	TurnID   uint64
}

func Exists(metadataDir string) (bool, error) {
	paths := PathsForMetadata(metadataDir)
	if _, err := os.Stat(paths.DBPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat index database: %w", err)
	}
	return true, nil
}

func Open(metadataDir string) (*Store, error) {
	paths := PathsForMetadata(metadataDir)
	db, err := sql.Open("sqlite", paths.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open index database: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db, paths: paths}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Healthy() (bool, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return false, fmt.Errorf("read index schema version: %w", err)
	}
	if version != SchemaVersion {
		return false, nil
	}

	var rebuiltAt string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'rebuilt_at'`).Scan(&rebuiltAt); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("read index metadata: %w", err)
	}
	return rebuiltAt != "", nil
}

func (s *Store) Search(query SearchQuery) ([]SearchResult, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}
	match, err := buildFTSMatchQuery(query.Query)
	if err != nil {
		return nil, err
	}

	args := []any{match}
	sqlText := `
		SELECT stream_id, worktree_id, session_id, turn_id, first_at, last_at, adapter, prompt, assistant, tools, paths,
		       snippet(turn_search, -1, '[', ']', ' ... ', 16), bm25(turn_search) AS rank
		FROM turn_search
		WHERE turn_search MATCH ?`
	if query.Session != "" {
		sqlText += ` AND session_id = ?`
		args = append(args, query.Session.String())
	}
	if query.WorktreeID != "" {
		sqlText += ` AND worktree_id = ?`
		args = append(args, query.WorktreeID.String())
	}
	sqlText += ` ORDER BY rank, session_id, CAST(turn_id AS INTEGER)`
	if query.Limit > 0 {
		sqlText += ` LIMIT ?`
		args = append(args, query.Limit)
	}

	rows, err := s.db.QueryContext(context.Background(), sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query search index: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var streamText string
		var worktreeText sql.NullString
		var sessionText string
		var turnText string
		var firstText sql.NullString
		var lastText sql.NullString
		var adapter sql.NullString
		var prompt sql.NullString
		var assistant sql.NullString
		var tools sql.NullString
		var paths sql.NullString
		var snippet sql.NullString
		var rank float64
		if err := rows.Scan(
			&streamText,
			&worktreeText,
			&sessionText,
			&turnText,
			&firstText,
			&lastText,
			&adapter,
			&prompt,
			&assistant,
			&tools,
			&paths,
			&snippet,
			&rank,
		); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}

		sessionID, err := primitives.ParseSessionID(sessionText)
		if err != nil {
			return nil, fmt.Errorf("index search session invariant failed: %w", err)
		}
		streamID, err := primitives.ParseEventStreamID(streamText)
		if err != nil {
			return nil, fmt.Errorf("index search stream invariant failed: %w", err)
		}
		var worktreeID primitives.WorktreeID
		if worktreeText.Valid && worktreeText.String != "" {
			worktreeID, err = primitives.ParseWorktreeID(worktreeText.String)
			if err != nil {
				return nil, fmt.Errorf("index search worktree invariant failed: %w", err)
			}
		}
		turnID, err := primitives.ParseTurnID(turnText)
		if err != nil {
			return nil, fmt.Errorf("index search turn invariant failed for session %s: %w", sessionID, err)
		}
		first, err := parseOptionalTime(firstText)
		if err != nil {
			return nil, fmt.Errorf("parse indexed search first event time for %s:%s: %w", sessionID, turnID, err)
		}
		last, err := parseOptionalTime(lastText)
		if err != nil {
			return nil, fmt.Errorf("parse indexed search last event time for %s:%s: %w", sessionID, turnID, err)
		}

		results = append(results, SearchResult{
			SessionID:  sessionID,
			WorktreeID: worktreeID,
			StreamID:   streamID,
			TurnID:     turnID,
			First:      first,
			Last:       last,
			Adapter:    nullableString(adapter),
			Prompt:     nullableString(prompt),
			Assistant:  nullableString(assistant),
			ToolNames:  splitSearchList(nullableString(tools)),
			Paths:      splitSearchList(nullableString(paths)),
			Snippet:    nullableString(snippet),
			Rank:       rank,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return results, nil
}

func (s *Store) LoadGraph(query GraphQuery) ([]GraphSession, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}

	ctx := context.Background()
	var rows *sql.Rows
	var err error
	if query.Session != "" && query.WorktreeID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM turns
			WHERE session_id = ? AND worktree_id = ?
			GROUP BY session_id
			ORDER BY session_id`, query.Session.String(), query.WorktreeID.String())
	} else if query.Session != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM turns
			WHERE session_id = ?
			GROUP BY session_id
			ORDER BY session_id`, query.Session.String())
	} else if query.WorktreeID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM turns
			WHERE worktree_id = ?
			GROUP BY session_id
			ORDER BY session_id`, query.WorktreeID.String())
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM turns
			GROUP BY session_id
			ORDER BY session_id`)
	}
	if err != nil {
		return nil, fmt.Errorf("query indexed sessions: %w", err)
	}
	defer rows.Close()

	type sessionTotal struct {
		sessionID  primitives.SessionID
		totalTurns int
	}
	var totals []sessionTotal
	for rows.Next() {
		var sessionText string
		var totalTurns int
		if err := rows.Scan(&sessionText, &totalTurns); err != nil {
			return nil, fmt.Errorf("scan indexed session: %w", err)
		}
		sessionID, err := primitives.ParseSessionID(sessionText)
		if err != nil {
			return nil, fmt.Errorf("index session invariant failed: %w", err)
		}
		totals = append(totals, sessionTotal{sessionID: sessionID, totalTurns: totalTurns})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close indexed sessions: %w", err)
	}

	var sessions []GraphSession
	for _, total := range totals {
		turns, err := s.loadGraphTurns(ctx, total.sessionID, query)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, GraphSession{
			ID:         total.sessionID,
			Turns:      turns,
			TotalTurns: total.totalTurns,
		})
	}
	return sessions, nil
}

func (s *Store) loadGraphTurns(ctx context.Context, sessionID primitives.SessionID, graphQuery GraphQuery) ([]GraphTurn, error) {
	checkpoints, err := s.loadCheckpoints(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	fileTouches, err := s.loadFileTouches(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	args := []any{sessionID.String()}
	query := `
		SELECT stream_id, worktree_id, turn_id, event_count, adapter, prompt_preview, assistant_preview,
		       tool_names_json, event_type_counts_json, events_first_at, events_last_at,
		       diff_loaded, diff_additions, diff_deletions, diff_binary_files, warnings_json
		FROM turns
		WHERE session_id = ?`
	if graphQuery.WorktreeID != "" {
		query += ` AND worktree_id = ?`
		args = append(args, graphQuery.WorktreeID.String())
	}
	query += ` ORDER BY turn_id DESC, stream_id`
	if graphQuery.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, graphQuery.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query indexed turns for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var turns []GraphTurn
	for rows.Next() {
		var streamText string
		var worktreeText sql.NullString
		var turnNumber int64
		var eventCount int
		var adapter sql.NullString
		var prompt sql.NullString
		var assistant sql.NullString
		var toolNamesJSON string
		var typeCountsJSON string
		var firstText sql.NullString
		var lastText sql.NullString
		var diffLoadedInt int
		var additions int
		var deletions int
		var binaryFiles int
		var warningsJSON string
		if err := rows.Scan(
			&streamText,
			&worktreeText,
			&turnNumber,
			&eventCount,
			&adapter,
			&prompt,
			&assistant,
			&toolNamesJSON,
			&typeCountsJSON,
			&firstText,
			&lastText,
			&diffLoadedInt,
			&additions,
			&deletions,
			&binaryFiles,
			&warningsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan indexed turn for session %s: %w", sessionID, err)
		}

		turnID, err := turnIDFromInt64(turnNumber)
		if err != nil {
			return nil, fmt.Errorf("index turn invariant failed for session %s: %w", sessionID, err)
		}
		streamID, err := primitives.ParseEventStreamID(streamText)
		if err != nil {
			return nil, fmt.Errorf("index stream invariant failed for session %s: %w", sessionID, err)
		}
		var worktreeID primitives.WorktreeID
		if worktreeText.Valid && worktreeText.String != "" {
			worktreeID, err = primitives.ParseWorktreeID(worktreeText.String)
			if err != nil {
				return nil, fmt.Errorf("index worktree invariant failed for session %s: %w", sessionID, err)
			}
		}
		turnKey := streamTurnDBKey{StreamID: streamID, TurnID: turnID.Uint64()}
		toolNames, err := decodeStringSlice(toolNamesJSON)
		if err != nil {
			return nil, fmt.Errorf("decode indexed tool names for %s:%s: %w", sessionID, turnID, err)
		}
		typeCounts, err := decodeTypeCounts(typeCountsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode indexed event type counts for %s:%s: %w", sessionID, turnID, err)
		}
		warnings, err := decodeStringSlice(warningsJSON)
		if err != nil {
			return nil, fmt.Errorf("decode indexed warnings for %s:%s: %w", sessionID, turnID, err)
		}
		first, err := parseOptionalTime(firstText)
		if err != nil {
			return nil, fmt.Errorf("parse indexed first event time for %s:%s: %w", sessionID, turnID, err)
		}
		last, err := parseOptionalTime(lastText)
		if err != nil {
			return nil, fmt.Errorf("parse indexed last event time for %s:%s: %w", sessionID, turnID, err)
		}

		diff := checkpoint.DiffSummary{
			Files:       fileTouches[turnKey],
			Additions:   additions,
			Deletions:   deletions,
			BinaryFiles: binaryFiles,
		}
		graphTurn := GraphTurn{
			WorktreeID: worktreeID,
			StreamID:   streamID,
			TurnID:     turnID,
			Diff:       diff,
			Events: TurnEventSummary{
				Count:      eventCount,
				Adapter:    nullableString(adapter),
				Prompt:     nullableString(prompt),
				Assistant:  nullableString(assistant),
				ToolNames:  toolNames,
				TypeCounts: typeCounts,
				First:      first,
				Last:       last,
			},
			DiffLoaded: diffLoadedInt != 0,
			Warnings:   warnings,
		}
		if refs := checkpoints[turnKey]; refs != nil {
			graphTurn.Pre = refs[primitives.CheckpointPhasePre]
			graphTurn.Post = refs[primitives.CheckpointPhasePost]
		}
		turns = append(turns, graphTurn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed turns for session %s: %w", sessionID, err)
	}
	return turns, nil
}

func (s *Store) loadCheckpoints(ctx context.Context, sessionID primitives.SessionID) (map[streamTurnDBKey]map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stream_id, worktree_id, checkpoint_id, canonical_ref, turn_id, phase, ref, commit_sha, committed_at
		FROM checkpoints
		WHERE session_id = ?`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("query indexed checkpoints for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	checkpoints := make(map[streamTurnDBKey]map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo)
	for rows.Next() {
		var streamText string
		var worktreeText sql.NullString
		var checkpointIDText sql.NullString
		var canonicalRefText sql.NullString
		var turnNumber int64
		var phaseText string
		var refText string
		var commitText string
		var timeText string
		if err := rows.Scan(&streamText, &worktreeText, &checkpointIDText, &canonicalRefText, &turnNumber, &phaseText, &refText, &commitText, &timeText); err != nil {
			return nil, fmt.Errorf("scan indexed checkpoint for session %s: %w", sessionID, err)
		}
		turnID, err := turnIDFromInt64(turnNumber)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint invariant failed for session %s: %w", sessionID, err)
		}
		streamID, err := primitives.ParseEventStreamID(streamText)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint stream invariant failed for %s:%s: %w", sessionID, turnID, err)
		}
		var worktreeID primitives.WorktreeID
		if worktreeText.Valid && worktreeText.String != "" {
			worktreeID, err = primitives.ParseWorktreeID(worktreeText.String)
			if err != nil {
				return nil, err
			}
		}
		var checkpointID primitives.CheckpointID
		if checkpointIDText.Valid && checkpointIDText.String != "" {
			checkpointID, err = primitives.ParseCheckpointID(checkpointIDText.String)
			if err != nil {
				return nil, err
			}
		}
		var canonicalRef primitives.CheckpointRef
		if canonicalRefText.Valid && canonicalRefText.String != "" {
			canonicalRef, err = primitives.ParseCheckpointRef(canonicalRefText.String)
			if err != nil {
				return nil, err
			}
		}
		ref, err := primitives.ParseCheckpointRef(refText)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint ref invariant failed for %s:%s: %w", sessionID, turnID, err)
		}
		commit, err := primitives.ParseCommitSHA(commitText)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint commit invariant failed for %s:%s: %w", sessionID, turnID, err)
		}
		committedAt, err := time.Parse(time.RFC3339Nano, timeText)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint time invariant failed for %s:%s: %w", sessionID, turnID, err)
		}

		var phase primitives.CheckpointPhase
		hasPhase := phaseText != ""
		if hasPhase {
			phase, err = primitives.ParseCheckpointPhase(phaseText)
			if err != nil {
				return nil, fmt.Errorf("index checkpoint phase invariant failed for %s:%s: %w", sessionID, turnID, err)
			}
		}
		if phase != primitives.CheckpointPhasePre && phase != primitives.CheckpointPhasePost {
			continue
		}

		key := streamTurnDBKey{StreamID: streamID, TurnID: turnID.Uint64()}
		if checkpoints[key] == nil {
			checkpoints[key] = make(map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo)
		}
		info := checkpoint.CheckpointRefInfo{
			ID:           checkpointID,
			Ref:          ref,
			CanonicalRef: canonicalRef,
			SessionID:    sessionID,
			WorktreeID:   worktreeID,
			StreamID:     streamID,
			TurnID:       turnID,
			Phase:        phase,
			HasPhase:     hasPhase,
			Commit:       commit,
			Time:         committedAt,
		}
		checkpoints[key][phase] = &info
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed checkpoints for session %s: %w", sessionID, err)
	}
	return checkpoints, nil
}

func (s *Store) loadFileTouches(ctx context.Context, sessionID primitives.SessionID) (map[streamTurnDBKey][]checkpoint.DiffFileStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stream_id, turn_id, path, additions, deletions, binary
		FROM file_touches
		WHERE session_id = ?
		ORDER BY turn_id DESC, path`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("query indexed file touches for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	fileTouches := make(map[streamTurnDBKey][]checkpoint.DiffFileStat)
	for rows.Next() {
		var streamText string
		var turnNumber int64
		var path string
		var additions int
		var deletions int
		var binaryInt int
		if err := rows.Scan(&streamText, &turnNumber, &path, &additions, &deletions, &binaryInt); err != nil {
			return nil, fmt.Errorf("scan indexed file touch for session %s: %w", sessionID, err)
		}
		turnID, err := turnIDFromInt64(turnNumber)
		if err != nil {
			return nil, fmt.Errorf("index file touch invariant failed for session %s: %w", sessionID, err)
		}
		streamID, err := primitives.ParseEventStreamID(streamText)
		if err != nil {
			return nil, fmt.Errorf("index file touch stream invariant failed for session %s: %w", sessionID, err)
		}
		key := streamTurnDBKey{StreamID: streamID, TurnID: turnID.Uint64()}
		fileTouches[key] = append(fileTouches[key], checkpoint.DiffFileStat{
			Path:      path,
			Additions: additions,
			Deletions: deletions,
			Binary:    binaryInt != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed file touches for session %s: %w", sessionID, err)
	}
	return fileTouches, nil
}

func (s *Store) LoadBlameCache(query BlameCacheQuery) (BlameCacheSnapshot, bool, error) {
	if query.Line < 0 {
		return BlameCacheSnapshot{}, false, fmt.Errorf("line must be zero or greater")
	}

	ctx := context.Background()
	scopeSession := query.ScopeSession.String()
	path := query.Path.String()

	var latestRefText string
	var latestCommitText string
	var latestTimeText string
	var completeTurns int
	var lineCount int
	var warningsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT latest_ref, latest_commit_sha, latest_committed_at, complete_turn_count, line_count, warnings_json
		FROM blame_cache
		WHERE scope_session_id = ?
		  AND path = ?
		  AND history_key = ?
		  AND latest_ref = ?
		  AND latest_commit_sha = ?
		  AND complete_turn_count = ?
		  AND line_no = 0`,
		scopeSession,
		path,
		query.HistoryKey,
		query.LatestRef.String(),
		query.LatestCommit.String(),
		query.CompleteTurns,
	).Scan(&latestRefText, &latestCommitText, &latestTimeText, &completeTurns, &lineCount, &warningsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return BlameCacheSnapshot{}, false, nil
		}
		return BlameCacheSnapshot{}, false, fmt.Errorf("query blame cache metadata for %s: %w", path, err)
	}

	latestRef, err := primitives.ParseCheckpointRef(latestRefText)
	if err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("blame cache latest ref invariant failed for %s: %w", path, err)
	}
	latestCommit, err := primitives.ParseCommitSHA(latestCommitText)
	if err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("blame cache latest commit invariant failed for %s: %w", path, err)
	}
	latestTime, err := time.Parse(time.RFC3339Nano, latestTimeText)
	if err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("blame cache latest time invariant failed for %s: %w", path, err)
	}
	warnings, err := decodeStringSlice(warningsJSON)
	if err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("decode blame cache warnings for %s: %w", path, err)
	}

	snapshot := BlameCacheSnapshot{
		ScopeSession:  query.ScopeSession,
		Path:          query.Path,
		HistoryKey:    query.HistoryKey,
		LatestRef:     latestRef,
		LatestCommit:  latestCommit,
		LatestTime:    latestTime,
		CompleteTurns: completeTurns,
		LineCount:     lineCount,
		Warnings:      warnings,
	}
	if query.Line > lineCount {
		return snapshot, true, nil
	}

	rowsQuery := `
		SELECT line_no, line_text, origin_kind, origin_session_id, origin_turn_id,
		       origin_checkpoint_ref, origin_commit_sha, origin_time, origin_adapter,
		       origin_prompt, origin_tool_names_json
		FROM blame_cache
		WHERE scope_session_id = ?
		  AND path = ?
		  AND history_key = ?
		  AND latest_ref = ?
		  AND latest_commit_sha = ?
		  AND complete_turn_count = ?`
	args := []any{
		scopeSession,
		path,
		query.HistoryKey,
		query.LatestRef.String(),
		query.LatestCommit.String(),
		query.CompleteTurns,
	}
	if query.Line > 0 {
		rowsQuery += ` AND line_no = ?`
		args = append(args, query.Line)
	} else {
		rowsQuery += ` AND line_no > 0`
	}
	rowsQuery += ` ORDER BY line_no`

	rows, err := s.db.QueryContext(ctx, rowsQuery, args...)
	if err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("query blame cache lines for %s: %w", path, err)
	}
	defer rows.Close()

	for rows.Next() {
		entry, err := scanBlameCacheEntry(rows, path)
		if err != nil {
			return BlameCacheSnapshot{}, false, err
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return BlameCacheSnapshot{}, false, fmt.Errorf("iterate blame cache lines for %s: %w", path, err)
	}
	if query.Line == 0 && len(snapshot.Entries) != lineCount {
		return BlameCacheSnapshot{}, false, fmt.Errorf("blame cache has %d lines for %s, want %d", len(snapshot.Entries), path, lineCount)
	}
	if query.Line > 0 && query.Line <= lineCount && len(snapshot.Entries) == 0 {
		return BlameCacheSnapshot{}, false, fmt.Errorf("blame cache missing line %d for %s", query.Line, path)
	}
	return snapshot, true, nil
}

func (s *Store) SaveBlameCache(snapshot BlameCacheSnapshot) error {
	if snapshot.Path == "" {
		return fmt.Errorf("blame cache path is required")
	}
	if snapshot.HistoryKey == "" {
		return fmt.Errorf("blame cache history key is required")
	}
	if snapshot.LatestRef == "" {
		return fmt.Errorf("blame cache latest ref is required")
	}
	if snapshot.LatestCommit == "" {
		return fmt.Errorf("blame cache latest commit is required")
	}
	if snapshot.CompleteTurns < 0 {
		return fmt.Errorf("blame cache complete turn count must be zero or greater")
	}
	if snapshot.LineCount < 0 {
		return fmt.Errorf("blame cache line count must be zero or greater")
	}
	if len(snapshot.Entries) != snapshot.LineCount {
		return fmt.Errorf("blame cache entries for %s = %d, want line count %d", snapshot.Path, len(snapshot.Entries), snapshot.LineCount)
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin blame cache transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	scopeSession := snapshot.ScopeSession.String()
	path := snapshot.Path.String()
	if _, err := tx.ExecContext(ctx, `DELETE FROM blame_cache WHERE scope_session_id = ? AND path = ?`, scopeSession, path); err != nil {
		return fmt.Errorf("clear blame cache for %s: %w", path, err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO blame_cache (
			scope_session_id, path, history_key, latest_ref, latest_commit_sha, latest_committed_at,
			complete_turn_count, line_count, line_no, line_text, warnings_json, origin_kind,
			origin_session_id, origin_turn_id, origin_checkpoint_ref, origin_commit_sha, origin_time,
			origin_adapter, origin_prompt, origin_tool_names_json, cached_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare blame cache insert: %w", err)
	}
	defer stmt.Close()

	warningsJSON, err := marshalJSON(snapshot.Warnings)
	if err != nil {
		return fmt.Errorf("encode blame cache warnings for %s: %w", path, err)
	}
	cachedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := insertBlameCacheEntry(ctx, stmt, snapshot, BlameCacheEntry{}, warningsJSON, cachedAt); err != nil {
		return err
	}
	for _, entry := range snapshot.Entries {
		if entry.Line <= 0 {
			return fmt.Errorf("blame cache line number %d for %s must be greater than zero", entry.Line, path)
		}
		if entry.Line > snapshot.LineCount {
			return fmt.Errorf("blame cache line number %d exceeds line count %d for %s", entry.Line, snapshot.LineCount, path)
		}
		if err := insertBlameCacheEntry(ctx, stmt, snapshot, entry, warningsJSON, cachedAt); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit blame cache for %s: %w", path, err)
	}
	return nil
}

type blameCacheScanner interface {
	Scan(dest ...any) error
}

func scanBlameCacheEntry(scanner blameCacheScanner, path string) (BlameCacheEntry, error) {
	var lineNumber int
	var lineText string
	var originKind string
	var originSessionText sql.NullString
	var originTurnNumber sql.NullInt64
	var originRefText sql.NullString
	var originCommitText sql.NullString
	var originTimeText sql.NullString
	var originAdapter sql.NullString
	var originPrompt sql.NullString
	var originToolNamesJSON string
	if err := scanner.Scan(
		&lineNumber,
		&lineText,
		&originKind,
		&originSessionText,
		&originTurnNumber,
		&originRefText,
		&originCommitText,
		&originTimeText,
		&originAdapter,
		&originPrompt,
		&originToolNamesJSON,
	); err != nil {
		return BlameCacheEntry{}, fmt.Errorf("scan blame cache line for %s: %w", path, err)
	}

	origin := BlameCacheOrigin{
		Kind:    originKind,
		Adapter: nullableString(originAdapter),
		Prompt:  nullableString(originPrompt),
	}
	if originSessionText.Valid && originSessionText.String != "" {
		sessionID, err := primitives.ParseSessionID(originSessionText.String)
		if err != nil {
			return BlameCacheEntry{}, fmt.Errorf("blame cache origin session invariant failed for %s:%d: %w", path, lineNumber, err)
		}
		origin.SessionID = sessionID
	}
	if originTurnNumber.Valid {
		turnID, err := turnIDFromInt64(originTurnNumber.Int64)
		if err != nil {
			return BlameCacheEntry{}, fmt.Errorf("blame cache origin turn invariant failed for %s:%d: %w", path, lineNumber, err)
		}
		origin.TurnID = turnID
	}
	if originRefText.Valid && originRefText.String != "" {
		ref, err := primitives.ParseCheckpointRef(originRefText.String)
		if err != nil {
			return BlameCacheEntry{}, fmt.Errorf("blame cache origin ref invariant failed for %s:%d: %w", path, lineNumber, err)
		}
		origin.CheckpointRef = ref
	}
	if originCommitText.Valid && originCommitText.String != "" {
		commit, err := primitives.ParseCommitSHA(originCommitText.String)
		if err != nil {
			return BlameCacheEntry{}, fmt.Errorf("blame cache origin commit invariant failed for %s:%d: %w", path, lineNumber, err)
		}
		origin.Commit = commit
	}
	originTime, err := parseOptionalTime(originTimeText)
	if err != nil {
		return BlameCacheEntry{}, fmt.Errorf("blame cache origin time invariant failed for %s:%d: %w", path, lineNumber, err)
	}
	origin.Time = originTime
	toolNames, err := decodeStringSlice(originToolNamesJSON)
	if err != nil {
		return BlameCacheEntry{}, fmt.Errorf("decode blame cache origin tools for %s:%d: %w", path, lineNumber, err)
	}
	origin.ToolNames = toolNames

	return BlameCacheEntry{
		Line:   lineNumber,
		Text:   lineText,
		Origin: origin,
	}, nil
}

func insertBlameCacheEntry(ctx context.Context, stmt *sql.Stmt, snapshot BlameCacheSnapshot, entry BlameCacheEntry, warningsJSON string, cachedAt string) error {
	origin := entry.Origin
	var turnArg any
	var err error
	if origin.TurnID != 0 {
		turnArg, err = int64FromUint64(origin.TurnID.Uint64())
		if err != nil {
			return fmt.Errorf("blame cache origin turn for %s:%d: %w", snapshot.Path, entry.Line, err)
		}
	}
	toolNamesJSON, err := marshalJSON(origin.ToolNames)
	if err != nil {
		return fmt.Errorf("encode blame cache origin tools for %s:%d: %w", snapshot.Path, entry.Line, err)
	}
	if _, err := stmt.ExecContext(ctx,
		snapshot.ScopeSession.String(),
		snapshot.Path.String(),
		snapshot.HistoryKey,
		snapshot.LatestRef.String(),
		snapshot.LatestCommit.String(),
		snapshot.LatestTime.UTC().Format(time.RFC3339Nano),
		snapshot.CompleteTurns,
		snapshot.LineCount,
		entry.Line,
		entry.Text,
		warningsJSON,
		origin.Kind,
		nullableText(origin.SessionID.String()),
		turnArg,
		nullableText(origin.CheckpointRef.String()),
		nullableText(origin.Commit.String()),
		nullableTime(origin.Time),
		nullableText(origin.Adapter),
		nullableText(origin.Prompt),
		toolNamesJSON,
		cachedAt,
	); err != nil {
		return fmt.Errorf("insert blame cache line %s:%d: %w", snapshot.Path, entry.Line, err)
	}
	return nil
}

func turnIDFromInt64(value int64) (primitives.TurnID, error) {
	if value <= 0 {
		return 0, fmt.Errorf("turn id %d must be greater than zero", value)
	}
	return primitives.NewTurnID(uint64(value))
}

func int64FromUint64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("%d overflows SQLite INTEGER", value)
	}
	return int64(value), nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func buildFTSMatchQuery(query string) (string, error) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", fmt.Errorf("search query must not be empty")
	}

	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(field, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("search query must include at least one searchable term")
	}
	return strings.Join(terms, " AND "), nil
}

func splitSearchList(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	result := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func parseOptionalTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value.String)
}

func decodeStringSlice(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var decoded []string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func decodeTypeCounts(value string) (map[primitives.EventType]int, error) {
	if value == "" {
		return nil, nil
	}
	var raw map[string]int
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	counts := make(map[primitives.EventType]int, len(raw))
	for eventTypeText, count := range raw {
		eventType, err := primitives.ParseEventType(eventTypeText)
		if err != nil {
			return nil, err
		}
		counts[eventType] = count
	}
	return counts, nil
}
