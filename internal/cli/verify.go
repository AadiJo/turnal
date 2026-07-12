package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/verifier"
	"github.com/spf13/cobra"
)

const verifyFailureExitCode = 3

func verifyCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "verify [<session>:<turn>:<pre|post>]",
		Short:        "Run repository-defined checks against a workspace state",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			effective, origins, err := config.Resolve(repo.WorkspaceRoot.String(), config.Overrides{})
			if err != nil {
				return err
			}
			if origins["verify"] != config.OriginWorkspace || len(effective.Verify) == 0 {
				return fmt.Errorf("no repository verifiers are configured in %s", config.WorkspacePath(repo.WorkspaceRoot.String()))
			}

			var prepared verifier.PreparedTarget
			if len(args) == 0 {
				prepared, err = verifier.LiveTarget(repo)
			} else {
				sessionID, turnID, phase, parseErr := parseVerifyTarget(args[0])
				if parseErr != nil {
					return parseErr
				}
				prepared, err = verifier.PrepareCheckpoint(repo, sessionID, turnID, phase)
			}
			if err != nil {
				return err
			}

			report, runErr := verifier.Run(context.Background(), verifier.Request{
				Root:      prepared.Root,
				Target:    prepared.Target,
				Verifiers: effective.Verify,
			})
			cleanupErr := prepared.Cleanup()
			if runErr != nil {
				return errors.Join(runErr, cleanupErr)
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return errors.Join(err, cleanupErr)
				}
			} else if err := verifier.WriteHuman(cmd.OutOrStdout(), report); err != nil {
				return errors.Join(err, cleanupErr)
			}
			if cleanupErr != nil {
				return cleanupErr
			}
			if !report.Successful() {
				return commandExitError{code: verifyFailureExitCode}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit a versioned JSON verifier report")
	return cmd
}
