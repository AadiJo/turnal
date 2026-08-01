//go:build windows

package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateStandaloneCleanupDirectory(t *testing.T) {
	installDir := t.TempDir()
	executable := filepath.Join(installDir, "turnal.exe")
	transactionDir := filepath.Join(installDir, ".turnal-upgrade-fixture")
	if err := os.Mkdir(transactionDir, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := validateStandaloneCleanupDirectory(transactionDir, executable)
	if err != nil {
		t.Fatalf("validateStandaloneCleanupDirectory: %v", err)
	}
	if got != transactionDir {
		t.Fatalf("transaction dir = %q, want %q", got, transactionDir)
	}
}

func TestValidateStandaloneCleanupDirectoryRejectsOutsideInstallDir(t *testing.T) {
	installDir := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), ".turnal-upgrade-fixture")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := validateStandaloneCleanupDirectory(outsideDir, filepath.Join(installDir, "turnal.exe"))
	if err == nil || !strings.Contains(err.Error(), "refusing standalone cleanup") {
		t.Fatalf("error = %v, want cleanup refusal", err)
	}
}
