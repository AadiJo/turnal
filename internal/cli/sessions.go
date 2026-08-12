package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sessionhistory"
	"github.com/spf13/cobra"
)

func sessionsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:          "sessions",
		Short:        "List recorded agent sessions",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}

			sessions, err := loadSessionViews(repo)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if jsonOutput {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(sessionsJSONFromViews(sessions))
			}
			var buffer bytes.Buffer
			if err := writeSessionsView(&buffer, sessions); err != nil {
				return err
			}
			output := buffer.Bytes()
			if !colorOutputEnabled(out) {
				output = stripANSIBytes(output)
			}
			_, err = out.Write(output)
			return err
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

type sessionView struct {
	ID                primitives.SessionID
	Adapter           string
	Model             string
	PermissionMode    string
	ProviderSessionID string
	Origin            string
	ReadOnly          bool
	Attachments       []sessionhistory.Attachment
	EventCount        int
	FirstActivity     time.Time
	LastActivity      time.Time
	Turns             map[uint64]*sessionViewTurn
	Rollbacks         []sessionViewRollback
	Head              *sessionViewHead
	Warnings          []string
}

type sessionViewTurn struct {
	TurnID primitives.TurnID
	Pre    *checkpoint.CheckpointRefInfo
	Post   *checkpoint.CheckpointRefInfo
	Events queryindex.TurnEventSummary
}

type sessionViewHead struct {
	TurnID primitives.TurnID
	Phase  primitives.CheckpointPhase
	Commit primitives.CommitSHA
	Ref    primitives.CheckpointRef
	Time   time.Time
}

type sessionViewRollback struct {
	Sequence      primitives.EventSeq
	StreamID      primitives.EventStreamID
	TurnID        uint64
	Target        string
	Phase         string
	Mode          string
	Time          time.Time
	ChangeSummary sessionJSONChangeSummary
}

const sessionsJSONSchemaVersion = 2

type sessionsJSONOutput struct {
	SchemaVersion int                  `json:"schema_version"`
	TotalSessions int                  `json:"total_sessions"`
	Sessions      []sessionJSONSummary `json:"sessions"`
}

type sessionJSONSummary struct {
	SessionID         string                  `json:"session_id"`
	Status            string                  `json:"status"`
	Adapter           string                  `json:"adapter,omitempty"`
	Model             string                  `json:"model,omitempty"`
	PermissionMode    string                  `json:"permission_mode,omitempty"`
	ProviderSessionID string                  `json:"provider_session_id,omitempty"`
	Origin            string                  `json:"origin,omitempty"`
	ReadOnly          bool                    `json:"read_only,omitempty"`
	Attachments       []sessionJSONAttachment `json:"attachments,omitempty"`
	TurnCount         int                     `json:"turn_count"`
	CompleteTurnCount int                     `json:"complete_turn_count"`
	ActiveTurnCount   int                     `json:"active_turn_count"`
	EventCount        int                     `json:"event_count"`
	RollbackCount     int                     `json:"rollback_count"`
	FirstActivity     string                  `json:"first_activity,omitempty"`
	LastActivity      string                  `json:"last_activity,omitempty"`
	Head              *sessionJSONHead        `json:"head,omitempty"`
	LatestTurn        *sessionJSONTurn        `json:"latest_turn,omitempty"`
	Turns             []sessionJSONTurn       `json:"turns,omitempty"`
	Rollbacks         []sessionJSONRollback   `json:"rollbacks"`
	Warnings          []string                `json:"warnings,omitempty"`
}

type sessionJSONAttachment struct {
	CommitSHA string `json:"commit_sha"`
	Revision  string `json:"revision,omitempty"`
	Time      string `json:"time"`
}

type sessionJSONHead struct {
	TurnID    uint64 `json:"turn_id"`
	Phase     string `json:"phase"`
	CommitSHA string `json:"commit_sha"`
	Ref       string `json:"ref"`
	Time      string `json:"time,omitempty"`
}

type sessionJSONTurn struct {
	TurnID        uint64   `json:"turn_id"`
	Status        string   `json:"status"`
	Prompt        string   `json:"prompt,omitempty"`
	Assistant     string   `json:"assistant,omitempty"`
	ToolNames     []string `json:"tool_names,omitempty"`
	FirstActivity string   `json:"first_activity,omitempty"`
	LastActivity  string   `json:"last_activity,omitempty"`
}

type sessionJSONRollback struct {
	Sequence      uint64                   `json:"sequence"`
	StreamID      string                   `json:"stream_id,omitempty"`
	TurnID        uint64                   `json:"turn_id,omitempty"`
	Target        string                   `json:"target"`
	Phase         string                   `json:"phase,omitempty"`
	Mode          string                   `json:"mode"`
	Time          string                   `json:"time"`
	ChangeSummary sessionJSONChangeSummary `json:"change_summary"`
}

type sessionJSONChangeSummary struct {
	Total       int `json:"total"`
	Added       int `json:"added"`
	Modified    int `json:"modified"`
	Deleted     int `json:"deleted"`
	ModeChanged int `json:"mode_changed"`
}

type sessionRollbackPayload struct {
	Turn          uint64                   `json:"turn,omitempty"`
	Phase         string                   `json:"phase,omitempty"`
	Mode          string                   `json:"mode,omitempty"`
	Target        string                   `json:"target"`
	ChangeSummary sessionJSONChangeSummary `json:"change_summary"`
}

type sessionStartPayload struct {
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
}

func loadSessionViews(repo *checkpoint.Repo) ([]sessionView, error) {
	if repo == nil {
		return nil, fmt.Errorf("sessions view requires checkpoint repo")
	}

	byID := make(map[string]*sessionView)
	ensure := func(sessionID primitives.SessionID) *sessionView {
		key := sessionID.String()
		session := byID[key]
		if session == nil {
			session = &sessionView{
				ID:    sessionID,
				Turns: make(map[uint64]*sessionViewTurn),
			}
			byID[key] = session
		}
		return session
	}
	ensureTurn := func(session *sessionView, turnID primitives.TurnID) *sessionViewTurn {
		key := turnID.Uint64()
		turn := session.Turns[key]
		if turn == nil {
			turn = &sessionViewTurn{TurnID: turnID}
			session.Turns[key] = turn
		}
		return turn
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Manual {
			continue
		}
		session := ensure(info.SessionID)
		turn := ensureTurn(session, info.TurnID)
		infoCopy := info
		switch info.Phase {
		case primitives.CheckpointPhasePre:
			turn.Pre = &infoCopy
		case primitives.CheckpointPhasePost:
			turn.Post = &infoCopy
		default:
			session.Warnings = append(session.Warnings, fmt.Sprintf("ignored unphased checkpoint ref %s", info.Ref))
		}
		session.noteActivity(info.Time)
		if session.Head == nil || info.Time.After(session.Head.Time) || (info.Time.Equal(session.Head.Time) && phaseRankForHead(info.Phase) > phaseRankForHead(session.Head.Phase)) {
			session.Head = &sessionViewHead{
				TurnID: info.TurnID,
				Phase:  info.Phase,
				Commit: info.Commit,
				Ref:    info.Ref,
				Time:   info.Time,
			}
		}
	}

	log := eventlog.Open(repo.MetadataDir)
	logSessions, err := log.ListSessions()
	if err != nil {
		return nil, err
	}
	for _, sessionID := range logSessions {
		session := ensure(sessionID)
		events, err := log.Read(sessionID)
		if err != nil {
			session.Warnings = append(session.Warnings, fmt.Sprintf("event log unavailable: %v", err))
			continue
		}

		session.EventCount = len(events)
		for _, event := range events {
			session.noteActivity(event.Time.Time)
			if session.Adapter == "" && event.Adapter != "" {
				session.Adapter = event.Adapter.String()
			}
			if event.Type == primitives.EventTypeSessionStart {
				session.applySessionStartPayload(event.Payload)
			}
			if event.Type == primitives.EventTypeRollback {
				rollback, err := sessionRollbackFromEvent(event)
				if err != nil {
					session.Warnings = append(session.Warnings, fmt.Sprintf("ignored rollback event %s: %v", event.Seq, err))
					continue
				}
				session.Rollbacks = append(session.Rollbacks, rollback)
				continue
			}
			if event.TurnID != nil {
				ensureTurn(session, *event.TurnID)
			}
		}
		historyInfo := sessionhistory.InspectSession(events)
		session.ProviderSessionID = historyInfo.ProviderSessionID
		session.Origin = historyInfo.Origin
		session.ReadOnly = historyInfo.ReadOnly
		session.Attachments = historyInfo.Attachments

		for turnKey, summary := range queryindex.SummarizeTurnEvents(nonRollbackEvents(events)) {
			turnID, err := primitives.NewTurnID(turnKey)
			if err != nil {
				session.Warnings = append(session.Warnings, fmt.Sprintf("ignored invalid turn id %d", turnKey))
				continue
			}
			turn := ensureTurn(session, turnID)
			turn.Events = summary
			if session.Adapter == "" && summary.Adapter != "" {
				session.Adapter = summary.Adapter
			}
			if session.Model == "" && summary.Model != "" {
				session.Model = summary.Model
			}
		}
	}

	sessions := make([]sessionView, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, *session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		left, right := sessions[i], sessions[j]
		if !left.LastActivity.Equal(right.LastActivity) {
			return left.LastActivity.After(right.LastActivity)
		}
		return left.ID.String() < right.ID.String()
	})
	return sessions, nil
}

func sessionRollbackFromEvent(event eventlog.Event) (sessionViewRollback, error) {
	var payload sessionRollbackPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return sessionViewRollback{}, fmt.Errorf("payload malformed: %w", err)
	}
	target := strings.TrimSpace(payload.Target)
	if target == "" {
		target = strings.TrimSpace(event.RawRef)
	}
	if target == "" {
		return sessionViewRollback{}, fmt.Errorf("target missing")
	}
	turnID := payload.Turn
	if event.TurnID != nil {
		turnID = event.TurnID.Uint64()
	}
	mode := strings.TrimSpace(payload.Mode)
	if mode == "" {
		mode = primitives.RollbackModeCheckpoint.String()
	}
	return sessionViewRollback{
		Sequence: event.Seq, StreamID: event.StreamID, TurnID: turnID,
		Target: target, Phase: strings.TrimSpace(payload.Phase), Mode: mode,
		Time: event.Time.Time, ChangeSummary: payload.ChangeSummary,
	}, nil
}

func nonRollbackEvents(events []eventlog.Event) []eventlog.Event {
	filtered := make([]eventlog.Event, 0, len(events))
	for _, event := range events {
		if event.Type != primitives.EventTypeRollback {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func (session *sessionView) noteActivity(at time.Time) {
	if at.IsZero() {
		return
	}
	at = at.UTC()
	if session.FirstActivity.IsZero() || at.Before(session.FirstActivity) {
		session.FirstActivity = at
	}
	if session.LastActivity.IsZero() || at.After(session.LastActivity) {
		session.LastActivity = at
	}
}

func (session *sessionView) applySessionStartPayload(payload json.RawMessage) {
	var decoded sessionStartPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return
	}
	if session.Model == "" {
		session.Model = decoded.Model
	}
	if session.PermissionMode == "" {
		session.PermissionMode = decoded.PermissionMode
	}
}

func writeSessionsView(w io.Writer, sessions []sessionView) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(w, "no sessions recorded")
		return err
	}

	if _, err := fmt.Fprintf(w, "%s %s\n\n", styleSessionsLabel("sessions"), styleSessionCount(len(sessions), "recorded")); err != nil {
		return err
	}
	for index, session := range sessions {
		if index > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writeSessionView(w, session); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionView(w io.Writer, session sessionView) error {
	counts := sessionTurnCounts(session)
	latest := latestSessionTurn(session)
	status := sessionStatus(session, counts)

	if _, err := fmt.Fprintf(w, "%s %s\n", styleSessionBadge(status), styleSessionID(session.ID.String())); err != nil {
		return err
	}

	adapter := formatSessionAdapterLine(session)
	if adapter == "" {
		adapter = styleSessionsMuted("unknown")
	}
	if err := writeSessionField(w, "adapter", adapter); err != nil {
		return err
	}

	if err := writeSessionField(w, "turns", formatSessionTurnCounts(counts)); err != nil {
		return err
	}
	if err := writeSessionField(w, "events", styleSessionNumber(session.EventCount)); err != nil {
		return err
	}
	if session.Origin == sessionhistory.OriginImported {
		if err := writeSessionField(w, "origin", ansiBlue+"imported / read-only"+ansiReset); err != nil {
			return err
		}
	}
	for _, attachment := range session.Attachments {
		if err := writeSessionField(w, "attached", styleSessionHash(formatObjectID(attachment.CommitSHA, false))); err != nil {
			return err
		}
	}
	if len(session.Rollbacks) > 0 {
		if err := writeSessionField(w, "rollbacks", styleSessionNumber(len(session.Rollbacks))); err != nil {
			return err
		}
	}
	if !session.FirstActivity.IsZero() || !session.LastActivity.IsZero() {
		if err := writeSessionField(w, "activity", styleSessionsMuted(formatSessionsActivityRange(session.FirstActivity, session.LastActivity))); err != nil {
			return err
		}
	}
	if session.Head != nil {
		head := fmt.Sprintf("turn %s %s %s",
			session.Head.TurnID,
			styleSessionPhase(session.Head.Phase),
			styleSessionHash(formatObjectID(session.Head.Commit, false)),
		)
		if err := writeSessionField(w, "head", head); err != nil {
			return err
		}
	}
	if latest != nil {
		if latest.Events.Prompt != "" {
			prompt := fmt.Sprintf("%q", truncateText(collapseSearchWhitespace(latest.Events.Prompt), 140))
			if err := writeSessionField(w, "prompt", styleSessionPrompt(prompt)); err != nil {
				return err
			}
		}
		if len(latest.Events.ToolNames) > 0 {
			if err := writeSessionField(w, "tools", styleSessionTools(latest.Events.ToolNames)); err != nil {
				return err
			}
		}
	}
	for _, warning := range session.Warnings {
		if err := writeSessionField(w, "warning", ansiRed+warning+ansiReset); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionField(w io.Writer, label, value string) error {
	_, err := fmt.Fprintf(w, "  %s %s\n", styleSessionFieldLabel(label), value)
	return err
}

func formatSessionAdapterLine(session sessionView) string {
	var parts []string
	if session.Adapter != "" {
		parts = append(parts, ansiBlue+session.Adapter+ansiReset)
	}
	if session.Model != "" {
		parts = append(parts, ansiYellow+session.Model+ansiReset)
	}
	if session.PermissionMode != "" {
		parts = append(parts, styleSessionsMuted(session.PermissionMode))
	}
	return strings.Join(parts, styleSessionsMuted(" / "))
}

type sessionTurnCountSummary struct {
	Total    int
	Complete int
	Active   int
	Orphan   int
	Events   int
	Empty    int
}

func sessionTurnCounts(session sessionView) sessionTurnCountSummary {
	var counts sessionTurnCountSummary
	for _, turn := range session.Turns {
		counts.Total++
		switch sessionTurnStatus(*turn) {
		case "complete":
			counts.Complete++
		case "active":
			counts.Active++
		case "orphan":
			counts.Orphan++
		case "events-only":
			counts.Events++
		default:
			counts.Empty++
		}
	}
	return counts
}

func sessionStatus(session sessionView, counts sessionTurnCountSummary) string {
	switch {
	case counts.Active > 0:
		return "active"
	case counts.Orphan > 0 || counts.Empty > 0:
		return "partial"
	case session.Origin == sessionhistory.OriginImported && counts.Total > 0:
		return "imported"
	case counts.Total == 0 && session.EventCount > 0:
		return "events-only"
	case counts.Total > 0 && counts.Complete == counts.Total:
		return "complete"
	case counts.Total > 0 && counts.Events == counts.Total:
		return "events-only"
	case counts.Total > 0:
		return "partial"
	default:
		return "empty"
	}
}

func sessionTurnStatus(turn sessionViewTurn) string {
	switch {
	case turn.Pre != nil && turn.Post != nil:
		return "complete"
	case turn.Pre != nil:
		return "active"
	case turn.Post != nil:
		return "orphan"
	case turn.Events.Count > 0:
		return "events-only"
	default:
		return "empty"
	}
}

func formatSessionTurnCounts(counts sessionTurnCountSummary) string {
	if counts.Total == 0 {
		return styleSessionsMuted("0 total")
	}

	parts := []string{fmt.Sprintf("%s total", styleSessionNumber(counts.Total))}
	if counts.Complete > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", styleSessionNumber(counts.Complete), styleSessionStatusWord("complete")))
	}
	if counts.Active > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", styleSessionNumber(counts.Active), styleSessionStatusWord("active")))
	}
	if counts.Orphan > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", styleSessionNumber(counts.Orphan), styleSessionStatusWord("orphan")))
	}
	if counts.Events > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", styleSessionNumber(counts.Events), styleSessionStatusWord("events-only")))
	}
	if counts.Empty > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", styleSessionNumber(counts.Empty), styleSessionStatusWord("empty")))
	}
	return strings.Join(parts, ", ")
}

func latestSessionTurn(session sessionView) *sessionViewTurn {
	turns := orderedSessionTurns(session)
	if len(turns) == 0 {
		return nil
	}
	return turns[0]
}

func orderedSessionTurns(session sessionView) []*sessionViewTurn {
	turns := make([]*sessionViewTurn, 0, len(session.Turns))
	for _, turn := range session.Turns {
		turns = append(turns, turn)
	}
	sort.Slice(turns, func(i, j int) bool {
		left := sessionTurnActivity(*turns[i])
		right := sessionTurnActivity(*turns[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		return turns[i].TurnID.Uint64() > turns[j].TurnID.Uint64()
	})
	return turns
}

func orderedSessionRollbacks(session sessionView) []sessionViewRollback {
	rollbacks := append([]sessionViewRollback(nil), session.Rollbacks...)
	sort.Slice(rollbacks, func(i, j int) bool {
		if !rollbacks[i].Time.Equal(rollbacks[j].Time) {
			return rollbacks[i].Time.After(rollbacks[j].Time)
		}
		if rollbacks[i].StreamID != rollbacks[j].StreamID {
			return rollbacks[i].StreamID.String() < rollbacks[j].StreamID.String()
		}
		return rollbacks[i].Sequence.Uint64() > rollbacks[j].Sequence.Uint64()
	})
	return rollbacks
}

func sessionTurnActivity(turn sessionViewTurn) time.Time {
	var latest time.Time
	for _, at := range []time.Time{
		checkpointInfoTime(turn.Pre),
		checkpointInfoTime(turn.Post),
		turn.Events.Last,
		turn.Events.First,
	} {
		if at.IsZero() {
			continue
		}
		if latest.IsZero() || at.After(latest) {
			latest = at
		}
	}
	return latest
}

func checkpointInfoTime(info *checkpoint.CheckpointRefInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.Time
}

func formatSessionsActivityRange(first time.Time, last time.Time) string {
	switch {
	case first.IsZero() && last.IsZero():
		return "unknown"
	case first.IsZero():
		return formatGraphTime(last)
	case last.IsZero() || first.Equal(last):
		return formatGraphTime(first)
	default:
		return formatGraphTime(first) + " -> " + formatGraphTime(last)
	}
}

func phaseRankForHead(phase primitives.CheckpointPhase) int {
	switch phase {
	case primitives.CheckpointPhasePost:
		return 2
	case primitives.CheckpointPhasePre:
		return 1
	default:
		return 0
	}
}

func styleSessionsLabel(value string) string {
	return ansiBlue + value + ansiReset
}

func styleSessionFieldLabel(value string) string {
	return ansiBlue + fmt.Sprintf("%-8s", value) + ansiReset
}

func styleSessionID(value string) string {
	return ansiBlue + value + ansiReset
}

func styleSessionCount(count int, label string) string {
	return fmt.Sprintf("%s %s", styleSessionNumber(count), styleSessionsMuted(label))
}

func styleSessionNumber(value int) string {
	return ansiYellow + fmt.Sprintf("%d", value) + ansiReset
}

func styleSessionBadge(status string) string {
	return styleSessionStatusColor(status) + "[" + strings.ToUpper(status) + "]" + ansiReset
}

func styleSessionStatusWord(status string) string {
	return styleSessionStatusColor(status) + status + ansiReset
}

func styleSessionStatusColor(status string) string {
	switch status {
	case "complete":
		return ansiGreen
	case "active":
		return ansiYellow
	case "partial", "orphan":
		return ansiRed
	case "events-only":
		return ansiBlue
	case "imported":
		return ansiBlue
	default:
		return ansiDim
	}
}

func styleSessionPhase(phase primitives.CheckpointPhase) string {
	switch phase {
	case primitives.CheckpointPhasePost:
		return ansiGreen + phase.String() + ansiReset
	case primitives.CheckpointPhasePre:
		return ansiYellow + phase.String() + ansiReset
	default:
		return styleSessionsMuted(phase.String())
	}
}

func styleSessionHash(value string) string {
	return ansiDim + value + ansiReset
}

func styleSessionPrompt(value string) string {
	return ansiYellow + value + ansiReset
}

func styleSessionTools(tools []string) string {
	if len(tools) == 0 {
		return ""
	}
	return ansiBlue + truncateText(strings.Join(tools, ", "), 140) + ansiReset
}

func styleSessionsMuted(value string) string {
	if value == "" {
		return ""
	}
	return ansiDim + value + ansiReset
}

func sessionsJSONFromViews(sessions []sessionView) sessionsJSONOutput {
	output := sessionsJSONOutput{
		SchemaVersion: sessionsJSONSchemaVersion,
		TotalSessions: len(sessions),
		Sessions:      make([]sessionJSONSummary, 0, len(sessions)),
	}
	for _, session := range sessions {
		counts := sessionTurnCounts(session)
		turns := orderedSessionTurns(session)
		rollbacks := orderedSessionRollbacks(session)
		summary := sessionJSONSummary{
			SessionID:         session.ID.String(),
			Status:            sessionStatus(session, counts),
			Adapter:           session.Adapter,
			Model:             session.Model,
			PermissionMode:    session.PermissionMode,
			ProviderSessionID: session.ProviderSessionID,
			Origin:            session.Origin,
			ReadOnly:          session.ReadOnly,
			TurnCount:         counts.Total,
			CompleteTurnCount: counts.Complete,
			ActiveTurnCount:   counts.Active,
			EventCount:        session.EventCount,
			RollbackCount:     len(rollbacks),
			Warnings:          session.Warnings,
			Turns:             make([]sessionJSONTurn, 0, len(turns)),
			Rollbacks:         make([]sessionJSONRollback, 0, len(rollbacks)),
		}
		for _, attachment := range session.Attachments {
			summary.Attachments = append(summary.Attachments, sessionJSONAttachment{
				CommitSHA: attachment.CommitSHA.String(), Revision: attachment.Revision,
				Time: attachment.Time.UTC().Format(time.RFC3339Nano),
			})
		}
		if !session.FirstActivity.IsZero() {
			summary.FirstActivity = session.FirstActivity.UTC().Format(time.RFC3339Nano)
		}
		if !session.LastActivity.IsZero() {
			summary.LastActivity = session.LastActivity.UTC().Format(time.RFC3339Nano)
		}
		if session.Head != nil {
			summary.Head = &sessionJSONHead{
				TurnID:    session.Head.TurnID.Uint64(),
				Phase:     session.Head.Phase.String(),
				CommitSHA: session.Head.Commit.String(),
				Ref:       session.Head.Ref.String(),
				Time:      session.Head.Time.UTC().Format(time.RFC3339Nano),
			}
		}
		for _, turn := range turns {
			jsonTurn := sessionJSONTurnFromView(*turn)
			summary.Turns = append(summary.Turns, jsonTurn)
		}
		for _, rollback := range rollbacks {
			summary.Rollbacks = append(summary.Rollbacks, sessionJSONRollback{
				Sequence: rollback.Sequence.Uint64(), StreamID: rollback.StreamID.String(),
				TurnID: rollback.TurnID, Target: rollback.Target, Phase: rollback.Phase,
				Mode: rollback.Mode, Time: rollback.Time.UTC().Format(time.RFC3339Nano),
				ChangeSummary: rollback.ChangeSummary,
			})
		}
		if len(summary.Turns) > 0 {
			latest := summary.Turns[0]
			summary.LatestTurn = &latest
		}
		output.Sessions = append(output.Sessions, summary)
	}
	return output
}

func sessionJSONTurnFromView(turn sessionViewTurn) sessionJSONTurn {
	output := sessionJSONTurn{
		TurnID:    turn.TurnID.Uint64(),
		Status:    sessionTurnStatus(turn),
		Prompt:    turn.Events.Prompt,
		Assistant: turn.Events.Assistant,
		ToolNames: turn.Events.ToolNames,
	}
	first := turn.Events.First
	last := sessionTurnActivity(turn)
	if first.IsZero() {
		first = checkpointInfoTime(turn.Pre)
	}
	if !first.IsZero() {
		output.FirstActivity = first.UTC().Format(time.RFC3339Nano)
	}
	if !last.IsZero() {
		output.LastActivity = last.UTC().Format(time.RFC3339Nano)
	}
	return output
}
