package cli

import (
	"fmt"
	"io"
	"path/filepath"
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
	var fromWorktree string

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

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			target, resolved, err := resolveRollbackSelection(repo, targetText, fromWorktree)
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
			effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), overrides)
			if err != nil {
				return err
			}
			useWorkspaceGit := effective.Rollback.Mode == primitives.RollbackModeWorkspaceGit
			// A manual save has no Git-sync state. Treat the configured default as
			// checkpoint mode, while preserving an explicit --workspace-git request
			// so the engine can reject it with the invariant-specific error.
			if resolved != nil && resolved.Manual && !cmd.Flags().Changed("workspace-git") {
				useWorkspaceGit = false
			}
			result, err := rollbackengine.New(repo).Run(rollbackengine.Request{
				Target:       target,
				Resolved:     resolved,
				DryRun:       dryRun,
				WorkspaceGit: useWorkspaceGit,
			})
			if err != nil {
				return err
			}

			return writeRollbackResult(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVar(&targetText, "to", "", "Checkpoint target or checkpoint id; targets without a phase default to post")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show changes without modifying the workspace")
	cmd.Flags().BoolVar(&workspaceGit, "workspace-git", false, "Restore captured workspace Git HEAD, index, dirty tracked files, and untracked files")
	cmd.Flags().StringVar(&fromWorktree, "from-worktree", "", "Explicit source worktree id for an otherwise ambiguous or cross-worktree target")
	return cmd
}

func resolveRollbackSelection(repo *checkpoint.Repo, value string, fromWorktree string) (primitives.TargetRef, *rollbackengine.ResolvedTarget, error) {
	selector := strings.ToLower(strings.TrimSpace(value))
	var matches []checkpoint.CheckpointRefInfo
	var target primitives.TargetRef
	var err error
	switch {
	case strings.HasPrefix(selector, "chk_"):
		if len(selector) < len("chk_")+minRollbackCheckpointIDPrefixLength {
			return target, nil, fmt.Errorf("checkpoint id prefix must include at least %d hex characters", minRollbackCheckpointIDPrefixLength)
		}
		matches, err = repo.FindCheckpointIDPrefix(selector)
	case looksLikeRollbackCheckpointID(selector):
		infos, listErr := repo.ListAllCheckpointRefInfos()
		if listErr != nil {
			return target, nil, listErr
		}
		for _, info := range infos {
			if strings.HasPrefix(info.Commit.String(), selector) {
				matches = append(matches, info)
			}
		}
	default:
		target, err = parseRollbackTarget(selector)
		if err == nil {
			phase, _ := target.Phase()
			matches, err = repo.FindCheckpointTargets(target.SessionID(), target.TurnID(), phase)
		}
	}
	if err != nil {
		return target, nil, err
	}

	wantedWorktree := repo.WorktreeID
	if fromWorktree != "" && fromWorktree != "current" {
		wantedWorktree, err = primitives.ParseWorktreeID(fromWorktree)
		if err != nil {
			return target, nil, err
		}
	}
	allMatches := append([]checkpoint.CheckpointRefInfo(nil), matches...)
	filtered := matches[:0]
	for _, info := range matches {
		if wantedWorktree == "" || info.WorktreeID == "" || info.WorktreeID == wantedWorktree {
			filtered = append(filtered, info)
		}
	}
	matches = filtered
	if len(matches) == 0 {
		if len(allMatches) > 0 {
			return target, nil, fmt.Errorf("checkpoint exists in another worktree; use --from-worktree with one of: %s", formatRollbackIDMatches(allMatches))
		}
		return target, nil, fmt.Errorf("checkpoint %s not found", selector)
	}
	if len(matches) > 1 {
		return target, nil, fmt.Errorf("checkpoint %s is ambiguous; matches %s", selector, formatRollbackIDMatches(matches))
	}
	info := matches[0]
	if info.Manual {
		resolved, err := rollbackengine.ResolveManualCheckpointInfo(info)
		return target, &resolved, err
	}
	if target.SessionID() == "" {
		target, err = primitives.NewTargetRef(info.SessionID, info.TurnID, info.Phase)
		if err != nil {
			return target, nil, err
		}
	}
	resolved, err := rollbackengine.ResolveCheckpointInfo(target, info)
	if err != nil {
		return target, nil, err
	}
	return target, &resolved, nil
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
		return primitives.TargetRef{}, fmt.Errorf("target must be <session>:<turn>[:pre|post], <session>:turn:<turn>[:pre|post], or a checkpoint id")
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

const minRollbackCheckpointIDPrefixLength = 7

func looksLikeRollbackCheckpointID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= minRollbackCheckpointIDPrefixLength && len(value) <= 64 && isHexText(value)
}

func parseRollbackCheckpointIDTarget(repo *checkpoint.Repo, value string) (primitives.TargetRef, error) {
	prefix := strings.ToLower(strings.TrimSpace(value))
	if len(prefix) < minRollbackCheckpointIDPrefixLength {
		return primitives.TargetRef{}, fmt.Errorf("checkpoint id prefix must be at least %d hex characters", minRollbackCheckpointIDPrefixLength)
	}
	if len(prefix) > 64 || !isHexText(prefix) {
		return primitives.TargetRef{}, fmt.Errorf("checkpoint id must be a hex SHA prefix")
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
		return primitives.TargetRef{}, fmt.Errorf("checkpoint id %s not found", prefix)
	case 1:
		return primitives.NewTargetRef(matches[0].SessionID, matches[0].TurnID, matches[0].Phase)
	default:
		return primitives.TargetRef{}, fmt.Errorf("checkpoint id %s is ambiguous; matches %s", prefix, formatRollbackIDMatches(matches))
	}
}

func formatRollbackIDMatches(matches []checkpoint.CheckpointRefInfo) string {
	const limit = 5
	count := len(matches)
	if count > limit {
		count = limit
	}
	parts := make([]string, 0, count+1)
	for _, match := range matches[:count] {
		identity := match.Commit.String()
		if match.ID != "" {
			identity = match.ID.String()
		}
		if match.Manual {
			parts = append(parts, fmt.Sprintf("manual worktree=%s (%s)", match.WorktreeID, identity))
		} else {
			parts = append(parts, fmt.Sprintf("%s:turn:%s:%s worktree=%s (%s)", match.SessionID, match.TurnID, match.Phase, match.WorktreeID, identity))
		}
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
		if err := writeRollbackTargetBlock(w, "Dry-run rollback", result); err != nil {
			return err
		}
		return writeRestoreChanges(w, result.Plan.Changes)
	}

	if err := writeRollbackTargetBlock(w, "Rollback complete", result); err != nil {
		return err
	}
	if result.Safety != nil {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		return writeHiddenSnapshotBlock(w, "Previous workspace saved", *result.Safety)
	}
	return nil
}

func writeWorkspaceGitRollbackResult(w io.Writer, result rollbackengine.Result) error {
	if result.DryRun {
		if err := writeWorkspaceGitTargetBlock(w, "Dry-run workspace-git rollback", result); err != nil {
			return err
		}
		if result.GitPlan != nil {
			if _, err := fmt.Fprintf(w, "  commits:      %s -> %s\n", formatObjectID(result.GitPlan.CurrentHead.Commit, false), formatObjectID(result.GitPlan.TargetHead.Commit, false)); err != nil {
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

	if err := writeWorkspaceGitTargetBlock(w, "Workspace Git rollback complete", result); err != nil {
		return err
	}
	if result.GitPlan != nil {
		if _, err := fmt.Fprintf(w, "  commits:      %s -> %s\n", formatObjectID(result.GitPlan.CurrentHead.Commit, false), formatObjectID(result.GitPlan.TargetHead.Commit, false)); err != nil {
			return err
		}
	}
	if result.Safety != nil {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if err := writeHiddenSnapshotBlock(w, "Previous workspace snapshot saved", *result.Safety); err != nil {
			return err
		}
	}
	if result.GitSafety != nil {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		return writeHiddenSnapshotBlock(w, "Previous workspace Git state saved", *result.GitSafety)
	}
	return nil
}

func writeRollbackTargetBlock(w io.Writer, title string, result rollbackengine.Result) error {
	if _, err := fmt.Fprintln(w, title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  target: %s\n", result.Target.Selector()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  id:     %s\n", formatObjectID(result.Target.Commit, false)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  ref:    %s\n", result.Target.CheckpointRef)
	return err
}

func writeWorkspaceGitTargetBlock(w io.Writer, title string, result rollbackengine.Result) error {
	if _, err := fmt.Fprintln(w, title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  target:       %s\n", result.Target.Target); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  checkpoint id:  %s\n", formatObjectID(result.Target.Commit, false)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  checkpoint ref: %s\n", result.Target.CheckpointRef); err != nil {
		return err
	}
	if result.GitSyncRef != "" {
		if _, err := fmt.Fprintf(w, "  git-sync ref: %s\n", result.GitSyncRef); err != nil {
			return err
		}
	}
	return nil
}

func writeHiddenSnapshotBlock(w io.Writer, title string, snapshot checkpoint.Snapshot) error {
	return writeHiddenIDRefBlock(w, title, snapshot.Commit, snapshot.Ref)
}

func writeHiddenIDRefBlock(w io.Writer, title string, id primitives.CommitSHA, ref string) error {
	if _, err := fmt.Fprintln(w, title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  id:  %s\n", formatObjectID(id, false)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  ref: %s\n", ref)
	return err
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
