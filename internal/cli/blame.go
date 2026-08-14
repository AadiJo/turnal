package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/blame"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/spf13/cobra"
)

func blameCmd() *cobra.Command {
	var session string
	var jsonOutput bool
	var verbose bool

	cmd := &cobra.Command{
		Use:   "blame <path>[:line]",
		Short: "Show which completed turn last changed a line",
		Long: `Show which completed turn last changed lines in a file.

Blame uses the disposable SQLite index as a line cache when available. Cache
misses are computed lazily from completed turn checkpoints and any recorded
per-action snapshots. It does not use Git commit ancestry, and it does not
inspect uncheckpointed workspace edits.

Without :line, all lines in the latest completed post checkpoint are shown.
Lines marked "baseline" existed before the scoped completed turn history. The
--session flag scopes both the replay history and the latest checkpoint to that
session. File renames are followed with Git rename detection, and unchanged
lines moved within a patch keep their earlier origin when the move can be
matched exactly.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, line, err := blame.ParsePathLine(args[0])
			if err != nil {
				return err
			}

			var sessionID primitives.SessionID
			if session != "" {
				sessionID, err = primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := blame.New(repo).Compute(blame.Query{
				Path:      path,
				Line:      line,
				SessionID: sessionID,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if jsonOutput {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			return writeBlameText(out, result, verbose)
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Restrict blame to one session id")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show intent, evidence, request, tool, and checkpoint metadata")
	return cmd
}

func writeBlameText(w io.Writer, result blame.Result, verbose bool) error {
	if len(result.Entries) == 0 {
		_, err := fmt.Fprintf(w, "%s has no lines at %s\n", result.Path, result.LatestRef)
		return err
	}

	sessionLabels := blameSessionLabels(result.Sessions)
	labelWidth := 8
	for _, entry := range result.Entries {
		if width := len(originLabel(entry.Origin, sessionLabels)); width > labelWidth {
			labelWidth = width
		}
	}

	notesByLine, unanchored := blameNotesByLine(result)
	printed := make(map[primitives.NoteID]struct{}, len(result.Notes))

	for _, entry := range result.Entries {
		label := originLabel(entry.Origin, sessionLabels)
		if _, err := fmt.Fprintf(w, "%-*s %6d | %s\n", labelWidth, label, entry.Line, entry.Text); err != nil {
			return err
		}
		for _, note := range notesByLine[entry.Line] {
			if _, seen := printed[note.Note.NoteID]; seen {
				continue
			}
			printed[note.Note.NoteID] = struct{}{}
			if err := writeBlameNote(w, note); err != nil {
				return err
			}
		}
		if verbose {
			if err := writeBlameOriginDetails(w, entry.Origin); err != nil {
				return err
			}
		} else {
			if entry.Origin.Intent != nil {
				if _, err := fmt.Fprintf(w, "  Intent: %s\n", truncateText(entry.Origin.Intent.Problem, 140)); err != nil {
					return err
				}
				if entry.Origin.Intent.Status != provenance.IntentStatusCaptured {
					if _, err := fmt.Fprintf(w, "  Intent note: %s\n", blameIntentConfidence(entry.Origin)); err != nil {
						return err
					}
				}
			} else if entry.Origin.Kind == "ambiguous" {
				if _, err := fmt.Fprintln(w, "  Intent: unavailable because no recorded intent could be safely tied to this change"); err != nil {
					return err
				}
			} else if entry.Origin.Kind == "concurrent" {
				if _, err := fmt.Fprintln(w, "  Intent: unavailable because concurrent agent turns overlapped"); err != nil {
					return err
				}
			} else if entry.Origin.Kind == "turn" {
				if _, err := fmt.Fprintln(w, "  Intent: no agent intent recorded for this change"); err != nil {
					return err
				}
			}
			request := truncateText(entry.Origin.Prompt, 140)
			if request == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "  Human request: %q\n", request); err != nil {
				return err
			}
		}
	}
	// A note whose anchored line is not in the displayed range still belongs to
	// this file. Dropping it would hide the reviewer's point entirely.
	var remaining []blame.FileNote
	remaining = append(remaining, unanchored...)
	for _, note := range result.Notes {
		if note.Line == 0 {
			continue
		}
		if _, seen := printed[note.Note.NoteID]; !seen {
			remaining = append(remaining, note)
		}
	}
	if len(remaining) > 0 {
		if _, err := fmt.Fprintln(w, "\nother notes on this file:"); err != nil {
			return err
		}
		for _, note := range remaining {
			if err := writeBlameNote(w, note); err != nil {
				return err
			}
		}
	}

	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

// blameNotesByLine groups anchored notes by every line they cover, so a range
// note appears against the first covered line that is actually displayed.
// File-scoped notes have no line and are returned separately.
func blameNotesByLine(result blame.Result) (map[int][]blame.FileNote, []blame.FileNote) {
	byLine := make(map[int][]blame.FileNote)
	var unanchored []blame.FileNote
	for _, note := range result.Notes {
		if note.Line == 0 {
			unanchored = append(unanchored, note)
			continue
		}
		end := note.LineEnd
		if end < note.Line {
			end = note.Line
		}
		for line := note.Line; line <= end; line++ {
			byLine[line] = append(byLine[line], note)
		}
	}
	return byLine, unanchored
}

func writeBlameNote(w io.Writer, note blame.FileNote) error {
	label := "Note"
	if note.Line > 0 {
		if note.LineEnd > note.Line {
			label = fmt.Sprintf("Note on %d-%d", note.Line, note.LineEnd)
		} else {
			label = fmt.Sprintf("Note on %d", note.Line)
		}
	}
	if _, err := fmt.Fprintf(w, "  %s: %s\n", label, escapeNoteText(truncateText(note.Note.Text, 300))); err != nil {
		return err
	}
	if note.Note.Author != "" {
		if _, err := fmt.Fprintf(w, "    by %s (self-asserted)\n", escapeNoteText(note.Note.Author)); err != nil {
			return err
		}
	}
	if note.Drift.Checked && note.Drift.Drifted {
		reason := note.Drift.Reason
		if reason == "" {
			reason = "anchored text changed"
		}
		if _, err := fmt.Fprintf(w, "    anchor drifted: %s\n", reason); err != nil {
			return err
		}
	}
	return nil
}

func writeBlameOriginDetails(w io.Writer, origin blame.Origin) error {
	if origin.Intent != nil {
		if _, err := fmt.Fprintf(w, "  problem: %s\n", origin.Intent.Problem); err != nil {
			return err
		}
		if len(origin.Intent.Scope) > 0 {
			if _, err := fmt.Fprintf(w, "  expected scope: %s\n", strings.Join(origin.Intent.Scope, ", ")); err != nil {
				return err
			}
		}
		if len(origin.Intent.Evidence) > 0 {
			if _, err := fmt.Fprintf(w, "  evidence: %s\n", strings.Join(origin.Intent.Evidence, ", ")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "  confidence: %s\n", blameIntentConfidence(origin)); err != nil {
			return err
		}
	} else if origin.Kind == "ambiguous" {
		if _, err := fmt.Fprintln(w, "  problem: unavailable because no recorded intent could be safely tied to this change"); err != nil {
			return err
		}
	} else if origin.Kind == "concurrent" {
		if _, err := fmt.Fprintln(w, "  problem: unavailable because concurrent agent turns overlapped"); err != nil {
			return err
		}
	} else if origin.Kind == "turn" {
		if _, err := fmt.Fprintln(w, "  problem: no agent intent recorded for this change"); err != nil {
			return err
		}
	}
	if origin.Prompt != "" {
		if _, err := fmt.Fprintf(w, "  human request: %s\n", truncateText(origin.Prompt, 800)); err != nil {
			return err
		}
	}
	if !origin.Time.IsZero() {
		if _, err := fmt.Fprintf(w, "  time: %s\n", formatGraphTime(origin.Time)); err != nil {
			return err
		}
	}
	if origin.SessionID != "" {
		if _, err := fmt.Fprintf(w, "  session: %s\n", origin.SessionID); err != nil {
			return err
		}
	}
	if origin.TurnID != 0 {
		if _, err := fmt.Fprintf(w, "  turn: %s\n", origin.TurnID); err != nil {
			return err
		}
	}
	if origin.Adapter != "" {
		if _, err := fmt.Fprintf(w, "  adapter: %s\n", origin.Adapter); err != nil {
			return err
		}
	}
	if len(origin.ToolNames) > 0 {
		if _, err := fmt.Fprintf(w, "  tools: %s\n", strings.Join(origin.ToolNames, ", ")); err != nil {
			return err
		}
	}
	if origin.ActionTool != "" {
		if _, err := fmt.Fprintf(w, "  action tool: %s\n", origin.ActionTool); err != nil {
			return err
		}
	}
	if origin.ActionAgentID != "" {
		agent := origin.ActionAgentID
		if origin.ActionAgentType != "" {
			agent = origin.ActionAgentType + " (" + agent + ")"
		}
		if _, err := fmt.Fprintf(w, "  action agent: %s\n", agent); err != nil {
			return err
		}
	}
	if origin.CheckpointRef != "" {
		if _, err := fmt.Fprintf(w, "  checkpoint: %s\n", origin.CheckpointRef); err != nil {
			return err
		}
	}
	if origin.Commit != "" {
		if _, err := fmt.Fprintf(w, "  id: %s\n", origin.Commit); err != nil {
			return err
		}
	}
	return nil
}

func blameIntentConfidence(origin blame.Origin) string {
	if origin.Intent == nil {
		return "unavailable"
	}
	switch origin.Intent.Status {
	case provenance.IntentStatusCaptured:
		return string(origin.Intent.Confidence) + " (stated before edit)"
	case provenance.IntentStatusLate:
		return string(origin.Intent.Confidence) + " (stated after edit)"
	case provenance.IntentStatusOutOfScope:
		return string(origin.Intent.Confidence) + " (outside stated scope)"
	case provenance.IntentStatusLateOutOfScope:
		return string(origin.Intent.Confidence) + " (stated after edit; outside stated scope)"
	case provenance.IntentStatusRedacted:
		if origin.Intent.Timing == provenance.IntentTimingAfter {
			return string(origin.Intent.Confidence) + " (intent redacted; stated after edit)"
		}
		return string(origin.Intent.Confidence) + " (intent redacted)"
	default:
		return fmt.Sprintf("%s (unknown intent status %q)", origin.Intent.Confidence, origin.Intent.Status)
	}
}

func originLabel(origin blame.Origin, sessionLabels map[primitives.SessionID]string) string {
	if (origin.Kind == "turn" || origin.Kind == "ambiguous") && origin.SessionID != "" && origin.TurnID != 0 {
		label := fmt.Sprintf("%s %s turn %s", formatBlameDisplayTime(origin.Time), blameOriginSessionLabel(origin, sessionLabels), origin.TurnID)
		if origin.Kind == "ambiguous" {
			label += " (ambiguous)"
		}
		return label
	}
	if origin.Kind != "" {
		return origin.Kind
	}
	return "unknown"
}

func blameSessionLabels(sessions []blame.SessionSummary) map[primitives.SessionID]string {
	graphSessions := make([]graphSession, 0, len(sessions))
	for _, session := range sessions {
		graphSessions = append(graphSessions, graphSession{
			ID:         session.ID,
			TotalTurns: 1,
			Turns: []graphTurn{
				{
					Post: &checkpoint.CheckpointRefInfo{Time: session.StartedAt},
					Events: turnEventSummary{
						Adapter: session.Adapter,
					},
				},
			},
		})
	}

	labels := buildGraphSessionLabels(graphSessions)
	bySession := make(map[primitives.SessionID]string, len(labels))
	for index, label := range labels {
		bySession[graphSessions[index].ID] = "[" + label + "]"
	}
	return bySession
}

func blameOriginSessionLabel(origin blame.Origin, sessionLabels map[primitives.SessionID]string) string {
	if label := sessionLabels[origin.SessionID]; label != "" {
		return label
	}
	if origin.Adapter != "" && !origin.Time.IsZero() {
		return "[" + normalizeSessionAgent(origin.Adapter) + " " + origin.Time.UTC().Format("15:04") + "]"
	}
	return "[" + origin.SessionID.String() + "]"
}

func formatBlameDisplayTime(value time.Time) string {
	if value.IsZero() {
		return "--:--"
	}
	return value.UTC().Format("15:04")
}
