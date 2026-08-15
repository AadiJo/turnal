package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
)

func TestRunEnvironmentPreservesExistingValuesAndReplacesCorrelation(t *testing.T) {
	runID, _ := primitives.ParseRunID("run_0123456789abcdef0123456789abcdef")
	got := runEnvironment([]string{"PATH=/tools", "EMPTY=", "turnal_run_id=lowercase", "TURNAL_RUN_ID=run_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, runID)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PATH=/tools") || !strings.Contains(joined, "EMPTY=") {
		t.Fatalf("existing environment was discarded: %#v", got)
	}
	if strings.Count(joined, runs.EnvRunID+"=") != 1 || !strings.Contains(joined, runs.EnvRunID+"="+runID.String()) {
		t.Fatalf("run correlation was not replaced safely: %#v", got)
	}
	if runtime.GOOS == "windows" && strings.Contains(joined, "turnal_run_id=lowercase") {
		t.Fatalf("case-insensitive Windows correlation was retained: %#v", got)
	}
	if runtime.GOOS != "windows" && !strings.Contains(joined, "turnal_run_id=lowercase") {
		t.Fatalf("distinct POSIX environment variable was removed: %#v", got)
	}
}

func TestRunCodexWrapperCreatesCheckpointsAndEnablesHooks(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	t.Setenv("TURNAL_HOOK_COMMAND", "turnal")

	argsPath := installFakeCodex(t, root.String(), 0)
	writeFile(t, root, "app.txt", "before\n")

	cmd := NewRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"run", "--quiet", "--", "codex", "exec", "change app.txt"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command: %v\nstdout=%s\nstderr=%s", err, out.String(), stderr.String())
	}

	if !strings.Contains(out.String(), "fake codex stdout") {
		t.Fatalf("stdout missing fake child output: %q", out.String())
	}
	if !strings.Contains(stderr.String(), "fake codex stderr") {
		t.Fatalf("stderr missing fake child output: %q", stderr.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake args: %v", err)
	}
	argsText := string(argsData)
	if !strings.Contains(argsText, "--enable\nhooks\nexec\nchange app.txt\n") {
		t.Fatalf("fake codex args did not enable hooks before child args:\n%s", argsText)
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		t.Fatalf("ListAllCheckpointRefInfos: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("checkpoint refs = %d, want 2: %#v", len(infos), infos)
	}
	journals, err := repo.ListCheckpointJournals()
	if err != nil {
		t.Fatalf("ListCheckpointJournals: %v", err)
	}
	if len(journals) != 0 {
		t.Fatalf("wrapper left checkpoint journals: %#v", journals)
	}
	inventory, err := runs.Inspect(repo)
	if err != nil {
		t.Fatalf("inspect runs: %v", err)
	}
	if len(inventory.Runs) != 1 || inventory.Runs[0].Shape != "wrapper-only" || inventory.Runs[0].Status != runs.StatusSucceeded {
		t.Fatalf("wrapper-only run projection = %+v", inventory)
	}
	sessionID := infos[0].SessionID
	turnID := infos[0].TurnID
	if !strings.HasPrefix(sessionID.String(), "codex-run-") {
		t.Fatalf("wrapper session = %s, want codex-run-*", sessionID)
	}
	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	if !containsAllCLI(string(diff), "-before", "+after") {
		t.Fatalf("wrapper diff missing child change:\n%s", diff)
	}

	configPath := filepath.Join(root.String(), ".codex", "config.toml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(configData), "hooks = true") || !strings.Contains(string(configData), "turnal codex-hook") {
		t.Fatalf("Codex config missing hook setup:\n%s", configData)
	}
}

func TestRunExternalProviderWrappersCreateCheckpointsAndInstallIntegration(t *testing.T) {
	for _, test := range []struct {
		name       string
		command    string
		adapter    primitives.AdapterName
		configPath string
		wantConfig string
	}{
		{name: "cursor", command: "cursor", adapter: primitives.AdapterCursor, configPath: filepath.Join(".cursor", "hooks.json"), wantConfig: "adapter capture cursor beforeSubmitPrompt"},
		{name: "pi", command: "pi", adapter: primitives.AdapterPi, configPath: filepath.Join(".pi", "extensions", "turnal.ts"), wantConfig: `"capture", "pi"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireGit(t)
			isolateAgentConfig(t)
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root.String())
			installFakeRunProvider(t, root.String(), test.command, 0)
			writeFile(t, root, "app.txt", "before\n")

			cmd := NewRootCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"run", "--quiet", "--", test.command, "change"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("run %s: %v", test.command, err)
			}
			infos, err := repo.ListAllCheckpointRefInfos()
			if err != nil || len(infos) != 2 {
				t.Fatalf("checkpoint infos = %#v err=%v", infos, err)
			}
			if !strings.HasPrefix(infos[0].SessionID.String(), test.adapter.String()+"-run-") {
				t.Fatalf("wrapper session = %s", infos[0].SessionID)
			}
			inventory, err := runs.Inspect(repo)
			if err != nil || len(inventory.Runs) != 1 || inventory.Runs[0].Status != runs.StatusSucceeded {
				t.Fatalf("run inventory = %+v err=%v", inventory, err)
			}
			config, err := os.ReadFile(filepath.Join(root.String(), test.configPath))
			if err != nil || !strings.Contains(string(config), test.wantConfig) {
				t.Fatalf("provider integration = %q err=%v", config, err)
			}
		})
	}
}

func TestRunRejectsCodexTrustFlagForExternalProviders(t *testing.T) {
	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", "--bypass-hook-trust", "--", "pi"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "applies only to Codex") {
		t.Fatalf("run error = %v", err)
	}
}

func TestWaitForChildEscalatesRepeatedSignal(t *testing.T) {
	done := make(chan error, 1)
	signals := make(chan os.Signal, 2)
	forwarded := make(chan os.Signal, 1)
	killed := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- waitForChild(done, signals, func(value os.Signal) error {
			forwarded <- value
			return nil
		}, func() error {
			killed <- struct{}{}
			return nil
		})
	}()
	signals <- os.Interrupt
	if got := <-forwarded; got != os.Interrupt {
		t.Fatalf("forwarded signal = %v, want interrupt", got)
	}
	signals <- os.Interrupt
	select {
	case <-killed:
	case <-time.After(time.Second):
		t.Fatal("second signal did not kill child")
	}
	done <- nil
	if err := <-result; err != nil {
		t.Fatalf("waitForChild: %v", err)
	}
}

func TestRunCodexWrapperUsesGlobalConfig(t *testing.T) {
	requireGit(t)
	writeGlobalAgentConfig(t, `
version = 1

[run]
quiet = true
bypass_hook_trust = true

[hooks]
command = "/tmp/custom-turnal"
`)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	argsPath := installFakeCodex(t, root.String(), 0)
	writeFile(t, root, "app.txt", "before\n")

	cmd := NewRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"run", "--", "codex", "exec", "change app.txt"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run command: %v\nstdout=%s\nstderr=%s", err, out.String(), stderr.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake args: %v", err)
	}
	if !strings.Contains(string(argsData), "--dangerously-bypass-hook-trust\n") {
		t.Fatalf("fake codex args missing bypass hook trust:\n%s", argsData)
	}
	if strings.Contains(stderr.String(), "turnal: recorded wrapper checkpoints") {
		t.Fatalf("quiet global config did not suppress wrapper status:\n%s", stderr.String())
	}

	configData, err := os.ReadFile(filepath.Join(root.String(), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(configData), "/tmp/custom-turnal codex-hook") {
		t.Fatalf("Codex config missing custom hook command:\n%s", configData)
	}
}

func TestRunCodexWrapperPropagatesChildExitCodeAndFinishesTurn(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	installFakeCodex(t, root.String(), 37)

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"run", "--quiet", "--bypass-hook-trust", "--", "codex", "exec", "fail"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("run command succeeded, want child exit error")
	}
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 37 {
		t.Fatalf("run error = %v, want exit code 37", err)
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		t.Fatalf("ListAllCheckpointRefInfos: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("checkpoint refs = %d, want pre and post despite child failure: %#v", len(infos), infos)
	}
	inventory, inspectErr := runs.Inspect(repo)
	if inspectErr != nil {
		t.Fatalf("inspect failed run: %v", inspectErr)
	}
	if len(inventory.Runs) != 1 || inventory.Runs[0].Status != runs.StatusFailed || inventory.Runs[0].Shape != "wrapper-only" {
		t.Fatalf("failed run projection = %+v", inventory)
	}

	argsData, err := os.ReadFile(filepath.Join(root.String(), "fake-codex-args.txt"))
	if err != nil {
		t.Fatalf("read fake args: %v", err)
	}
	if !strings.Contains(string(argsData), "--dangerously-bypass-hook-trust\n") {
		t.Fatalf("fake codex args missing bypass hook trust:\n%s", argsData)
	}
}

func TestRunCodexSetupFailureFinalizesRunIncomplete(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root.String())
	installFakeCodex(t, root.String(), 0)
	journalDir := filepath.Join(repo.TmpDir, "checkpoints")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, "broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", "--quiet", "--", "codex", "exec", "never-starts"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("setup failure unexpectedly succeeded")
	}
	inventory, err := runs.Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Runs) != 1 || inventory.Runs[0].Status != runs.StatusIncomplete {
		t.Fatalf("setup failure projection = %+v", inventory)
	}
}

func TestRunCodexLiveEndToEnd(t *testing.T) {
	if os.Getenv("TURNAL_LIVE_CODEX_TEST") != "1" {
		t.Skip("set TURNAL_LIVE_CODEX_TEST=1 to run authenticated Codex integration test")
	}
	isolateAgentConfig(t)
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex executable not found")
	}
	requireGit(t)

	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "turnal")
	build := exec.Command("go", "build", "-o", bin, "./cmd/turnal")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build turnal: %v\n%s", err, output)
	}

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	codexHome := liveCodexHome(t, root.String())
	live := exec.CommandContext(ctx, bin, "run", "--quiet", "--bypass-hook-trust", "--", "codex", "--ask-for-approval", "never", "exec", "--skip-git-repo-check", "--sandbox", "read-only", "Reply with exactly: turnal-live")
	live.Dir = root.String()
	live.Env = append(os.Environ(), "TURNAL_HOOK_COMMAND="+bin, "CODEX_HOME="+codexHome)
	output, err := live.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("live Codex test timed out:\n%s", output)
	}
	if err != nil {
		t.Fatalf("live Codex run failed: %v\n%s", err, output)
	}

	rawCount, err := codexRawRecordCount(repo.MetadataDir)
	if err != nil {
		t.Fatalf("codex raw record count: %v", err)
	}
	if rawCount == 0 {
		t.Fatalf("live Codex run produced no raw hook records:\n%s", output)
	}
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		t.Fatalf("ListAllCheckpointRefInfos: %v", err)
	}
	if len(infos) < 2 {
		t.Fatalf("checkpoint refs = %d, want at least wrapper pre/post", len(infos))
	}
}

func TestRunCursorLiveEndToEnd(t *testing.T) {
	if os.Getenv("TURNAL_LIVE_CURSOR_TEST") != "1" {
		t.Skip("set TURNAL_LIVE_CURSOR_TEST=1 to run the authenticated Cursor integration test")
	}
	runLiveExternalProvider(t, "agent", primitives.AdapterCursor, []string{"--print", "--mode", "ask", "--trust", "Reply with exactly: turnal-live"})
}

func TestRunPiLiveEndToEnd(t *testing.T) {
	if os.Getenv("TURNAL_LIVE_PI_TEST") != "1" {
		t.Skip("set TURNAL_LIVE_PI_TEST=1 to run the authenticated Pi integration test")
	}
	runLiveExternalProvider(t, "pi", primitives.AdapterPi, []string{"--print", "--approve", "--no-tools", "Reply with exactly: turnal-live"})
}

func runLiveExternalProvider(t *testing.T, executable string, adapter primitives.AdapterName, providerArgs []string) {
	t.Helper()
	isolateAgentConfig(t)
	providerPath, err := exec.LookPath(executable)
	if err != nil {
		t.Skipf("%s executable not found", executable)
	}
	requireGit(t)
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	turnalBin := filepath.Join(binDir, "turnal")
	for output, buildErr := range map[string]error{
		"turnal":  buildGoCommand(moduleRoot, turnalBin, "./cmd/turnal"),
		"adapter": buildGoCommand(moduleRoot, filepath.Join(binDir, "turnal-adapter-"+adapter.String()), "./cmd/turnal-adapter-"+adapter.String()),
	} {
		if buildErr != nil {
			t.Fatalf("build live %s: %v", output, buildErr)
		}
	}
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"run", "--quiet", "--", providerPath}, providerArgs...)
	live := exec.CommandContext(ctx, turnalBin, args...)
	live.Dir = root.String()
	live.Env = append(os.Environ(), "TURNAL_HOOK_COMMAND="+turnalBin, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := live.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("live %s test timed out:\n%s", adapter, output)
	}
	if err != nil {
		t.Fatalf("live %s run failed: %v\n%s", adapter, err, output)
	}
	rawCount, err := adapterRawRecordCount(repo.MetadataDir, adapter)
	if err != nil || rawCount == 0 {
		t.Fatalf("live %s raw records = %d err=%v\n%s", adapter, rawCount, err, output)
	}
	inventory, err := runs.Inspect(repo)
	if err != nil || len(inventory.Runs) != 1 || inventory.Runs[0].Shape == "wrapper-only" {
		t.Fatalf("live %s run did not correlate provider capture: %+v err=%v\n%s", adapter, inventory, err, output)
	}
}

func buildGoCommand(moduleRoot, outputPath, packagePath string) error {
	command := exec.Command("go", "build", "-o", outputPath, packagePath)
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func liveCodexHome(t *testing.T, trustedProjectRoot string) string {
	t.Helper()
	sourceHome := os.Getenv("CODEX_HOME")
	if sourceHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("resolve home directory for Codex auth: %v", err)
		}
		sourceHome = filepath.Join(home, ".codex")
	}

	authData, err := os.ReadFile(filepath.Join(sourceHome, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("Codex auth file not found")
		}
		t.Fatalf("read Codex auth file: %v", err)
	}

	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), authData, 0o600); err != nil {
		t.Fatalf("write isolated Codex auth file: %v", err)
	}
	config := "[projects." + strconv.Quote(trustedProjectRoot) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write isolated Codex config: %v", err)
	}
	return codexHome
}

func installFakeCodex(t *testing.T, root string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex fixture requires a POSIX shell")
	}
	binDir := t.TempDir()
	argsPath := filepath.Join(root, "fake-codex-args.txt")
	script := filepath.Join(binDir, "codex")
	content := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$FAKE_CODEX_ARGS"
done
printf 'fake codex stdout\n'
printf 'fake codex stderr\n' >&2
printf 'after\n' > app.txt
exit "$FAKE_CODEX_EXIT"
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("FAKE_CODEX_ARGS", argsPath)
	t.Setenv("FAKE_CODEX_EXIT", strconv.Itoa(exitCode))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func installFakeRunProvider(t *testing.T, root, name string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake provider fixture requires a POSIX shell")
	}
	binDir := t.TempDir()
	script := filepath.Join(binDir, name)
	content := `#!/bin/sh
printf 'after\n' > app.txt
exit "$FAKE_PROVIDER_EXIT"
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PROVIDER_EXIT", strconv.Itoa(exitCode))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func containsAllCLI(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
}
