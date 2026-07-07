package cli

import (
	"fmt"
	"os"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func checkpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "checkpoint",
		Short:  "Manage hidden Git checkpoints",
		Hidden: true,
	}
	cmd.AddCommand(checkpointCreateCmd())
	return cmd
}

func checkpointCreateCmd() *cobra.Command {
	var session string
	var turn uint64
	var phase string

	cmd := &cobra.Command{
		Use:          "create",
		Short:        "Create a hidden Git checkpoint commit",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				return fmt.Errorf("--session is required")
			}
			if turn == 0 {
				return fmt.Errorf("--turn must be greater than zero")
			}
			if phase == "" {
				return fmt.Errorf("--phase is required")
			}

			sessionID, err := primitives.ParseSessionID(session)
			if err != nil {
				return err
			}
			turnID, err := primitives.NewTurnID(turn)
			if err != nil {
				return err
			}
			checkpointPhase, err := primitives.ParseCheckpointPhase(phase)
			if err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			created, err := repo.CreateCheckpoint(sessionID, turnID, checkpointPhase)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", created.Commit, created.Ref)
			return nil
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id for the checkpoint")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number for the checkpoint")
	cmd.Flags().StringVar(&phase, "phase", "", "Checkpoint phase: pre or post")
	return cmd
}

func openCheckpointRepo() (*checkpoint.Repo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return nil, err
	}
	return checkpoint.Open(root)
}
