package cli

import (
	"fmt"
	"os"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show agent-vcs workspace status",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			root, err := checkpoint.FindRoot(cwd)
			var rootErr error
			if err != nil {
				rootErr = err
				root, err = primitives.ParseWorkspaceRoot(cwd)
				if err != nil {
					return rootErr
				}
			}

			status := checkpoint.Inspect(root)
			if rootErr != nil {
				status.Problems = append([]string{rootErr.Error()}, status.Problems...)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "workspace: %s\n", status.WorkspaceRoot)
			fmt.Fprintf(out, "metadata:  %s\n", status.MetadataDir)
			fmt.Fprintf(out, "hidden git: %s\n", status.GitDir)
			fmt.Fprintf(out, "version:    %s\n", status.Version)
			fmt.Fprintf(out, "gitignore:  %s\n", status.GitignorePath)
			fmt.Fprintf(out, "state:      ")
			if status.OK() {
				fmt.Fprintln(out, "ok")
				return nil
			}

			fmt.Fprintln(out, "needs attention")
			for _, problem := range status.Problems {
				fmt.Fprintf(out, "- %s\n", problem)
			}
			return fmt.Errorf("agent-vcs workspace has %d problem(s)", len(status.Problems))
		},
	}
}
