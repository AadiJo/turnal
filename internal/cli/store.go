package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func storeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Manage physical Turnal store identity",
	}
	cmd.AddCommand(storeRekeyCmd())
	return cmd
}

func storeRekeyCmd() *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:          "rekey",
		Short:        "Assign new store, worktree, and producer identities after copying .turnal",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return fmt.Errorf("store rekey changes durable identity; rerun with --confirm after verifying this is a copied store")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := repo.RekeyStore()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rekeyed store: %s -> %s\n", result.OldStoreID, result.NewStoreID)
			fmt.Fprintf(cmd.OutOrStdout(), "rekeyed worktrees: %d\n", result.Worktrees)
			fmt.Fprintln(cmd.OutOrStdout(), "existing events and checkpoint commits were not rewritten")
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm this store is a copy that needs independent future identity")
	return cmd
}
