package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func worktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Inspect and manage attached Git worktrees",
	}
	cmd.AddCommand(worktreeListCmd())
	cmd.AddCommand(worktreeAttachCmd())
	cmd.AddCommand(worktreeRepairCmd())
	return cmd
}

func worktreeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List worktrees attached to this Turnal store",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			worktrees, err := repo.ListWorktrees()
			if err != nil {
				return err
			}
			sort.Slice(worktrees, func(i, j int) bool {
				if worktrees[i].Primary != worktrees[j].Primary {
					return worktrees[i].Primary
				}
				return worktrees[i].Root < worktrees[j].Root
			})
			for _, worktree := range worktrees {
				marker := " "
				if worktree.WorktreeID == repo.WorktreeID {
					marker = "*"
				}
				role := "linked"
				if worktree.Primary {
					role = "primary"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s  %s  %s\n", marker, worktree.WorktreeID, role, worktree.Root)
			}
			return nil
		},
	}
}

func worktreeAttachCmd() *cobra.Command {
	var storePath string
	cmd := &cobra.Command{
		Use:          "attach --store <path-to-.turnal>",
		Short:        "Attach the current Git worktree to an existing Turnal store",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if storePath == "" {
				return fmt.Errorf("--store is required")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			root, err := primitives.ParseWorkspaceRoot(cwd)
			if err != nil {
				return err
			}
			absoluteStore, err := filepath.Abs(storePath)
			if err != nil {
				return fmt.Errorf("resolve store path: %w", err)
			}
			repo, err := checkpoint.OpenAt(root, absoluteStore)
			if err != nil {
				return err
			}
			if err := repo.RepairRegistration(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "attached turnal store: %s\n", repo.MetadataDir)
			fmt.Fprintf(cmd.OutOrStdout(), "worktree: %s\n", repo.WorkspaceRoot)
			fmt.Fprintf(cmd.OutOrStdout(), "worktree id: %s\n", repo.WorktreeID)
			return nil
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "Existing .turnal store path")
	return cmd
}

func worktreeRepairCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "repair",
		Short:        "Refresh worktree bindings and the user-state registry",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := repo.RepairRegistration(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "repaired worktree registration: %s\n", repo.WorktreeID)
			fmt.Fprintf(cmd.OutOrStdout(), "store: %s\n", repo.MetadataDir)
			return nil
		},
	}
}
