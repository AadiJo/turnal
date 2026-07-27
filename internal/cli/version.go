package cli

import (
	"encoding/json"
	"fmt"

	"github.com/AadiJo/turnal/internal/buildinfo"
	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Print version",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			metadata := currentBuildMetadata()
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(metadata)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "version:        %s\n", metadata.Version)
			fmt.Fprintf(out, "channel:        %s\n", metadata.Channel)
			fmt.Fprintf(out, "commit:         %s\n", metadata.Commit)
			fmt.Fprintf(out, "install_source: %s\n", metadata.InstallSource)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func currentBuildMetadata() upgrade.Metadata {
	return buildinfo.Current()
}
