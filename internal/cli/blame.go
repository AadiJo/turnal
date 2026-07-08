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
misses are computed lazily from completed pre/post checkpoint pairs. It does
not use Git commit ancestry, and it does not inspect uncheckpointed workspace
edits.

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
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show prompt, tool, and checkpoint id metadata")
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

	for _, entry := range result.Entries {
		label := originLabel(entry.Origin, sessionLabels)
		if _, err := fmt.Fprintf(w, "%-*s %6d | %s\n", labelWidth, label, entry.Line, entry.Text); err != nil {
			return err
		}
		if prompt := truncateText(entry.Origin.Prompt, 140); prompt != "" {
			if _, err := fmt.Fprintf(w, "  Prompt: %q\n", prompt); err != nil {
				return err
			}
		}
		if verbose {
			if err := writeBlameOriginDetails(w, entry.Origin); err != nil {
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

func writeBlameOriginDetails(w io.Writer, origin blame.Origin) error {
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

func originLabel(origin blame.Origin, sessionLabels map[primitives.SessionID]string) string {
	if origin.Kind == "turn" && origin.SessionID != "" && origin.TurnID != 0 {
		return fmt.Sprintf("%s %s turn %s", formatBlameDisplayTime(origin.Time), blameOriginSessionLabel(origin, sessionLabels), origin.TurnID)
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
