package cli

import (
	"fmt"

	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func diffCmd() *cobra.Command {
	var session string
	var turn uint64
	var preRef string
	var postRef string

	cmd := &cobra.Command{
		Use:          "diff [session:turn]",
		Short:        "Show the diff between hidden Git checkpoints",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}

			var diff []byte
			switch {
			case len(args) == 1:
				if session != "" || turn != 0 || preRef != "" || postRef != "" {
					return fmt.Errorf("target argument cannot be combined with --session, --turn, --pre-ref, or --post-ref")
				}
				sessionID, turnID, err := parseTurnTarget(args[0])
				if err != nil {
					return err
				}
				diff, err = repo.DiffTurn(sessionID, turnID)
				if err != nil {
					return err
				}
			case preRef != "" || postRef != "":
				if preRef == "" || postRef == "" {
					return fmt.Errorf("--pre-ref and --post-ref must be provided together")
				}
				pre, err := primitives.ParseCheckpointRef(preRef)
				if err != nil {
					return err
				}
				post, err := primitives.ParseCheckpointRef(postRef)
				if err != nil {
					return err
				}
				diff, err = repo.DiffRefs(pre, post)
				if err != nil {
					return err
				}
			default:
				if session == "" {
					return fmt.Errorf("--session is required")
				}
				if turn == 0 {
					return fmt.Errorf("--turn must be greater than zero")
				}
				sessionID, err := primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
				turnID, err := primitives.NewTurnID(turn)
				if err != nil {
					return err
				}
				diff, err = repo.DiffTurn(sessionID, turnID)
				if err != nil {
					return err
				}
			}

			_, err = cmd.OutOrStdout().Write(diff)
			return err
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to diff")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number to diff")
	cmd.Flags().StringVar(&preRef, "pre-ref", "", "Explicit pre checkpoint ref")
	cmd.Flags().StringVar(&postRef, "post-ref", "", "Explicit post checkpoint ref")
	for _, name := range []string{"session", "turn", "pre-ref", "post-ref"} {
		_ = cmd.Flags().MarkHidden(name)
	}
	return cmd
}
