package cli

import (
	"encoding/json"

	"agent-vcs-again/internal/recall"
	"github.com/spf13/cobra"
)

func showCmd() *cobra.Command {
	var jsonOutput bool
	var includeRaw bool
	var includeTranscript bool
	var full bool

	cmd := &cobra.Command{
		Use:          "show [turn-ref]",
		Short:        "Show an agent turn",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}

			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}

			reader := recall.NewReader(repo.MetadataDir)
			target, err := reader.ResolveTurnRef(ref)
			if err != nil {
				return err
			}

			options := recall.Options{
				IncludeRaw:        includeRaw || full,
				IncludeTranscript: includeTranscript || full,
			}
			recalled, err := reader.RecallTurn(target.SessionID, target.TurnID, options)
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
