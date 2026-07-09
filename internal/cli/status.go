package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/importer"
	"github.com/AadiJo/turnal/internal/integrity"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show turnal workspace status",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			root, err := checkpoint.FindRoot(cwd)
			var rootErr error
			if err != nil {
				rootErr = err
				root, err = primitives.ParseWorkspaceRoot(cwd)
				if err != nil {
					return rootErr
				}
			}

			status := checkpoint.Inspect(root)
			if rootErr != nil {
				status.Problems = append([]string{rootErr.Error()}, status.Problems...)
			} else {
				repo, openErr := checkpoint.Open(root)
				if openErr != nil {
					status.Problems = append(status.Problems, openErr.Error())
				} else {
					report := integrity.Inspect(repo)
					status.Problems = append(status.Problems, report.Problems...)
					pending, pendingErr := importer.Pending(repo)
					if pendingErr != nil {
						status.Problems = append(status.Problems, pendingErr.Error())
					} else {
						for _, journal := range pending {
							status.Problems = append(status.Problems, fmt.Sprintf("import journal pending: %s state=%s; run turnal merge --recover or turnal merge --abort", journal.ImportID, journal.State))
						}
					}
				}
			}
			effective, _, configErr := agentconfig.ResolvePath(filepath.Join(status.MetadataDir, "config.toml"), agentconfig.Overrides{})
			if configErr != nil {
				status.Problems = append(status.Problems, configErr.Error())
			}
			var hookHealth []adapters.HookHealth
			hooksOK := false
			if configErr == nil {
				hooksOK = true
				if effective.Init.InstallHooks {
					targets, err := adapters.ResolveTargets(root.String(), adapters.Target(effective.Init.Agent))
					if err != nil {
						hooksOK = false
						status.Problems = append(status.Problems, err.Error())
					} else {
						hookHealth = adapters.InspectHooksForTargets(root.String(), effective.Hooks.Command, targets)
						for _, health := range hookHealth {
							if !health.OK() {
								hooksOK = false
								status.Problems = append(status.Problems, health.Problems...)
							}
						}
					}
				}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "workspace: %s\n", status.WorkspaceRoot)
			fmt.Fprintf(out, "metadata:  %s\n", status.MetadataDir)
			fmt.Fprintf(out, "repo id:   %s\n", status.RepoID)
			fmt.Fprintf(out, "store id:  %s\n", status.StoreID)
			fmt.Fprintf(out, "worktree:  %s\n", status.WorktreeID)
			fmt.Fprintf(out, "attached:  %t\n", status.Attached)
			fmt.Fprintf(out, "hidden git: %s\n", status.GitDir)
			fmt.Fprintf(out, "version:    %s\n", status.Version)
			fmt.Fprintf(out, "gitignore:  %s\n", status.GitignorePath)
			if configErr == nil {
				fmt.Fprintf(out, "git-sync:   %t\n", effective.GitSync.Enabled)
				fmt.Fprintf(out, "rollback:   %s\n", effective.Rollback.Mode)
				if hooksOK {
					fmt.Fprintln(out, "hooks:      ok")
				} else {
					fmt.Fprintln(out, "hooks:      needs attention")
				}
			}
			fmt.Fprintf(out, "state:      ")
			if status.OK() {
				fmt.Fprintln(out, "ok")
				return nil
			}

			fmt.Fprintln(out, "needs attention")
			for _, problem := range status.Problems {
				fmt.Fprintf(out, "- %s\n", problem)
			}
			return fmt.Errorf("turnal workspace has %d problem(s)", len(status.Problems))
		},
	}
}
