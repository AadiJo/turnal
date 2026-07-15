//go:build linux

package experiments

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestUnixProcessControllerCloseIsOneShot(t *testing.T) {
	originalKill := killForkProcessGroup
	defer func() { killForkProcessGroup = originalKill }()
	var calls atomic.Int32
	killForkProcessGroup = func(pid int, signal syscall.Signal) error {
		if pid != -4242 || signal != syscall.SIGKILL {
			t.Fatalf("kill = (%d, %v), want (-4242, SIGKILL)", pid, signal)
		}
		calls.Add(1)
		return nil
	}
	controller := &unixForkProcessController{cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := controller.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	group.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("process group kill calls = %d, want 1", got)
	}
}

func TestExecRunnerBoundsInheritedOutputPipeWait(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh required")
	}
	var output bytes.Buffer
	started := time.Now()
	code, err := (ExecRunner{Stdout: &output, Stderr: &output, Env: []string{"PATH=" + os.Getenv("PATH")}}).Run(context.Background(), t.TempDir(), []string{"sh", "-c", "(sleep 30) &"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run waited %s for descendant-held output pipes", elapsed)
	}
}

func TestExecRunnerClosesGroupBeforeLeaderReap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh required")
	}
	originalKill := killForkProcessGroup
	defer func() { killForkProcessGroup = originalKill }()
	var checked atomic.Bool
	killForkProcessGroup = func(target int, signal syscall.Signal) error {
		pid := -target
		if target >= 0 || signal != syscall.SIGKILL {
			t.Fatalf("kill = (%d, %v), want a process group and SIGKILL", target, signal)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("fork leader %d was reaped before process-group cleanup: %v", pid, err)
		}
		checked.Store(true)
		return originalKill(target, signal)
	}
	code, err := (ExecRunner{Env: []string{"PATH=" + os.Getenv("PATH")}}).Run(context.Background(), t.TempDir(), []string{"sh", "-c", "(sleep 30) &"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if !checked.Load() {
		t.Fatal("process-group cleanup was not observed")
	}
}

func TestExecRunnerCancellationTerminatesProcessGroupWithBufferedOutput(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh required")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "survived-cancel.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	code, err := (ExecRunner{Stdout: &output, Stderr: &output, Env: []string{"PATH=" + os.Getenv("PATH")}}).Run(ctx, root, []string{"sh", "-c", "(sleep 0.5; printf survived > survived-cancel.txt) & sleep 30"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || code != -1 {
		t.Fatalf("Run = %d, %v, want context deadline", code, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("canceled Run took %s", elapsed)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("background descendant survived cancellation: %v", err)
	}
}

func TestExecRunnerTerminatesBackgroundProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh required")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "survived.txt")
	runner := ExecRunner{Env: []string{"PATH=" + os.Getenv("PATH")}}
	code, err := runner.Run(context.Background(), root, []string{"sh", "-c", "(sleep 0.3; printf survived > survived.txt) &"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("background descendant survived fork completion: %v", err)
	}
}
