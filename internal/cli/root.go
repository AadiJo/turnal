package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.0.1"

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "agent-vcs",
		Short:   "Local-first version control for AI agent activity",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(checkpointCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(versionCmd())

	return rootCmd
}

func Execute() {
	rootCmd := NewRootCmd()
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
