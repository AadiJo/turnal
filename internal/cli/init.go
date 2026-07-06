package cli

import (
	"fmt"
	"os"

	"agent-vcs-again/internal/adapters"
	"agent-vcs-again/internal/checkpoint"
	agentconfig "agent-vcs-again/internal/config"
	"agent-vcs-again/internal/primitives"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var agent string
	var skipHooks bool
	var enableGitSync bool

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

			overrides := agentconfig.Overrides{}
			if cmd.Flags().Changed("agent") {
				overrides.InitAgent = &agent
			}
			if cmd.Flags().Changed("skip-hooks") {
				installHooks := !skipHooks
				overrides.InitInstallHooks = &installHooks
			}
			if cmd.Flags().Changed("git-sync") {
				overrides.GitSyncEnabled = &enableGitSync
			}
			effective, _, err := agentconfig.Resolve(root.String(), overrides)
			if err != nil {
				return err
			}

			result, err := checkpoint.BootstrapWithOptions(root, checkpoint.BootstrapOptions{
				InitWorkspaceGit: effective.Bootstrap.InitWorkspaceGit,
				UpdateGitignore:  effective.Bootstrap.UpdateGitignore,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized hidden git repo: %s\n", result.Repo.GitDir)
			if effective.Bootstrap.InitWorkspaceGit {
				if result.WorkspaceGitInitialized {
					fmt.Fprintf(cmd.OutOrStdout(), "initialized workspace git repo: %s\n", result.WorkspaceGitPath)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "workspace git already configured: %s\n", result.WorkspaceGitPath)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "workspace git skipped")
			}
			if effective.Bootstrap.UpdateGitignore {
				if result.GitignoreUpdated {
					fmt.Fprintf(cmd.OutOrStdout(), "updated gitignore: %s\n", result.GitignorePath)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "gitignore already configured: %s\n", result.GitignorePath)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "gitignore update skipped")
			}
			if effective.GitSync.Enabled {
				if err := enableWorkspaceGitSync(root.String()); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "enabled git-sync capture: %s\n", agentconfig.WorkspacePath(root.String()))
			}

			targets, err := adapters.ResolveTargets(root.String(), adapters.Target(effective.Init.Agent))
			if err != nil {
				return err
			}
			if !effective.Init.InstallHooks || len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "adapter hooks skipped")
				return nil
			}

			installed, err := adapters.InstallWithOptions(root.String(), targets, adapters.InstallOptions{
				HookCommand: effective.Hooks.Command,
			})
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
	cmd.Flags().BoolVar(&enableGitSync, "git-sync", false, "Enable opt-in workspace Git state capture for future workspace-git rollbacks")
	return cmd
}

func enableWorkspaceGitSync(root string) error {
	path := agentconfig.WorkspacePath(root)
	file, err := agentconfig.ReadFileLayer(path)
	if err != nil {
		return err
	}
	version := 1
	enabled := true
	file.Version = &version
	file.GitSync = &agentconfig.GitSyncFile{Enabled: &enabled}
	data, err := toml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal workspace config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write workspace config %s: %w", path, err)
	}
	return nil
}
