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

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	paths Paths
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
		SELECT session_id, turn_id, first_at, last_at, adapter, prompt, assistant, tools, paths,
		       snippet(turn_search, -1, '[', ']', ' ... ', 16), bm25(turn_search) AS rank
		FROM turn_search
		WHERE turn_search MATCH ?`
	if query.Session != "" {
		sqlText += ` AND session_id = ?`
		args = append(args, query.Session.String())
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
			SessionID: sessionID,
			TurnID:    turnID,
			First:     first,
			Last:      last,
			Adapter:   nullableString(adapter),
			Prompt:    nullableString(prompt),
			Assistant: nullableString(assistant),
			ToolNames: splitSearchList(nullableString(tools)),
			Paths:     splitSearchList(nullableString(paths)),
			Snippet:   nullableString(snippet),
			Rank:      rank,
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
	if query.Session != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM turns
			WHERE session_id = ?
			GROUP BY session_id
			ORDER BY session_id`, query.Session.String())
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
		turns, err := s.loadGraphTurns(ctx, total.sessionID, query.Limit)
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

func (s *Store) loadGraphTurns(ctx context.Context, sessionID primitives.SessionID, limit int) ([]GraphTurn, error) {
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
		SELECT turn_id, event_count, adapter, prompt_preview, assistant_preview,
		       tool_names_json, event_type_counts_json, events_first_at, events_last_at,
		       diff_loaded, diff_additions, diff_deletions, diff_binary_files, warnings_json
		FROM turns
		WHERE session_id = ?
		ORDER BY turn_id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query indexed turns for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var turns []GraphTurn
	for rows.Next() {
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
			Files:       fileTouches[turnID.Uint64()],
			Additions:   additions,
			Deletions:   deletions,
			BinaryFiles: binaryFiles,
		}
		graphTurn := GraphTurn{
			TurnID: turnID,
			Diff:   diff,
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
		if refs := checkpoints[turnID.Uint64()]; refs != nil {
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

func (s *Store) loadCheckpoints(ctx context.Context, sessionID primitives.SessionID) (map[uint64]map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT turn_id, phase, ref, commit_sha, committed_at
		FROM checkpoints
		WHERE session_id = ?`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("query indexed checkpoints for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	checkpoints := make(map[uint64]map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo)
	for rows.Next() {
		var turnNumber int64
		var phaseText string
		var refText string
		var commitText string
		var timeText string
		if err := rows.Scan(&turnNumber, &phaseText, &refText, &commitText, &timeText); err != nil {
			return nil, fmt.Errorf("scan indexed checkpoint for session %s: %w", sessionID, err)
		}
		turnID, err := turnIDFromInt64(turnNumber)
		if err != nil {
			return nil, fmt.Errorf("index checkpoint invariant failed for session %s: %w", sessionID, err)
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

		if checkpoints[turnID.Uint64()] == nil {
			checkpoints[turnID.Uint64()] = make(map[primitives.CheckpointPhase]*checkpoint.CheckpointRefInfo)
		}
		info := checkpoint.CheckpointRefInfo{
			Ref:       ref,
			SessionID: sessionID,
			TurnID:    turnID,
			Phase:     phase,
			HasPhase:  hasPhase,
			Commit:    commit,
			Time:      committedAt,
		}
		checkpoints[turnID.Uint64()][phase] = &info
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed checkpoints for session %s: %w", sessionID, err)
	}
	return checkpoints, nil
}

func (s *Store) loadFileTouches(ctx context.Context, sessionID primitives.SessionID) (map[uint64][]checkpoint.DiffFileStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT turn_id, path, additions, deletions, binary
		FROM file_touches
		WHERE session_id = ?
		ORDER BY turn_id DESC, path`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("query indexed file touches for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	fileTouches := make(map[uint64][]checkpoint.DiffFileStat)
	for rows.Next() {
		var turnNumber int64
		var path string
		var additions int
		var deletions int
		var binaryInt int
		if err := rows.Scan(&turnNumber, &path, &additions, &deletions, &binaryInt); err != nil {
			return nil, fmt.Errorf("scan indexed file touch for session %s: %w", sessionID, err)
		}
		turnID, err := turnIDFromInt64(turnNumber)
		if err != nil {
			return nil, fmt.Errorf("index file touch invariant failed for session %s: %w", sessionID, err)
		}
		fileTouches[turnID.Uint64()] = append(fileTouches[turnID.Uint64()], checkpoint.DiffFileStat{
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
