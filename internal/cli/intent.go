package cli

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/spf13/cobra"
)

func intentCmd() *cobra.Command {
	var session string
	var problem string
	var scope []string
	var evidence []string

	cmd := &cobra.Command{
		Use:   "intent",
		Short: "Record the problem an upcoming agent change is meant to solve",
		Long: `Record an agent's stated intent for upcoming file changes in the active turn.

The problem should describe the defect or goal, not the edit steps or private
reasoning. Repeat --scope for expected repository paths. Repeat --evidence for
references such as event:12, path:src/retry.go:42, or test:TestRetryReset.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, err := primitives.ParseSessionID(session)
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			unlock, err := adapters.AcquireSessionLock(repo, sessionID)
			if err != nil {
				return err
			}
			defer unlock()

			event, err := provenance.Record(repo, provenance.RecordInput{
				SessionID: sessionID,
				Problem:   problem,
				Scope:     scope,
				Evidence:  evidence,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "recorded intent for %s turn %s at event %s\n", event.SessionID, *event.TurnID, event.Seq)
			return err
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Active agent session id")
	cmd.Flags().StringVar(&problem, "problem", "", "Defect or goal the upcoming change addresses")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Expected repository path (repeatable)")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil, "Evidence reference (repeatable)")
	_ = cmd.MarkFlagRequired("session")
	_ = cmd.MarkFlagRequired("problem")
	return cmd
}
