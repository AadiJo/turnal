package cli

import (
	"fmt"
	"io"
	"strings"

	"agent-vcs-again/internal/checkpoint"
	agentconfig "agent-vcs-again/internal/config"
	"agent-vcs-again/internal/primitives"
	rollbackengine "agent-vcs-again/internal/rollback"
	"github.com/spf13/cobra"
)

func rollbackCmd() *cobra.Command {
	var targetText string
	var dryRun bool
	var workspaceGit bool

	cmd := &cobra.Command{
		Use:          "rollback --to <session:turn:<turn>:pre|post>",
		Short:        "Restore the workspace to a checkpoint",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetText != "" && len(args) > 0 {
				return fmt.Errorf("target argument cannot be combined with --to")
			}
			if targetText == "" {
				if len(args) == 0 {
					return fmt.Errorf("--to is required")
				}
				targetText = args[0]
			}
			target, err := parseRollbackTarget(targetText)
			if err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			overrides := agentconfig.Overrides{}
			if cmd.Flags().Changed("workspace-git") {
				mode := primitives.RollbackModeCheckpoint
				if workspaceGit {
					mode = primitives.RollbackModeWorkspaceGit
				}
				overrides.RollbackMode = &mode
			}
			effective, _, err := agentconfig.Resolve(repo.WorkspaceRoot.String(), overrides)
			if err != nil {
				return err
			}
			useWorkspaceGit := effective.Rollback.Mode == primitives.RollbackModeWorkspaceGit
			result, err := rollbackengine.New(repo).Run(rollbackengine.Request{
				Target:       target,
				DryRun:       dryRun,
				WorkspaceGit: useWorkspaceGit,
			})
			if err != nil {
				return err
			}

			return writeRollbackResult(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&targetText, "to", "", "Checkpoint target, for example demo:turn:1:pre")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show changes without modifying the workspace")
	cmd.Flags().BoolVar(&workspaceGit, "workspace-git", false, "Restore captured workspace Git HEAD, index, dirty tracked files, and untracked files")
	return cmd
}

func parseRollbackTarget(value string) (primitives.TargetRef, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return primitives.TargetRef{}, fmt.Errorf("rollback target is required")
	}

	if strings.Contains(value, ":turn:") {
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return primitives.TargetRef{}, err
		}
		return targetWithDefaultPhase(target)
	}

	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return primitives.TargetRef{}, fmt.Errorf("target must be <session>:<turn>[:pre|post] or <session>:turn:<turn>[:pre|post]")
	}
	sessionID, err := primitives.ParseSessionID(parts[0])
	if err != nil {
		return primitives.TargetRef{}, err
	}
	turnID, err := primitives.ParseTurnID(parts[1])
	if err != nil {
		return primitives.TargetRef{}, err
	}
	phase := primitives.CheckpointPhasePre
	if len(parts) == 3 {
		phase, err = primitives.ParseCheckpointPhase(parts[2])
		if err != nil {
			return primitives.TargetRef{}, err
		}
	}
	return primitives.NewTargetRef(sessionID, turnID, phase)
}

func targetWithDefaultPhase(target primitives.TargetRef) (primitives.TargetRef, error) {
	if _, ok := target.Phase(); ok {
		return target, nil
	}
	return primitives.NewTargetRef(target.SessionID(), target.TurnID(), primitives.CheckpointPhasePre)
}

func writeRollbackResult(w io.Writer, result rollbackengine.Result) error {
	if result.Mode == primitives.RollbackModeWorkspaceGit {
		return writeWorkspaceGitRollbackResult(w, result)
	}
	if result.DryRun {
		if _, err := fmt.Fprintf(w, "dry-run rollback to %s %s\n", result.Target.Commit, result.Target.CheckpointRef); err != nil {
			return err
		}
		return writeRestoreChanges(w, result.Plan.Changes)
	}

	if _, err := fmt.Fprintf(w, "rolled back to %s %s\n", result.Target.Commit, result.Target.CheckpointRef); err != nil {
		return err
	}
	if result.Safety != nil {
		if _, err := fmt.Fprintf(w, "safety checkpoint %s %s\n", result.Safety.Commit, result.Safety.Ref); err != nil {
			return err
		}
	}
	return nil
}

func writeWorkspaceGitRollbackResult(w io.Writer, result rollbackengine.Result) error {
	if result.DryRun {
		if _, err := fmt.Fprintf(w, "dry-run workspace-git rollback to %s %s\n", result.Target.Target, result.GitSyncRef); err != nil {
			return err
		}
		if result.GitPlan != nil {
			if _, err := fmt.Fprintf(w, "head: %s -> %s\n", result.GitPlan.CurrentHead.Commit, result.GitPlan.TargetHead.Commit); err != nil {
				return err
			}
			if err := writeRepoPathGroup(w, "staged", result.GitPlan.StagedPaths); err != nil {
				return err
			}
			if err := writeRepoPathGroup(w, "unstaged", result.GitPlan.UnstagedPaths); err != nil {
				return err
			}
			if err := writeRepoPathGroup(w, "untracked", result.GitPlan.Untracked); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := fmt.Fprintf(w, "rolled back workspace git to %s %s\n", result.Target.Target, result.GitSyncRef); err != nil {
		return err
	}
	if result.Safety != nil {
		if _, err := fmt.Fprintf(w, "safety checkpoint %s %s\n", result.Safety.Commit, result.Safety.Ref); err != nil {
			return err
		}
	}
	if result.GitSafety != nil {
		if _, err := fmt.Fprintf(w, "safety git-sync state %s %s\n", result.GitSafety.Commit, result.GitSafety.Ref); err != nil {
			return err
		}
	}
	return nil
}

func writeRepoPathGroup(w io.Writer, label string, paths []primitives.RepoPath) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "%s:\n", label); err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := fmt.Fprintf(w, "  %s\n", path); err != nil {
			return err
		}
	}
	return nil
}

func writeRestoreChanges(w io.Writer, changes []checkpoint.RestoreChange) error {
	if len(changes) == 0 {
		_, err := fmt.Fprintln(w, "no changes")
		return err
	}

	for _, group := range []struct {
		action checkpoint.RestoreAction
		label  string
	}{
		{checkpoint.RestoreActionAdded, "added"},
		{checkpoint.RestoreActionModified, "modified"},
		{checkpoint.RestoreActionDeleted, "deleted"},
		{checkpoint.RestoreActionModeChanged, "mode-changed"},
	} {
		var paths []string
		for _, change := range changes {
			if change.Action == group.action {
				paths = append(paths, change.Path)
			}
		}
		if len(paths) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s:\n", group.label); err != nil {
			return err
		}
		for _, path := range paths {
			if _, err := fmt.Fprintf(w, "  %s\n", path); err != nil {
				return err
			}
		}
	}
	return nil
}
