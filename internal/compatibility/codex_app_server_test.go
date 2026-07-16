package compatibility

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppServerProbePerformsMinimalHandshake(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	var launchedName string
	var launchedArgs []string
	probe := testAppServerProbe("success")
	probe.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		launchedName = name
		launchedArgs = append([]string(nil), args...)
		return helperCommand(ctx, "success")
	}
	result, err := probe.Probe(context.Background(), root, "turnal codex-hook")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if launchedName != "codex" || strings.Join(launchedArgs, " ") != "app-server --enable hooks" {
		t.Fatalf("launch = %q %#v", launchedName, launchedArgs)
	}
	if len(result.Hooks) != 4 || result.Hooks[0].CWD != root {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "keep this warning" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestAppServerProbeFailureModes(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		want     string
		adjust   func(*AppServerProbe)
	}{
		{"JSON-RPC error", "rpc-error", "JSON-RPC error", nil},
		{"unrelated notification", "notification", "", nil},
		{"malformed JSON", "malformed", "decode Codex app-server JSON", nil},
		{"oversized response", "oversized", "message exceeds", func(probe *AppServerProbe) { probe.MaxMessageBytes = 256 }},
		{"early exit", "early-exit", "exited before hooks/list", nil},
		{"duplicate response", "duplicate", "duplicate response id", nil},
		{"timeout", "timeout", "timed out", func(probe *AppServerProbe) { probe.Timeout = 150 * time.Millisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := testAppServerProbe(test.scenario)
			if test.adjust != nil {
				test.adjust(&probe)
			}
			started := time.Now()
			_, err := probe.Probe(context.Background(), t.TempDir(), "turnal codex-hook")
			if test.want == "" {
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if test.scenario == "timeout" && time.Since(started) > 2*time.Second {
				t.Fatalf("timeout child was not terminated promptly: %s", time.Since(started))
			}
		})
	}
}

func TestAppServerProbeReportsMissingExecutable(t *testing.T) {
	probe := DefaultAppServerProbe()
	probe.Executable = filepath.Join(t.TempDir(), "missing-codex")
	_, err := probe.Probe(context.Background(), t.TempDir(), "turnal codex-hook")
	if err == nil || !strings.Contains(err.Error(), "start Codex app-server") {
		t.Fatalf("error = %v", err)
	}
}

func TestAppServerProbePreservesSuccessfulStderr(t *testing.T) {
	probe := testAppServerProbe("stderr")
	result, err := probe.Probe(context.Background(), t.TempDir(), "turnal codex-hook")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	joined := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joined, "diagnostic on stderr") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestAppServerProbeBoundsDescendantHoldingStderr(t *testing.T) {
	probe := testAppServerProbe("descendant-stderr")
	probe.ShutdownTimeout = 100 * time.Millisecond
	started := time.Now()
	result, err := probe.Probe(context.Background(), t.TempDir(), "turnal codex-hook")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(result.Hooks) != 4 {
		t.Fatalf("hooks = %#v", result.Hooks)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("probe shutdown took %s", elapsed)
	}
}

func testAppServerProbe(scenario string) AppServerProbe {
	probe := DefaultAppServerProbe()
	probe.ShutdownTimeout = 200 * time.Millisecond
	probe.Command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return helperCommand(ctx, scenario)
	}
	return probe
}

func helperCommand(ctx context.Context, scenario string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCodexAppServerHelperProcess", "--", scenario)
	command.Env = append(os.Environ(), "TURNAL_APP_SERVER_HELPER=1")
	return command
}

func TestCodexAppServerHelperProcess(t *testing.T) {
	if os.Getenv("TURNAL_APP_SERVER_HELPER") != "1" {
		return
	}
	if os.Getenv("TURNAL_APP_SERVER_DESCENDANT") == "1" {
		if marker := os.Getenv("TURNAL_APP_SERVER_DESCENDANT_MARKER"); marker != "" {
			if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				os.Exit(11)
			}
		}
		time.Sleep(time.Hour)
		return
	}
	scenario := os.Args[len(os.Args)-1]
	if scenario == "fast-descendant" {
		marker := os.Getenv("TURNAL_APP_SERVER_DESCENDANT_MARKER")
		descendant := exec.Command(os.Args[0], "-test.run=TestCodexAppServerHelperProcess")
		descendant.Env = append(os.Environ(), "TURNAL_APP_SERVER_HELPER=1", "TURNAL_APP_SERVER_DESCENDANT=1", "TURNAL_APP_SERVER_DESCENDANT_MARKER="+marker)
		descendant.Stdout = io.Discard
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(12)
		}
		defer descendant.Process.Release()
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				os.Exit(0)
			}
			if time.Now().After(deadline) {
				os.Exit(13)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	reader := bufio.NewScanner(os.Stdin)
	if !reader.Scan() {
		os.Exit(2)
	}
	var initialize map[string]any
	if json.Unmarshal(reader.Bytes(), &initialize) != nil || initialize["method"] != "initialize" || int(initialize["id"].(float64)) != initializeRequestID {
		os.Exit(3)
	}
	if scenario == "early-exit" {
		fmt.Fprintln(os.Stderr, "helper exited early")
		os.Exit(4)
	}
	if scenario == "timeout" {
		time.Sleep(time.Hour)
	}
	if scenario == "malformed" {
		fmt.Println("{not-json")
		os.Exit(0)
	}
	if scenario == "oversized" {
		fmt.Printf("{\"id\":1,\"result\":\"%s\"}\n", strings.Repeat("x", 2048))
		os.Exit(0)
	}
	if scenario == "rpc-error" {
		fmt.Println(`{"id":1,"error":{"code":-32600,"message":"bad initialize"}}`)
		os.Exit(0)
	}
	if scenario == "notification" {
		fmt.Println(`{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`)
	}
	if scenario == "stderr" {
		fmt.Fprintln(os.Stderr, "diagnostic on stderr")
	}
	fmt.Println(`{"id":1,"result":{"userAgent":"fake"}}`)
	if scenario == "duplicate" {
		fmt.Println(`{"id":1,"result":{"userAgent":"fake-again"}}`)
		// Keep the helper alive to accept the probe's initialized and
		// hooks/list writes. Exiting here races duplicate-response parsing
		// against a broken pipe on macOS.
	}
	if !reader.Scan() { // initialized notification
		os.Exit(5)
	}
	var initialized map[string]any
	if json.Unmarshal(reader.Bytes(), &initialized) != nil || initialized["method"] != "initialized" {
		os.Exit(6)
	}
	if _, hasID := initialized["id"]; hasID {
		os.Exit(7)
	}
	if !reader.Scan() { // hooks/list
		os.Exit(8)
	}
	var hooksList struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			CWDs []string `json:"cwds"`
		} `json:"params"`
	}
	if json.Unmarshal(reader.Bytes(), &hooksList) != nil || hooksList.ID != hooksListRequestID || hooksList.Method != "hooks/list" || len(hooksList.Params.CWDs) != 1 {
		os.Exit(9)
	}
	root := hooksList.Params.CWDs[0]
	hooks := []map[string]any{}
	for _, event := range []string{"stop", "sessionStart", "post_tool_use", "userPromptSubmit"} {
		hooks = append(hooks, map[string]any{
			"eventName": event, "command": "turnal codex-hook", "source": "project",
			"sourcePath": filepath.Join(root, ".codex", "config.toml"), "enabled": true,
			"currentHash": "sha256:fake", "trustStatus": "trusted",
		})
	}
	response := map[string]any{
		"id": hooksListRequestID,
		"result": map[string]any{"data": []any{map[string]any{
			"cwd": root, "hooks": hooks, "warnings": []string{"keep this warning"}, "errors": []any{},
		}}},
	}
	encoded, _ := json.Marshal(response)
	if scenario == "descendant-stderr" {
		descendant := exec.Command(os.Args[0], "-test.run=TestCodexAppServerHelperProcess")
		descendant.Env = append(os.Environ(), "TURNAL_APP_SERVER_HELPER=1", "TURNAL_APP_SERVER_DESCENDANT=1")
		descendant.Stdout = io.Discard
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(10)
		}
	}
	fmt.Println(string(encoded))
	if scenario == "descendant-stderr" {
		time.Sleep(time.Hour)
	}
}
