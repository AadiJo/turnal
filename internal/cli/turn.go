package cli

import (
	"fmt"

	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
	"github.com/spf13/cobra"
)

func turnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "turn",
		Short: "Create manual turn checkpoints",
	}
	cmd.AddCommand(turnStartCmd())
	cmd.AddCommand(turnFinishCmd())
	return cmd
}

func turnStartCmd() *cobra.Command {
	var session string
	var turn uint64

	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Create the pre checkpoint for a manual turn",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				return fmt.Errorf("--session is required")
			}

			sessionID, err := primitives.ParseSessionID(session)
			if err != nil {
				return err
			}
			turnID, err := optionalTurnID(turn)
			if err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := turns.NewManager(repo).Start(sessionID, turnID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "started turn %s\n", result.TurnID)
			fmt.Fprintf(out, "pre %s %s\n", result.Pre.Commit, result.Pre.Ref)
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id for the turn")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number to start; defaults to the next turn")
	return cmd
}

func turnFinishCmd() *cobra.Command {
	var session string
	var turn uint64

	cmd := &cobra.Command{
		Use:          "finish",
		Short:        "Create the post checkpoint for a manual turn",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				return fmt.Errorf("--session is required")
			}

			sessionID, err := primitives.ParseSessionID(session)
			if err != nil {
				return err
			}
			turnID, err := optionalTurnID(turn)
			if err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := turns.NewManager(repo).Finish(sessionID, turnID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "finished turn %s\n", result.TurnID)
			fmt.Fprintf(out, "post %s %s\n", result.Post.Commit, result.Post.Ref)
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id for the turn")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number to finish; defaults to the active turn")
	return cmd
}

func optionalTurnID(value uint64) (primitives.TurnID, error) {
	if value == 0 {
		return 0, nil
	}
	return primitives.NewTurnID(value)
}
