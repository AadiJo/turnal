package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/AadiJo/turnal/internal/checkpoint"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func noteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Attach a reviewer note to a recorded turn",
		Long: `Attach a note to a recorded turn.

A note is your statement about a turn, not recorded evidence. It never changes
the workspace and never claims a turn was right or wrong.

Notes are stored in this worktree's own note stream, so noting a turn does not
modify the recorded agent history it discusses.

Removing a note hides it. The original note stays in the append-only log, and any
copy already published to teammates cannot be recalled.`,
	}
	cmd.AddCommand(noteAddCmd())
	cmd.AddCommand(noteListCmd())
	cmd.AddCommand(noteRemoveCmd())
	return cmd
}

func noteAddCmd() *cobra.Command {
	var path string
	var line string
	var author string
	var stream string
	var fromFile string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "add <session>:<turn> [text]",
		Short: "Record a note about a recorded turn",
		Long: `Record a note about a recorded turn.

Pass the note text as an argument, or use --file to read it from a path, or
--file - to read it from stdin.

Use --path to scope a note to one file, and --line to anchor it to a line or an
inclusive range such as 40-48. An anchored note records the file text as it was
at the turn's post checkpoint. Turnal later reports whether that text changed; it
never guesses where a line moved to.`,
		SilenceUsage: true,
		Args:         cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, turnID, err := parseTurnTarget(args[0])
			if err != nil {
				return err
			}
			text, err := noteText(cmd, args, fromFile)
			if err != nil {
				return err
			}
			lineStart, lineEnd, err := parseNoteLines(line)
			if err != nil {
				return err
			}
			if path == "" && lineStart != 0 {
				return fmt.Errorf("--line requires --path")
			}
			var repoPath primitives.RepoPath
			if path != "" {
				if repoPath, err = primitives.ParseRepoPath(path); err != nil {
					return err
				}
			}
			var streamID primitives.EventStreamID
			if stream != "" {
				if streamID, err = primitives.ParseEventStreamID(stream); err != nil {
					return err
				}
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			resolved, err := notes.ResolveLocalTurn(repo, sessionID, turnID, streamID)
			if err != nil {
				return err
			}
			if lineStart != 0 && resolved.PostCommit == "" {
				return fmt.Errorf("turn %s:%s has no post checkpoint, so a line anchor cannot be verified; record the note without --line", sessionID, turnID)
			}

			note, err := notes.Record(repo, notes.RecordInput{
				Target:       resolved.Target,
				Text:         text,
				Path:         repoPath,
				LineStart:    lineStart,
				LineEnd:      lineEnd,
				AnchorCommit: resolved.PostCommit,
				Author:       author,
			})
			if err != nil {
				return err
			}
			if err := queryindex.Invalidate(repo.MetadataDir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: note recorded, but the disposable index could not be invalidated: %v\n", err)
			}

			if jsonOutput {
				return writeJSON(cmd, note)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Recorded note %s on %s:%s\n", note.NoteID, note.Target.SessionID, note.Target.TurnID)
			if note.Anchor != nil {
				fmt.Fprintf(out, "  anchor: %s\n", formatNoteAnchor(*note.Anchor))
			}
			if note.Redacted {
				fmt.Fprintln(out, "  note: text withheld by this workspace's secrets policy")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Repository path the note is about")
	cmd.Flags().StringVar(&line, "line", "", "Line or inclusive range within --path, such as 42 or 40-48")
	cmd.Flags().StringVar(&author, "author", "", "Self-asserted author label; it authenticates nothing on its own")
	cmd.Flags().StringVar(&stream, "stream", "", "Select an event stream when session and turn are ambiguous")
	cmd.Flags().StringVar(&fromFile, "file", "", "Read note text from a path, or - for stdin")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func noteListCmd() *cobra.Command {
	var jsonOutput bool
	var path string

	cmd := &cobra.Command{
		Use:          "list [<session>:<turn>]",
		Short:        "List notes recorded in this store",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := notes.Query{}
			if len(args) == 1 {
				sessionID, turnID, err := parseTurnTarget(args[0])
				if err != nil {
					return err
				}
				query.SessionID, query.TurnID = sessionID, turnID
			}
			if path != "" {
				repoPath, err := primitives.ParseRepoPath(path)
				if err != nil {
					return err
				}
				query.Path = repoPath
			}

			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			listed, err := notes.List(repo, query)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, listed)
			}
			return writeNoteList(cmd.OutOrStdout(), repo, listed)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().StringVar(&path, "path", "", "Only notes anchored to this repository path")
	return cmd
}

func noteRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <note-id>",
		Short: "Hide a note this worktree recorded",
		Long: `Hide a note this worktree recorded.

This appends a tombstone. It does not erase anything: the original note remains
in the append-only event log, and any copy already published to teammates stays
recoverable from shared history. Only the worktree that authored a note can hide
it.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			noteID, err := primitives.ParseNoteID(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := notes.Delete(repo, noteID); err != nil {
				return err
			}
			if err := queryindex.Invalidate(repo.MetadataDir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: note hidden, but the disposable index could not be invalidated: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Hid note %s\n", noteID)
			fmt.Fprintln(cmd.OutOrStdout(), "  the original note remains in the durable log and in any published copy")
			return nil
		},
	}
	return cmd
}

func noteText(cmd *cobra.Command, args []string, fromFile string) (string, error) {
	inline := ""
	if len(args) == 2 {
		inline = args[1]
	}
	if fromFile == "" {
		if strings.TrimSpace(inline) == "" {
			return "", fmt.Errorf("note text is required; pass it as an argument or use --file")
		}
		return inline, nil
	}
	if strings.TrimSpace(inline) != "" {
		return "", fmt.Errorf("pass note text either as an argument or with --file, not both")
	}
	var data []byte
	var err error
	if fromFile == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(fromFile)
	}
	if err != nil {
		return "", fmt.Errorf("read note text: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func parseNoteLines(value string) (int, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, nil
	}
	startText, endText, ranged := strings.Cut(value, "-")
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start <= 0 {
		return 0, 0, fmt.Errorf("invalid note line %q: must be a positive line number or range", value)
	}
	if !ranged {
		return start, start, nil
	}
	end, err := strconv.Atoi(strings.TrimSpace(endText))
	if err != nil || end <= 0 {
		return 0, 0, fmt.Errorf("invalid note line range %q: must be <start>-<end>", value)
	}
	if end < start {
		return 0, 0, fmt.Errorf("invalid note line range %q: end precedes start", value)
	}
	return start, end, nil
}

func writeNoteList(w io.Writer, repo *checkpoint.Repo, listed []notes.Note) error {
	if len(listed) == 0 {
		_, err := fmt.Fprintln(w, "no notes recorded")
		return err
	}
	for _, note := range listed {
		if _, err := fmt.Fprintf(w, "%s  %s:%s  %s\n", note.NoteID, note.Target.SessionID, note.Target.TurnID, formatGraphTime(note.CreatedAt.Time)); err != nil {
			return err
		}
		if note.Anchor != nil {
			label := formatNoteAnchor(*note.Anchor)
			if drift := noteAnchorDrift(repo, note); drift != "" {
				label += " (" + drift + ")"
			}
			if _, err := fmt.Fprintf(w, "  anchor: %s\n", label); err != nil {
				return err
			}
		}
		if note.Author != "" {
			if _, err := fmt.Fprintf(w, "  author: %s (self-asserted)\n", escapeNoteLine(note.Author)); err != nil {
				return err
			}
		}
		if note.Target.Locator != "" {
			if _, err := fmt.Fprintf(w, "  reviewed: %s\n", escapeNoteLine(note.Target.Locator)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "  %s\n", escapeNoteText(note.Text)); err != nil {
			return err
		}
	}
	return nil
}

// formatNoteAnchor renders an anchor for a terminal. The path is escaped even
// though recording now rejects control characters, because a note recorded by an
// earlier build is durable and still has to render safely. Newline and tab are
// escaped too: unlike note prose, a path occupies one line, so either would let
// it forge output structure.
func formatNoteAnchor(anchor notes.Anchor) string {
	path := escapeNoteLine(anchor.Path.String())
	switch {
	case anchor.LineStart == 0:
		return path
	case anchor.LineEnd > anchor.LineStart:
		return fmt.Sprintf("%s:%d-%d", path, anchor.LineStart, anchor.LineEnd)
	default:
		return fmt.Sprintf("%s:%d", path, anchor.LineStart)
	}
}

// noteAnchorDrift compares an anchored note against the latest post checkpoint
// for its target turn's session. An empty result means the anchor was not
// checked, which is reported as nothing rather than as agreement.
func noteAnchorDrift(repo *checkpoint.Repo, note notes.Note) string {
	if repo == nil || note.Anchor == nil || note.Anchor.LineSHA == "" {
		return ""
	}
	commit, ok := latestPostCommit(repo, note.Target.SessionID)
	if !ok {
		return ""
	}
	drift := notes.CheckAnchor(repo, note, commit)
	if !drift.Checked || !drift.Drifted {
		return ""
	}
	if drift.Reason != "" {
		return "anchor drifted: " + drift.Reason
	}
	return "anchor drifted"
}

func latestPostCommit(repo *checkpoint.Repo, sessionID primitives.SessionID) (primitives.CommitSHA, bool) {
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return "", false
	}
	var commit primitives.CommitSHA
	var newest int64
	for _, info := range infos {
		if info.Manual || info.Phase != primitives.CheckpointPhasePost {
			continue
		}
		if sessionID != "" && info.SessionID != sessionID {
			continue
		}
		if commit == "" || info.Time.UnixNano() > newest {
			commit, newest = info.Commit, info.Time.UnixNano()
		}
	}
	return commit, commit != ""
}

// escapeNoteLine escapes a single-line field such as an anchor path or author
// label, where newline and tab are structure rather than formatting.
func escapeNoteLine(value string) string {
	var safe strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			fmt.Fprintf(&safe, "\\u%04x", character)
			continue
		}
		safe.WriteRune(character)
	}
	return safe.String()
}

// escapeNoteText neutralizes terminal control sequences in note text. Note text
// is human-authored and, once shared history carries it, may come from another
// machine, so it is never written to a terminal verbatim. Newline and tab are
// preserved because a note body is prose that may legitimately contain them.
func escapeNoteText(value string) string {
	var safe strings.Builder
	for _, character := range value {
		switch {
		case character == '\n' || character == '\t':
			safe.WriteRune(character)
		case unicode.IsControl(character) || unicode.Is(unicode.Cf, character):
			fmt.Fprintf(&safe, "\\u%04x", character)
		default:
			safe.WriteRune(character)
		}
	}
	return strings.ReplaceAll(safe.String(), "\n", "\n  ")
}
