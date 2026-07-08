package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/spf13/cobra"
)

func rollbackCmd() *cobra.Command {
	var targetText string
	var dryRun bool
	var workspaceGit bool

	cmd := &cobra.Command{
		Use:          "rollback --to <target|checkpoint-hash>",
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

			target, parseErr := parseRollbackTarget(targetText)
			var repo *checkpoint.Repo
			var err error
			if parseErr != nil && !looksLikeRollbackCheckpointCommit(targetText) {
				return parseErr
			}
			repo, err = openCheckpointRepo()
			if err != nil {
				return err
			}
			if parseErr != nil {
				target, err = parseRollbackCheckpointCommitTarget(repo, targetText)
				if err != nil {
					return err
				}
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
	cmd.Flags().StringVar(&targetText, "to", "", "Checkpoint target or checkpoint commit hash; targets without a phase default to post")
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
		return primitives.TargetRef{}, fmt.Errorf("target must be <session>:<turn>[:pre|post], <session>:turn:<turn>[:pre|post], or a checkpoint commit hash")
	}
	sessionID, err := primitives.ParseSessionID(parts[0])
	if err != nil {
		return primitives.TargetRef{}, err
	}
	turnID, err := primitives.ParseTurnID(parts[1])
	if err != nil {
		return primitives.TargetRef{}, err
	}
	phase := rollbackengine.DefaultTargetPhase
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
	return primitives.NewTargetRef(target.SessionID(), target.TurnID(), rollbackengine.DefaultTargetPhase)
}

const minRollbackCheckpointCommitPrefixLength = 7

func looksLikeRollbackCheckpointCommit(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= minRollbackCheckpointCommitPrefixLength && len(value) <= 64 && isHexText(value)
}

func parseRollbackCheckpointCommitTarget(repo *checkpoint.Repo, value string) (primitives.TargetRef, error) {
	prefix := strings.ToLower(strings.TrimSpace(value))
	if len(prefix) < minRollbackCheckpointCommitPrefixLength {
		return primitives.TargetRef{}, fmt.Errorf("checkpoint commit hash prefix must be at least %d hex characters", minRollbackCheckpointCommitPrefixLength)
	}
	if len(prefix) > 64 || !isHexText(prefix) {
		return primitives.TargetRef{}, fmt.Errorf("checkpoint commit hash must be a hex SHA prefix")
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return primitives.TargetRef{}, err
	}

	var matches []checkpoint.CheckpointRefInfo
	for _, info := range infos {
		if !info.HasPhase {
			continue
		}
		if strings.HasPrefix(info.Commit.String(), prefix) {
			matches = append(matches, info)
		}
	}

	switch len(matches) {
	case 0:
		return primitives.TargetRef{}, fmt.Errorf("checkpoint commit %s not found", prefix)
	case 1:
		return primitives.NewTargetRef(matches[0].SessionID, matches[0].TurnID, matches[0].Phase)
	default:
		return primitives.TargetRef{}, fmt.Errorf("checkpoint commit %s is ambiguous; matches %s", prefix, formatRollbackCommitMatches(matches))
	}
}

func formatRollbackCommitMatches(matches []checkpoint.CheckpointRefInfo) string {
	const limit = 5
	count := len(matches)
	if count > limit {
		count = limit
	}
	parts := make([]string, 0, count+1)
	for _, match := range matches[:count] {
		parts = append(parts, fmt.Sprintf("%s:turn:%s:%s (%s)", match.SessionID, match.TurnID, match.Phase, match.Commit))
	}
	if len(matches) > limit {
		parts = append(parts, fmt.Sprintf("+%d more", len(matches)-limit))
	}
	return strings.Join(parts, ", ")
}

func isHexText(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
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
