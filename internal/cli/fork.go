package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	caseengine "github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	experimentengine "github.com/AadiJo/turnal/internal/experiments"
	forkengine "github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

type forkExecutionJSON struct {
	Version     int                     `json:"version"`
	CaseCreated bool                    `json:"case_created"`
	Result      experimentengine.Result `json:"result"`
}

func forkCmd() *cobra.Command {
	var dryRun bool
	var jsonOutput bool
	var keep bool
	var noReplayInstruction bool

	cmd := &cobra.Command{
		Use:          "fork <session:turn|case-id> [-- <command...>]",
		Short:        "Rerun a recorded task from its historical workspace",
		SilenceUsage: true,
		Args: func(command *cobra.Command, args []string) error {
			if dryRun {
				return cobra.ExactArgs(1)(command, args)
			}
			return cobra.MinimumNArgs(2)(command, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return runForkDryRun(cmd, args[0], jsonOutput)
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			definition, created, err := resolveForkCase(repo, args[0])
			if err != nil {
				return err
			}
			command, err := prepareForkExecutionCommand(args[1:], definition, noReplayInstruction)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			childStdout := cmd.OutOrStdout()
			if jsonOutput {
				childStdout = cmd.ErrOrStderr()
			}
			result, executeErr := experimentengine.Execute(ctx, repo, experimentengine.Request{
				Case: definition, Command: command, Keep: keep,
				Runner: experimentengine.ExecRunner{Stdin: os.Stdin, Stdout: childStdout, Stderr: cmd.ErrOrStderr()},
			})
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(forkExecutionJSON{Version: caseengine.JSONVersion, CaseCreated: created, Result: result}); err != nil {
					return err
				}
			} else {
				if err := writeForkExecution(cmd.OutOrStdout(), result, created); err != nil {
					return err
				}
			}
			if executeErr != nil {
				return executeErr
			}
			if result.Status == caseengine.AttemptStatusFailed && result.ExitCode != nil {
				return childExitError{code: *result.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Inspect fork readiness without creating files or running an agent")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&keep, "keep", false, "Keep the isolated attempt workspace after execution")
	cmd.Flags().BoolVar(&noReplayInstruction, "no-replay-instruction", false, "Do not append the captured instruction to a bare codex or codex exec command")
	return cmd
}

func runForkDryRun(cmd *cobra.Command, target string, jsonOutput bool) error {
	repo, err := openCheckpointRepoReadOnly()
	if err != nil {
		return err
	}
	var report forkengine.Report
	if strings.HasPrefix(strings.TrimSpace(target), "case_") {
		caseID, err := primitives.ParseCaseID(target)
		if err != nil {
			return err
		}
		projection, err := caseengine.Rebuild(repo)
		if err != nil {
			return err
		}
		definition, ok := projection.Case(caseID)
		if !ok {
			return fmt.Errorf("case %s does not exist in this Turnal store", caseID)
		}
		report = definition.Readiness
	} else {
		sessionID, turnID, err := parseTurnTarget(target)
		if err != nil {
			return err
		}
		report, err = forkengine.NewAnalyzer(repo).Inspect(sessionID, turnID)
		if err != nil {
			return err
		}
	}
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return writeForkReadiness(cmd.OutOrStdout(), report)
}

func resolveForkCase(checkpointRepo *checkpoint.Repo, target string) (caseengine.Case, bool, error) {
	if strings.HasPrefix(strings.TrimSpace(target), "case_") {
		caseID, err := primitives.ParseCaseID(target)
		if err != nil {
			return caseengine.Case{}, false, err
		}
		projection, err := caseengine.Rebuild(checkpointRepo)
		if err != nil {
			return caseengine.Case{}, false, err
		}
		definition, exists := projection.Case(caseID)
		if !exists {
			return caseengine.Case{}, false, fmt.Errorf("case %s does not exist in this Turnal store", caseID)
		}
		return definition, false, nil
	}
	sessionID, turnID, err := parseTurnTarget(target)
	if err != nil {
		return caseengine.Case{}, false, err
	}
	projection, err := caseengine.Rebuild(checkpointRepo)
	if err != nil {
		return caseengine.Case{}, false, err
	}
	var matches []caseengine.Case
	for _, definition := range projection.Cases {
		if definition.Source.SessionID == sessionID && definition.Source.TurnID == turnID && definition.Scope.WorktreeID == checkpointRepo.WorktreeID {
			matches = append(matches, definition)
		}
	}
	if len(matches) > 1 {
		return caseengine.Case{}, false, fmt.Errorf("source turn %s:%s has multiple cases; rerun with an explicit case id", sessionID, turnID)
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}
	created, err := caseengine.Create(checkpointRepo, caseengine.CreateRequest{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		return caseengine.Case{}, false, err
	}
	return created.Case, true, nil
}

func prepareForkExecutionCommand(command []string, definition caseengine.Case, noReplay bool) ([]string, error) {
	prepared := append([]string(nil), command...)
	if noReplay || len(prepared) == 0 || !isCodexCommand(prepared[0]) {
		return prepared, nil
	}
	bare := len(prepared) == 1 || (len(prepared) == 2 && prepared[1] == "exec")
	if !bare {
		return prepared, nil
	}
	if definition.Readiness.Instruction.Status != forkengine.InstructionAvailable || strings.TrimSpace(definition.Readiness.Instruction.Text) == "" {
		return nil, fmt.Errorf("case %s has no captured instruction; provide the command's prompt explicitly or pass --no-replay-instruction", definition.ID)
	}
	return append(prepared, definition.Readiness.Instruction.Text), nil
}

func writeForkExecution(writer io.Writer, result experimentengine.Result, caseCreated bool) error {
	if caseCreated {
		if _, err := fmt.Fprintf(writer, "created case:   %s\n", result.CaseID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer,
		"case:           %s\n"+
			"attempt:        %s\n"+
			"run:            %s\n"+
			"status:         %s\n"+
			"base commit:    %s\n"+
			"result commit:  %s\n",
		result.CaseID, result.AttemptID, result.RunID, result.Status, result.BaseCommit, result.PostCommit,
	); err != nil {
		return err
	}
	if result.ExitCode != nil {
		if _, err := fmt.Fprintf(writer, "exit code:      %d\n", *result.ExitCode); err != nil {
			return err
		}
	}
	if result.Error != "" {
		if _, err := fmt.Fprintf(writer, "error:          %s\n", result.Error); err != nil {
			return err
		}
	}
	if result.Verification != nil {
		if _, err := fmt.Fprintf(writer, "verification:   %s (%d passed, %d failed)\n", result.Verification.Summary.Outcome, result.Verification.Summary.Passed, result.Verification.Summary.Failed+result.Verification.Summary.TimedOut+result.Verification.Summary.LaunchError+result.Verification.Summary.InfrastructureErrors); err != nil {
			return err
		}
	}
	if result.WorkspaceKept {
		_, err := fmt.Fprintf(writer, "workspace:      %s (kept)\n", result.Workspace)
		return err
	}
	_, err := fmt.Fprintln(writer, "workspace:      removed; source workspace unchanged")
	return err
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
	if report.Source.MetadataAdapter != "" {
		if _, err := fmt.Fprintf(writer, "metadata adapter: %s\n", report.Source.MetadataAdapter); err != nil {
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
