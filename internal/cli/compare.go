package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	caseengine "github.com/AadiJo/turnal/internal/cases"
	experimentengine "github.com/AadiJo/turnal/internal/experiments"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func compareCmd() *cobra.Command {
	var jsonOutput bool
	var patchText string
	cmd := &cobra.Command{
		Use:          "compare <case-id>",
		Short:        "Compare Case attempts against their common base",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			caseID, err := primitives.ParseCaseID(args[0])
			if err != nil {
				return err
			}
			var patchAttempt primitives.AttemptID
			if strings.TrimSpace(patchText) != "" {
				patchAttempt, err = primitives.ParseAttemptID(patchText)
				if err != nil {
					return err
				}
			}
			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			comparison, err := experimentengine.Compare(repo, caseID, patchAttempt)
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(comparison)
			}
			return writeComparison(cmd.OutOrStdout(), comparison)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	cmd.Flags().StringVar(&patchText, "patch", "", "Include the full base-to-result patch for one attempt id")
	return cmd
}

func selectCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "select <case-id> <attempt-id>",
		Short:        "Select a completed Case attempt",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			caseID, err := primitives.ParseCaseID(args[0])
			if err != nil {
				return err
			}
			attemptID, err := primitives.ParseAttemptID(args[1])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			definition, err := caseengine.SelectAttempt(repo, caseID, attemptID)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeCaseJSON(cmd.OutOrStdout(), caseJSON{Version: caseengine.JSONVersion, Case: definition})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "selected attempt %s for case %s\n", attemptID, caseID)
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	return cmd
}

func writeComparison(writer io.Writer, comparison experimentengine.Comparison) error {
	if _, err := fmt.Fprintf(writer, "case:        %s\nbase commit: %s\nattempts:    %d\n", comparison.CaseID, comparison.BaseCommit, len(comparison.Attempts)); err != nil {
		return err
	}
	if len(comparison.Attempts) == 0 {
		_, err := fmt.Fprintln(writer, "  none")
		return err
	}
	for _, attempt := range comparison.Attempts {
		selection := ""
		if attempt.Selected {
			selection = " [selected]"
		}
		if _, err := fmt.Fprintf(writer, "\n%s%s\n  status: %s\n  commit: %s\n  changes: %d files, +%d -%d", attempt.AttemptID, selection, attempt.Status, attempt.PostCommit, len(attempt.Files), attempt.Additions, attempt.Deletions); err != nil {
			return err
		}
		if attempt.BinaryFiles > 0 {
			if _, err := fmt.Fprintf(writer, ", %d binary", attempt.BinaryFiles); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
		for _, file := range attempt.Files {
			if file.Binary {
				if _, err := fmt.Fprintf(writer, "    binary  %s\n", file.Path); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(writer, "    +%-5d -%-5d %s\n", file.Additions, file.Deletions, file.Path); err != nil {
				return err
			}
		}
		if attempt.Patch != "" {
			if _, err := fmt.Fprintf(writer, "\npatch %s:\n%s", attempt.AttemptID, attempt.Patch); err != nil {
				return err
			}
			if !strings.HasSuffix(attempt.Patch, "\n") {
				if _, err := fmt.Fprintln(writer); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
