package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
)

type rebuildData struct {
	Sessions        []sessionRecord
	Events          []eventRecord
	Turns           []turnRecord
	Checkpoints     []checkpointRecord
	FileTouches     []fileTouchRecord
	SearchDocuments []searchDocumentRecord
}

type sessionRecord struct {
	SessionID      primitives.SessionID
	FirstEventAt   time.Time
	LastEventAt    time.Time
	PrimaryAdapter string
	EventCount     int
	TurnCount      int
}

type eventRecord struct {
	Event eventlog.Event
}

type turnRecord struct {
	SessionID  primitives.SessionID
	WorktreeID primitives.WorktreeID
	StreamID   primitives.EventStreamID
	TurnID     primitives.TurnID
	Pre        *checkpoint.CheckpointRefInfo
	Post       *checkpoint.CheckpointRefInfo
	Diff       checkpoint.DiffSummary
	DiffLoaded bool
	Events     TurnEventSummary
	Warnings   []string
}

type checkpointRecord struct {
	Info checkpoint.CheckpointRefInfo
}

type fileTouchRecord struct {
	SessionID  primitives.SessionID
	WorktreeID primitives.WorktreeID
	StreamID   primitives.EventStreamID
	TurnID     primitives.TurnID
	File       checkpoint.DiffFileStat
}

type searchDocumentRecord struct {
	SessionID  primitives.SessionID
	WorktreeID primitives.WorktreeID
	StreamID   primitives.EventStreamID
	TurnID     primitives.TurnID
	Events     TurnEventSummary
	Paths      []string
	EventText  string
	// NoteText is reviewer commentary about this turn. Notes live outside the
	// turn's event stream, so they are joined in explicitly rather than arriving
	// with the turn's own events.
	NoteText string
}

func Rebuild(repo *checkpoint.Repo) (RebuildStats, error) {
	if repo == nil {
		return RebuildStats{}, fmt.Errorf("rebuild index requires checkpoint repo")
	}

	paths := PathsForMetadata(repo.MetadataDir)
	var stats RebuildStats
	for attempt := 1; attempt <= 3; attempt++ {
		fingerprintBefore, err := sourceFingerprint(repo.MetadataDir)
		if err != nil {
			return RebuildStats{DBPath: paths.DBPath}, err
		}
		data, err := collectRebuildData(repo)
		if err != nil {
			return RebuildStats{DBPath: paths.DBPath}, err
		}
		stats = RebuildStats{
			DBPath: paths.DBPath, Sessions: len(data.Sessions), Turns: len(data.Turns),
			Events: len(data.Events), Checkpoints: len(data.Checkpoints),
			FileTouches: len(data.FileTouches), SearchDocuments: len(data.SearchDocuments),
			SourceFingerprint: fingerprintBefore,
		}
		fingerprintAfter, err := sourceFingerprint(repo.MetadataDir)
		if err != nil {
			return stats, err
		}
		if fingerprintBefore != fingerprintAfter {
			continue
		}
		if err := writeRebuiltDatabase(paths, data, stats); err != nil {
			return stats, err
		}
		return stats, nil
	}
	return stats, fmt.Errorf("durable records kept changing during 3 index rebuild attempts; retry when capture activity is quieter")
}

func collectRebuildData(repo *checkpoint.Repo) (rebuildData, error) {
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return rebuildData{}, err
	}

	log := eventlog.Open(repo.MetadataDir)
	logSessions, err := log.ListSessions()
	if err != nil {
		return rebuildData{}, err
	}

	sessionSet := make(map[string]primitives.SessionID)
	turnsBySession := make(map[string]map[StreamTurnKey]*turnRecord)
	var checkpointRows []checkpointRecord
	for _, info := range infos {
		// Manual checkpoints are workspace timeline records, not turn rows. The
		// graph reads them from durable refs/events until the disposable index
		// grows a dedicated workspace-event projection.
		if info.Manual {
			continue
		}
		sessionKey := info.SessionID.String()
		sessionSet[sessionKey] = info.SessionID
		if turnsBySession[sessionKey] == nil {
			turnsBySession[sessionKey] = make(map[StreamTurnKey]*turnRecord)
		}
		turnKey := StreamTurnKey{StreamID: info.StreamID, TurnID: info.TurnID.Uint64()}
		turn := turnsBySession[sessionKey][turnKey]
		if turn == nil {
			turn = &turnRecord{
				SessionID:  info.SessionID,
				WorktreeID: info.WorktreeID,
				StreamID:   info.StreamID,
				TurnID:     info.TurnID,
			}
			turnsBySession[sessionKey][turnKey] = turn
		}

		infoCopy := info
		switch info.Phase {
		case primitives.CheckpointPhasePre:
			turn.Pre = &infoCopy
		case primitives.CheckpointPhasePost:
			turn.Post = &infoCopy
		default:
			turn.Warnings = append(turn.Warnings, fmt.Sprintf("unphased checkpoint ref ignored: %s", info.Ref))
		}
		checkpointRows = append(checkpointRows, checkpointRecord{Info: info})
	}
	for _, sessionID := range logSessions {
		sessionSet[sessionID.String()] = sessionID
	}

	sessionIDs := make([]primitives.SessionID, 0, len(sessionSet))
	for _, sessionID := range sessionSet {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Slice(sessionIDs, func(i, j int) bool {
		return sessionIDs[i].String() < sessionIDs[j].String()
	})

	summariesBySession := make(map[string]map[StreamTurnKey]TurnEventSummary)
	eventTextBySessionTurn := make(map[string]map[StreamTurnKey][]string)
	worktreeBySessionTurn := make(map[string]map[StreamTurnKey]primitives.WorktreeID)
	eventStatsBySession := make(map[string]sessionRecord)
	var eventRows []eventRecord
	for _, sessionID := range sessionIDs {
		events, err := log.Read(sessionID)
		if err != nil {
			return rebuildData{}, err
		}
		stats := sessionRecord{SessionID: sessionID, EventCount: len(events)}
		for _, event := range events {
			if event.WorktreeID == "" {
				event.WorktreeID = repo.WorktreeID
			}
			if stats.FirstEventAt.IsZero() || event.Time.Time.Before(stats.FirstEventAt) {
				stats.FirstEventAt = event.Time.Time
			}
			if stats.LastEventAt.IsZero() || event.Time.Time.After(stats.LastEventAt) {
				stats.LastEventAt = event.Time.Time
			}
			if stats.PrimaryAdapter == "" && event.Adapter != "" {
				stats.PrimaryAdapter = event.Adapter.String()
			}
			eventRows = append(eventRows, eventRecord{Event: event})
			if event.TurnID != nil {
				sessionKey := sessionID.String()
				turnKey := StreamTurnKey{StreamID: event.StreamID, TurnID: event.TurnID.Uint64()}
				if eventTextBySessionTurn[sessionKey] == nil {
					eventTextBySessionTurn[sessionKey] = make(map[StreamTurnKey][]string)
				}
				eventTextBySessionTurn[sessionKey][turnKey] = append(eventTextBySessionTurn[sessionKey][turnKey], eventSearchText(event))
				if worktreeBySessionTurn[sessionKey] == nil {
					worktreeBySessionTurn[sessionKey] = make(map[StreamTurnKey]primitives.WorktreeID)
				}
				worktreeBySessionTurn[sessionKey][turnKey] = event.WorktreeID
			}
		}
		summariesBySession[sessionID.String()] = SummarizeTurnEventsByStream(events)
		eventStatsBySession[sessionID.String()] = stats
	}

	var sessionRows []sessionRecord
	var turnRows []turnRecord
	var fileTouchRows []fileTouchRecord
	pathsBySessionTurn := make(map[string]map[StreamTurnKey][]string)
	for _, sessionID := range sessionIDs {
		sessionKey := sessionID.String()
		turnMap := turnsBySession[sessionKey]
		turnKeys := make([]StreamTurnKey, 0, len(turnMap))
		for turnKey := range turnMap {
			turnKeys = append(turnKeys, turnKey)
		}
		sort.Slice(turnKeys, func(i, j int) bool {
			if turnKeys[i].TurnID != turnKeys[j].TurnID {
				return turnKeys[i].TurnID < turnKeys[j].TurnID
			}
			return turnKeys[i].StreamID.String() < turnKeys[j].StreamID.String()
		})

		stats := eventStatsBySession[sessionKey]
		stats.SessionID = sessionID
		stats.TurnCount = len(turnKeys)
		for _, turnKey := range turnKeys {
			turn := turnMap[turnKey]
			if worktreeBySessionTurn[sessionKey] == nil {
				worktreeBySessionTurn[sessionKey] = make(map[StreamTurnKey]primitives.WorktreeID)
			}
			worktreeBySessionTurn[sessionKey][turnKey] = turn.WorktreeID
			turn.Events = summariesBySession[sessionKey][turnKey]
			if stats.PrimaryAdapter == "" && turn.Events.Adapter != "" {
				stats.PrimaryAdapter = turn.Events.Adapter
			}
			if turn.Pre != nil && turn.Post != nil {
				diff, err := repo.DiffStatRefs(turn.Pre.Ref, turn.Post.Ref)
				if err != nil {
					turn.Warnings = append(turn.Warnings, fmt.Sprintf("diff stats unavailable: %v", err))
				} else {
					turn.Diff = diff
					turn.DiffLoaded = true
					for _, file := range diff.Files {
						fileTouchRows = append(fileTouchRows, fileTouchRecord{
							SessionID:  sessionID,
							WorktreeID: turn.WorktreeID,
							StreamID:   turn.StreamID,
							TurnID:     turn.TurnID,
							File:       file,
						})
						if pathsBySessionTurn[sessionKey] == nil {
							pathsBySessionTurn[sessionKey] = make(map[StreamTurnKey][]string)
						}
						pathsBySessionTurn[sessionKey][turnKey] = append(pathsBySessionTurn[sessionKey][turnKey], file.Path)
					}
				}
			}
			turnRows = append(turnRows, *turn)
		}
		sessionRows = append(sessionRows, stats)
	}

	sort.Slice(checkpointRows, func(i, j int) bool {
		return checkpointRows[i].Info.Ref.String() < checkpointRows[j].Info.Ref.String()
	})
	sort.Slice(fileTouchRows, func(i, j int) bool {
		left, right := fileTouchRows[i], fileTouchRows[j]
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		if left.StreamID != right.StreamID {
			return left.StreamID.String() < right.StreamID.String()
		}
		if left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		return left.File.Path < right.File.Path
	})
	noteTextBySessionTurn, err := collectNoteText(repo)
	if err != nil {
		return rebuildData{}, err
	}
	searchDocuments := buildSearchDocuments(sessionIDs, summariesBySession, pathsBySessionTurn, eventTextBySessionTurn, worktreeBySessionTurn, noteTextBySessionTurn)

	return rebuildData{
		Sessions:        sessionRows,
		Events:          eventRows,
		Turns:           turnRows,
		Checkpoints:     checkpointRows,
		FileTouches:     fileTouchRows,
		SearchDocuments: searchDocuments,
	}, nil
}

// collectNoteText indexes reviewer commentary by the turn it discusses.
//
// Notes are keyed by session and turn rather than by stream: a note names its
// target turn explicitly, and a reviewer searching for their own words should
// find the turn whether or not the note came from this machine's stream.
// Redacted notes contribute nothing, so a withheld body is never indexed.
func collectNoteText(repo *checkpoint.Repo) (map[string]map[uint64][]string, error) {
	recorded, err := notes.List(repo, notes.Query{})
	if err != nil {
		// Commentary is not evidence. An unreadable note log must not prevent the
		// disposable index from being rebuilt from durable records.
		return nil, nil
	}
	result := make(map[string]map[uint64][]string)
	for _, note := range recorded {
		if note.Redacted {
			continue
		}
		sessionKey := note.Target.SessionID.String()
		if result[sessionKey] == nil {
			result[sessionKey] = make(map[uint64][]string)
		}
		turn := note.Target.TurnID.Uint64()
		parts := []string{note.Text}
		if note.Anchor != nil {
			parts = append(parts, note.Anchor.Path.String())
		}
		if note.Author != "" {
			parts = append(parts, note.Author)
		}
		result[sessionKey][turn] = append(result[sessionKey][turn], strings.Join(parts, " "))
	}
	return result, nil
}

func buildSearchDocuments(
	sessionIDs []primitives.SessionID,
	summariesBySession map[string]map[StreamTurnKey]TurnEventSummary,
	pathsBySessionTurn map[string]map[StreamTurnKey][]string,
	eventTextBySessionTurn map[string]map[StreamTurnKey][]string,
	worktreeBySessionTurn map[string]map[StreamTurnKey]primitives.WorktreeID,
	noteTextBySessionTurn map[string]map[uint64][]string,
) []searchDocumentRecord {
	var documents []searchDocumentRecord
	for _, sessionID := range sessionIDs {
		sessionKey := sessionID.String()
		turnSet := make(map[StreamTurnKey]struct{})
		for turnKey := range summariesBySession[sessionKey] {
			turnSet[turnKey] = struct{}{}
		}
		for turnKey := range pathsBySessionTurn[sessionKey] {
			turnSet[turnKey] = struct{}{}
		}
		for turnKey := range eventTextBySessionTurn[sessionKey] {
			turnSet[turnKey] = struct{}{}
		}

		turnKeys := make([]StreamTurnKey, 0, len(turnSet))
		for turnKey := range turnSet {
			turnKeys = append(turnKeys, turnKey)
		}
		sort.Slice(turnKeys, func(i, j int) bool {
			if turnKeys[i].TurnID != turnKeys[j].TurnID {
				return turnKeys[i].TurnID < turnKeys[j].TurnID
			}
			return turnKeys[i].StreamID.String() < turnKeys[j].StreamID.String()
		})

		for _, turnKey := range turnKeys {
			turnID, err := primitives.NewTurnID(turnKey.TurnID)
			if err != nil {
				continue
			}
			paths := uniqueSortedStrings(pathsBySessionTurn[sessionKey][turnKey])
			documents = append(documents, searchDocumentRecord{
				SessionID:  sessionID,
				WorktreeID: worktreeBySessionTurn[sessionKey][turnKey],
				StreamID:   turnKey.StreamID,
				TurnID:     turnID,
				Events:     summariesBySession[sessionKey][turnKey],
				Paths:      paths,
				EventText:  strings.Join(nonEmptyStrings(eventTextBySessionTurn[sessionKey][turnKey]), "\n"),
				NoteText:   strings.Join(nonEmptyStrings(noteTextBySessionTurn[sessionKey][turnKey.TurnID]), "\n"),
			})
		}
	}
	return documents
}

func eventSearchText(event eventlog.Event) string {
	parts := []string{
		event.Type.String(),
		event.Adapter.String(),
		event.SourceID,
		event.RawRef,
	}
	appendPayloadSearchText(&parts, event.Payload)
	return strings.Join(nonEmptyStrings(parts), " ")
}

func appendPayloadSearchText(parts *[]string, payload json.RawMessage) {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		*parts = append(*parts, string(payload))
		return
	}
	appendJSONSearchValue(parts, decoded)
}

func appendJSONSearchValue(parts *[]string, value any) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		*parts = append(*parts, typed)
	case bool:
		*parts = append(*parts, strconv.FormatBool(typed))
	case float64:
		*parts = append(*parts, strconv.FormatFloat(typed, 'f', -1, 64))
	case []any:
		for _, item := range typed {
			appendJSONSearchValue(parts, item)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			*parts = append(*parts, key)
			appendJSONSearchValue(parts, typed[key])
		}
	}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var unique []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func nonEmptyStrings(values []string) []string {
	filtered := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func writeRebuiltDatabase(paths Paths, data rebuildData, stats RebuildStats) error {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("create index dir: %w", err)
	}
	if err := os.Chmod(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("secure index dir: %w", err)
	}

	tempFile, err := os.CreateTemp(paths.Dir, DBFileName+".rebuild-*")
	if err != nil {
		return fmt.Errorf("create temporary index database: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close temporary index database: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("prepare temporary index database: %w", err)
	}

	success := false
	defer func() {
		if !success {
			_ = os.Remove(tempPath)
			_ = os.Remove(tempPath + "-journal")
		}
	}()

	db, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return fmt.Errorf("open temporary index database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() {
		_ = db.Close()
	}()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = DELETE; PRAGMA synchronous = FULL; PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("configure temporary index database: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin index rebuild transaction: %w", err)
	}
	if err := insertRebuildData(ctx, tx, data, stats); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit index rebuild transaction: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close rebuilt index database: %w", err)
	}
	if err := os.Rename(tempPath, paths.DBPath); err != nil {
		return fmt.Errorf("install rebuilt index database: %w", err)
	}
	success = true
	return nil
}

func insertRebuildData(ctx context.Context, tx *sql.Tx, data rebuildData, stats RebuildStats) error {
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create index schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("set index schema version: %w", err)
	}

	if err := insertMeta(ctx, tx, stats); err != nil {
		return err
	}
	if err := insertSessions(ctx, tx, data.Sessions); err != nil {
		return err
	}
	if err := insertEvents(ctx, tx, data.Events); err != nil {
		return err
	}
	if err := insertTurns(ctx, tx, data.Turns); err != nil {
		return err
	}
	if err := insertCheckpoints(ctx, tx, data.Checkpoints); err != nil {
		return err
	}
	if err := insertFileTouches(ctx, tx, data.FileTouches); err != nil {
		return err
	}
	if err := insertSearchDocuments(ctx, tx, data.SearchDocuments); err != nil {
		return err
	}
	return nil
}

func insertMeta(ctx context.Context, tx *sql.Tx, stats RebuildStats) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare index metadata insert: %w", err)
	}
	defer stmt.Close()

	values := map[string]string{
		"schema_version":       strconv.Itoa(SchemaVersion),
		"rebuilt_at":           time.Now().UTC().Format(time.RFC3339Nano),
		"session_count":        strconv.Itoa(stats.Sessions),
		"turn_count":           strconv.Itoa(stats.Turns),
		"event_row_count":      strconv.Itoa(stats.Events),
		"checkpoint_ref_count": strconv.Itoa(stats.Checkpoints),
		"file_touch_count":     strconv.Itoa(stats.FileTouches),
		"search_doc_count":     strconv.Itoa(stats.SearchDocuments),
		"source_fingerprint":   stats.SourceFingerprint,
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, key, values[key]); err != nil {
			return fmt.Errorf("insert index metadata %s: %w", key, err)
		}
	}
	return nil
}

func insertSessions(ctx context.Context, tx *sql.Tx, sessions []sessionRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sessions (session_id, first_event_at, last_event_at, primary_adapter, event_count, turn_count)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare sessions insert: %w", err)
	}
	defer stmt.Close()

	for _, session := range sessions {
		if _, err := stmt.ExecContext(ctx,
			session.SessionID.String(),
			nullableTime(session.FirstEventAt),
			nullableTime(session.LastEventAt),
			nullableText(session.PrimaryAdapter),
			session.EventCount,
			session.TurnCount,
		); err != nil {
			return fmt.Errorf("insert session %s: %w", session.SessionID, err)
		}
	}
	return nil
}

func insertEvents(ctx context.Context, tx *sql.Tx, events []eventRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (repo_id, worktree_id, stream_id, session_id, seq, turn_id, event_type, adapter, event_time, source_id, raw_ref, prev_hash, event_hash, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare events insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range events {
		event := row.Event
		seq, err := int64FromUint64(event.Seq.Uint64())
		if err != nil {
			return fmt.Errorf("event %s seq: %w", event.SessionID, err)
		}
		var turnArg any
		if event.TurnID != nil {
			turnArg, err = int64FromUint64(event.TurnID.Uint64())
			if err != nil {
				return fmt.Errorf("event %s turn: %w", event.SessionID, err)
			}
		}
		if _, err := stmt.ExecContext(ctx,
			nullableText(event.RepoID.String()),
			nullableText(event.WorktreeID.String()),
			event.StreamID.String(),
			event.SessionID.String(),
			seq,
			turnArg,
			event.Type.String(),
			nullableText(event.Adapter.String()),
			event.Time.String(),
			nullableText(event.SourceID),
			nullableText(event.RawRef),
			event.PrevHash.String(),
			event.Hash.String(),
			string(event.Payload),
		); err != nil {
			return fmt.Errorf("insert event %s seq %s: %w", event.SessionID, event.Seq, err)
		}
	}
	return nil
}

func insertTurns(ctx context.Context, tx *sql.Tx, turns []turnRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO turns (
			stream_id, worktree_id, session_id, turn_id, status, event_count, adapter, model, prompt_preview, assistant_preview,
			tool_names_json, event_type_counts_json, events_first_at, events_last_at,
			diff_loaded, diff_file_count, diff_additions, diff_deletions, diff_binary_files, warnings_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare turns insert: %w", err)
	}
	defer stmt.Close()

	for _, turn := range turns {
		turnNumber, err := int64FromUint64(turn.TurnID.Uint64())
		if err != nil {
			return fmt.Errorf("turn %s:%s: %w", turn.SessionID, turn.TurnID, err)
		}
		toolNamesJSON, err := marshalJSON(turn.Events.ToolNames)
		if err != nil {
			return fmt.Errorf("encode tool names for %s:%s: %w", turn.SessionID, turn.TurnID, err)
		}
		typeCountsJSON, err := marshalTypeCounts(turn.Events.TypeCounts)
		if err != nil {
			return fmt.Errorf("encode event type counts for %s:%s: %w", turn.SessionID, turn.TurnID, err)
		}
		warningsJSON, err := marshalJSON(turn.Warnings)
		if err != nil {
			return fmt.Errorf("encode warnings for %s:%s: %w", turn.SessionID, turn.TurnID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			turn.StreamID.String(),
			nullableText(turn.WorktreeID.String()),
			turn.SessionID.String(),
			turnNumber,
			turnStatus(turn),
			turn.Events.Count,
			nullableText(turn.Events.Adapter),
			nullableText(turn.Events.Model),
			nullableText(turn.Events.Prompt),
			nullableText(turn.Events.Assistant),
			toolNamesJSON,
			typeCountsJSON,
			nullableTime(turn.Events.First),
			nullableTime(turn.Events.Last),
			boolInt(turn.DiffLoaded),
			len(turn.Diff.Files),
			turn.Diff.Additions,
			turn.Diff.Deletions,
			turn.Diff.BinaryFiles,
			warningsJSON,
		); err != nil {
			return fmt.Errorf("insert turn %s:%s: %w", turn.SessionID, turn.TurnID, err)
		}
	}
	return nil
}

func insertCheckpoints(ctx context.Context, tx *sql.Tx, checkpoints []checkpointRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO checkpoints (ref, checkpoint_id, canonical_ref, stream_id, worktree_id, session_id, turn_id, phase, commit_sha, committed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare checkpoints insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range checkpoints {
		info := row.Info
		turnNumber, err := int64FromUint64(info.TurnID.Uint64())
		if err != nil {
			return fmt.Errorf("checkpoint %s: %w", info.Ref, err)
		}
		if _, err := stmt.ExecContext(ctx,
			info.Ref.String(),
			nullableText(info.ID.String()),
			nullableText(info.CanonicalRef.String()),
			info.StreamID.String(),
			nullableText(info.WorktreeID.String()),
			info.SessionID.String(),
			turnNumber,
			info.Phase.String(),
			info.Commit.String(),
			info.Time.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert checkpoint %s: %w", info.Ref, err)
		}
	}
	return nil
}

func insertFileTouches(ctx context.Context, tx *sql.Tx, files []fileTouchRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO file_touches (stream_id, worktree_id, session_id, turn_id, path, additions, deletions, binary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare file touches insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range files {
		turnNumber, err := int64FromUint64(row.TurnID.Uint64())
		if err != nil {
			return fmt.Errorf("file touch %s:%s: %w", row.SessionID, row.TurnID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			row.StreamID.String(),
			nullableText(row.WorktreeID.String()),
			row.SessionID.String(),
			turnNumber,
			row.File.Path,
			row.File.Additions,
			row.File.Deletions,
			boolInt(row.File.Binary),
		); err != nil {
			return fmt.Errorf("insert file touch %s:%s:%s: %w", row.SessionID, row.TurnID, row.File.Path, err)
		}
	}
	return nil
}

func insertSearchDocuments(ctx context.Context, tx *sql.Tx, documents []searchDocumentRecord) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO turn_search (stream_id, worktree_id, session_id, turn_id, first_at, last_at, adapter, model, prompt, assistant, tools, paths, event_text, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare search document insert: %w", err)
	}
	defer stmt.Close()

	for _, document := range documents {
		turnNumber, err := int64FromUint64(document.TurnID.Uint64())
		if err != nil {
			return fmt.Errorf("search document %s:%s: %w", document.SessionID, document.TurnID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			document.StreamID.String(),
			nullableText(document.WorktreeID.String()),
			document.SessionID.String(),
			strconv.FormatInt(turnNumber, 10),
			nullableTime(document.Events.First),
			nullableTime(document.Events.Last),
			nullableText(document.Events.Adapter),
			nullableText(document.Events.Model),
			nullableText(document.Events.Prompt),
			nullableText(document.Events.Assistant),
			nullableText(strings.Join(document.Events.ToolNames, "\n")),
			nullableText(strings.Join(document.Paths, "\n")),
			nullableText(document.EventText),
			nullableText(document.NoteText),
		); err != nil {
			return fmt.Errorf("insert search document %s:%s: %w", document.SessionID, document.TurnID, err)
		}
	}
	return nil
}

func turnStatus(turn turnRecord) string {
	switch {
	case turn.Pre != nil && turn.Post != nil:
		return "complete"
	case turn.Pre != nil:
		return "active"
	case turn.Post != nil:
		return "orphan"
	default:
		return "empty"
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func marshalJSON(value []string) (string, error) {
	if value == nil {
		value = []string{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func marshalTypeCounts(counts map[primitives.EventType]int) (string, error) {
	raw := make(map[string]int, len(counts))
	for eventType, count := range counts {
		raw[eventType.String()] = count
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
