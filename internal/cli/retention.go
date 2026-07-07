package cli

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/retention"
	"github.com/spf13/cobra"
)

func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage retained session data",
	}
	cmd.AddCommand(sessionDropCmd())
	return cmd
}

func sessionDropCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "drop <session>",
		Aliases:      []string{"delete"},
		Short:        "Delete a session event log and private refs",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, err := primitives.ParseSessionID(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := retention.DropSession(repo, sessionID, dryRun)
			if err != nil {
				return err
			}
			prefix := "dropped"
			if dryRun {
				prefix = "would drop"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s session %s: %d refs, %d files\n", prefix, sessionID, len(result.DeletedRefs), len(result.DeletedFiles))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without deleting it")
	return cmd
}

func retentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Prune retained private refs",
	}
	cmd.AddCommand(retentionPruneCmd())
	return cmd
}

func retentionPruneCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "prune",
		Short:        "Delete private refs not referenced by durable records",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := retention.PruneOrphanRefs(repo, dryRun)
			if err != nil {
				return err
			}
			prefix := "pruned"
			if dryRun {
				prefix = "would prune"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %d private refs\n", prefix, len(result.DeletedRefs))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pruned without deleting it")
	return cmd
}

func maintenanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Run explicit maintenance tasks",
	}
	cmd.AddCommand(maintenanceGCCmd())
	return cmd
}

func maintenanceGCCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "gc",
		Short:        "Run hidden Git garbage collection after ref deletion",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if _, err := retention.RunHiddenGitGC(repo, dryRun); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "would run hidden git reflog expire and gc --prune=now")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ran hidden git reflog expire and gc --prune=now")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the GC policy without running Git GC")
	return cmd
}
