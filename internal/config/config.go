package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-vcs-again/internal/hookcmd"
	"agent-vcs-again/internal/primitives"
	"github.com/pelletier/go-toml/v2"
)

const (
	ConfigEnvVar      = "AGENT_VCS_CONFIG"
	HookCommandEnvVar = "AGENT_VCS_HOOK_COMMAND"
)

type Origin string

const (
	OriginDefault   Origin = "default"
	OriginGlobal    Origin = "global"
	OriginWorkspace Origin = "workspace"
	OriginEnv       Origin = "env"
	OriginFlag      Origin = "flag"
)

type File struct {
	Version   *int           `toml:"version,omitempty"`
	Init      *InitFile      `toml:"init,omitempty"`
	Run       *RunFile       `toml:"run,omitempty"`
	Hooks     *HooksFile     `toml:"hooks,omitempty"`
	Bootstrap *BootstrapFile `toml:"bootstrap,omitempty"`
	GitSync   *GitSyncFile   `toml:"git_sync,omitempty"`
	Rollback  *RollbackFile  `toml:"rollback,omitempty"`
}

type InitFile struct {
	Agent        *string `toml:"agent,omitempty"`
	InstallHooks *bool   `toml:"install_hooks,omitempty"`
}

type RunFile struct {
	InstallHooks    *bool `toml:"install_hooks,omitempty"`
	Quiet           *bool `toml:"quiet,omitempty"`
	BypassHookTrust *bool `toml:"bypass_hook_trust,omitempty"`
}

type HooksFile struct {
	Command *string `toml:"command,omitempty"`
}

type BootstrapFile struct {
	InitWorkspaceGit *bool `toml:"init_workspace_git,omitempty"`
	UpdateGitignore  *bool `toml:"update_gitignore,omitempty"`
}

type GitSyncFile struct {
	Enabled *bool `toml:"enabled,omitempty"`
}

type RollbackFile struct {
	Mode *string `toml:"mode,omitempty"`
}

type Effective struct {
	Init      Init
	Run       Run
	Hooks     Hooks
	Bootstrap Bootstrap
	GitSync   GitSync
	Rollback  Rollback
}

type Init struct {
	Agent        string
	InstallHooks bool
}

type Run struct {
	InstallHooks    bool
	Quiet           bool
	BypassHookTrust bool
}

type Hooks struct {
	Command string
}

type Bootstrap struct {
	InitWorkspaceGit bool
	UpdateGitignore  bool
}

type GitSync struct {
	Enabled bool
}

type Rollback struct {
	Mode primitives.RollbackMode
}

type Overrides struct {
	InitAgent                 *string
	InitInstallHooks          *bool
	RunInstallHooks           *bool
	RunQuiet                  *bool
	RunBypassHookTrust        *bool
	BootstrapInitWorkspaceGit *bool
	BootstrapUpdateGitignore  *bool
	GitSyncEnabled            *bool
	RollbackMode              *primitives.RollbackMode
}

type Loader struct {
	UserConfigDir func() (string, error)
	ReadFile      func(string) ([]byte, error)
	LookupEnv     func(string) (string, bool)
}

func DefaultLoader() Loader {
	return Loader{
		UserConfigDir: os.UserConfigDir,
		ReadFile:      os.ReadFile,
		LookupEnv:     os.LookupEnv,
	}
}

func Defaults() Effective {
	return Effective{
		Init: Init{
			Agent:        "auto",
			InstallHooks: true,
		},
		Run: Run{
			InstallHooks:    true,
			Quiet:           false,
			BypassHookTrust: false,
		},
		Hooks: Hooks{
			Command: hookcmd.Default(),
		},
		Bootstrap: Bootstrap{
			InitWorkspaceGit: true,
			UpdateGitignore:  true,
		},
		GitSync: GitSync{
			Enabled: false,
		},
		Rollback: Rollback{
			Mode: primitives.RollbackModeCheckpoint,
		},
	}
}

func GlobalPath() (string, error) {
	return DefaultLoader().GlobalPath()
}

func (l Loader) GlobalPath() (string, error) {
	lookupEnv := l.lookupEnv()
	if configured, ok := lookupEnv(ConfigEnvVar); ok {
		configured = strings.TrimSpace(configured)
		if configured != "" {
			return filepath.Clean(configured), nil
		}
	}

	userConfigDir, err := l.userConfigDir()()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(userConfigDir, "agent-vcs", "config.toml"), nil
}

func WorkspacePath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".agent-vcs", "config.toml")
}

func ReadFileLayer(path string) (File, error) {
	return DefaultLoader().ReadFileLayer(path)
}

func (l Loader) ReadFileLayer(path string) (File, error) {
	data, err := l.readFile()(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return File{}, nil
	}

	var file File
	if err := toml.Unmarshal(data, &file); err != nil {
		return File{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, nil
}

func Resolve(workspaceRoot string, overrides Overrides) (Effective, map[string]Origin, error) {
	return DefaultLoader().Resolve(workspaceRoot, overrides)
}

func (l Loader) Resolve(workspaceRoot string, overrides Overrides) (Effective, map[string]Origin, error) {
	effective := Defaults()
	origins := defaultOrigins()

	globalPath, err := l.GlobalPath()
	if err != nil {
		return Effective{}, nil, err
	}
	globalFile, err := l.ReadFileLayer(globalPath)
	if err != nil {
		return Effective{}, nil, err
	}
	if err := applyFile(&effective, origins, globalFile, OriginGlobal); err != nil {
		return Effective{}, nil, fmt.Errorf("config %s: %w", globalPath, err)
	}

	if strings.TrimSpace(workspaceRoot) != "" {
		workspacePath := WorkspacePath(workspaceRoot)
		workspaceFile, err := l.ReadFileLayer(workspacePath)
		if err != nil {
			return Effective{}, nil, err
		}
		if err := applyFile(&effective, origins, workspaceFile, OriginWorkspace); err != nil {
			return Effective{}, nil, fmt.Errorf("config %s: %w", workspacePath, err)
		}
	}

	if command, ok := l.lookupEnv()(HookCommandEnvVar); ok {
		command = strings.TrimSpace(command)
		if command != "" {
			effective.Hooks.Command = command
			origins["hooks.command"] = OriginEnv
		}
	}

	if err := applyOverrides(&effective, origins, overrides); err != nil {
		return Effective{}, nil, err
	}
	return effective, origins, nil
}

func (l Loader) userConfigDir() func() (string, error) {
	if l.UserConfigDir != nil {
		return l.UserConfigDir
	}
	return os.UserConfigDir
}

func (l Loader) readFile() func(string) ([]byte, error) {
	if l.ReadFile != nil {
		return l.ReadFile
	}
	return os.ReadFile
}

func (l Loader) lookupEnv() func(string) (string, bool) {
	if l.LookupEnv != nil {
		return l.LookupEnv
	}
	return os.LookupEnv
}

func defaultOrigins() map[string]Origin {
	return map[string]Origin{
		"init.agent":                   OriginDefault,
		"init.install_hooks":           OriginDefault,
		"run.install_hooks":            OriginDefault,
		"run.quiet":                    OriginDefault,
		"run.bypass_hook_trust":        OriginDefault,
		"hooks.command":                OriginDefault,
		"bootstrap.init_workspace_git": OriginDefault,
		"bootstrap.update_gitignore":   OriginDefault,
		"git_sync.enabled":             OriginDefault,
		"rollback.mode":                OriginDefault,
	}
}

func applyFile(effective *Effective, origins map[string]Origin, file File, origin Origin) error {
	if file.Version != nil && *file.Version != 1 {
		return fmt.Errorf("unsupported version %d", *file.Version)
	}
	if file.Init != nil {
		if file.Init.Agent != nil {
			agent, err := normalizeAgent(*file.Init.Agent)
			if err != nil {
				return err
			}
			effective.Init.Agent = agent
			origins["init.agent"] = origin
		}
		if file.Init.InstallHooks != nil {
			effective.Init.InstallHooks = *file.Init.InstallHooks
			origins["init.install_hooks"] = origin
		}
	}
	if file.Run != nil {
		if file.Run.InstallHooks != nil {
			effective.Run.InstallHooks = *file.Run.InstallHooks
			origins["run.install_hooks"] = origin
		}
		if file.Run.Quiet != nil {
			effective.Run.Quiet = *file.Run.Quiet
			origins["run.quiet"] = origin
		}
		if file.Run.BypassHookTrust != nil {
			effective.Run.BypassHookTrust = *file.Run.BypassHookTrust
			origins["run.bypass_hook_trust"] = origin
		}
	}
	if file.Hooks != nil {
		if file.Hooks.Command != nil {
			command := strings.TrimSpace(*file.Hooks.Command)
			if command == "" {
				return fmt.Errorf("hooks.command must not be empty")
			}
			effective.Hooks.Command = command
			origins["hooks.command"] = origin
		}
	}
	if file.Bootstrap != nil {
		if file.Bootstrap.InitWorkspaceGit != nil {
			effective.Bootstrap.InitWorkspaceGit = *file.Bootstrap.InitWorkspaceGit
			origins["bootstrap.init_workspace_git"] = origin
		}
		if file.Bootstrap.UpdateGitignore != nil {
			effective.Bootstrap.UpdateGitignore = *file.Bootstrap.UpdateGitignore
			origins["bootstrap.update_gitignore"] = origin
		}
	}
	if file.GitSync != nil {
		if file.GitSync.Enabled != nil {
			effective.GitSync.Enabled = *file.GitSync.Enabled
			origins["git_sync.enabled"] = origin
		}
	}
	if file.Rollback != nil {
		if file.Rollback.Mode != nil {
			mode, err := primitives.ParseRollbackMode(*file.Rollback.Mode)
			if err != nil {
				return err
			}
			effective.Rollback.Mode = mode
			origins["rollback.mode"] = origin
		}
	}
	return nil
}

func applyOverrides(effective *Effective, origins map[string]Origin, overrides Overrides) error {
	if overrides.InitAgent != nil {
		agent, err := normalizeAgent(*overrides.InitAgent)
		if err != nil {
			return err
		}
		effective.Init.Agent = agent
		origins["init.agent"] = OriginFlag
	}
	if overrides.InitInstallHooks != nil {
		effective.Init.InstallHooks = *overrides.InitInstallHooks
		origins["init.install_hooks"] = OriginFlag
	}
	if overrides.RunInstallHooks != nil {
		effective.Run.InstallHooks = *overrides.RunInstallHooks
		origins["run.install_hooks"] = OriginFlag
	}
	if overrides.RunQuiet != nil {
		effective.Run.Quiet = *overrides.RunQuiet
		origins["run.quiet"] = OriginFlag
	}
	if overrides.RunBypassHookTrust != nil {
		effective.Run.BypassHookTrust = *overrides.RunBypassHookTrust
		origins["run.bypass_hook_trust"] = OriginFlag
	}
	if overrides.BootstrapInitWorkspaceGit != nil {
		effective.Bootstrap.InitWorkspaceGit = *overrides.BootstrapInitWorkspaceGit
		origins["bootstrap.init_workspace_git"] = OriginFlag
	}
	if overrides.BootstrapUpdateGitignore != nil {
		effective.Bootstrap.UpdateGitignore = *overrides.BootstrapUpdateGitignore
		origins["bootstrap.update_gitignore"] = OriginFlag
	}
	if overrides.GitSyncEnabled != nil {
		effective.GitSync.Enabled = *overrides.GitSyncEnabled
		origins["git_sync.enabled"] = OriginFlag
	}
	if overrides.RollbackMode != nil {
		mode, err := primitives.ParseRollbackMode(overrides.RollbackMode.String())
		if err != nil {
			return err
		}
		effective.Rollback.Mode = mode
		origins["rollback.mode"] = OriginFlag
	}
	return nil
}

func normalizeAgent(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "auto", "claude", "codex", "all", "none":
		return value, nil
	default:
		return "", fmt.Errorf("invalid init.agent %q; expected auto, claude, codex, all, or none", value)
	}
}
