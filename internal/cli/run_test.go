package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
)

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

	argsData, err := os.ReadFile(filepath.Join(root.String(), "fake-codex-args.txt"))
	if err != nil {
		t.Fatalf("read fake args: %v", err)
	}
	if !strings.Contains(string(argsData), "--dangerously-bypass-hook-trust\n") {
		t.Fatalf("fake codex args missing bypass hook trust:\n%s", argsData)
	}
}

func TestRunCodexLiveEndToEnd(t *testing.T) {
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
	live := exec.CommandContext(ctx, bin, "run", "--quiet", "--bypass-hook-trust", "--", "codex", "--ask-for-approval", "never", "exec", "--sandbox", "read-only", "Reply with exactly: turnal-live")
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

func containsAllCLI(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
}
