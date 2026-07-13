package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
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
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

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
			defer func() {
				if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
					if _, hasExitCode := commandExitCode(returnErr); hasExitCode {
						returnErr = cleanupErr
					} else {
						returnErr = errors.Join(returnErr, cleanupErr)
					}
				}
			}()

			report, runErr := verifier.Run(ctx, verifier.Request{
				Root:      prepared.Root,
				Target:    prepared.Target,
				Verifiers: effective.Verify,
			})
			if runErr != nil {
				return runErr
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else if err := verifier.WriteHuman(cmd.OutOrStdout(), report); err != nil {
				return err
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
