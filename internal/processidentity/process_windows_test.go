//go:build windows

package processidentity

import (
	"os"
	"os/exec"
	"testing"
)

func TestExitCodeStillActiveDoesNotMeanProcessIsAlive(t *testing.T) {
	if os.Getenv("TURNAL_EXIT_259_HELPER") == "1" {
		os.Exit(259)
	}
	command := exec.Command(os.Args[0], "-test.run=TestExitCodeStillActiveDoesNotMeanProcessIsAlive$")
	command.Env = append(os.Environ(), "TURNAL_EXIT_259_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started, err := startedAt(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	err = command.Wait()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 259 {
		t.Fatalf("helper exit = %v", err)
	}
	matches, err := Matches(command.Process.Pid, started)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("terminated process with exit code 259 reported alive")
	}
}
