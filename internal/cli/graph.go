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

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func graphCmd() *cobra.Command {
	var session string
	var limit int
	var verbose bool
	var pager bool

	cmd := &cobra.Command{
		Use:          "graph",
		Short:        "Show checkpoint history as an ASCII graph",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or greater")
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}

			var infos []checkpoint.CheckpointRefInfo
			if session != "" {
				sessionID, err := primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
				infos, err = repo.ListCheckpointRefInfos(sessionID)
				if err != nil {
					return err
				}
			} else {
				infos, err = repo.ListAllCheckpointRefInfos()
				if err != nil {
					return err
				}
			}

			sessions := buildGraphSessions(infos, limit)
			attachGraphDiffs(repo, sessions)
			attachGraphEventSummaries(repo, sessions)

			options := graphRenderOptions{
				Verbose: verbose,
			}
			if pager {
				var buf bytes.Buffer
				if err := renderCheckpointGraph(&buf, sessions, options); err != nil {
					return err
				}
				return pageOutput(cmd.OutOrStdout(), buf.Bytes())
			}
			return renderCheckpointGraph(cmd.OutOrStdout(), sessions, options)
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to show; defaults to all sessions")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum turns per session; 0 shows all")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show full refs, commit ids, event counts, and per-file stats")
	cmd.Flags().BoolVar(&pager, "pager", false, "Open graph in $PAGER, defaulting to less -R")
	return cmd
}

type graphRenderOptions struct {
	Verbose bool
}

type graphSession struct {
	ID         primitives.SessionID
	Turns      []graphTurn
	TotalTurns int
	Warnings   []string
}

type graphTurn struct {
	TurnID     primitives.TurnID
	Pre        *checkpoint.CheckpointRefInfo
	Post       *checkpoint.CheckpointRefInfo
	Diff       checkpoint.DiffSummary
	DiffLoaded bool
	Events     turnEventSummary
	Warnings   []string
}

type turnEventSummary struct {
	Count      int
	Adapter    string
	Prompt     string
	Assistant  string
	ToolNames  []string
	TypeCounts map[primitives.EventType]int
	First      time.Time
	Last       time.Time
}

type graphTimelineRow struct {
	SessionID    primitives.SessionID
	SessionIndex int
	Turn         graphTurn
}

type laneSpan struct {
	First int
	Last  int
}

func buildGraphSessions(infos []checkpoint.CheckpointRefInfo, limit int) []graphSession {
	type sessionBuilder struct {
		id    primitives.SessionID
		turns map[uint64]*graphTurn
	}

	builders := make(map[string]*sessionBuilder)
	for _, info := range infos {
		sessionKey := info.SessionID.String()
		builder := builders[sessionKey]
		if builder == nil {
			builder = &sessionBuilder{
				id:    info.SessionID,
				turns: make(map[uint64]*graphTurn),
			}
			builders[sessionKey] = builder
		}

		turnKey := info.TurnID.Uint64()
		turn := builder.turns[turnKey]
		if turn == nil {
			turn = &graphTurn{
				TurnID: info.TurnID,
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
			return session.Turns[i].TurnID.Uint64() > session.Turns[j].TurnID.Uint64()
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
		summaries := summarizeTurnEvents(events)
		for turnIndex := range sessions[sessionIndex].Turns {
			turnID := sessions[sessionIndex].Turns[turnIndex].TurnID.Uint64()
			sessions[sessionIndex].Turns[turnIndex].Events = summaries[turnID]
		}
	}
}

func summarizeTurnEvents(events []eventlog.Event) map[uint64]turnEventSummary {
	summaries := make(map[uint64]turnEventSummary)
	seenTools := make(map[uint64]map[string]struct{})

	for _, event := range events {
		if event.TurnID == nil {
			continue
		}

		turnKey := event.TurnID.Uint64()
		summary := summaries[turnKey]
		summary.Count++
		if summary.TypeCounts == nil {
			summary.TypeCounts = make(map[primitives.EventType]int)
		}
		summary.TypeCounts[event.Type]++
		if summary.Adapter == "" && event.Adapter != "" {
			summary.Adapter = event.Adapter.String()
		}
		if summary.First.IsZero() || event.Time.Time.Before(summary.First) {
			summary.First = event.Time.Time
		}
		if summary.Last.IsZero() || event.Time.Time.After(summary.Last) {
			summary.Last = event.Time.Time
		}

		switch event.Type {
		case primitives.EventTypePromptUser:
			if summary.Prompt == "" {
				summary.Prompt = payloadString(event.Payload, "text")
			}
		case primitives.EventTypeAssistantMessage:
			if summary.Assistant == "" {
				summary.Assistant = payloadString(event.Payload, "text")
			}
		case primitives.EventTypeToolCall:
			toolName := payloadString(event.Payload, "tool_name")
			if toolName != "" {
				if seenTools[turnKey] == nil {
					seenTools[turnKey] = make(map[string]struct{})
				}
				if _, ok := seenTools[turnKey][toolName]; !ok {
					seenTools[turnKey][toolName] = struct{}{}
					summary.ToolNames = append(summary.ToolNames, toolName)
				}
			}
		}

		summaries[turnKey] = summary
	}

	return summaries
}

func renderCheckpointGraph(w io.Writer, sessions []graphSession, options graphRenderOptions) error {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No checkpoints recorded yet.")
		return nil
	}

	rows := buildGraphTimelineRows(sessions)
	spans := buildLaneSpans(rows)
	totalTurns := 0
	totalShownTurns := 0
	for _, session := range sessions {
		totalTurns += session.TotalTurns
		totalShownTurns += len(session.Turns)
	}
	if totalShownTurns == totalTurns {
		fmt.Fprintf(w, "checkpoint graph: %d %s, %d %s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalTurns, pluralWord(totalTurns, "turn", "turns"))
	} else {
		fmt.Fprintf(w, "checkpoint graph: %d %s, showing %d of %d %s\n\n",
			len(sessions), pluralWord(len(sessions), "session", "sessions"),
			totalShownTurns, totalTurns, pluralWord(totalTurns, "turn", "turns"))
	}

	fmt.Fprintf(w, "sessions:")
	for sessionIndex, session := range sessions {
		label := formatSessionLabel(session.ID, sessionIndex, options)
		if len(session.Turns) == session.TotalTurns {
			fmt.Fprintf(w, " %s", label)
		} else {
			fmt.Fprintf(w, " %s(%d/%d)", label, len(session.Turns), session.TotalTurns)
		}
	}
	fmt.Fprintln(w)

	for _, session := range sessions {
		for _, warning := range session.Warnings {
			fmt.Fprintf(w, "warning %s: %s\n", session.ID, warning)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	fmt.Fprintln(w)

	for rowIndex, row := range rows {
		turn := row.Turn
		linePrefix := renderLanePrefix(rowIndex, row.SessionIndex, len(sessions), spans, true, options)
		detailPrefix := renderLanePrefix(rowIndex, row.SessionIndex, len(sessions), spans, false, options)
		fmt.Fprintf(w, "%s%s - %s %s turn %-6s %s %s %s\n",
			linePrefix,
			styleHash(formatDisplayCommit(turn), options),
			formatDisplayTime(turn),
			formatSessionLabel(row.SessionID, row.SessionIndex, options),
			turn.TurnID,
			styleTool(formatTurnAction(turn), options),
			styleDim(turnStatus(turn), options),
			formatTurnHeadlineSummary(turn, options),
		)

		if prompt := truncateText(turn.Events.Prompt, 140); prompt != "" {
			fmt.Fprintf(w, "%s%s %q\n", detailPrefix, styleDim("Human:", options), prompt)
		}
		if options.Verbose {
			renderVerboseTurnDetails(w, detailPrefix, turn, options)
		}
		for _, warning := range turn.Warnings {
			fmt.Fprintf(w, "%swarning: %s\n", detailPrefix, warning)
		}
		if rowIndex < len(rows)-1 {
			fmt.Fprintln(w, renderLanePrefix(rowIndex, row.SessionIndex, len(sessions), spans, false, options))
		}
	}

	return nil
}

func buildGraphTimelineRows(sessions []graphSession) []graphTimelineRow {
	var rows []graphTimelineRow
	for sessionIndex, session := range sessions {
		for _, turn := range session.Turns {
			rows = append(rows, graphTimelineRow{
				SessionID:    session.ID,
				SessionIndex: sessionIndex,
				Turn:         turn,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftTime := turnDisplayTime(rows[i].Turn)
		rightTime := turnDisplayTime(rows[j].Turn)
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		if rows[i].SessionIndex != rows[j].SessionIndex {
			return rows[i].SessionIndex < rows[j].SessionIndex
		}
		return rows[i].Turn.TurnID.Uint64() > rows[j].Turn.TurnID.Uint64()
	})
	return rows
}

func buildLaneSpans(rows []graphTimelineRow) map[int]laneSpan {
	spans := make(map[int]laneSpan)
	for rowIndex, row := range rows {
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

func renderVerboseTurnDetails(w io.Writer, prefix string, turn graphTurn, options graphRenderOptions) {
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
		fmt.Fprintf(w, "%spre:  %s  %s\n", prefix, turn.Pre.Commit, turn.Pre.Ref)
	}
	if turn.Post != nil {
		fmt.Fprintf(w, "%spost: %s  %s\n", prefix, turn.Post.Commit, turn.Post.Ref)
	}
	for _, file := range turn.Diff.Files {
		fmt.Fprintf(w, "%sfile: %s\n", prefix, formatDiffFileStat(file))
	}
}

func formatDisplayCommit(turn graphTurn) string {
	switch {
	case turn.Post != nil:
		return formatCommit(turn.Post.Commit, false)
	case turn.Pre != nil:
		return formatCommit(turn.Pre.Commit, false)
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
		pre = "pre " + formatCommit(turn.Pre.Commit, full)
	}
	post := "post pending"
	if turn.Post != nil {
		post = "post " + formatCommit(turn.Post.Commit, full)
	}
	return pre + " -> " + post
}

func formatCommit(commit primitives.CommitSHA, full bool) string {
	value := commit.String()
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

func formatDiffFileStat(file checkpoint.DiffFileStat) string {
	if file.Binary {
		return file.Path + " binary"
	}
	return fmt.Sprintf("%s +%d -%d", file.Path, file.Additions, file.Deletions)
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

func payloadString(payload json.RawMessage, key string) string {
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
	return value
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
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBlue  = "\x1b[38;5;111m"
)

var graphSessionColors = []string{
	"\x1b[38;5;120m",
	"\x1b[38;5;220m",
	"\x1b[38;5;183m",
	"\x1b[38;5;80m",
	"\x1b[38;5;209m",
	"\x1b[38;5;147m",
}

func formatSessionLabel(sessionID primitives.SessionID, sessionIndex int, options graphRenderOptions) string {
	return styleSession("["+sessionID.String()+"]", sessionIndex, options)
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

func styleDim(value string, options graphRenderOptions) string {
	return ansiDim + value + ansiReset
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
