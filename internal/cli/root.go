package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.0.1"

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "agent-vcs",
		Short:         "Local-first version control for AI agent activity",
		Version:       version,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(logCmd())
	rootCmd.AddCommand(reindexCmd())
	rootCmd.AddCommand(sessionCmd())
	rootCmd.AddCommand(retentionCmd())
	rootCmd.AddCommand(maintenanceCmd())
	rootCmd.AddCommand(showCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(turnCmd())
	rootCmd.AddCommand(checkpointCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(blameCmd())
	rootCmd.AddCommand(rollbackCmd())
	rootCmd.AddCommand(replayCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(claudeHookCmd())
	rootCmd.AddCommand(codexHookCmd())

	return rootCmd
}

func Execute() {
	rootCmd := NewRootCmd()
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	if err := rootCmd.Execute(); err != nil {
		if code, ok := commandExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func commandExitCode(err error) (int, bool) {
	var childErr childExitError
	if errors.As(err, &childErr) {
		return childErr.ExitCode(), true
	}
	return 0, false
}
