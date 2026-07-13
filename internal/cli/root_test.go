package cli

import (
	"fmt"
	"os/exec"
	"testing"
)

func TestCommandExitCodeOnlyHonorsChildExitError(t *testing.T) {
	code, ok := commandExitCode(childExitError{code: 37})
	if !ok || code != 37 {
		t.Fatalf("child exit code = %d %v, want 37 true", code, ok)
	}

	err := &exec.ExitError{}
	code, ok = commandExitCode(fmt.Errorf("wrapped git failure: %w", err))
	if ok {
		t.Fatalf("wrapped exec.ExitError was treated as command exit code %d", code)
	}
}
