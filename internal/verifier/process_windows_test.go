//go:build windows

package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/config"
	"golang.org/x/sys/windows"
)

func TestWindowsTimeoutTerminatesProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() { terminateWindowsPIDFromMarker(marker) })
	definition := config.Verifier{
		Name:    "windows-process-tree",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestVerifierWindowsTreeHelperProcess$", "--", "parent", marker},
		Timeout: 500 * time.Millisecond,
	}
	started := time.Now()
	report, err := Run(context.Background(), Request{
		Root:        t.TempDir(),
		Target:      Target{Kind: TargetLiveWorkspace},
		Verifiers:   []config.Verifier{definition},
		OutputLimit: DefaultOutputLimit,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Windows process-tree timeout took %s", elapsed)
	}
	if report.Checks[0].Status != StatusTimedOut {
		t.Fatalf("check = %#v, want timeout", report.Checks[0])
	}
	pid := readWindowsPIDMarker(t, marker)
	if !waitForWindowsProcessExit(pid, 3*time.Second) {
		t.Fatalf("descendant process %d survived verifier timeout", pid)
	}
}

func TestVerifierWindowsTreeHelperProcess(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+2 >= len(os.Args) {
		return
	}
	mode, marker := os.Args[separator+1], os.Args[separator+2]
	if mode == "child" {
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(41)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode != "parent" {
		os.Exit(42)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestVerifierWindowsTreeHelperProcess$", "--", "child", marker)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(43)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(44)
}

func readWindowsPIDMarker(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read child pid marker: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	return pid
}

func waitForWindowsProcessExit(pid int, timeout time.Duration) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	result, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	return err == nil && result == windows.WAIT_OBJECT_0
}

func terminateWindowsPIDFromMarker(marker string) {
	data, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(process)
	_ = windows.TerminateProcess(process, 1)
}
