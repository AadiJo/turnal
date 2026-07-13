package cli

import (
	"fmt"

	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/spf13/cobra"
)

func recoveryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Inspect or resolve an interrupted rollback",
	}
	cmd.AddCommand(recoveryStatusCmd(), recoveryResumeCmd(), recoveryRestoreSafetyCmd())
	return cmd
}

func recoveryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Describe an interrupted rollback and its recovery choices",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			journal, ok, err := rollbackengine.RecoveryStatus(repo)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "rollback recovery: none")
				return nil
			}
			phase := journal.RestorePhase
			if phase == "" {
				phase = journal.State
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rollback recovery: pending\nphase: %s\ntarget: %s\nmode: %s\nsafety ref: %s\nsafety commit: %s\n", phase, journal.Target, journal.Mode, journal.SafetyRef, journal.SafetyCommitSHA)
			fmt.Fprintln(cmd.OutOrStdout(), "choices: turnal recovery resume --yes | turnal recovery restore-safety --yes")
			return nil
		},
	}
}

func recoveryResumeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:          "resume",
		Short:        "Explicitly reapply the rollback target and finalize it",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("resume may reapply a partially completed restore; rerun with --yes after reviewing turnal recovery status")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := rollbackengine.New(repo).ResumeRecovery(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "rollback recovery resumed and finalized")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm target reapplication")
	return cmd
}

func recoveryRestoreSafetyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:          "restore-safety",
		Short:        "Abandon the target and restore the pre-rollback safety snapshot",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("restoring safety replaces the current workspace; rerun with --yes after reviewing turnal recovery status")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := rollbackengine.New(repo).RestoreSafety(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "pre-rollback safety snapshot restored")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm safety restoration")
	return cmd
}
