package cli

import (
	"fmt"

	queryindex "agent-vcs-again/internal/index"
	"github.com/spf13/cobra"
)

func reindexCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:          "reindex",
		Short:        "Rebuild the disposable SQLite query index",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			stats, err := queryindex.Rebuild(repo)
			if err != nil {
				return err
			}
			if !quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "reindexed %s: %d sessions, %d turns, %d events, %d checkpoints, %d file touches\n",
					stats.DBPath,
					stats.Sessions,
					stats.Turns,
					stats.Events,
					stats.Checkpoints,
					stats.FileTouches,
				)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress rebuild summary output")
	return cmd
}
