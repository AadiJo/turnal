package cli

import (
	"fmt"
	"os"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/destroy"
	"github.com/spf13/cobra"
)

func destroyCmd() *cobra.Command {
	var dryRun bool
	var removeHooks bool
	var agent string

	cmd := &cobra.Command{
		Use:          "destroy",
		Short:        "Remove all turnal metadata from this workspace",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			var hookTargets []adapters.Target
			hookTargetsSet := false
			if removeHooks && cmd.Flags().Changed("agent") {
				root, _, err := destroy.FindRoot(cwd)
				if err != nil {
					return err
				}
				hookTargets, err = adapters.ResolveTargets(root.String(), adapters.Target(agent))
				if err != nil {
					return err
				}
				hookTargetsSet = true
			}

			result, err := destroy.Run(cwd, destroy.Options{
				DryRun:         dryRun,
				RemoveHooks:    removeHooks,
				HookTargets:    hookTargets,
				HookTargetsSet: hookTargetsSet,
			})
			if err != nil {
				return err
			}

			for _, hookResult := range result.HookResults {
				printDestroyHookResult(cmd, hookResult)
			}

			prefix := "removed"
			if dryRun {
				prefix = "would remove"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s metadata: %s\n", prefix, result.MetadataDir)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without deleting it")
	cmd.Flags().BoolVar(&removeHooks, "remove-hooks", false, "Remove turnal commands from supported agent hook configs")
	cmd.Flags().StringVar(&agent, "agent", string(adapters.TargetAuto), "Agent hooks to remove: auto, claude, codex, cursor, pi, all, or none")
	return cmd
}

func printDestroyHookResult(cmd *cobra.Command, result adapters.UninstallResult) {
	prefix := "removed"
	if result.DryRun {
		prefix = "would remove"
	}
	if !result.ConfigExists {
		fmt.Fprintf(cmd.OutOrStdout(), "no %s hook config: %s\n", result.Target, result.ConfigPath)
		return
	}
	if result.RemovedCommands == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no turnal %s hooks: %s\n", result.Target, result.ConfigPath)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s hooks: %s (%d commands)\n", prefix, result.Target, result.ConfigPath, result.RemovedCommands)
}
