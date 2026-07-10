package cli

import (
	"github.com/AadiJo/turnal/internal/viewer"
	"github.com/spf13/cobra"
)

func uiCmd() *cobra.Command {
	var port int
	var noOpen bool
	var session string
	cmd := &cobra.Command{
		Use:          "ui",
		Short:        "Open Turnal Prism, the local read-only history viewer",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			return viewer.Run(cmd.Context(), viewer.Options{
				Repo: repo, Port: port, NoOpen: noOpen, InitialSession: session, Out: cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Loopback port; zero chooses an available port")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "Print the viewer URL without opening a browser")
	cmd.Flags().StringVar(&session, "session", "", "Open a recorded session by display id; ambiguous ids fail closed")
	return cmd
}
