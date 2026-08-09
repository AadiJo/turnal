//go:build !windows

package sharedhistory

func platformWorkspaceRootAliases(string) []string {
	return nil
}
