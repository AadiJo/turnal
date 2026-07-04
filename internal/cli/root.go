package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.0.1"

var rootCmd = &cobra.Command{
	Use:     "acs",
	Short:   "acs is a CLI application",
	Version: version,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func Execute() {
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
