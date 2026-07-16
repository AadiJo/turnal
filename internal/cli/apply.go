package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	experimentengine "github.com/AadiJo/turnal/internal/experiments"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func applyCmd() *cobra.Command {
	var attemptText string
	var dryRun bool
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "apply <case-id>",
		Short:        "Apply a selected Case attempt to an exact-base workspace",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			caseID, err := primitives.ParseCaseID(args[0])
			if err != nil {
				return err
			}
			var attemptID primitives.AttemptID
			if strings.TrimSpace(attemptText) != "" {
				attemptID, err = primitives.ParseAttemptID(attemptText)
				if err != nil {
					return err
				}
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := experimentengine.Apply(repo, experimentengine.ApplyRequest{CaseID: caseID, AttemptID: attemptID, DryRun: dryRun})
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			return writeApplyResult(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&attemptText, "attempt", "", "Apply this completed attempt instead of the currently selected attempt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview exact-base validation and planned file changes")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	return cmd
}

func writeApplyResult(writer io.Writer, result experimentengine.ApplyResult) error {
	action := "apply preview"
	if !result.DryRun {
		action = "applied"
	}
	if _, err := fmt.Fprintf(writer,
		"%s: %s\ncase: %s\nbase commit: %s\nresult commit: %s\nchanges: %d\n",
		action, result.AttemptID, result.CaseID, result.BaseCommit, result.PostCommit, len(result.Changes),
	); err != nil {
		return err
	}
	for _, change := range result.Changes {
		if _, err := fmt.Fprintf(writer, "  %-12s %s\n", change.Action, change.Path); err != nil {
			return err
		}
	}
	if !result.DryRun {
		_, err := fmt.Fprintf(writer, "safety ref: %s\nsafety commit: %s\n", result.SafetyRef, result.SafetyCommit)
		return err
	}
	return nil
}
