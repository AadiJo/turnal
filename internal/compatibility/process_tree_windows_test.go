//go:build windows

package compatibility

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/AadiJo/turnal/internal/adapters"
)

func TestCodexHookCWDComparisonIgnoresWindowsPathCasing(t *testing.T) {
	root := `C:\Users\Turnal\Workspace`
	upperRoot := strings.ToUpper(root)
	raw, err := json.Marshal(map[string]any{"data": []any{map[string]any{
		"cwd": upperRoot,
		"hooks": []any{map[string]any{
			"eventName": "Stop", "command": "turnal codex-hook", "source": "project",
			"sourcePath": filepath.Join(upperRoot, ".codex", "config.toml"), "enabled": true,
			"trustStatus": "trusted",
		}},
	}}})
	if err != nil {
		t.Fatalf("marshal hooks/list result: %v", err)
	}
	decoded, err := decodeHooksResult(raw, root)
	if err != nil || len(decoded.Hooks) != 1 {
		t.Fatalf("decoded hooks = %#v, err = %v", decoded.Hooks, err)
	}

	var hooks []CodexHook
	for _, event := range expectedCodexEventNames {
		hooks = append(hooks, CodexHook{
			CWD: upperRoot, EventName: event, Command: "turnal codex-hook", Source: "project",
			SourcePath: filepath.Join(upperRoot, ".codex", "config.toml"), Enabled: true,
			TrustStatus: "trusted",
		})
	}
	health := adapters.HookHealth{Target: adapters.TargetCodex, Status: adapters.HookConfigurationConfigured}
	classified := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks})
	if classified.Discovered != 4 || classified.Expectation != CaptureAvailable {
		t.Fatalf("classified hooks = %#v", classified)
	}
}

func TestAppServerProbeContainsFastDescendantFromStartup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant.pid")
	probe := testAppServerProbe("fast-descendant")
	probe.Command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		command := helperCommand(ctx, "fast-descendant")
		command.Env = append(command.Env, "TURNAL_APP_SERVER_DESCENDANT_MARKER="+marker)
		return command
	}
	_, err := probe.Probe(context.Background(), t.TempDir(), "turnal codex-hook")
	if err == nil || !strings.Contains(err.Error(), "exited before hooks/list") {
		t.Fatalf("Probe error = %v", err)
	}

	pid := readWindowsPIDMarker(t, marker)
	t.Cleanup(func() { terminateWindowsProcess(pid) })
	if !waitForWindowsProcessExit(pid, 3*time.Second) {
		t.Fatalf("descendant process %d survived probe shutdown", pid)
	}
}

func readWindowsPIDMarker(t *testing.T, marker string) uint32 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, conversionErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if conversionErr != nil {
				t.Fatalf("parse descendant PID: %v", conversionErr)
			}
			return uint32(pid)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read descendant PID: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not write its PID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWindowsProcessExit(pid uint32, timeout time.Duration) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	return err == nil && status == windows.WAIT_OBJECT_0
}

func terminateWindowsProcess(pid uint32) {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(process)
	_ = windows.TerminateProcess(process, 1)
}
