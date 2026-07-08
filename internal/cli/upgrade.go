package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

var newUpgradeRegistry = func() upgrade.Registry {
	return upgrade.NPMRegistry{}
}

var runUpgradeCommand = executeUpgradeCommand

func upgradeCmd() *cobra.Command {
	var checkOnly bool
	var dryRun bool
	var yes bool
	var stable bool
	var nightly bool
	var jsonOutput bool
	var exitCode bool

	cmd := &cobra.Command{
		Use:          "upgrade",
		Aliases:      []string{"update"},
		Short:        "Upgrade Turnal",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stable && nightly {
				fmt.Fprintln(cmd.ErrOrStderr(), "--stable and --nightly cannot be used together")
				return commandExitError{code: 2}
			}

			requestedChannel := ""
			if stable {
				requestedChannel = upgrade.ChannelStable
			}
			if nightly {
				requestedChannel = upgrade.ChannelNightly
			}

			plan, err := upgrade.BuildPlan(context.Background(), upgrade.PlanOptions{
				Current:          currentBuildMetadata(),
				RequestedChannel: requestedChannel,
				Registry:         newUpgradeRegistry(),
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				if err := writeUpgradeJSON(cmd.OutOrStdout(), plan); err != nil {
					return err
				}
				return finishUpgradePlan(cmd, plan, upgradeRunOptions{
					CheckOnly: checkOnly,
					DryRun:    dryRun,
					Yes:       yes,
					ExitCode:  exitCode,
					JSON:      true,
				})
			}

			if err := writeUpgradeHuman(cmd.OutOrStdout(), plan); err != nil {
				return err
			}
			return finishUpgradePlan(cmd, plan, upgradeRunOptions{
				CheckOnly: checkOnly,
				DryRun:    dryRun,
				Yes:       yes,
				ExitCode:  exitCode,
			})
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Check for an upgrade without installing it")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the planned upgrade without installing it")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm channel switches or downgrades")
	cmd.Flags().BoolVar(&stable, "stable", false, "Switch to the stable channel")
	cmd.Flags().BoolVar(&nightly, "nightly", false, "Switch to the nightly channel")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "With --check, exit 3 when a newer version is available")
	return cmd
}

type upgradeRunOptions struct {
	CheckOnly bool
	DryRun    bool
	Yes       bool
	ExitCode  bool
	JSON      bool
}

func finishUpgradePlan(cmd *cobra.Command, plan upgrade.Plan, opts upgradeRunOptions) error {
	if opts.CheckOnly {
		if opts.ExitCode && plan.UpdateAvailable {
			return commandExitError{code: 3}
		}
		return nil
	}
	if opts.DryRun || plan.UpToDate {
		return nil
	}
	manualInstructionsShown := false
	if plan.Action.RequiresConfirmation && !opts.Yes {
		if opts.JSON {
			return commandExitError{code: 4}
		}
		if err := writeUpgradeConfirmation(cmd, plan); err != nil {
			return err
		}
		manualInstructionsShown = plan.Action.Kind == upgrade.ActionManual
		confirmed, err := confirmUpgrade(cmd)
		if err != nil {
			return err
		}
		if !confirmed {
			return commandExitError{code: 4}
		}
	}
	if plan.Action.Kind == upgrade.ActionManual {
		if opts.JSON {
			return nil
		}
		if manualInstructionsShown {
			return nil
		}
		return writeManualUpgradeInstructions(cmd.OutOrStdout(), plan)
	}
	if plan.Action.Kind != upgrade.ActionNPMInstallGlobal {
		return nil
	}

	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	if opts.JSON {
		stdout = stderr
	}
	if err := runUpgradeCommand(context.Background(), plan.Action.Command, stdout, stderr); err != nil {
		return err
	}
	return nil
}

func writeUpgradeJSON(w io.Writer, plan upgrade.Plan) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func writeUpgradeHuman(w io.Writer, plan upgrade.Plan) error {
	if _, err := fmt.Fprintln(w, "Turnal upgrade"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "current: %s %s\n", plan.Current.Version, plan.Current.Channel); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "target:  %s %s\n", plan.Target.Version, plan.Target.Channel); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "source:  %s\n", plan.Current.InstallSource); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "action:  %s\n", upgradeActionText(plan))
	return err
}

func upgradeActionText(plan upgrade.Plan) string {
	switch plan.Action.Kind {
	case upgrade.ActionNone:
		return "already up to date"
	case upgrade.ActionNPMInstallGlobal:
		return strings.Join(plan.Action.Command, " ")
	case upgrade.ActionManual:
		return "manual update"
	default:
		return plan.Action.Kind
	}
}

func writeUpgradeConfirmation(cmd *cobra.Command, plan upgrade.Plan) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}

	channelSwitch := upgradeReason(plan, "channel_switch")
	targetOlder := upgradeReason(plan, "target_older")
	switch {
	case channelSwitch && targetOlder:
		if _, err := fmt.Fprintf(out, "This switches from %s to %s and will install an older version.\n", plan.Current.Channel, plan.Target.Channel); err != nil {
			return err
		}
	case channelSwitch:
		if _, err := fmt.Fprintf(out, "This switches from %s to %s.\n", plan.Current.Channel, plan.Target.Channel); err != nil {
			return err
		}
	case targetOlder:
		if _, err := fmt.Fprintln(out, "This will install an older version."); err != nil {
			return err
		}
	}
	if channelSwitch && plan.Target.Channel == upgrade.ChannelNightly {
		if _, err := fmt.Fprintln(out, "Nightly builds may be less stable than releases."); err != nil {
			return err
		}
	}
	if upgradeReason(plan, "manual_install_source") {
		if err := writeManualUpgradeInstructions(out, plan); err != nil {
			return err
		}
	}
	if !canPrompt(cmd.InOrStdin()) {
		_, err := fmt.Fprintln(out, "Run again with --yes to continue.")
		return err
	}
	return nil
}

func upgradeReason(plan upgrade.Plan, reason string) bool {
	for _, candidate := range plan.Action.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

func writeManualUpgradeInstructions(w io.Writer, plan upgrade.Plan) error {
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Turnal was not installed through a known package manager."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Current version: %s\n", plan.Current.Version); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Target channel: %s\n", plan.Target.Channel); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Update manually:"); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  %s\n", strings.Join(plan.Action.Command, " "))
	return err
}

func confirmUpgrade(cmd *cobra.Command) (bool, error) {
	in := cmd.InOrStdin()
	if !canPrompt(in) {
		return false, nil
	}
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), "Continue? [y/N] "); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func canPrompt(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

func executeUpgradeCommand(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
	if len(command) == 0 {
		return fmt.Errorf("upgrade command is empty")
	}
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = stdout
	child.Stderr = stderr
	child.Env = os.Environ()
	return child.Run()
}
