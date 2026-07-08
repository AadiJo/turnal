package cli

import (
	"encoding/json"
	"fmt"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/spf13/cobra"
)

func turnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "turn",
		Short:  "Create manual turn checkpoints",
		Hidden: true,
	}
	cmd.AddCommand(turnStartCmd())
	cmd.AddCommand(turnFinishCmd())
	cmd.AddCommand(turnRecallCmd())
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
			result, err := turnevents.Recorder{
				Log:     eventlog.Open(repo.MetadataDir),
				Manager: turns.NewManager(repo),
				Adapter: primitives.AdapterManual,
			}.Start(sessionID, turnID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "started turn %s\n", result.TurnID)
			return writeHiddenIDRefBlock(out, "Pre checkpoint", result.Pre.Commit, result.Pre.Ref.String())
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
			result, err := turnevents.Recorder{
				Log:     eventlog.Open(repo.MetadataDir),
				Manager: turns.NewManager(repo),
				Adapter: primitives.AdapterManual,
			}.Finish(sessionID, turnID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "finished turn %s\n", result.TurnID)
			return writeHiddenIDRefBlock(out, "Post checkpoint", result.Post.Commit, result.Post.Ref.String())
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

func turnRecallCmd() *cobra.Command {
	var session string
	var turn uint64
	var jsonOutput bool
	var includeTranscript bool
	includeRaw := true

	cmd := &cobra.Command{
		Use:          "recall",
		Short:        "Recall normalized events and raw adapter records for a turn",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			recalled, err := recall.NewReader(repo.MetadataDir).RecallTurn(sessionID, turnID, recall.Options{
				IncludeRaw:        includeRaw,
				IncludeTranscript: includeTranscript,
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

	cmd.Flags().StringVar(&session, "session", "", "Session id for the turn")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number to recall")
	cmd.Flags().BoolVar(&includeRaw, "raw", true, "Include referenced raw adapter hook records")
	cmd.Flags().BoolVar(&includeTranscript, "transcript", false, "Include assistant text from the captured provider transcript path")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}
