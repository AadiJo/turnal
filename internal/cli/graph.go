package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func logCmd() *cobra.Command {
	var session string
	var limit int
	var verbose bool
	var noPager bool
	var useIndex bool
	var durable bool
	var transcript bool
	var worktree string
	var allWorktrees bool
	var stream string

	cmd := &cobra.Command{
		Use:          "log",
		Aliases:      []string{"graph"},
		Short:        "Show checkpoint history",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or greater")
			}
			if useIndex && durable {
				return fmt.Errorf("--index and --durable cannot be combined")
			}
			if session != "" {
				if _, err := primitives.ParseSessionID(session); err != nil {
					return err
				}
			}
			if allWorktrees && worktree != "" {
				return fmt.Errorf("--all-worktrees and --worktree cannot be combined")
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			scope := graphScope{AllWorktrees: allWorktrees}
			if !allWorktrees {
				scope.WorktreeID = repo.WorktreeID
			}
			if worktree != "" && worktree != "current" {
				scope.WorktreeID, err = primitives.ParseWorktreeID(worktree)
				if err != nil {
					return err
				}
			}
			if stream != "" {
				scope.StreamID, err = primitives.ParseEventStreamID(stream)
				if err != nil {
					return err
				}
			}

			graphLimit := limit
			if transcript {
				graphLimit = 0
			}

			var sessions []graphSession
			if useIndex {
				var loaded bool
				sessions, loaded, err = tryLoadIndexedGraph(repo, session, graphLimit, scope)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: index unavailable: %v\n", err)
				}
				if !loaded {
					sessions, err = loadDurableGraph(repo, session, graphLimit, scope)
					if err != nil {
						return err
					}
				}
			} else {
				sessions, err = loadDurableGraph(repo, session, graphLimit, scope)
				if err != nil {
					return err
				}
			}

			options := graphRenderOptions{
				Verbose: verbose,
			}
			var buf bytes.Buffer
			if transcript {
				sessions, err = attachTranscriptEvents(repo, sessions, session, limit, scope)
				if err != nil {
					return err
				}
				if err := renderTranscriptLog(&buf, sessions, options); err != nil {
					return err
				}
			} else {
				attachGraphRollbackEvents(repo, sessions, scope)
				var saves []graphSave
				var workspaceRollbacks []graphRollback
				if session == "" {
					saves, workspaceRollbacks, err = loadWorkspaceGraphEvents(repo, scope)
					if err != nil {
						return err
					}
				}
				if err := renderCheckpointGraphWithWorkspace(&buf, sessions, saves, workspaceRollbacks, options); err != nil {
					return err
				}
			}
			output := buf.Bytes()
			if !colorOutputEnabled(cmd.OutOrStdout()) {
				output = stripANSIBytes(output)
			}
			if shouldPageOutput(cmd.OutOrStdout(), noPager, output) {
				return pageOutput(cmd.OutOrStdout(), output)
			}
			_, err = cmd.OutOrStdout().Write(output)
			return err
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to show; defaults to all sessions")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum turns per session; 0 shows all")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show full refs, checkpoint ids, event counts, and per-file stats")
	cmd.Flags().BoolVar(&noPager, "no-pager", false, "Print directly instead of opening a pager")
	cmd.Flags().BoolVar(&useIndex, "index", false, "Read from the disposable SQLite index when available")
	cmd.Flags().BoolVar(&durable, "durable", false, "Read directly from durable logs and checkpoints")
	cmd.Flags().BoolVar(&transcript, "transcript", false, "Show a readable prompt, assistant, and tool-call transcript")
	cmd.Flags().StringVar(&worktree, "worktree", "", "Worktree id to show; defaults to current")
	cmd.Flags().BoolVar(&allWorktrees, "all-worktrees", false, "Show history from every attached worktree")
	cmd.Flags().StringVar(&stream, "stream", "", "Event stream id to show")
	return cmd
}

type graphScope struct {
	WorktreeID   primitives.WorktreeID
	StreamID     primitives.EventStreamID
	AllWorktrees bool
}

func (scope graphScope) matches(worktreeID primitives.WorktreeID, streamID primitives.EventStreamID) bool {
	if scope.StreamID != "" && streamID != scope.StreamID {
		return false
	}
	if !scope.AllWorktrees && scope.WorktreeID != "" && worktreeID != "" && worktreeID != scope.WorktreeID {
		return false
	}
	return true
}

type graphRenderOptions struct {
	Verbose bool
}

type graphSession struct {
	ID         primitives.SessionID
	Turns      []graphTurn
	Rollbacks  []graphRollback
	TotalTurns int
	Warnings   []string
}

type graphTurn struct {
	WorktreeID primitives.WorktreeID
	StreamID   primitives.EventStreamID
	TurnID     primitives.TurnID
	Pre        *checkpoint.CheckpointRefInfo
	Post       *checkpoint.CheckpointRefInfo
	Diff       checkpoint.DiffSummary
	DiffLoaded bool
	Events     turnEventSummary
	EventLog   []eventlog.Event
	Warnings   []string
}

type graphRollback struct {
	Time            time.Time
	Seq             primitives.EventSeq
	Target          primitives.TargetRef
	CheckpointRef   string
	CommitSHA       primitives.CommitSHA
	SafetyRef       string
	SafetyCommitSHA string
	Mode            string
	SourceID        string
	Warnings        []string
	Manual          bool
	TargetText      string
	WorktreeID      primitives.WorktreeID
}

type graphSave struct {
	Time     time.Time
	Seq      primitives.EventSeq
	Info     checkpoint.CheckpointRefInfo
	Message  string
	SourceID string
	Warnings []string
}

type turnEventSummary = queryindex.TurnEventSummary

type graphTimelineRow struct {
	SessionID    primitives.SessionID
	SessionIndex int
	Turn         *graphTurn
	Rollback     *graphRollback
	Save         *graphSave
}

type laneSpan struct {
	First int
	Last  int
}

func loadDurableGraph(repo *checkpoint.Repo, session string, limit int, scope graphScope) ([]graphSession, error) {
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	var sessionID primitives.SessionID
	if session != "" {
		sessionID, err = primitives.ParseSessionID(session)
		if err != nil {
			return nil, err
		}
	}
	filtered := infos[:0]
	for _, info := range infos {
		if info.Manual {
			continue
		}
		if sessionID != "" && info.SessionID != sessionID {
			continue
		}
		if !scope.matches(info.WorktreeID, info.StreamID) {
			continue
		}
		filtered = append(filtered, info)
	}

	sessions := buildGraphSessions(filtered, limit)
	attachGraphDiffs(repo, sessions)
	attachGraphEventSummaries(repo, sessions)
	return sessions, nil
}

func tryLoadIndexedGraph(repo *checkpoint.Repo, session string, limit int, scope graphScope) ([]graphSession, bool, error) {
	if scope.StreamID != "" {
		return nil, false, nil
	}
	var sessionID primitives.SessionID
	var err error
	if session != "" {
		sessionID, err = primitives.ParseSessionID(session)
		if err != nil {
			return nil, false, err
		}
	}

	exists, err := queryindex.Exists(repo.MetadataDir)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}

	store, err := queryindex.Open(repo.MetadataDir)
	if err != nil {
		return nil, false, err
	}
	defer store.Close()

	healthy, err := store.Healthy()
	if err != nil {
		return nil, false, err
	}
	if !healthy {
		return nil, false, fmt.Errorf("schema version mismatch or missing rebuild metadata")
	}

	indexSessions, err := store.LoadGraph(queryindex.GraphQuery{
		Session:    sessionID,
		WorktreeID: scope.WorktreeID,
		Limit:      limit,
	})
	if err != nil {
		return nil, false, err
	}
	return graphSessionsFromIndex(indexSessions), true, nil
}

func graphSessionsFromIndex(indexSessions []queryindex.GraphSession) []graphSession {
	sessions := make([]graphSession, 0, len(indexSessions))
	for _, indexSession := range indexSessions {
		session := graphSession{
			ID:         indexSession.ID,
			TotalTurns: indexSession.TotalTurns,
			Warnings:   append([]string(nil), indexSession.Warnings...),
		}
		for _, indexTurn := range indexSession.Turns {
			session.Turns = append(session.Turns, graphTurn{
				WorktreeID: indexTurn.WorktreeID,
				StreamID:   indexTurn.StreamID,
				TurnID:     indexTurn.TurnID,
				Pre:        indexTurn.Pre,
				Post:       indexTurn.Post,
				Diff:       indexTurn.Diff,
				DiffLoaded: indexTurn.DiffLoaded,
				Events:     indexTurn.Events,
				Warnings:   append([]string(nil), indexTurn.Warnings...),
			})
		}
		sessions = append(sessions, session)
	}
	return sessions
}

func buildGraphSessions(infos []checkpoint.CheckpointRefInfo, limit int) []graphSession {
	type sessionBuilder struct {
		id    primitives.SessionID
		turns map[string]*graphTurn
	}

	builders := make(map[string]*sessionBuilder)
	for _, info := range infos {
		sessionKey := info.SessionID.String()
		builder := builders[sessionKey]
		if builder == nil {
			builder = &sessionBuilder{
				id:    info.SessionID,
				turns: make(map[string]*graphTurn),
			}
			builders[sessionKey] = builder
		}

		turnKey := graphTurnKey(info.StreamID, info.TurnID)
		turn := builder.turns[turnKey]
		if turn == nil {
			turn = &graphTurn{
				WorktreeID: info.WorktreeID,
				StreamID:   info.StreamID,
				TurnID:     info.TurnID,
			}
			builder.turns[turnKey] = turn
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
	}

	sessions := make([]graphSession, 0, len(builders))
	for _, builder := range builders {
		session := graphSession{ID: builder.id}
		for _, turn := range builder.turns {
			session.Turns = append(session.Turns, *turn)
		}
		sort.Slice(session.Turns, func(i, j int) bool {
			if session.Turns[i].TurnID != session.Turns[j].TurnID {
				return session.Turns[i].TurnID.Uint64() > session.Turns[j].TurnID.Uint64()
			}
			return session.Turns[i].StreamID.String() < session.Turns[j].StreamID.String()
		})
		session.TotalTurns = len(session.Turns)
		if limit > 0 && len(session.Turns) > limit {
			session.Turns = session.Turns[:limit]
		}
		sessions = append(sessions, session)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ID.String() < sessions[j].ID.String()
	})
	return sessions
}

func graphTurnKey(streamID primitives.EventStreamID, turnID primitives.TurnID) string {
	return streamID.String() + ":" + turnID.String()
}

func attachGraphDiffs(repo *checkpoint.Repo, sessions []graphSession) {
	for sessionIndex := range sessions {
		for turnIndex := range sessions[sessionIndex].Turns {
			turn := &sessions[sessionIndex].Turns[turnIndex]
			if turn.Pre == nil || turn.Post == nil {
				continue
			}
			diff, err := repo.DiffStatRefs(turn.Pre.Ref, turn.Post.Ref)
			if err != nil {
				turn.Warnings = append(turn.Warnings, fmt.Sprintf("diff stats unavailable: %v", err))
				continue
			}
			turn.Diff = diff
			turn.DiffLoaded = true
		}
	}
}

func attachGraphEventSummaries(repo *checkpoint.Repo, sessions []graphSession) {
	log := eventlog.Open(repo.MetadataDir)
	for sessionIndex := range sessions {
		events, err := log.Read(sessions[sessionIndex].ID)
		if err != nil {
			sessions[sessionIndex].Warnings = append(sessions[sessionIndex].Warnings, fmt.Sprintf("event log unavailable: %v", err))
			continue
		}
		summaries := queryindex.SummarizeTurnEventsByStream(events)
		for turnIndex := range sessions[sessionIndex].Turns {
			turn := &sessions[sessionIndex].Turns[turnIndex]
			key := queryindex.StreamTurnKey{StreamID: turn.StreamID, TurnID: turn.TurnID.Uint64()}
			turn.Events = summaries[key]
		}
	}
}

func attachGraphRollbackEvents(repo *checkpoint.Repo, sessions []graphSession, scope graphScope) {
	log := eventlog.Open(repo.MetadataDir)
	for sessionIndex := range sessions {
		events, err := log.Read(sessions[sessionIndex].ID)
		if err != nil {
			sessions[sessionIndex].Warnings = append(sessions[sessionIndex].Warnings, fmt.Sprintf("rollback events unavailable: %v", err))
			continue
		}
		for _, event := range events {
			if !scope.matches(event.WorktreeID, event.StreamID) {
				continue
			}
			if event.Type != primitives.EventTypeRollback {
				continue
			}
			rollback, err := graphRollbackFromEvent(event)
			if err != nil {
				sessions[sessionIndex].Warnings = append(sessions[sessionIndex].Warnings, fmt.Sprintf("rollback event %s unavailable: %v", event.Seq, err))
				continue
			}
			sessions[sessionIndex].Rollbacks = append(sessions[sessionIndex].Rollbacks, rollback)
		}
	}
}

func loadWorkspaceGraphEvents(repo *checkpoint.Repo, scope graphScope) ([]graphSave, []graphRollback, error) {
	saved, err := manualcheckpoints.Read(repo, scope.AllWorktrees)
	if err != nil {
		return nil, nil, err
	}
	saves := make([]graphSave, 0, len(saved))
	for _, item := range saved {
		if !scope.matches(item.Checkpoint.WorktreeID, item.Event.StreamID) {
			continue
		}
		at := item.Event.Time.Time
		if at.IsZero() {
			at = item.Checkpoint.Time
		}
		saves = append(saves, graphSave{
			Time: at, Seq: item.Event.Seq, Info: item.Checkpoint, Message: item.Message,
			SourceID: item.Event.SourceID, Warnings: append([]string(nil), item.Warnings...),
		})
	}
	events, err := manualcheckpoints.ReadEvents(repo, scope.AllWorktrees)
	if err != nil {
		return nil, nil, err
	}
	var rollbacks []graphRollback
	for _, event := range events {
		if event.Type != primitives.EventTypeRollback || !scope.matches(event.WorktreeID, event.StreamID) {
			continue
		}
		if _, err := manualcheckpoints.ValidateRollbackEvent(repo, event); err != nil {
			rollbacks = append(rollbacks, graphRollback{
				Time: event.Time.Time, Seq: event.Seq, Manual: true, TargetText: event.RawRef,
				WorktreeID: event.WorktreeID, SourceID: event.SourceID,
				Warnings: []string{fmt.Sprintf("workspace rollback event unavailable: %v", err)},
			})
			continue
		}
		rollback, err := graphRollbackFromEvent(event)
		if err != nil {
			rollbacks = append(rollbacks, graphRollback{
				Time: event.Time.Time, Seq: event.Seq, Manual: true, TargetText: event.RawRef,
				WorktreeID: event.WorktreeID, SourceID: event.SourceID,
				Warnings: []string{fmt.Sprintf("workspace rollback event unavailable: %v", err)},
			})
			continue
		}
		rollbacks = append(rollbacks, rollback)
	}
	return saves, rollbacks, nil
}

type graphRollbackPayload struct {
	Target          string `json:"target"`
	Ref             string `json:"ref"`
	CommitSHA       string `json:"commit_sha"`
	SafetyRef       string `json:"safety_ref"`
	SafetyCommitSHA string `json:"safety_commit_sha"`
	Mode            string `json:"mode"`
}

func graphRollbackFromEvent(event eventlog.Event) (graphRollback, error) {
	if event.TurnID == nil {
		parsed, err := manualcheckpoints.ParseRollbackEvent(event)
		if err != nil {
			return graphRollback{}, err
		}
		return graphRollback{
			Time: event.Time.Time, Seq: event.Seq, Manual: true,
			TargetText: parsed.Target.String(), CheckpointRef: parsed.Ref.String(), CommitSHA: parsed.Target,
			SafetyRef: parsed.Payload.SafetyRef, SafetyCommitSHA: parsed.SafetyCommit.String(),
			Mode: parsed.Mode.String(), SourceID: event.SourceID, WorktreeID: parsed.WorktreeID,
		}, nil
	}
	var payload graphRollbackPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return graphRollback{}, fmt.Errorf("payload invariant failed: %w", err)
	}

	targetText := payload.Target
	if targetText == "" {
		targetText = event.RawRef
	}
	if targetText == "" {
		return graphRollback{}, fmt.Errorf("target invariant failed: must not be empty")
	}
	commit, err := primitives.ParseCommitSHA(payload.CommitSHA)
	if err != nil {
		return graphRollback{}, fmt.Errorf("checkpoint id invariant failed: %w", err)
	}

	rollback := graphRollback{
		Time:            event.Time.Time,
		Seq:             event.Seq,
		CheckpointRef:   payload.Ref,
		CommitSHA:       commit,
		SafetyRef:       payload.SafetyRef,
		SafetyCommitSHA: payload.SafetyCommitSHA,
		Mode:            payload.Mode,
		SourceID:        event.SourceID,
		TargetText:      targetText,
		WorktreeID:      event.WorktreeID,
	}
	target, targetErr := primitives.ParseTargetRef(targetText)
	if targetErr == nil {
		target, targetErr = targetWithDefaultPhase(target)
		if targetErr != nil {
			return graphRollback{}, targetErr
		}
		rollback.Target = target
	} else {
		selector, selectorErr := primitives.ParseCommitSHA(targetText)
		ref, refErr := primitives.ParseCheckpointRef(payload.Ref)
		if selectorErr != nil || selector != commit || refErr != nil {
			return graphRollback{}, fmt.Errorf("target invariant failed: %w", targetErr)
		}
		parts, partsErr := ref.Parts()
		if partsErr != nil || !parts.Manual {
			return graphRollback{}, fmt.Errorf("target invariant failed: manual checkpoint ref required")
		}
		rollback.Manual = true
		rollback.WorktreeID = parts.WorktreeID
	}
	if rollback.Mode == "" {
		rollback.Mode = primitives.RollbackModeCheckpoint.String()
	}
	if rollback.CheckpointRef == "" {
		rollback.Warnings = append(rollback.Warnings, "checkpoint ref missing from rollback payload")
	}
	if rollback.SafetyRef == "" || rollback.SafetyCommitSHA == "" {
		rollback.Warnings = append(rollback.Warnings, "safety id missing from rollback payload")
	}
	return rollback, nil
}

func renderCheckpointGraph(w io.Writer, sessions []graphSession, options graphRenderOptions) error {
	return renderCheckpointGraphWithWorkspace(w, sessions, nil, nil, options)
}

func renderCheckpointGraphWithWorkspace(w io.Writer, sessions []graphSession, saves []graphSave, workspaceRollbacks []graphRollback, options graphRenderOptions) error {
	fmt.Fprintln(w)
	if len(sessions) == 0 && len(saves) == 0 && len(workspaceRollbacks) == 0 {
		fmt.Fprintln(w, "No checkpoints recorded yet.")
		fmt.Fprintln(w)
		return nil
	}

	rows := buildGraphTimelineRowsWithWorkspace(sessions, saves, workspaceRollbacks)
	spans := buildLaneSpans(rows)
	labels := buildGraphSessionLabels(sessions)
	totalTurns := 0
	totalShownTurns := 0
	totalRollbacks := 0
	for _, session := range sessions {
		totalTurns += session.TotalTurns
		totalShownTurns += len(session.Turns)
		totalRollbacks += len(session.Rollbacks)
	}
	totalRollbacks += len(workspaceRollbacks)
	countSuffix := formatRollbackCountSuffix(totalRollbacks) + formatSaveCountSuffix(len(saves))
	if totalShownTurns == totalTurns {
		fmt.Fprintf(w, "checkpoint graph: %d %s, %d %s%s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalTurns, pluralWord(totalTurns, "turn", "turns"),
			countSuffix)
	} else {
		fmt.Fprintf(w, "checkpoint graph: %d %s, showing %d of %d %s%s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalShownTurns, totalTurns, pluralWord(totalTurns, "turn", "turns"),
			countSuffix)
	}

	if len(sessions) > 0 {
		fmt.Fprintf(w, "sessions:")
		for sessionIndex, session := range sessions {
			label := formatSessionLabel(labels[sessionIndex], sessionIndex, options)
			if len(session.Turns) == session.TotalTurns {
				fmt.Fprintf(w, " %s", label)
			} else {
				fmt.Fprintf(w, " %s(%d/%d)", label, len(session.Turns), session.TotalTurns)
			}
		}
		fmt.Fprintln(w)
	}

	for _, session := range sessions {
		for _, warning := range session.Warnings {
			fmt.Fprintf(w, "warning %s: %s\n", session.ID, warning)
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(w)
		return nil
	}
	fmt.Fprintln(w)

	for rowIndex, row := range rows {
		detailPrefix := renderTimelineDetailPrefix(rowIndex, row, len(sessions), spans, options)
		if row.Save != nil {
			renderSaveRow(w, *row.Save, len(sessions), options)
			if options.Verbose {
				renderVerboseSaveDetails(w, detailPrefix, *row.Save)
			}
			for _, warning := range row.Save.Warnings {
				fmt.Fprintf(w, "%swarning: %s\n", detailPrefix, warning)
			}
		} else if row.Rollback != nil {
			renderRollbackRow(w, *row.Rollback, sessions, labels, options)
			if options.Verbose {
				renderVerboseRollbackDetails(w, detailPrefix, *row.Rollback, options)
			}
			for _, warning := range row.Rollback.Warnings {
				fmt.Fprintf(w, "%swarning: %s\n", detailPrefix, warning)
			}
		} else if row.Turn != nil {
			turn := *row.Turn
			linePrefix := renderLanePrefix(rowIndex, row.SessionIndex, len(sessions), spans, true, options)
			fmt.Fprintf(w, "%s%s - %s %s turn %-6s %s %s %s\n",
				linePrefix,
				styleHash(formatDisplayID(turn), options),
				formatDisplayTime(turn),
				formatSessionLabel(labels[row.SessionIndex], row.SessionIndex, options),
				turn.TurnID,
				styleTool(formatTurnAction(turn), options),
				styleDim(turnStatus(turn), options),
				formatTurnHeadlineSummary(turn, options),
			)

			if prompt := truncateText(turn.Events.Prompt, 140); prompt != "" {
				fmt.Fprintf(w, "%s%s %q\n", detailPrefix, styleDim("Prompt:", options), prompt)
			}
			if options.Verbose {
				renderVerboseTurnDetails(w, detailPrefix, row.SessionID, turn, options)
			}
			for _, warning := range turn.Warnings {
				fmt.Fprintf(w, "%swarning: %s\n", detailPrefix, warning)
			}
		}
		if rowIndex < len(rows)-1 {
			fmt.Fprintln(w, detailPrefix)
		}
	}

	fmt.Fprintln(w)
	return nil
}

func attachTranscriptEvents(repo *checkpoint.Repo, sessions []graphSession, sessionFilter string, limit int, scope graphScope) ([]graphSession, error) {
	log := eventlog.Open(repo.MetadataDir)
	sessionIDs := make([]primitives.SessionID, 0, len(sessions))
	sessionIndexes := make(map[string]int, len(sessions))
	for index, session := range sessions {
		sessionIDs = append(sessionIDs, session.ID)
		sessionIndexes[session.ID.String()] = index
	}

	if sessionFilter != "" {
		sessionID, err := primitives.ParseSessionID(sessionFilter)
		if err != nil {
			return nil, err
		}
		if _, ok := sessionIndexes[sessionID.String()]; !ok {
			sessions = append(sessions, graphSession{ID: sessionID})
			sessionIndexes[sessionID.String()] = len(sessions) - 1
			sessionIDs = append(sessionIDs, sessionID)
		}
	} else {
		logSessions, err := log.ListSessions()
		if err != nil {
			return nil, fmt.Errorf("list event log sessions: %w", err)
		}
		for _, sessionID := range logSessions {
			if _, ok := sessionIndexes[sessionID.String()]; ok {
				continue
			}
			sessions = append(sessions, graphSession{ID: sessionID})
			sessionIndexes[sessionID.String()] = len(sessions) - 1
			sessionIDs = append(sessionIDs, sessionID)
		}
	}

	for _, sessionID := range sessionIDs {
		sessionIndex := sessionIndexes[sessionID.String()]
		events, err := log.Read(sessionID)
		if err != nil {
			sessions[sessionIndex].Warnings = append(sessions[sessionIndex].Warnings, fmt.Sprintf("event log unavailable: %v", err))
			continue
		}
		filteredEvents := events[:0]
		for _, event := range events {
			if scope.matches(event.WorktreeID, event.StreamID) {
				filteredEvents = append(filteredEvents, event)
			}
		}
		events = filteredEvents

		turnIndexes := make(map[string]int, len(sessions[sessionIndex].Turns))
		for turnIndex := range sessions[sessionIndex].Turns {
			turn := sessions[sessionIndex].Turns[turnIndex]
			turnIndexes[graphTurnKey(turn.StreamID, turn.TurnID)] = turnIndex
		}
		summaries := queryindex.SummarizeTurnEventsByStream(events)
		for _, event := range events {
			if event.TurnID == nil {
				continue
			}
			turnKey := graphTurnKey(event.StreamID, *event.TurnID)
			turnIndex, ok := turnIndexes[turnKey]
			if !ok {
				sessions[sessionIndex].Turns = append(sessions[sessionIndex].Turns, graphTurn{WorktreeID: event.WorktreeID, StreamID: event.StreamID, TurnID: *event.TurnID})
				turnIndex = len(sessions[sessionIndex].Turns) - 1
				turnIndexes[turnKey] = turnIndex
			}
			sessions[sessionIndex].Turns[turnIndex].EventLog = append(sessions[sessionIndex].Turns[turnIndex].EventLog, event)
		}
		for turnKey, summary := range summaries {
			turnID, err := primitives.NewTurnID(turnKey.TurnID)
			if err != nil {
				continue
			}
			key := graphTurnKey(turnKey.StreamID, turnID)
			turnIndex, ok := turnIndexes[key]
			if !ok {
				sessions[sessionIndex].Turns = append(sessions[sessionIndex].Turns, graphTurn{StreamID: turnKey.StreamID, TurnID: turnID})
				turnIndex = len(sessions[sessionIndex].Turns) - 1
				turnIndexes[key] = turnIndex
			}
			sessions[sessionIndex].Turns[turnIndex].Events = summary
		}

		sort.Slice(sessions[sessionIndex].Turns, func(i, j int) bool {
			leftTime := turnDisplayTime(sessions[sessionIndex].Turns[i])
			rightTime := turnDisplayTime(sessions[sessionIndex].Turns[j])
			if !leftTime.Equal(rightTime) {
				return leftTime.After(rightTime)
			}
			return sessions[sessionIndex].Turns[i].TurnID.Uint64() > sessions[sessionIndex].Turns[j].TurnID.Uint64()
		})
		sessions[sessionIndex].TotalTurns = len(sessions[sessionIndex].Turns)
		if limit > 0 && len(sessions[sessionIndex].Turns) > limit {
			sessions[sessionIndex].Turns = sessions[sessionIndex].Turns[:limit]
		}
	}

	filtered := sessions[:0]
	for _, session := range sessions {
		if len(session.Turns) == 0 && len(session.Warnings) == 0 {
			continue
		}
		filtered = append(filtered, session)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID.String() < filtered[j].ID.String()
	})
	return filtered, nil
}

func renderTranscriptLog(w io.Writer, sessions []graphSession, options graphRenderOptions) error {
	fmt.Fprintln(w)
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No transcript events recorded yet.")
		fmt.Fprintln(w)
		return nil
	}

	labels := buildGraphSessionLabels(sessions)
	totalTurns := 0
	totalShownTurns := 0
	for _, session := range sessions {
		totalTurns += session.TotalTurns
		totalShownTurns += len(session.Turns)
	}
	if totalShownTurns == totalTurns {
		fmt.Fprintf(w, "transcript log: %d %s, %d %s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalTurns, pluralWord(totalTurns, "turn", "turns"))
	} else {
		fmt.Fprintf(w, "transcript log: %d %s, showing %d of %d %s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalShownTurns, totalTurns, pluralWord(totalTurns, "turn", "turns"))
	}

	fmt.Fprintf(w, "sessions:")
	for sessionIndex, session := range sessions {
		label := formatSessionLabel(labels[sessionIndex], sessionIndex, options)
		if len(session.Turns) == session.TotalTurns {
			fmt.Fprintf(w, " %s", label)
		} else {
			fmt.Fprintf(w, " %s(%d/%d)", label, len(session.Turns), session.TotalTurns)
		}
	}
	fmt.Fprintln(w)

	for sessionIndex, session := range sessions {
		for _, warning := range session.Warnings {
			fmt.Fprintf(w, "warning %s: %s\n", session.ID, warning)
		}
		if len(session.Turns) == 0 {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Session: %s\n", formatSessionLabel(labels[sessionIndex], sessionIndex, options))
		for turnIndex := len(session.Turns) - 1; turnIndex >= 0; turnIndex-- {
			if err := renderTranscriptTurn(w, session.Turns[turnIndex], options); err != nil {
				return err
			}
			if turnIndex > 0 {
				fmt.Fprintln(w)
			}
		}
	}

	fmt.Fprintln(w)
	return nil
}

func renderTranscriptTurn(w io.Writer, turn graphTurn, options graphRenderOptions) error {
	fmt.Fprintf(w, "* %s\n", formatTranscriptTurnHeader(turn, options))
	if turn.DiffLoaded && len(turn.Diff.Files) > 0 {
		for _, file := range turn.Diff.Files {
			fmt.Fprintf(w, "  %s\n", formatTranscriptDiffFileStat(file, options))
		}
		fmt.Fprintln(w)
	}
	transcript := transcriptFromEvents(turn.EventLog)
	if len(transcript.Prompts) == 0 && len(transcript.Assistant) == 0 && len(transcript.Tools) == 0 && len(transcript.Errors) == 0 {
		fmt.Fprintln(w, "  No normalized transcript events.")
		for _, warning := range turn.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", warning)
		}
		return nil
	}

	for _, prompt := range transcript.Prompts {
		writeTranscriptText(w, "  ", "Human:", prompt)
	}
	if len(transcript.Prompts) > 0 && (len(transcript.Assistant) > 0 || len(transcript.Tools) > 0) {
		fmt.Fprintln(w, "    ↓")
	}
	for _, assistant := range transcript.Assistant {
		writeTranscriptText(w, "  ", "Agent:", assistant)
	}
	if len(transcript.Tools) > 0 {
		if len(transcript.Assistant) == 0 {
			fmt.Fprintln(w, "  Tools:")
		}
		for index, tool := range transcript.Tools {
			prefix := "├─"
			if index == len(transcript.Tools)-1 {
				prefix = "└─"
			}
			fmt.Fprintf(w, "      %s %s\n", prefix, formatTranscriptTool(tool, options))
		}
	}
	for _, message := range transcript.Errors {
		writeTranscriptText(w, "  ", "Error:", message)
	}
	for _, warning := range turn.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	return nil
}

func formatTranscriptTurnHeader(turn graphTurn, options graphRenderOptions) string {
	parts := []string{}
	if id := formatDisplayID(turn); id != "unknown" {
		parts = append(parts, styleHash(id, options))
	}
	if action := formatTurnAction(turn); action != "Turn" {
		parts = append(parts, styleTool(action, options))
	}
	if at := formatDisplayTime(turn); at != "--:--" {
		parts = append(parts, at)
	}
	parts = append(parts, "turn "+turn.TurnID.String())
	return strings.Join(parts, " - ")
}

type transcriptTurn struct {
	Prompts   []string
	Assistant []string
	Tools     []transcriptTool
	Errors    []string
}

type transcriptTool struct {
	Name  string
	Input json.RawMessage
}

func transcriptFromEvents(events []eventlog.Event) transcriptTurn {
	var transcript transcriptTurn
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypePromptUser:
			if text := payloadText(event.Payload, "text"); text != "" {
				transcript.Prompts = append(transcript.Prompts, text)
			}
		case primitives.EventTypeAssistantMessage:
			if text := payloadText(event.Payload, "text"); text != "" {
				transcript.Assistant = append(transcript.Assistant, text)
			}
		case primitives.EventTypeToolCall:
			var payload struct {
				ToolName string          `json:"tool_name"`
				Input    json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				transcript.Errors = append(transcript.Errors, fmt.Sprintf("malformed tool call event %s", event.Seq))
				continue
			}
			if payload.ToolName == "" {
				payload.ToolName = "Tool"
			}
			transcript.Tools = append(transcript.Tools, transcriptTool{Name: payload.ToolName, Input: payload.Input})
		case primitives.EventTypeError:
			message := payloadText(event.Payload, "message")
			if message == "" {
				message = string(event.Payload)
			}
			transcript.Errors = append(transcript.Errors, message)
		}
	}
	return transcript
}

func payloadText(payload json.RawMessage, key string) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return ""
	}
	raw, ok := object[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func writeTranscriptText(w io.Writer, prefix, label, text string) {
	lines := wrapTranscriptText(text, 90)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "%s%s %s\n", prefix, label, lines[0])
	continuation := prefix + strings.Repeat(" ", len(label)+1)
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "%s%s\n", continuation, line)
	}
}

func wrapTranscriptText(text string, limit int) []string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil
	}
	if limit <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	var current strings.Builder
	for _, word := range words {
		if current.Len() == 0 {
			current.WriteString(word)
			continue
		}
		if current.Len()+1+len(word) <= limit {
			current.WriteString(" ")
			current.WriteString(word)
			continue
		}
		lines = append(lines, current.String())
		current.Reset()
		current.WriteString(word)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func formatTranscriptTool(tool transcriptTool, options graphRenderOptions) string {
	if strings.TrimSpace(tool.Name) == "" {
		tool.Name = "Tool"
	}
	args := formatTranscriptToolArgs(tool.Input)
	if args == "" {
		return styleTranscriptToolName(tool.Name, options)
	}
	return styleTranscriptToolName(tool.Name, options) + " (" + args + ")"
}

func formatTranscriptToolArgs(input json.RawMessage) string {
	if len(input) == 0 || strings.TrimSpace(string(input)) == "null" {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(input, &object); err != nil {
		return ""
	}

	keys := transcriptToolArgKeys(object)
	args := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		if formatted := formatTranscriptToolArgValue(value); formatted != "" {
			args = append(args, key+": "+formatted)
		}
	}
	return strings.Join(args, ", ")
}

func transcriptToolArgKeys(object map[string]any) []string {
	priority := []string{"file_path", "path", "command", "query", "description", "prompt"}
	seen := make(map[string]struct{}, len(priority))
	var keys []string
	for _, key := range priority {
		if _, ok := object[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}

	var rest []string
	for key := range object {
		if key == "content" || key == "input" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		rest = append(rest, key)
	}
	sort.Strings(rest)
	for _, key := range rest {
		if len(keys) >= 3 {
			break
		}
		keys = append(keys, key)
	}
	return keys
}

func formatTranscriptToolArgValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return truncateText(typed, 80)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return truncateText(string(data), 80)
	}
}

func buildGraphTimelineRows(sessions []graphSession) []graphTimelineRow {
	return buildGraphTimelineRowsWithWorkspace(sessions, nil, nil)
}

func buildGraphTimelineRowsWithWorkspace(sessions []graphSession, saves []graphSave, workspaceRollbacks []graphRollback) []graphTimelineRow {
	var rows []graphTimelineRow
	for sessionIndex, session := range sessions {
		for _, turn := range session.Turns {
			turnCopy := turn
			rows = append(rows, graphTimelineRow{
				SessionID:    session.ID,
				SessionIndex: sessionIndex,
				Turn:         &turnCopy,
			})
		}
		for _, rollback := range session.Rollbacks {
			rollbackCopy := rollback
			rows = append(rows, graphTimelineRow{
				SessionID:    session.ID,
				SessionIndex: sessionIndex,
				Rollback:     &rollbackCopy,
			})
		}
	}
	for _, save := range saves {
		saveCopy := save
		rows = append(rows, graphTimelineRow{SessionIndex: -1, Save: &saveCopy})
	}
	for _, rollback := range workspaceRollbacks {
		rollbackCopy := rollback
		rows = append(rows, graphTimelineRow{SessionIndex: -1, Rollback: &rollbackCopy})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftTime := rowDisplayTime(rows[i])
		rightTime := rowDisplayTime(rows[j])
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		if rows[i].Rollback != nil && rows[j].Rollback == nil {
			return true
		}
		if rows[i].Rollback == nil && rows[j].Rollback != nil {
			return false
		}
		if rows[i].SessionIndex != rows[j].SessionIndex {
			return rows[i].SessionIndex < rows[j].SessionIndex
		}
		if rows[i].Turn != nil && rows[j].Turn != nil {
			return rows[i].Turn.TurnID.Uint64() > rows[j].Turn.TurnID.Uint64()
		}
		if rows[i].Rollback != nil && rows[j].Rollback != nil {
			return rows[i].Rollback.Seq.Uint64() > rows[j].Rollback.Seq.Uint64()
		}
		if rows[i].Save != nil && rows[j].Save != nil {
			return rows[i].Save.Seq.Uint64() > rows[j].Save.Seq.Uint64()
		}
		return false
	})
	return rows
}

func rowDisplayTime(row graphTimelineRow) time.Time {
	if row.Save != nil {
		return row.Save.Time
	}
	if row.Rollback != nil {
		return row.Rollback.Time
	}
	if row.Turn != nil {
		return turnDisplayTime(*row.Turn)
	}
	return time.Time{}
}

func buildGraphSessionLabels(sessions []graphSession) []string {
	labels := make([]string, len(sessions))
	counts := make(map[string]int, len(sessions))
	for index, session := range sessions {
		labels[index] = graphSessionDisplayName(session)
		counts[labels[index]]++
	}

	for index, session := range sessions {
		if counts[labels[index]] <= 1 {
			continue
		}
		labels[index] = labels[index] + " " + shortSessionID(session.ID.String())
	}
	return labels
}

func graphSessionDisplayName(session graphSession) string {
	agent := sessionDisplayAgent(session)
	if at := sessionDisplayTime(session); agent != "" && !at.IsZero() {
		return agent + " " + at.UTC().Format("15:04")
	}
	return session.ID.String()
}

func sessionDisplayAgent(session graphSession) string {
	for _, turn := range session.Turns {
		if turn.Events.Adapter != "" {
			return normalizeSessionAgent(turn.Events.Adapter)
		}
	}
	return ""
}

func normalizeSessionAgent(agent string) string {
	switch agent {
	case "claude-code":
		return "claude"
	case "t3-code":
		return "t3"
	default:
		return agent
	}
}

func sessionDisplayTime(session graphSession) time.Time {
	var earliest time.Time
	for _, turn := range session.Turns {
		at := turnDisplayTime(turn)
		if at.IsZero() {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

func shortSessionID(sessionID string) string {
	parts := strings.FieldsFunc(sessionID, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			if len(parts[i]) <= 8 {
				return parts[i]
			}
			return parts[i][len(parts[i])-8:]
		}
	}
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[len(sessionID)-8:]
}

func buildLaneSpans(rows []graphTimelineRow) map[int]laneSpan {
	spans := make(map[int]laneSpan)
	for rowIndex, row := range rows {
		if row.Turn == nil {
			continue
		}
		span, ok := spans[row.SessionIndex]
		if !ok {
			spans[row.SessionIndex] = laneSpan{First: rowIndex, Last: rowIndex}
			continue
		}
		if rowIndex < span.First {
			span.First = rowIndex
		}
		if rowIndex > span.Last {
			span.Last = rowIndex
		}
		spans[row.SessionIndex] = span
	}
	return spans
}

func renderLanePrefix(rowIndex, currentSessionIndex, sessionCount int, spans map[int]laneSpan, marker bool, options graphRenderOptions) string {
	var line strings.Builder
	for sessionIndex := 0; sessionIndex < sessionCount; sessionIndex++ {
		span, active := spans[sessionIndex]
		active = active && rowIndex >= span.First && rowIndex <= span.Last
		token := "  "
		if sessionIndex == currentSessionIndex {
			if marker {
				token = "* "
			} else {
				token = "| "
			}
		} else if active {
			token = "| "
		}
		line.WriteString(styleSession(token, sessionIndex, options))
	}
	return line.String()
}

func renderTimelineDetailPrefix(rowIndex int, row graphTimelineRow, sessionCount int, spans map[int]laneSpan, options graphRenderOptions) string {
	if row.Rollback != nil || row.Save != nil {
		return renderAllLanePrefix(sessionCount, options)
	}
	return renderLanePrefix(rowIndex, row.SessionIndex, sessionCount, spans, false, options)
}

func renderAllLanePrefix(sessionCount int, options graphRenderOptions) string {
	var line strings.Builder
	if sessionCount == 0 {
		sessionCount = 1
	}
	for sessionIndex := 0; sessionIndex < sessionCount; sessionIndex++ {
		line.WriteString(styleSession("| ", sessionIndex, options))
	}
	return line.String()
}

func renderRollbackLanePrefix(sessionCount int, options graphRenderOptions) string {
	var line strings.Builder
	if sessionCount == 0 {
		sessionCount = 1
	}
	for range sessionCount {
		line.WriteString(styleRollback("! ", options))
	}
	return line.String()
}

func renderRollbackRow(w io.Writer, rollback graphRollback, sessions []graphSession, labels []string, options graphRenderOptions) {
	if rollback.Manual {
		id := formatObjectID(rollback.CommitSHA, false)
		if id == "" {
			id = "<invalid>"
		}
		fmt.Fprintf(w, "%s%s %s %s\n",
			renderRollbackLanePrefix(len(sessions), options),
			styleRollback("------------", options),
			styleRollback("reverted to saved", options),
			styleHash(id, options),
		)
		return
	}
	sessionIndex := graphSessionIndex(sessions, rollback.Target.SessionID())
	targetLabel := "[" + rollback.Target.SessionID().String() + "]"
	if sessionIndex >= 0 {
		targetLabel = formatSessionLabel(labels[sessionIndex], sessionIndex, options)
	}
	phase := formatRollbackPhase(rollback, options)
	if phase != "" {
		phase += " "
	}
	fmt.Fprintf(w, "%s%s %s %s turn %s %s%s\n",
		renderRollbackLanePrefix(len(sessions), options),
		styleRollback("------------", options),
		styleRollback("reverted to", options),
		targetLabel,
		rollback.Target.TurnID(),
		phase,
		styleHash(formatObjectID(rollback.CommitSHA, false), options),
	)
}

func renderVerboseRollbackDetails(w io.Writer, prefix string, rollback graphRollback, options graphRenderOptions) {
	if !rollback.Time.IsZero() {
		fmt.Fprintf(w, "%srollback: %s\n", prefix, formatGraphTime(rollback.Time))
	}
	target := rollback.Target.String()
	if rollback.Manual {
		target = rollback.TargetText
		if target == "" {
			target = "<invalid>"
		}
	}
	fmt.Fprintf(w, "%starget: %s\n", prefix, target)
	if rollback.Mode != "" {
		fmt.Fprintf(w, "%smode: %s\n", prefix, rollback.Mode)
	}
	if rollback.CheckpointRef != "" {
		fmt.Fprintf(w, "%scheckpoint:\n", prefix)
		fmt.Fprintf(w, "%s  id:  %s\n", prefix, rollback.CommitSHA)
		fmt.Fprintf(w, "%s  ref: %s\n", prefix, rollback.CheckpointRef)
	}
	if rollback.SafetyRef != "" {
		fmt.Fprintf(w, "%ssafety:\n", prefix)
		if rollback.SafetyCommitSHA != "" {
			fmt.Fprintf(w, "%s  id:  %s\n", prefix, rollback.SafetyCommitSHA)
		}
		fmt.Fprintf(w, "%s  ref: %s\n", prefix, rollback.SafetyRef)
	}
	if rollback.SourceID != "" {
		fmt.Fprintf(w, "%ssource: %s\n", prefix, rollback.SourceID)
	}
}

func renderSaveRow(w io.Writer, save graphSave, sessionCount int, options graphRenderOptions) {
	count := sessionCount
	if count == 0 {
		count = 1
	}
	var prefix strings.Builder
	for range count {
		prefix.WriteString(styleTool("+ ", options))
	}
	message := ""
	if save.Message != "" {
		message = " " + fmt.Sprintf("%q", truncateText(save.Message, 120))
	}
	fmt.Fprintf(w, "%s%s %s%s\n",
		prefix.String(),
		styleTool("------------ saved", options),
		styleHash(formatObjectID(save.Info.Commit, false), options),
		message,
	)
}

func renderVerboseSaveDetails(w io.Writer, prefix string, save graphSave) {
	if !save.Time.IsZero() {
		fmt.Fprintf(w, "%ssaved: %s\n", prefix, formatGraphTime(save.Time))
	}
	fmt.Fprintf(w, "%scheckpoint:\n", prefix)
	fmt.Fprintf(w, "%s  id:  %s\n", prefix, save.Info.Commit)
	fmt.Fprintf(w, "%s  ref: %s\n", prefix, save.Info.Ref)
	if save.Info.CanonicalRef != "" {
		fmt.Fprintf(w, "%s  canonical: %s\n", prefix, save.Info.CanonicalRef)
	}
	if save.Info.WorktreeID != "" {
		fmt.Fprintf(w, "%sworktree: %s\n", prefix, save.Info.WorktreeID)
	}
	if save.Message != "" {
		fmt.Fprintf(w, "%smessage: %q\n", prefix, save.Message)
	}
	if save.SourceID != "" {
		fmt.Fprintf(w, "%ssource: %s\n", prefix, save.SourceID)
	}
}

func graphSessionIndex(sessions []graphSession, sessionID primitives.SessionID) int {
	for index, session := range sessions {
		if session.ID == sessionID {
			return index
		}
	}
	return -1
}

func renderVerboseTurnDetails(w io.Writer, prefix string, sessionID primitives.SessionID, turn graphTurn, options graphRenderOptions) {
	fmt.Fprintf(w, "%ssession: %s\n", prefix, sessionID)
	if turn.WorktreeID != "" {
		fmt.Fprintf(w, "%sworktree: %s\n", prefix, turn.WorktreeID)
	}
	if turn.StreamID != "" {
		fmt.Fprintf(w, "%sstream: %s\n", prefix, turn.StreamID)
	}
	if turn.DiffLoaded {
		fmt.Fprintf(w, "%sdiff: %s\n", prefix, formatDiffSummary(turn.Diff))
	}
	if eventSummary := formatEventSummary(turn.Events, true); eventSummary != "" {
		fmt.Fprintf(w, "%sevents: %s\n", prefix, eventSummary)
	}
	if turn.Events.Assistant != "" {
		fmt.Fprintf(w, "%sassistant: %s\n", prefix, truncateText(turn.Events.Assistant, 140))
	}
	if typeCounts := formatEventTypeCounts(turn.Events.TypeCounts); typeCounts != "" {
		fmt.Fprintf(w, "%sevent types: %s\n", prefix, typeCounts)
	}
	if turn.Pre != nil {
		renderVerboseCheckpointDetails(w, prefix, "pre", turn.Pre)
	}
	if turn.Post != nil {
		renderVerboseCheckpointDetails(w, prefix, "post", turn.Post)
	}
	for _, file := range turn.Diff.Files {
		fmt.Fprintf(w, "%sfile: %s\n", prefix, formatDiffFileStat(file))
	}
}

func renderVerboseCheckpointDetails(w io.Writer, prefix string, label string, info *checkpoint.CheckpointRefInfo) {
	fmt.Fprintf(w, "%s%s:\n", prefix, label)
	if info.ID != "" {
		fmt.Fprintf(w, "%s  checkpoint: %s\n", prefix, info.ID)
	}
	fmt.Fprintf(w, "%s  commit: %s\n", prefix, info.Commit)
	fmt.Fprintf(w, "%s  ref: %s\n", prefix, info.Ref)
}

func formatDisplayID(turn graphTurn) string {
	switch {
	case turn.Post != nil:
		return formatObjectID(turn.Post.Commit, false)
	case turn.Pre != nil:
		return formatObjectID(turn.Pre.Commit, false)
	default:
		return "unknown"
	}
}

func formatDisplayTime(turn graphTurn) string {
	displayTime := turnDisplayTime(turn)
	if displayTime.IsZero() {
		return "--:--"
	}
	return displayTime.UTC().Format("15:04")
}

func turnDisplayTime(turn graphTurn) time.Time {
	switch {
	case turn.Post != nil && !turn.Post.Time.IsZero():
		return turn.Post.Time
	case turn.Pre != nil && !turn.Pre.Time.IsZero():
		return turn.Pre.Time
	case !turn.Events.Last.IsZero():
		return turn.Events.Last
	case !turn.Events.First.IsZero():
		return turn.Events.First
	default:
		return time.Time{}
	}
}

func formatTurnAction(turn graphTurn) string {
	switch {
	case len(turn.Events.ToolNames) == 1:
		return turn.Events.ToolNames[0]
	case len(turn.Events.ToolNames) > 1:
		return fmt.Sprintf("%s +%d", turn.Events.ToolNames[0], len(turn.Events.ToolNames)-1)
	case turn.Events.Prompt != "":
		return "Prompt"
	case turn.Pre != nil || turn.Post != nil:
		return "Checkpoint"
	default:
		return "Turn"
	}
}

func formatTurnHeadlineSummary(turn graphTurn, options graphRenderOptions) string {
	var parts []string
	if turn.DiffLoaded {
		parts = append(parts, styleDim(formatDiffSummary(turn.Diff), options))
	}
	if !options.Verbose {
		if eventSummary := formatEventSummary(turn.Events, false); eventSummary != "" {
			parts = append(parts, styleDim(eventSummary, options))
		}
	}
	return strings.Join(parts, "  ")
}

func formatRollbackPhase(rollback graphRollback, options graphRenderOptions) string {
	phase, ok := rollback.Target.Phase()
	if !ok || phase != primitives.CheckpointPhasePre {
		return ""
	}
	return styleRollbackPhase(phase.String(), options)
}

func turnStatus(turn graphTurn) string {
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

func formatTurnRefs(turn graphTurn, full bool) string {
	pre := "pre missing"
	if turn.Pre != nil {
		pre = "pre " + formatObjectID(turn.Pre.Commit, full)
	}
	post := "post pending"
	if turn.Post != nil {
		post = "post " + formatObjectID(turn.Post.Commit, full)
	}
	return pre + " -> " + post
}

func formatObjectID(id primitives.CommitSHA, full bool) string {
	value := id.String()
	if full || len(value) <= 12 {
		return value
	}
	return value[:12]
}

func formatTurnTimeRange(turn graphTurn) string {
	switch {
	case turn.Pre != nil && turn.Post != nil:
		return formatTimeRange(turn.Pre.Time, turn.Post.Time)
	case turn.Pre != nil:
		return formatGraphTime(turn.Pre.Time)
	case turn.Post != nil:
		return formatGraphTime(turn.Post.Time)
	default:
		return ""
	}
}

func formatTimeRange(start, end time.Time) string {
	if start.IsZero() && end.IsZero() {
		return ""
	}
	if start.IsZero() {
		return formatGraphTime(end)
	}
	if end.IsZero() {
		return formatGraphTime(start)
	}

	start = start.UTC()
	end = end.UTC()
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return start.Format("2006-01-02 15:04:05") + ".." + end.Format("15:04:05 UTC")
	}
	return formatGraphTime(start) + ".." + formatGraphTime(end)
}

func formatGraphTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatDiffSummary(summary checkpoint.DiffSummary) string {
	fileCount := len(summary.Files)
	if fileCount == 0 {
		return "no file changes"
	}

	parts := []string{
		fmt.Sprintf("%d %s", fileCount, pluralWord(fileCount, "file", "files")),
		fmt.Sprintf("+%d", summary.Additions),
		fmt.Sprintf("-%d", summary.Deletions),
	}
	if summary.BinaryFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", summary.BinaryFiles, pluralWord(summary.BinaryFiles, "binary", "binaries")))
	}
	return strings.Join(parts, " ")
}

func formatRollbackCountSuffix(count int) string {
	if count == 0 {
		return ""
	}
	return fmt.Sprintf(", %d %s", count, pluralWord(count, "rollback", "rollbacks"))
}

func formatSaveCountSuffix(count int) string {
	if count == 0 {
		return ""
	}
	return fmt.Sprintf(", %d %s", count, pluralWord(count, "save", "saves"))
}

func formatDiffFileStat(file checkpoint.DiffFileStat) string {
	if file.Binary {
		return file.Path + " binary"
	}
	return fmt.Sprintf("%s +%d -%d", file.Path, file.Additions, file.Deletions)
}

func formatTranscriptDiffFileStat(file checkpoint.DiffFileStat, options graphRenderOptions) string {
	if file.Binary {
		return fmt.Sprintf("%s %s", file.Path, styleDim("(binary)", options))
	}
	return fmt.Sprintf("%s %s %s",
		file.Path,
		styleAddition(fmt.Sprintf("+%d", file.Additions), options),
		styleDeletion(fmt.Sprintf("-%d", file.Deletions), options),
	)
}

func formatEventSummary(summary turnEventSummary, verbose bool) string {
	if summary.Count == 0 {
		return ""
	}

	parts := []string{fmt.Sprintf("%d %s", summary.Count, pluralWord(summary.Count, "event", "events"))}
	if summary.Adapter != "" {
		parts = append(parts, summary.Adapter)
	}
	if len(summary.ToolNames) > 0 {
		parts = append(parts, "tools: "+strings.Join(summary.ToolNames, ", "))
	}
	if verbose && !summary.First.IsZero() && !summary.Last.IsZero() {
		parts = append(parts, "span: "+formatTimeRange(summary.First, summary.Last))
	}
	return strings.Join(parts, "; ")
}

func formatEventTypeCounts(typeCounts map[primitives.EventType]int) string {
	if len(typeCounts) == 0 {
		return ""
	}

	types := make([]string, 0, len(typeCounts))
	for eventType := range typeCounts {
		types = append(types, eventType.String())
	}
	sort.Strings(types)

	parts := make([]string, 0, len(types))
	for _, eventType := range types {
		parts = append(parts, fmt.Sprintf("%s=%d", eventType, typeCounts[primitives.EventType(eventType)]))
	}
	return strings.Join(parts, ", ")
}

func truncateText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || limit <= 0 {
		return value
	}

	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func pluralWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBlue   = "\x1b[38;5;111m"
	ansiRed    = "\x1b[38;5;203m"
	ansiGreen  = "\x1b[38;5;48m"
	ansiYellow = "\x1b[38;5;220m"
)

var graphSessionColors = []string{
	"\x1b[38;5;120m",
	"\x1b[38;5;220m",
	"\x1b[38;5;183m",
	"\x1b[38;5;80m",
	"\x1b[38;5;209m",
	"\x1b[38;5;147m",
}

func formatSessionLabel(label string, sessionIndex int, options graphRenderOptions) string {
	return styleSession("["+label+"]", sessionIndex, options)
}

func styleSession(value string, sessionIndex int, options graphRenderOptions) string {
	return graphSessionColors[sessionIndex%len(graphSessionColors)] + value + ansiReset
}

func styleHash(value string, options graphRenderOptions) string {
	return styleDim(value, options)
}

func styleTool(value string, options graphRenderOptions) string {
	return ansiBlue + value + ansiReset
}

func styleTranscriptToolName(value string, options graphRenderOptions) string {
	switch value {
	case "Edit":
		return ansiYellow + value + ansiReset
	case "Write":
		return ansiGreen + value + ansiReset
	case "Bash", "Read", "VectorSearch":
		return ansiBlue + value + ansiReset
	default:
		return styleTool(value, options)
	}
}

func styleRollback(value string, options graphRenderOptions) string {
	return ansiRed + value + ansiReset
}

func styleRollbackPhase(value string, options graphRenderOptions) string {
	return ansiBlue + value + ansiReset
}

func styleDim(value string, options graphRenderOptions) string {
	return ansiDim + value + ansiReset
}

func styleAddition(value string, options graphRenderOptions) string {
	return ansiGreen + value + ansiReset
}

func styleDeletion(value string, options graphRenderOptions) string {
	return ansiRed + value + ansiReset
}

func shouldPageOutput(w io.Writer, noPager bool, data []byte) bool {
	if noPager {
		return false
	}
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	height, width, ok := terminalSize(file)
	if !ok || height <= 0 {
		return false
	}
	return shouldPageRenderedOutput(noPager, data, height, width)
}

func renderedLineCount(data []byte, width int) int {
	if len(data) == 0 {
		return 0
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return 0
	}

	total := 0
	for _, line := range lines {
		columns := printableColumns(line)
		if width <= 0 || columns <= width {
			total++
			continue
		}
		total += (columns + width - 1) / width
	}
	return total
}

func shouldPageRenderedOutput(noPager bool, data []byte, height, width int) bool {
	if noPager || height <= 0 {
		return false
	}
	return renderedLineCount(data, width) > height
}

func printableColumns(value string) int {
	columns := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			if i+1 < len(value) && value[i+1] == '[' {
				i += 2
				for i < len(value) {
					if value[i] >= '@' && value[i] <= '~' {
						i++
						break
					}
					i++
				}
				continue
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		columns++
		i += size
	}
	return columns
}

func pageOutput(fallback io.Writer, data []byte) error {
	pager := strings.TrimSpace(os.Getenv("PAGER"))
	if pager == "" {
		pager = "less -R"
	}
	if pager == "cat" {
		_, err := fallback.Write(data)
		return err
	}

	cmd := exec.Command("sh", "-c", pager)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, writeErr := fallback.Write(data)
		return writeErr
	}
	return nil
}
