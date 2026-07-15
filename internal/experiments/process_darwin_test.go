//go:build darwin

package experiments

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

const darwinArgvHelperEnvironment = "TURNAL_TEST_DARWIN_ARGV_GATE"

func TestDarwinForkGatePreservesArgvZero(t *testing.T) {
	if os.Getenv(darwinArgvHelperEnvironment) == "1" {
		if os.Args[0] != "turnal-fork-argv-helper" {
			t.Fatalf("argv[0] = %q", os.Args[0])
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(bin, "turnal-fork-argv-helper")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(darwinArgvHelperEnvironment, "1")
	runner := ExecRunner{}
	code, err := runner.Run(context.Background(), t.TempDir(), []string{"turnal-fork-argv-helper", "-test.run=^TestDarwinForkGatePreservesArgvZero$"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
}

func TestDarwinForkGateObservesFastExit(t *testing.T) {
	for range 50 {
		code, err := (ExecRunner{}).Run(context.Background(), t.TempDir(), []string{"/usr/bin/true"}, nil)
		if err != nil || code != 0 {
			t.Fatalf("Run = %d, %v", code, err)
		}
	}
}

func TestDarwinForkGateDefersBashStartupEnvironment(t *testing.T) {
	root := t.TempDir()
	hook := filepath.Join(root, "bash-env.sh")
	if err := os.WriteFile(hook, []byte("printf x >> bash-env-runs.txt\nset -e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"BASH_ENV="+hook,
		"ENV="+hook,
		"SHELLOPTS=braceexpand:errexit:hashall:interactive-comments",
	)
	runner := ExecRunner{Env: environment}
	code, err := runner.Run(context.Background(), root, []string{"/bin/bash", "--noprofile", "--norc", "-c", `case $- in *e*) ;; *) exit 12;; esac; printf target > target.txt`}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	runs, err := os.ReadFile(filepath.Join(root, "bash-env-runs.txt"))
	if err != nil || string(runs) != "x" {
		t.Fatalf("BASH_ENV runs = %q, %v, want one target-process run", runs, err)
	}
	target, err := os.ReadFile(filepath.Join(root, "target.txt"))
	if err != nil || string(target) != "target" {
		t.Fatalf("target output = %q, %v", target, err)
	}
}

func TestValidateDarwinForkExitRejectsRegistrationError(t *testing.T) {
	_, err := validateDarwinForkExit(unix.Kevent_t{Flags: unix.EV_ERROR, Data: int64(unix.ESRCH)}, 42)
	if err == nil {
		t.Fatal("registration error was accepted as process exit")
	}
}
