package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/compatibility"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/importer"
	"github.com/AadiJo/turnal/internal/integrity"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	probe := compatibility.DefaultAppServerProbe()
	return statusCmdWithProbe(probe)
}

func statusCmdWithProbe(codexProbe compatibility.CodexProbe) *cobra.Command {
	var probeAgentCapture bool
	cmd := &cobra.Command{
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
				repo, openErr := checkpoint.OpenReadOnly(root)
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
			var targets []adapters.Target
			hooksOK := false
			if configErr == nil {
				hooksOK = true
				if effective.Init.InstallHooks {
					targets, err = adapters.ResolveTargets(root.String(), adapters.Target(effective.Init.Agent))
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
			var captureReport compatibility.Report
			if probeAgentCapture && configErr == nil && effective.Init.InstallHooks {
				captureReport = compatibility.Diagnose(context.Background(), compatibility.Options{
					WorkspaceRoot: root.String(),
					HookCommand:   effective.Hooks.Command,
					Targets:       targets,
					ProbeCodex:    true,
					CodexProbe:    codexProbe,
				})
				for _, surface := range captureReport.Surfaces {
					if surface.ProbeError != "" {
						status.Problems = append(status.Problems, fmt.Sprintf("%s probe failed: %s", surface.Surface, surface.ProbeError))
						continue
					}
					if surface.Configuration == adapters.HookConfigurationConfigured && surface.Expectation == compatibility.CaptureUnavailable {
						status.Problems = append(status.Problems, fmt.Sprintf("%s capture unavailable: execution is %s", surface.Surface, surface.Execution))
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
			if probeAgentCapture {
				if !effective.Init.InstallHooks {
					fmt.Fprintln(out, "\nAgent capture compatibility\n  probe skipped: hook installation is disabled in Turnal configuration")
				} else {
					printCaptureCompatibility(out, captureReport)
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
	cmd.Flags().BoolVar(&probeAgentCapture, "probe-agent-capture", false, "Probe capture compatibility for configured agent execution surfaces")
	return cmd
}

func printCaptureCompatibility(out interface{ Write([]byte) (int, error) }, report compatibility.Report) {
	fmt.Fprintln(out, "\nAgent capture compatibility")
	for _, surface := range report.Surfaces {
		fmt.Fprintf(out, "\n%s\n", surface.Surface)
		fmt.Fprintf(out, "  project hooks:       %s\n", compatibility.ConfigurationSummary(surface.Configuration))
		if surface.Surface == compatibility.SurfaceCodexAppServer {
			if surface.ProbeError != "" {
				fmt.Fprintln(out, "  runtime probe:       unavailable")
				fmt.Fprintf(out, "  probe failure:       %s\n", surface.ProbeError)
			} else {
				fmt.Fprintf(out, "  discovery:           %d/%d Turnal hooks\n", surface.Discovered, surface.Expected)
				fmt.Fprintf(out, "  enabled:             %d/%d\n", surface.Enabled, surface.Expected)
				fmt.Fprintf(out, "  trusted:             %d/%d\n", surface.Trusted, surface.Expected)
			}
		} else {
			fmt.Fprintf(out, "  runtime visibility:  %s\n", surface.Visibility)
		}
		fmt.Fprintf(out, "  expected capture:    %s\n", surface.Expectation)
		fmt.Fprintf(out, "  certainty:           %s\n", surface.Certainty)
		for _, warning := range surface.Warnings {
			fmt.Fprintf(out, "  warning:             %s\n", warning)
		}
		for _, limitation := range surface.Limitations {
			fmt.Fprintf(out, "  limitation:          %s\n", limitation)
		}
		for _, guidance := range surface.Guidance {
			fmt.Fprintf(out, "  guidance:            %s\n", guidance)
		}
	}
}
