package cli

import (
	"fmt"
	"os"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "init",
		Short:        "Initialize agent-vcs metadata in this workspace",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			root, err := primitives.ParseWorkspaceRoot(cwd)
			if err != nil {
				return err
			}
			result, err := checkpoint.Bootstrap(root)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized hidden git repo: %s\n", result.Repo.GitDir)
			if result.GitignoreUpdated {
				fmt.Fprintf(cmd.OutOrStdout(), "updated gitignore: %s\n", result.GitignorePath)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "gitignore already configured: %s\n", result.GitignorePath)
			}
			return nil
		},
	}
}
