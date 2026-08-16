package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/agentskills"
	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var agent string
	var skipHooks bool
	var enableGitSync bool
	var updateGitignore bool
	var storePath string

	cmd := &cobra.Command{
		Use:          "init",
		Short:        "Initialize turnal metadata in this workspace",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			root, err := primitives.ParseWorkspaceRoot(cwd)
			if err != nil {
				return err
			}

			overrides := agentconfig.Overrides{}
			if cmd.Flags().Changed("agent") {
				overrides.InitAgent = &agent
			}
			if cmd.Flags().Changed("skip-hooks") {
				installHooks := !skipHooks
				overrides.InitInstallHooks = &installHooks
			}
			if cmd.Flags().Changed("git-sync") {
				overrides.GitSyncEnabled = &enableGitSync
			}
			if cmd.Flags().Changed("update-gitignore") {
				overrides.BootstrapUpdateGitignore = &updateGitignore
			}
			effective, _, err := agentconfig.Resolve(root.String(), overrides)
			if err != nil {
				return err
			}

			result, err := checkpoint.BootstrapWithOptions(root, checkpoint.BootstrapOptions{
				UpdateGitignore: effective.Bootstrap.UpdateGitignore,
				StorePath:       storePath,
			})
			if err != nil {
				return err
			}
			// Bootstrap auto-registers Git workspaces only; turnal init is an
			// explicit adoption, so non-Git projects are registered here too and
			// show up in the machine-wide project index.
			if err := result.Repo.RegisterStore(); err != nil {
				return err
			}
			if result.Attached {
				fmt.Fprintf(cmd.OutOrStdout(), "initialized worktree: %s\n", result.Repo.WorkspaceRoot)
				fmt.Fprintf(cmd.OutOrStdout(), "attached turnal store: %s\n", result.Repo.MetadataDir)
				fmt.Fprintf(cmd.OutOrStdout(), "worktree id: %s\n", result.Repo.WorktreeID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "initialized hidden git repo: %s\n", result.Repo.GitDir)
				fmt.Fprintf(cmd.OutOrStdout(), "worktree id: %s\n", result.Repo.WorktreeID)
			}
			if effective.Bootstrap.UpdateGitignore {
				if result.GitignoreUpdated {
					fmt.Fprintf(cmd.OutOrStdout(), "updated gitignore: %s\n", result.GitignorePath)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "gitignore already configured: %s\n", result.GitignorePath)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "gitignore update skipped")
			}
			if effective.GitSync.Enabled {
				if err := persistInitConfig(result.Repo.MetadataDir, initConfigPersistence{
					Agent:        persistedString(cmd.Flags().Changed("agent"), agent),
					InstallHooks: persistedBool(cmd.Flags().Changed("skip-hooks"), !skipHooks),
					GitSync:      persistedBool(true, true),
				}); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "enabled git-sync capture: %s\n", filepath.Join(result.Repo.MetadataDir, "config.toml"))
			} else if cmd.Flags().Changed("agent") || cmd.Flags().Changed("skip-hooks") {
				if err := persistInitConfig(result.Repo.MetadataDir, initConfigPersistence{
					Agent:        persistedString(cmd.Flags().Changed("agent"), agent),
					InstallHooks: persistedBool(cmd.Flags().Changed("skip-hooks"), !skipHooks),
				}); err != nil {
					return err
				}
			}

			targets, err := adapters.ResolveTargets(root.String(), adapters.Target(effective.Init.Agent))
			if err != nil {
				return err
			}
			if !effective.Init.InstallHooks || len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "adapter hooks skipped")
			} else {
				installed, err := adapters.InstallWithOptions(root.String(), targets, adapters.InstallOptions{
					HookCommand: effective.Hooks.Command,
				})
				if err != nil {
					return err
				}
				for _, adapter := range installed {
					fmt.Fprintf(cmd.OutOrStdout(), "configured %s hooks: %s\n", adapter.Target, adapter.ConfigPath)
					if adapter.BackupPath != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "backed up invalid hook config: %s\n", adapter.BackupPath)
					}
				}
				if containsAdapterTarget(targets, adapters.TargetCodex) {
					writeCodexTrustNotice(cmd.OutOrStdout())
				}
			}

			return offerSkillInstallation(cmd, root.String(), result.Repo.MetadataDir, targets)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", string(adapters.TargetAuto), "Agent hooks to configure: auto, claude, codex, copilot, cursor, gemini, opencode, pi, all, or none")
	cmd.Flags().BoolVar(&skipHooks, "skip-hooks", false, "Skip automatic agent hook configuration")
	cmd.Flags().BoolVar(&enableGitSync, "git-sync", false, "Enable opt-in workspace Git state capture for future workspace-git rollbacks")
	cmd.Flags().BoolVar(&updateGitignore, "update-gitignore", true, "Add Turnal metadata to .gitignore")
	cmd.Flags().StringVar(&storePath, "store", "", "Use or create a Turnal store at this explicit .turnal path")
	return cmd
}

func containsAdapterTarget(targets []adapters.Target, expected adapters.Target) bool {
	for _, target := range targets {
		if target == expected {
			return true
		}
	}
	return false
}

func writeCodexTrustNotice(w io.Writer) {
	title := "Codex hook trust required"
	lines := []string{
		"Before using Turnal with the Codex desktop app or an app-server wrapper,",
		"open the Codex CLI in this workspace and trust the Turnal hooks there.",
	}
	contentWidth := utf8.RuneCountInString(title) + 1
	for _, line := range lines {
		if width := utf8.RuneCountInString(line); width > contentWidth {
			contentWidth = width
		}
	}

	prefix, suffix := "", ""
	if colorOutputEnabled(w) {
		prefix, suffix = "\x1b[33m", "\x1b[0m"
	}
	fmt.Fprintf(w, "\n%s┌─ %s %s┐%s\n", prefix, title, strings.Repeat("─", contentWidth-utf8.RuneCountInString(title)-1), suffix)
	for _, line := range lines {
		fmt.Fprintf(w, "%s│ %s%s │%s\n", prefix, line, strings.Repeat(" ", contentWidth-utf8.RuneCountInString(line)), suffix)
	}
	fmt.Fprintf(w, "%s└%s┘%s\n\n", prefix, strings.Repeat("─", contentWidth+2), suffix)
}

func offerSkillInstallation(cmd *cobra.Command, projectRoot, metadataDir string, targets []adapters.Target) error {
	if len(targets) == 0 {
		return nil
	}
	if !canPrompt(cmd.InOrStdin()) {
		fmt.Fprintln(cmd.OutOrStdout(), "agent skills not installed (run turnal init interactively to install them)")
		return nil
	}
	confirmed, err := confirmSkillInstallation(cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(cmd.OutOrStdout(), "agent skills skipped")
		return nil
	}
	agents := make([]string, 0, len(targets))
	for _, target := range targets {
		agents = append(agents, string(target))
	}
	installed, err := agentskills.Install(projectRoot, metadataDir, agents)
	if err != nil {
		return err
	}
	for _, result := range installed {
		fmt.Fprintf(cmd.OutOrStdout(), "linked %s skills: %s\n", result.Agent, result.Path)
	}
	return nil
}

func confirmSkillInstallation(in io.Reader, prompt io.Writer) (bool, error) {
	if _, err := fmt.Fprint(prompt, "Install Turnal agent skills for the initialized agents? [Y/n] "); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

type initConfigPersistence struct {
	Agent        *string
	InstallHooks *bool
	GitSync      *bool
}

func persistedString(enabled bool, value string) *string {
	if !enabled {
		return nil
	}
	return &value
}

func persistedBool(enabled bool, value bool) *bool {
	if !enabled {
		return nil
	}
	return &value
}

func persistInitConfig(metadataDir string, persistence initConfigPersistence) error {
	path := filepath.Join(metadataDir, "config.toml")
	file, err := agentconfig.ReadFileLayer(path)
	if err != nil {
		return err
	}
	version := 1
	file.Version = &version
	if persistence.Agent != nil || persistence.InstallHooks != nil {
		if file.Init == nil {
			file.Init = &agentconfig.InitFile{}
		}
		if persistence.Agent != nil {
			file.Init.Agent = persistence.Agent
		}
		if persistence.InstallHooks != nil {
			file.Init.InstallHooks = persistence.InstallHooks
		}
	}
	if persistence.GitSync != nil {
		file.GitSync = &agentconfig.GitSyncFile{Enabled: persistence.GitSync}
	}
	data, err := toml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal workspace config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write workspace config %s: %w", path, err)
	}
	return nil
}
