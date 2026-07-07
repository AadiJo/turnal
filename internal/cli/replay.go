package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
	replayengine "github.com/AadiJo/turnal/internal/replay"
	"github.com/spf13/cobra"
)

func replayCmd() *cobra.Command {
	var path string
	var worktree string

	cmd := &cobra.Command{
		Use:          "replay [target]",
		Short:        "Inspect checkpoints in an isolated replay worktree",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			replayPath, err := replayPathFlag(path, worktree)
			if err != nil {
				return err
			}
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, err := manager.Checkout(args[0], replayPath)
			if err != nil {
				return err
			}
			return writeReplayResult(cmd.OutOrStdout(), result)
		},
	}
	cmd.PersistentFlags().StringVar(&path, "path", "", "Replay worktree path")
	cmd.PersistentFlags().StringVar(&worktree, "worktree", "", "Replay worktree path")

	cmd.AddCommand(replayCheckoutCmd(&path, &worktree))
	cmd.AddCommand(replayNextCmd())
	cmd.AddCommand(replayPrevCmd())
	cmd.AddCommand(replayGotoCmd())
	cmd.AddCommand(replayDiffCmd())
	cmd.AddCommand(replayShowCmd())
	cmd.AddCommand(replayKeepCmd(&path, &worktree))
	cmd.AddCommand(replayStopCmd())
	cmd.AddCommand(replayRemoveCmd())
	cmd.AddCommand(replayListCmd())
	return cmd
}

func replayCheckoutCmd(path *string, worktree *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "checkout <target>",
		Aliases:      []string{"start"},
		Short:        "Create an isolated replay worktree at a checkpoint",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			replayPath, err := replayPathFlag(*path, *worktree)
			if err != nil {
				return err
			}
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, err := manager.Checkout(args[0], replayPath)
			if err != nil {
				return err
			}
			return writeReplayResult(cmd.OutOrStdout(), result)
		},
	}
	return cmd
}

func replayNextCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "next",
		Short:        "Move the active replay worktree to the next checkpoint",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, err := manager.Move(1)
			if err != nil {
				return err
			}
			return writeReplayResult(cmd.OutOrStdout(), result)
		},
	}
}

func replayPrevCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "prev",
		Short:        "Move the active replay worktree to the previous checkpoint",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, err := manager.Move(-1)
			if err != nil {
				return err
			}
			return writeReplayResult(cmd.OutOrStdout(), result)
		},
	}
}

func replayGotoCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "goto <target>",
		Short:        "Move the active replay worktree to a checkpoint",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, err := manager.Goto(args[0])
			if err != nil {
				return err
			}
			return writeReplayResult(cmd.OutOrStdout(), result)
		},
	}
}

func replayDiffCmd() *cobra.Command {
	var next bool
	var workspace bool

	cmd := &cobra.Command{
		Use:          "diff",
		Short:        "Show a diff for the active replay checkpoint",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if next && workspace {
				return fmt.Errorf("--next cannot be combined with --workspace")
			}
			mode := replayengine.DiffPrevious
			if next {
				mode = replayengine.DiffNext
			}
			if workspace {
				mode = replayengine.DiffWorkspace
			}
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			diff, err := manager.Diff(mode)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(diff)
			return err
		},
	}
	cmd.Flags().BoolVar(&next, "next", false, "Diff the current replay checkpoint to the next checkpoint")
	cmd.Flags().BoolVar(&workspace, "workspace", false, "Diff the current replay checkpoint to the source workspace")
	return cmd
}

func replayShowCmd() *cobra.Command {
	var jsonOutput bool
	var includeRaw bool
	var includeTranscript bool
	var full bool

	cmd := &cobra.Command{
		Use:          "show",
		Short:        "Show the turn events for the active replay checkpoint",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			manager := replayengine.New(repo)
			session, err := manager.Active()
			if err != nil {
				return err
			}
			current := session.Sequence[session.Current]
			sessionID, turnID, err := replayTurn(current)
			if err != nil {
				return err
			}
			reader := recall.NewReader(repo.MetadataDir)
			recalled, err := reader.RecallTurn(sessionID, turnID, recall.Options{
				IncludeRaw:        includeRaw || full,
				IncludeTranscript: includeTranscript || full,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(recalled)
			}
			return recall.WriteText(out, recalled)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&includeRaw, "raw", false, "Include referenced raw adapter hook records")
	cmd.Flags().BoolVar(&includeTranscript, "transcript", false, "Include assistant text from the captured provider transcript path")
	cmd.Flags().BoolVar(&full, "full", false, "Include raw adapter records and provider transcript text")
	return cmd
}

func replayKeepCmd(path *string, worktree *string) *cobra.Command {
	return &cobra.Command{
		Use:          "keep [path]",
		Short:        "Keep or copy the current replay checkpoint",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			replayPath, err := replayPathFlag(*path, *worktree)
			if err != nil {
				return err
			}
			if replayPath != "" && len(args) > 0 {
				return fmt.Errorf("path argument cannot be combined with --path or --worktree")
			}
			if len(args) > 0 {
				replayPath = args[0]
			}
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			result, keptPath, err := manager.Keep(replayPath)
			if err != nil {
				return err
			}
			return writeReplayKeepResult(cmd.OutOrStdout(), result, keptPath, replayPath != "")
		},
	}
}

func replayStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "stop",
		Short:        "Stop the active replay session",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			session, removed, err := manager.Stop()
			if err != nil {
				return err
			}
			return writeReplayStopResult(cmd.OutOrStdout(), "stopped", session, removed)
		},
	}
}

func replayRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "remove [session-or-path]",
		Aliases:      []string{"rm"},
		Short:        "Remove a replay session and its worktree",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := ""
			if len(args) > 0 {
				selector = args[0]
			}
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			session, removed, err := manager.Remove(selector)
			if err != nil {
				return err
			}
			return writeReplayStopResult(cmd.OutOrStdout(), "removed", session, removed)
		},
	}
}

func replayListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List replay sessions",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := openReplayManager()
			if err != nil {
				return err
			}
			sessions, activeID, err := manager.List()
			if err != nil {
				return err
			}
			return writeReplayList(cmd.OutOrStdout(), sessions, activeID)
		},
	}
}

func openReplayManager() (replayengine.Manager, error) {
	repo, err := openCheckpointRepo()
	if err != nil {
		return replayengine.Manager{}, err
	}
	return replayengine.New(repo), nil
}

func replayPathFlag(path string, worktree string) (string, error) {
	if path != "" && worktree != "" {
		return "", fmt.Errorf("--path cannot be combined with --worktree")
	}
	if worktree != "" {
		return worktree, nil
	}
	return path, nil
}

func writeReplayResult(w io.Writer, result replayengine.Result) error {
	if _, err := fmt.Fprintf(w, "replay worktree: %s\n", result.Session.Path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "state: %s\n", formatReplayCheckpoint(result.Current)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "commands:"); err != nil {
		return err
	}
	for _, command := range []string{
		"turnal replay next",
		"turnal replay prev",
		"turnal replay goto " + result.Current.Target,
		"turnal replay diff",
		"turnal replay show",
		"turnal replay keep",
		"turnal replay stop",
	} {
		if _, err := fmt.Fprintf(w, "  %s\n", command); err != nil {
			return err
		}
	}
	return nil
}

func writeReplayKeepResult(w io.Writer, result replayengine.Result, keptPath string, copied bool) error {
	if copied {
		if _, err := fmt.Fprintf(w, "kept replay state: %s\n", keptPath); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(w, "kept replay worktree: %s\n", keptPath); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "state: %s\n", formatReplayCheckpoint(result.Current))
	return err
}

func writeReplayStopResult(w io.Writer, action string, session replayengine.Session, removed bool) error {
	if _, err := fmt.Fprintf(w, "%s replay session: %s\n", action, session.ID); err != nil {
		return err
	}
	if removed {
		_, err := fmt.Fprintf(w, "removed replay worktree: %s\n", session.Path)
		return err
	}
	_, err := fmt.Fprintf(w, "kept replay worktree: %s\n", session.Path)
	return err
}

func writeReplayList(w io.Writer, sessions []replayengine.Session, activeID string) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(w, "no replay sessions")
		return err
	}
	for _, session := range sessions {
		current := session.Sequence[session.Current]
		prefix := " "
		if session.ID == activeID {
			prefix = "*"
		}
		if _, err := fmt.Fprintf(w, "%s %s  %s  %s\n", prefix, session.ID, current.Target, session.Path); err != nil {
			return err
		}
	}
	return nil
}

func formatReplayCheckpoint(checkpoint replayengine.Checkpoint) string {
	return fmt.Sprintf("%s turn %d %s", checkpoint.SessionID, checkpoint.Turn, checkpoint.Phase)
}

func replayTurn(checkpoint replayengine.Checkpoint) (primitives.SessionID, primitives.TurnID, error) {
	sessionID, err := primitives.ParseSessionID(checkpoint.SessionID)
	if err != nil {
		return "", 0, err
	}
	turnID, err := primitives.NewTurnID(checkpoint.Turn)
	if err != nil {
		return "", 0, err
	}
	if _, err := primitives.ParseCheckpointPhase(checkpoint.Phase); err != nil {
		return "", 0, err
	}
	return sessionID, turnID, nil
}
