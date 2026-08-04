//go:build !windows

package upgrade

import (
	"fmt"
	"os"
)

func finalizeStandaloneTransaction(transactionDir, _ string) error {
	return os.RemoveAll(transactionDir)
}

func RunStandaloneCleanup(_ int, _ string) error {
	return fmt.Errorf("standalone upgrade cleanup is only supported on Windows")
}
