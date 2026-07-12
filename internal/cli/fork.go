package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	forkengine "github.com/AadiJo/turnal/internal/fork"
	"github.com/spf13/cobra"
)

func forkCmd() *cobra.Command {
	var dryRun bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:          "fork <session:turn>",
		Short:        "Inspect whether a recorded turn can be rerun",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun {
				return fmt.Errorf("fork execution is not implemented; pass --dry-run to inspect readiness")
			}
			sessionID, turnID, err := parseTurnTarget(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			report, err := forkengine.NewAnalyzer(repo).Inspect(sessionID, turnID)
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			return writeForkReadiness(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Inspect fork readiness without creating files or running an agent")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func writeForkReadiness(writer io.Writer, report forkengine.Report) error {
	status := "incomplete"
	if report.Source.Complete {
		status = "complete"
	}
	if _, err := fmt.Fprintf(writer,
		"fork readiness: %s\n"+
			"target:         %s\n"+
			"fidelity:       %s\n"+
			"source turn:    %s:%s (%s)\n",
		report.Readiness,
		report.Target,
		report.FidelityLevel,
		report.Source.SessionID,
		report.Source.TurnID,
		status,
	); err != nil {
		return err
	}

	if len(report.Source.Adapters) > 0 {
		adapters := make([]string, 0, len(report.Source.Adapters))
		for _, adapter := range report.Source.Adapters {
			adapters = append(adapters, adapter.String())
		}
		if _, err := fmt.Fprintf(writer, "adapter:        %s\n", strings.Join(adapters, ", ")); err != nil {
			return err
		}
	}
	if report.Source.Model != "" {
		if _, err := fmt.Fprintf(writer, "model:          %s\n", report.Source.Model); err != nil {
			return err
		}
	}
	if report.Source.PermissionMode != "" {
		if _, err := fmt.Fprintf(writer, "permissions:    %s\n", report.Source.PermissionMode); err != nil {
			return err
		}
	}

	if report.Base.Status == "available" {
		if _, err := fmt.Fprintf(writer,
			"base:           %s\n"+
				"commit:         %s\n"+
				"captured files: %d\n",
			report.Base.Ref,
			report.Base.CommitSHA,
			report.Base.CapturedFiles,
		); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(writer, "base:           missing pre-turn checkpoint"); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(writer, "instruction:    %s\n", report.Instruction.Status); err != nil {
		return err
	}
	if report.Instruction.Text != "" {
		if _, err := fmt.Fprintf(writer, "  %s\n", indentForkText(report.Instruction.Text, "  ")); err != nil {
			return err
		}
	}

	conditions := []struct {
		name      string
		condition forkengine.Condition
	}{
		{name: "workspace files", condition: report.Conditions.WorkspaceFiles},
		{name: "workspace VCS", condition: report.Conditions.WorkspaceVCS},
		{name: "conversation", condition: report.Conditions.ConversationContext},
		{name: "toolchain", condition: report.Conditions.Toolchain},
		{name: "secrets", condition: report.Conditions.Secrets},
		{name: "network", condition: report.Conditions.Network},
		{name: "evaluators", condition: report.Conditions.Evaluators},
	}
	if _, err := fmt.Fprintln(writer, "conditions:"); err != nil {
		return err
	}
	for _, item := range conditions {
		if _, err := fmt.Fprintf(writer, "  %-16s %-24s %s\n", item.name, item.condition.Status, item.condition.Detail); err != nil {
			return err
		}
	}
	if len(report.Limitations) > 0 {
		if _, err := fmt.Fprintln(writer, "limitations:"); err != nil {
			return err
		}
		for _, limitation := range report.Limitations {
			if _, err := fmt.Fprintf(writer, "  - %s\n", limitation); err != nil {
				return err
			}
		}
	}
	return nil
}

func indentForkText(value, prefix string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n"+prefix)
}
