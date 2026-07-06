package cli

import (
	"fmt"
	"os"

	"agent-vcs-again/internal/adapters"
	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var agent string
	var skipHooks bool

	cmd := &cobra.Command{
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

			targets, err := adapters.ResolveTargets(root.String(), adapters.Target(agent))
			if err != nil {
				return err
			}
			if skipHooks || len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "adapter hooks skipped")
				return nil
			}

			installed, err := adapters.Install(root.String(), targets)
			if err != nil {
				return err
			}
			for _, adapter := range installed {
				fmt.Fprintf(cmd.OutOrStdout(), "configured %s hooks: %s\n", adapter.Target, adapter.ConfigPath)
				if adapter.BackupPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "backed up invalid hook config: %s\n", adapter.BackupPath)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agent, "agent", string(adapters.TargetAuto), "Agent hooks to configure: auto, claude, codex, all, or none")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip automatic agent hook configuration")
	return cmd
}
