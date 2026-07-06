package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

type rollbackPayload struct {
	Turn      uint64 `json:"turn"`
	Phase     string `json:"phase"`
	CommitSHA string `json:"commit_sha"`
	Ref       string `json:"ref"`
}

func rollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "rollback <session:turn[:phase]>",
		Short:        "Restore the workspace to a checkpoint",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, turnID, phase, err := parseRollbackTarget(args[0])
			if err != nil {
				return err
			}
			ref, err := primitives.NewCheckpointRef(sessionID, turnID, phase)
			if err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			commit, err := repo.CheckpointCommit(ref)
			if err != nil {
				return err
			}
			if err := repo.RestoreCheckpoint(ref); err != nil {
				return err
			}

			payload, err := json.Marshal(rollbackPayload{
				Turn:      turnID.Uint64(),
				Phase:     phase.String(),
				CommitSHA: commit.String(),
				Ref:       ref.String(),
			})
			if err != nil {
				return fmt.Errorf("marshal rollback event: %w", err)
			}
			if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
				SessionID: sessionID,
				TurnID:    &turnID,
				Type:      primitives.EventTypeRollback,
				Payload:   payload,
			}); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "rolled back to %s %s\n", commit, ref)
			return nil
		},
	}
	return cmd
}

func parseRollbackTarget(value string) (primitives.SessionID, primitives.TurnID, primitives.CheckpointPhase, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, "", fmt.Errorf("rollback target is required")
	}

	if strings.Contains(value, ":turn:") {
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return "", 0, "", err
		}
		phase, ok := target.Phase()
		if !ok {
			return target.SessionID(), target.TurnID(), primitives.CheckpointPhasePre, nil
		}
		return target.SessionID(), target.TurnID(), phase, nil
	}

	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return "", 0, "", fmt.Errorf("target must be <session>:<turn>[:pre|post] or <session>:turn:<turn>[:pre|post]")
	}
	sessionID, err := primitives.ParseSessionID(parts[0])
	if err != nil {
		return "", 0, "", err
	}
	turnID, err := primitives.ParseTurnID(parts[1])
	if err != nil {
		return "", 0, "", err
	}
	phase := primitives.CheckpointPhasePre
	if len(parts) == 3 {
		phase, err = primitives.ParseCheckpointPhase(parts[2])
		if err != nil {
			return "", 0, "", err
		}
	}
	return sessionID, turnID, phase, nil
}
