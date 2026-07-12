package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestResolveVerifierConfiguration(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, WorkspacePath(root), `
version = 1

[[verify]]
name = "unit-tests"
command = "go"
args = ["test", "./..."]
timeout = "2m"

[[verify]]
name = "literal-arguments"
command = "test helper"
args = ["$HOME", "argument with spaces"]
timeout = "1500ms"
`)

	effective, origins, err := testLoader(t, nil).Resolve(root, Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(effective.Verify) != 2 {
		t.Fatalf("verify count = %d, want 2", len(effective.Verify))
	}
	if effective.Verify[0].Name != "unit-tests" || effective.Verify[1].Name != "literal-arguments" {
		t.Fatalf("verifier order = %#v", effective.Verify)
	}
	if effective.Verify[0].Command != "go" || effective.Verify[0].Timeout != 2*time.Minute {
		t.Fatalf("first verifier = %#v", effective.Verify[0])
	}
	if got := effective.Verify[1].Args; len(got) != 2 || got[0] != "$HOME" || got[1] != "argument with spaces" {
		t.Fatalf("literal args = %#v", got)
	}
	if origins["verify"] != OriginWorkspace {
		t.Fatalf("verify origin = %q, want workspace", origins["verify"])
	}
}

func TestResolveExistingConfigurationHasNoVerifiers(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, WorkspacePath(root), "version = 1\n[run]\nquiet = true\n")

	effective, _, err := testLoader(t, nil).Resolve(root, Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if effective.Verify == nil || len(effective.Verify) != 0 {
		t.Fatalf("verify = %#v, want non-nil empty list", effective.Verify)
	}
}

func TestResolveRejectsInvalidVerifiers(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "duplicate name", body: verifierTOML("same", "helper", `timeout = "1s"`) + verifierTOML("same", "helper", `timeout = "1s"`), wantErr: `verify[2] "same": name duplicates verify[1]`},
		{name: "empty name", body: verifierTOML("", "helper", `timeout = "1s"`), wantErr: "verify[1]: name must not be empty"},
		{name: "empty command", body: verifierTOML("empty-command", "", `timeout = "1s"`), wantErr: `verify[1] "empty-command": command must not be empty`},
		{name: "missing timeout", body: verifierTOML("missing-timeout", "helper", ""), wantErr: `verify[1] "missing-timeout": timeout must not be empty`},
		{name: "invalid timeout", body: verifierTOML("bad-timeout", "helper", `timeout = "later"`), wantErr: `verify[1] "bad-timeout": timeout`},
		{name: "zero timeout", body: verifierTOML("zero-timeout", "helper", `timeout = "0s"`), wantErr: `verify[1] "zero-timeout": timeout must be positive`},
		{name: "negative timeout", body: verifierTOML("negative-timeout", "helper", `timeout = "-1s"`), wantErr: `verify[1] "negative-timeout": timeout must be positive`},
		{name: "nul name", body: "\n[[verify]]\nname = \"bad\\u0000name\"\ncommand = \"helper\"\ntimeout = \"1s\"\n", wantErr: `verify[1] "bad\x00name": name must not contain NUL`},
		{name: "nul command", body: "\n[[verify]]\nname = \"nul-command\"\ncommand = \"bad\\u0000command\"\ntimeout = \"1s\"\n", wantErr: `verify[1] "nul-command": command must not contain NUL`},
		{name: "nul argument", body: verifierTOML("nul-argument", "helper", "args = [\"bad\\u0000arg\"]\ntimeout = \"1s\""), wantErr: `verify[1] "nul-argument": args[1] must not contain NUL`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, WorkspacePath(root), "version = 1\n"+test.body)
			_, _, err := testLoader(t, nil).Resolve(root, Overrides{})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Resolve error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestResolveRejectsVerifierBounds(t *testing.T) {
	t.Run("verifier count", func(t *testing.T) {
		root := t.TempDir()
		var body strings.Builder
		body.WriteString("version = 1\n")
		for index := 0; index <= MaxVerifierCount; index++ {
			body.WriteString(verifierTOML(fmt.Sprintf("check-%d", index), "helper", `timeout = "1s"`))
		}
		writeConfig(t, WorkspacePath(root), body.String())
		_, _, err := testLoader(t, nil).Resolve(root, Overrides{})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d entries", MaxVerifierCount)) {
			t.Fatalf("Resolve error = %v", err)
		}
	})

	t.Run("argument count", func(t *testing.T) {
		root := t.TempDir()
		args := make([]string, MaxVerifierArgCount+1)
		for index := range args {
			args[index] = `"arg"`
		}
		body := verifierTOML("too-many-args", "helper", "args = ["+strings.Join(args, ",")+"]\ntimeout = \"1s\"")
		writeConfig(t, WorkspacePath(root), "version = 1\n"+body)
		_, _, err := testLoader(t, nil).Resolve(root, Overrides{})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d arguments", MaxVerifierArgCount)) {
			t.Fatalf("Resolve error = %v", err)
		}
	})

	t.Run("argument size", func(t *testing.T) {
		root := t.TempDir()
		body := verifierTOML("large-arg", "helper", fmt.Sprintf("args = [%q]\ntimeout = \"1s\"", strings.Repeat("x", MaxVerifierArgBytes+1)))
		writeConfig(t, WorkspacePath(root), "version = 1\n"+body)
		_, _, err := testLoader(t, nil).Resolve(root, Overrides{})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("args[1] must be at most %d bytes", MaxVerifierArgBytes)) {
			t.Fatalf("Resolve error = %v", err)
		}
	})
}

func verifierTOML(name, command, extra string) string {
	return fmt.Sprintf("\n[[verify]]\nname = %q\ncommand = %q\n%s\n", name, command, extra)
}

func TestResolveDefaultsWhenFilesAreMissing(t *testing.T) {
	loader := testLoader(t, map[string]string{})

	effective, origins, err := loader.Resolve("", Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if effective.Init.Agent != "auto" || !effective.Init.InstallHooks {
		t.Fatalf("init defaults = %+v", effective.Init)
	}
	if !effective.Run.InstallHooks || effective.Run.Quiet || effective.Run.BypassHookTrust {
		t.Fatalf("run defaults = %+v", effective.Run)
	}
	if effective.Hooks.Command == "" {
		t.Fatal("hooks command default is empty")
	}
	if !effective.Bootstrap.UpdateGitignore {
		t.Fatalf("bootstrap defaults = %+v", effective.Bootstrap)
	}
	if effective.GitSync.Enabled {
		t.Fatal("git-sync default = true, want false")
	}
	if effective.Rollback.Mode != primitives.RollbackModeCheckpoint {
		t.Fatalf("rollback mode default = %q, want checkpoint", effective.Rollback.Mode)
	}
	if origins["run.quiet"] != OriginDefault {
		t.Fatalf("run.quiet origin = %q, want default", origins["run.quiet"])
	}
}

func TestResolveMergesWorkspaceOverGlobal(t *testing.T) {
	root := t.TempDir()
	userConfigDir := t.TempDir()
	writeConfig(t, filepath.Join(userConfigDir, "turnal", "config.toml"), `
version = 1

[init]
agent = "all"
install_hooks = true

[run]
quiet = true

[hooks]
command = "global-turnal"

[bootstrap]
update_gitignore = true

[git_sync]
enabled = true

[rollback]
mode = "workspace-git"
`)
	writeConfig(t, WorkspacePath(root), `
version = 1

[init]
install_hooks = false

[hooks]
command = "workspace-turnal"

[bootstrap]
update_gitignore = false

[git_sync]
enabled = false
`)

	loader := Loader{
		UserConfigDir: func() (string, error) { return userConfigDir, nil },
		ReadFile:      os.ReadFile,
		LookupEnv:     func(string) (string, bool) { return "", false },
	}
	effective, origins, err := loader.Resolve(root, Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if effective.Init.Agent != "all" {
		t.Fatalf("init agent = %q, want global all", effective.Init.Agent)
	}
	if effective.Init.InstallHooks {
		t.Fatal("workspace install_hooks=false did not override global true")
	}
	if !effective.Run.Quiet {
		t.Fatal("global run.quiet=true was not applied")
	}
	if effective.Hooks.Command != "workspace-turnal" {
		t.Fatalf("hooks command = %q, want workspace command", effective.Hooks.Command)
	}
	if effective.Bootstrap.UpdateGitignore {
		t.Fatal("workspace bootstrap update_gitignore=false was not applied")
	}
	if effective.GitSync.Enabled {
		t.Fatal("workspace git_sync.enabled=false did not override global true")
	}
	if effective.Rollback.Mode != primitives.RollbackModeWorkspaceGit {
		t.Fatalf("rollback mode = %q, want global workspace-git", effective.Rollback.Mode)
	}
	if origins["hooks.command"] != OriginWorkspace {
		t.Fatalf("hooks.command origin = %q, want workspace", origins["hooks.command"])
	}
}

func TestResolveEnvAndOverridesWin(t *testing.T) {
	loader := testLoader(t, map[string]string{
		HookCommandEnvVar: "env-turnal",
	})
	installHooks := false
	quiet := false
	agent := "none"
	gitSync := true
	rollbackMode := primitives.RollbackModeWorkspaceGit

	effective, origins, err := loader.Resolve("", Overrides{
		InitAgent:       &agent,
		RunInstallHooks: &installHooks,
		RunQuiet:        &quiet,
		GitSyncEnabled:  &gitSync,
		RollbackMode:    &rollbackMode,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if effective.Hooks.Command != "env-turnal" {
		t.Fatalf("hooks command = %q, want env", effective.Hooks.Command)
	}
	if effective.Init.Agent != "none" {
		t.Fatalf("init agent = %q, want flag override", effective.Init.Agent)
	}
	if effective.Run.InstallHooks {
		t.Fatal("flag run install hooks false was not applied")
	}
	if effective.Run.Quiet {
		t.Fatal("flag run quiet false was not applied")
	}
	if !effective.GitSync.Enabled {
		t.Fatal("flag git-sync true was not applied")
	}
	if effective.Rollback.Mode != primitives.RollbackModeWorkspaceGit {
		t.Fatalf("flag rollback mode = %q, want workspace-git", effective.Rollback.Mode)
	}
	if origins["hooks.command"] != OriginEnv {
		t.Fatalf("hooks.command origin = %q, want env", origins["hooks.command"])
	}
	if origins["run.install_hooks"] != OriginFlag {
		t.Fatalf("run.install_hooks origin = %q, want flag", origins["run.install_hooks"])
	}
	if origins["git_sync.enabled"] != OriginFlag {
		t.Fatalf("git_sync.enabled origin = %q, want flag", origins["git_sync.enabled"])
	}
}

func TestGlobalPathUsesEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "turnal-test.toml")
	loader := testLoader(t, map[string]string{
		ConfigEnvVar: want,
	})

	path, err := loader.GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if path != want {
		t.Fatalf("GlobalPath = %q, want env override", path)
	}
}

func TestGlobalPathPropagatesUserConfigDirError(t *testing.T) {
	want := errors.New("no config dir")
	loader := Loader{
		UserConfigDir: func() (string, error) { return "", want },
		ReadFile:      os.ReadFile,
		LookupEnv:     func(string) (string, bool) { return "", false },
	}

	_, err := loader.GlobalPath()
	if !errors.Is(err, want) {
		t.Fatalf("GlobalPath error = %v, want %v", err, want)
	}
}

func TestResolveReportsInvalidTOMLWithPath(t *testing.T) {
	userConfigDir := t.TempDir()
	path := filepath.Join(userConfigDir, "turnal", "config.toml")
	writeConfig(t, path, "[broken\n")
	loader := Loader{
		UserConfigDir: func() (string, error) { return userConfigDir, nil },
		ReadFile:      os.ReadFile,
		LookupEnv:     func(string) (string, bool) { return "", false },
	}

	_, _, err := loader.Resolve("", Overrides{})
	if err == nil {
		t.Fatal("Resolve succeeded, want parse error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not include path %s", err, path)
	}
}

func TestResolveRejectsUnknownAgent(t *testing.T) {
	userConfigDir := t.TempDir()
	path := filepath.Join(userConfigDir, "turnal", "config.toml")
	writeConfig(t, path, `
version = 1

[init]
agent = "both"
`)
	loader := Loader{
		UserConfigDir: func() (string, error) { return userConfigDir, nil },
		ReadFile:      os.ReadFile,
		LookupEnv:     func(string) (string, bool) { return "", false },
	}

	_, _, err := loader.Resolve("", Overrides{})
	if err == nil {
		t.Fatal("Resolve succeeded, want invalid agent error")
	}
	if !strings.Contains(err.Error(), "invalid init.agent") {
		t.Fatalf("error = %v, want invalid init.agent", err)
	}
}

func TestResolveRejectsUnsupportedVersion(t *testing.T) {
	userConfigDir := t.TempDir()
	path := filepath.Join(userConfigDir, "turnal", "config.toml")
	writeConfig(t, path, "version = 2\n")
	loader := Loader{
		UserConfigDir: func() (string, error) { return userConfigDir, nil },
		ReadFile:      os.ReadFile,
		LookupEnv:     func(string) (string, bool) { return "", false },
	}

	_, _, err := loader.Resolve("", Overrides{})
	if err == nil {
		t.Fatal("Resolve succeeded, want unsupported version error")
	}
	if !strings.Contains(err.Error(), "unsupported version 2") {
		t.Fatalf("error = %v, want unsupported version", err)
	}
}

func testLoader(t *testing.T, env map[string]string) Loader {
	t.Helper()
	userConfigDir := t.TempDir()
	return Loader{
		UserConfigDir: func() (string, error) { return userConfigDir, nil },
		ReadFile:      os.ReadFile,
		LookupEnv: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}
