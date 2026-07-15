package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	caseengine "github.com/AadiJo/turnal/internal/cases"
	experimentengine "github.com/AadiJo/turnal/internal/experiments"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

type caseJSON struct {
	Version int             `json:"version"`
	Case    caseengine.Case `json:"case"`
}

type caseCreateJSON struct {
	Version     int             `json:"version"`
	TaskCreated bool            `json:"task_created"`
	Task        caseengine.Task `json:"task"`
	Case        caseengine.Case `json:"case"`
}

func caseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "case", Short: "Create and inspect immutable experimental cases"}
	cmd.AddCommand(caseCreateCmd())
	cmd.AddCommand(caseShowCmd())
	cmd.AddCommand(caseDeleteCmd())
	return cmd
}

func caseDeleteCmd() *cobra.Command {
	var dryRun bool
	var yes bool
	cmd := &cobra.Command{
		Use:          "delete <case-id>",
		Aliases:      []string{"drop"},
		Short:        "Tombstone a case so its retained sessions can be deleted",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			caseID, err := primitives.ParseCaseID(args[0])
			if err != nil {
				return err
			}
			if !dryRun && !yes {
				return fmt.Errorf("case deletion is irreversible; rerun with --yes or inspect with --dry-run")
			}
			if dryRun {
				repo, err := openCheckpointRepoReadOnly()
				if err != nil {
					return err
				}
				projection, err := caseengine.Rebuild(repo)
				if err != nil {
					return err
				}
				if _, ok := projection.Case(caseID); !ok {
					return fmt.Errorf("case %s does not exist in this Turnal store", caseID)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "would delete case %s; its source and attempt sessions would become eligible for session drop\n", caseID)
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := experimentengine.RecoverAbandoned(repo); err != nil {
				return fmt.Errorf("recover abandoned fork attempts: %w", err)
			}
			if _, err := caseengine.Delete(repo, caseID); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted case %s; use turnal session drop for its retained source and attempt sessions\n", caseID)
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Check that the Case can be deleted without writing a tombstone")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm irreversible Case deletion")
	return cmd
}

func caseCreateCmd() *cobra.Command {
	var taskText string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "create <session>:<turn>",
		Short:        "Promote a recorded turn into an immutable case",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, turnID, err := parseTurnTarget(args[0])
			if err != nil {
				return err
			}
			var taskID primitives.TaskID
			if strings.TrimSpace(taskText) != "" {
				taskID, err = primitives.ParseTaskID(taskText)
				if err != nil {
					return err
				}
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := experimentengine.RecoverAbandoned(repo); err != nil {
				return fmt.Errorf("recover abandoned fork attempts: %w", err)
			}
			created, err := caseengine.Create(repo, caseengine.CreateRequest{SessionID: sessionID, TurnID: turnID, TaskID: taskID})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeCaseJSON(cmd.OutOrStdout(), caseCreateJSON{Version: caseengine.JSONVersion, TaskCreated: created.TaskCreated, Task: created.Task, Case: created.Case})
			}
			if created.TaskCreated {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "created task %s\n", created.Task.ID); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "created case %s\n", created.Case.ID); err != nil {
				return err
			}
			return writeCase(cmd.OutOrStdout(), created.Case)
		},
	}
	cmd.Flags().StringVar(&taskText, "task", "", "Create a sibling Case under an existing Task")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	return cmd
}

func caseShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "show <case-id>",
		Short:        "Show one immutable case",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			caseID, err := primitives.ParseCaseID(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := experimentengine.RecoverAbandoned(repo); err != nil {
				return fmt.Errorf("recover abandoned fork attempts: %w", err)
			}
			projection, err := caseengine.Rebuild(repo)
			if err != nil {
				return err
			}
			definition, ok := projection.Case(caseID)
			if !ok {
				return fmt.Errorf("case %s does not exist in this Turnal store", caseID)
			}
			if jsonOutput {
				return writeCaseJSON(cmd.OutOrStdout(), caseJSON{Version: caseengine.JSONVersion, Case: definition})
			}
			return writeCase(cmd.OutOrStdout(), definition)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	return cmd
}

func writeCase(writer io.Writer, definition caseengine.Case) error {
	if _, err := fmt.Fprintf(writer,
		"case:           %s\n"+
			"task:           %s revision %d\n"+
			"source turn:    %s:%s\n"+
			"base ref:       %s\n"+
			"base commit:    %s\n"+
			"instruction:    %s\n"+
			"readiness:      %s\n"+
			"fidelity:       %s\n",
		definition.ID,
		definition.TaskID,
		definition.TaskRevision,
		definition.Source.SessionID,
		definition.Source.TurnID,
		definition.Readiness.Base.Ref,
		definition.Readiness.Base.CommitSHA,
		definition.Readiness.Instruction.Status,
		definition.Readiness.Readiness,
		definition.Readiness.FidelityLevel,
	); err != nil {
		return err
	}
	if definition.Readiness.Instruction.Text != "" {
		if _, err := fmt.Fprintf(writer, "  %s\n", indentForkText(definition.Readiness.Instruction.Text, "  ")); err != nil {
			return err
		}
	}
	if len(definition.Readiness.Source.Adapters) > 0 {
		adapters := make([]string, 0, len(definition.Readiness.Source.Adapters))
		for _, adapter := range definition.Readiness.Source.Adapters {
			adapters = append(adapters, adapter.String())
		}
		if _, err := fmt.Fprintf(writer, "adapter:         %s\n", strings.Join(adapters, ", ")); err != nil {
			return err
		}
	}
	if definition.Readiness.Source.Model != "" {
		if _, err := fmt.Fprintf(writer, "model:           %s\n", definition.Readiness.Source.Model); err != nil {
			return err
		}
	}
	if definition.Readiness.Source.PermissionMode != "" {
		if _, err := fmt.Fprintf(writer, "permissions:     %s\n", definition.Readiness.Source.PermissionMode); err != nil {
			return err
		}
	}
	if len(definition.Verifiers) == 0 {
		if _, err := fmt.Fprintln(writer, "verifiers:      none"); err != nil {
			return err
		}
	} else {
		names := make([]string, 0, len(definition.Verifiers))
		for _, verifier := range definition.Verifiers {
			names = append(names, verifier.Name)
		}
		if _, err := fmt.Fprintf(writer, "verifiers:      %s\n", strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	if len(definition.AttemptLinks) == 0 {
		if _, err := fmt.Fprintln(writer, "attempts:       none linked"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(writer, "attempts:"); err != nil {
			return err
		}
		for _, link := range definition.AttemptLinks {
			status := caseengine.AttemptStatusRunning
			if link.Result != nil {
				status = link.Result.Status
			}
			if _, err := fmt.Fprintf(writer, "  - %s (run %s, %s)\n", link.AttemptID, link.RunID, status); err != nil {
				return err
			}
			if link.Result != nil && link.Result.Verification != nil {
				if _, err := fmt.Fprintf(writer, "    verification: %s\n", link.Result.Verification.Summary.Outcome); err != nil {
					return err
				}
			}
		}
	}
	if definition.Selection != nil {
		if _, err := fmt.Fprintf(writer, "selected:       %s\n", definition.Selection.AttemptID); err != nil {
			return err
		}
	}
	if len(definition.Limitations) > 0 {
		if _, err := fmt.Fprintln(writer, "limitations:"); err != nil {
			return err
		}
		for _, limitation := range definition.Limitations {
			if _, err := fmt.Fprintf(writer, "  - %s\n", limitation); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCaseJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
