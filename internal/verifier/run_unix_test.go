//go:build unix

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
	"syscall"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/config"
)

func TestTimeoutWaitDelayBoundsDetachedPipeHolder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "detached.pid")
	t.Cleanup(func() {
		data, err := os.ReadFile(marker)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	definition := config.Verifier{
		Name:    "detached-child",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestVerifierDetachedHelperProcess$", "--", "parent", marker},
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
		t.Fatalf("timeout remained blocked by detached output pipes for %s", elapsed)
	}
	if report.Checks[0].Status != StatusTimedOut {
		t.Fatalf("check = %#v, want timeout", report.Checks[0])
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read detached pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse detached pid: %v", err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		t.Fatalf("terminate detached test child %d: %v", pid, err)
	}
}

func TestSuccessfulVerifierTerminatesBackgroundProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "background.pid")
	t.Cleanup(func() {
		data, err := os.ReadFile(marker)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	definition := config.Verifier{
		Name:    "background-child",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestVerifierBackgroundHelperProcess$", "--", "parent", marker},
		Timeout: 5 * time.Second,
	}
	report, err := Run(context.Background(), Request{
		Root:        t.TempDir(),
		Target:      Target{Kind: TargetLiveWorkspace},
		Verifiers:   []config.Verifier{definition},
		OutputLimit: DefaultOutputLimit,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Checks[0].Status != StatusPassed {
		t.Fatalf("check = %#v, want pass", report.Checks[0])
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read background pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse background pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background process %d survived successful verifier completion", pid)
}

func TestVerifierBackgroundHelperProcess(t *testing.T) {
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
			os.Exit(35)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode != "parent" {
		os.Exit(36)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestVerifierBackgroundHelperProcess$", "--", "child", marker)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(37)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(38)
}

func TestVerifierDetachedHelperProcess(t *testing.T) {
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
			os.Exit(31)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if mode != "parent" {
		os.Exit(32)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestVerifierDetachedHelperProcess$", "--", "child", marker)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(33)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(34)
}
