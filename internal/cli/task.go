package cli

import (
	"fmt"
	"io"

	caseengine "github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

type taskJSON struct {
	Version int                 `json:"version"`
	Task    caseengine.Task     `json:"task"`
	Cases   []primitives.CaseID `json:"case_ids"`
}

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Inspect evolving task identity"}
	cmd.AddCommand(taskShowCmd())
	return cmd
}

func taskShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "show <task-id>",
		Short:        "Show one task and its preserved revisions",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID, err := primitives.ParseTaskID(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			projection, err := caseengine.Rebuild(repo)
			if err != nil {
				return err
			}
			task, ok := projection.Task(taskID)
			if !ok {
				return fmt.Errorf("task %s does not exist in this Turnal store", taskID)
			}
			var caseIDs []primitives.CaseID
			for _, definition := range projection.Cases {
				if definition.TaskID == taskID {
					caseIDs = append(caseIDs, definition.ID)
				}
			}
			if jsonOutput {
				return writeCaseJSON(cmd.OutOrStdout(), taskJSON{Version: caseengine.JSONVersion, Task: task, Cases: caseIDs})
			}
			return writeTask(cmd.OutOrStdout(), task, caseIDs)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit versioned JSON")
	return cmd
}

func writeTask(writer io.Writer, task caseengine.Task, caseIDs []primitives.CaseID) error {
	if _, err := fmt.Fprintf(writer,
		"task:       %s\n"+
			"repository: %s\n"+
			"store:      %s\n"+
			"worktree:   %s\n"+
			"revisions:\n",
		task.ID, task.Scope.RepoID, task.Scope.StoreID, task.Scope.WorktreeID,
	); err != nil {
		return err
	}
	for _, revision := range task.Revisions {
		if _, err := fmt.Fprintf(writer, "  %d  %s  source %s:%s\n", revision.Number, revision.Instruction.Status, revision.Source.SessionID, revision.Source.TurnID); err != nil {
			return err
		}
		if revision.Instruction.Text != "" {
			if _, err := fmt.Fprintf(writer, "     %s\n", indentForkText(revision.Instruction.Text, "     ")); err != nil {
				return err
			}
		}
	}
	if len(caseIDs) == 0 {
		_, err := fmt.Fprintln(writer, "cases:      none")
		return err
	}
	if _, err := fmt.Fprintln(writer, "cases:"); err != nil {
		return err
	}
	for _, caseID := range caseIDs {
		if _, err := fmt.Fprintf(writer, "  - %s\n", caseID); err != nil {
			return err
		}
	}
	return nil
}
