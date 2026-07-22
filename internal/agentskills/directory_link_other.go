//go:build !windows

package agentskills

import "os"

func createDirectoryLink(_ string, target string, destination string) error {
	return os.Symlink(target, destination)
}
