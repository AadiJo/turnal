package cli

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/importer"
	"github.com/spf13/cobra"
)

func mergeCmd() *cobra.Command {
	var dryRun bool
	var adoptRepo bool
	var recoverImport bool
	var abortImport bool

	cmd := &cobra.Command{
		Use:          "merge [path-to-store]",
		Short:        "Import immutable history from another Turnal store",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if recoverImport && abortImport {
				return fmt.Errorf("--recover and --abort cannot be combined")
			}
			if (recoverImport || abortImport) && (len(args) != 0 || dryRun || adoptRepo) {
				return fmt.Errorf("--recover and --abort do not accept a source path or merge options")
			}
			if !recoverImport && !abortImport && len(args) != 1 {
				return fmt.Errorf("source store path is required")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if abortImport {
				if err := importer.Abort(repo); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "aborted pending import")
				return nil
			}
			var result importer.Result
			if recoverImport {
				result, err = importer.Recover(repo)
			} else {
				result, err = importer.Run(repo, args[0], importer.Options{
					DryRun:                   dryRun,
					AdoptSourceAsCurrentRepo: adoptRepo,
				})
			}
			if err != nil {
				return err
			}
			writeMergeResult(cmd, result)
			if result.IndexError != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: import is durable but index rebuild failed: %v; run turnal reindex\n", result.IndexError)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Verify and report the import without modifying the destination")
	cmd.Flags().BoolVar(&adoptRepo, "adopt-source-as-current-repo", false, "Assert that a source with a different repo id is the same logical project")
	cmd.Flags().BoolVar(&recoverImport, "recover", false, "Resume the one pending import journal")
	cmd.Flags().BoolVar(&abortImport, "abort", false, "Remove staging data for the one pending import journal")
	return cmd
}

func writeMergeResult(cmd *cobra.Command, result importer.Result) {
	plan := result.Plan
	title := "import complete"
	if plan.DryRun {
		title = "dry-run import"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", title, plan.ImportID)
	fmt.Fprintf(cmd.OutOrStdout(), "source store: %s (%s)\n", plan.SourceStoreID, plan.SourcePath)
	fmt.Fprintf(cmd.OutOrStdout(), "source repo: %s\n", plan.SourceRepoID)
	fmt.Fprintf(cmd.OutOrStdout(), "destination store: %s\n", plan.DestinationID)
	fmt.Fprintf(cmd.OutOrStdout(), "repo adoption asserted: %t\n", plan.AdoptedRepo)
	fmt.Fprintf(cmd.OutOrStdout(), "streams: %d (%d duplicate)\n", len(plan.Streams), plan.Duplicates)
	fmt.Fprintf(cmd.OutOrStdout(), "events: %d\n", mergeEventCount(plan.Streams))
	fmt.Fprintf(cmd.OutOrStdout(), "checkpoints: %d\n", plan.Checkpoints)
	fmt.Fprintf(cmd.OutOrStdout(), "refs: %d\n", plan.Refs)
	fmt.Fprintf(cmd.OutOrStdout(), "bytes: %d\n", plan.Bytes)
	for _, stream := range plan.Streams {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s  session=%s worktree=%s events=%d bytes=%d status=%s\n", stream.StreamID, stream.SessionID, stream.WorktreeID, stream.Events, stream.Bytes, stream.Status)
	}
	if result.Manifest != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "manifest: %s\n", result.Manifest)
	}
}

func mergeEventCount(streams []importer.StreamPlan) int {
	total := 0
	for _, stream := range streams {
		total += stream.Events
	}
	return total
}
