//go:build unix

package experiments

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
