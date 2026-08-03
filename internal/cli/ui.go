package cli

import (
	"fmt"
	"path/filepath"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/viewer"
	"github.com/spf13/cobra"
)

func uiCmd() *cobra.Command {
	var port int
	var noOpen bool
	var session string
	var project string
	cmd := &cobra.Command{
		Use:          "ui",
		Short:        "Open Turnal Prism, the local history viewer for every recorded project",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := resolveViewerProject(project)
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
	cmd.Flags().StringVar(&project, "project", "", "Preselect the project recorded at this directory")
	return cmd
}

// resolveViewerProject picks the project to preselect. The viewer itself always
// serves every registered project, so failing to find one here is not an error:
// running turnal ui from an unrecorded directory opens the global index.
func resolveViewerProject(project string) (*checkpoint.Repo, error) {
	if project == "" {
		repo, err := openCheckpointRepo()
		if err != nil {
			return nil, nil
		}
		return repo, nil
	}
	absolute, err := filepath.Abs(project)
	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}
	root, err := checkpoint.FindRoot(absolute)
	if err != nil {
		// Fall back to treating the path as a workspace root so the error names
		// the directory the user actually passed.
		parsed, parseErr := primitives.ParseWorkspaceRoot(absolute)
		if parseErr != nil {
			return nil, parseErr
		}
		root = parsed
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open project at %s: %w", absolute, err)
	}
	return repo, nil
}
