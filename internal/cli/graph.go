package cli

import (
	"bytes"
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

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}

			var sessions []graphSession
			if useIndex {
				var loaded bool
				sessions, loaded, err = tryLoadIndexedGraph(repo, session, limit)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: index unavailable: %v\n", err)
				}
				if !loaded {
					sessions, err = loadDurableGraph(repo, session, limit)
					if err != nil {
						return err
					}
				}
			} else {
				sessions, err = loadDurableGraph(repo, session, limit)
				if err != nil {
					return err
				}
			}

			options := graphRenderOptions{
				Verbose: verbose,
			}
			var buf bytes.Buffer
			if err := renderCheckpointGraph(&buf, sessions, options); err != nil {
				return err
			}
			if shouldPageOutput(cmd.OutOrStdout(), noPager, buf.Bytes()) {
				return pageOutput(cmd.OutOrStdout(), buf.Bytes())
			}
			_, err = cmd.OutOrStdout().Write(buf.Bytes())
			return err
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to show; defaults to all sessions")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum turns per session; 0 shows all")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show full refs, commit ids, event counts, and per-file stats")
	cmd.Flags().BoolVar(&noPager, "no-pager", false, "Print directly instead of opening a pager")
	cmd.Flags().BoolVar(&useIndex, "index", false, "Read from the disposable SQLite index when available")
	cmd.Flags().BoolVar(&durable, "durable", false, "Read directly from durable logs and checkpoints")
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

type turnEventSummary = queryindex.TurnEventSummary

type graphTimelineRow struct {
	SessionID    primitives.SessionID
	SessionIndex int
	Turn         graphTurn
}

type laneSpan struct {
	First int
	Last  int
}

func loadDurableGraph(repo *checkpoint.Repo, session string, limit int) ([]graphSession, error) {
	var infos []checkpoint.CheckpointRefInfo
	var err error
	if session != "" {
		sessionID, err := primitives.ParseSessionID(session)
		if err != nil {
			return nil, err
		}
		infos, err = repo.ListCheckpointRefInfos(sessionID)
		if err != nil {
			return nil, err
		}
	} else {
		infos, err = repo.ListAllCheckpointRefInfos()
		if err != nil {
			return nil, err
		}
	}

	sessions := buildGraphSessions(infos, limit)
	attachGraphDiffs(repo, sessions)
	attachGraphEventSummaries(repo, sessions)
	return sessions, nil
}

func tryLoadIndexedGraph(repo *checkpoint.Repo, session string, limit int) ([]graphSession, bool, error) {
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
		Session: sessionID,
		Limit:   limit,
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
		summaries := queryindex.SummarizeTurnEvents(events)
		for turnIndex := range sessions[sessionIndex].Turns {
			turnID := sessions[sessionIndex].Turns[turnIndex].TurnID.Uint64()
			sessions[sessionIndex].Turns[turnIndex].Events = summaries[turnID]
		}
	}
}

func renderCheckpointGraph(w io.Writer, sessions []graphSession, options graphRenderOptions) error {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No checkpoints recorded yet.")
		return nil
	}

	rows := buildGraphTimelineRows(sessions)
	spans := buildLaneSpans(rows)
	labels := buildGraphSessionLabels(sessions)
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
		label := formatSessionLabel(labels[sessionIndex], sessionIndex, options)
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
			formatSessionLabel(labels[row.SessionIndex], row.SessionIndex, options),
			turn.TurnID,
			styleTool(formatTurnAction(turn), options),
			styleDim(turnStatus(turn), options),
			formatTurnHeadlineSummary(turn, options),
		)

		if prompt := truncateText(turn.Events.Prompt, 140); prompt != "" {
			fmt.Fprintf(w, "%s%s %q\n", detailPrefix, styleDim("Human:", options), prompt)
		}
		if options.Verbose {
			renderVerboseTurnDetails(w, detailPrefix, row.SessionID, turn, options)
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

func renderVerboseTurnDetails(w io.Writer, prefix string, sessionID primitives.SessionID, turn graphTurn, options graphRenderOptions) {
	fmt.Fprintf(w, "%ssession: %s\n", prefix, sessionID)
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

func styleDim(value string, options graphRenderOptions) string {
	return ansiDim + value + ansiReset
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
