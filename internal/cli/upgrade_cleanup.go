package cli

import (
	"fmt"
	"strconv"

	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

func standaloneUpgradeCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:    upgrade.StandaloneCleanupCommand + " PARENT_PID TRANSACTION_DIRECTORY",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			parentPID, err := strconv.Atoi(args[0])
			if err != nil || parentPID <= 0 {
				return fmt.Errorf("invalid standalone upgrade parent PID %q", args[0])
			}
			return upgrade.RunStandaloneCleanup(parentPID, args[1])
		},
	}
}
