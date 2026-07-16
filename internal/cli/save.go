package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/spf13/cobra"
)

func saveCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "save [message]",
		Short:        "Save the current workspace as a rollback checkpoint",
		SilenceUsage: true,
		Args:         cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			message := strings.TrimSpace(strings.Join(args, " "))
			if err := manualcheckpoints.ValidateMessage(message); err != nil {
				return err
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			var created checkpoint.Checkpoint
			err = repo.WithWorkspaceLock("save checkpoint", func() error {
				if journals, err := repo.ListCheckpointJournals(); err != nil {
					return err
				} else if len(journals) > 0 {
					return fmt.Errorf("cannot save while checkpoint recovery is pending; run turnal status and resolve the interrupted capture first")
				}
				if _, pending, err := rollbackengine.RecoveryStatus(repo); err != nil {
					return err
				} else if pending {
					return fmt.Errorf("cannot save while rollback recovery is pending; run turnal recovery status")
				}
				created, err = repo.CreateManualCheckpointLocked()
				if err != nil {
					return err
				}
				if _, err := manualcheckpoints.Append(repo, created, message); err != nil {
					return fmt.Errorf("manual checkpoint %s was captured at %s, but its event could not be recorded: %w", created.ID, created.Commit, err)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if err := queryindex.Invalidate(repo.MetadataDir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: checkpoint saved, but the disposable index could not be invalidated: %v\n", err)
			}

			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					CheckpointID string `json:"checkpoint_id"`
					CommitSHA    string `json:"commit_sha"`
					Ref          string `json:"ref"`
					Message      string `json:"message,omitempty"`
				}{created.ID.String(), created.Commit.String(), created.Ref.String(), message})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Saved checkpoint %s\n", formatObjectID(created.Commit, false))
			fmt.Fprintf(out, "  hash: %s\n", created.Commit)
			if message != "" {
				fmt.Fprintf(out, "  message: %q\n", message)
			}
			fmt.Fprintf(out, "  rollback: turnal rollback --to %s\n", created.Commit)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}
