//go:build windows

package upgrade

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const standaloneCleanupTimeout = 30 * time.Second

func finalizeStandaloneTransaction(transactionDir, installedTurnal string) error {
	command := exec.Command(
		installedTurnal,
		StandaloneCleanupCommand,
		strconv.Itoa(os.Getpid()),
		transactionDir,
	)
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start standalone cleanup helper: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release standalone cleanup helper: %w", err)
	}
	return nil
}

func RunStandaloneCleanup(parentPID int, transactionDir string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve cleanup executable: %w", err)
	}
	validatedDir, err := validateStandaloneCleanupDirectory(transactionDir, executable)
	if err != nil {
		return err
	}
	if err := waitForStandaloneParent(parentPID); err != nil {
		return err
	}

	deadline := time.Now().Add(standaloneCleanupTimeout)
	for {
		err := os.RemoveAll(validatedDir)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remove standalone transaction %s: %w", validatedDir, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func validateStandaloneCleanupDirectory(transactionDir, executable string) (string, error) {
	transactionDir, err := filepath.Abs(transactionDir)
	if err != nil {
		return "", fmt.Errorf("resolve standalone transaction: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve installed executable: %w", err)
	}
	installDir := filepath.Dir(executable)
	if !strings.EqualFold(filepath.Dir(transactionDir), installDir) ||
		!strings.HasPrefix(filepath.Base(transactionDir), ".turnal-upgrade-") {
		return "", fmt.Errorf("refusing standalone cleanup outside %s", installDir)
	}
	info, err := os.Lstat(transactionDir)
	if err != nil {
		return "", fmt.Errorf("inspect standalone transaction: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("standalone transaction is not a regular directory: %s", transactionDir)
	}
	return transactionDir, nil
}

func waitForStandaloneParent(parentPID int) error {
	if parentPID <= 0 {
		return fmt.Errorf("invalid standalone upgrade parent PID %d", parentPID)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open standalone upgrade parent %d: %w", parentPID, err)
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for standalone upgrade parent %d: %w", parentPID, err)
	}
	if status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("wait for standalone upgrade parent %d returned status %d", parentPID, status)
	}
	return nil
}
