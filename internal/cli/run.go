package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/spf13/cobra"
)

type runSessionPayload struct {
	ProviderSessionID string   `json:"provider_session_id"`
	Source            string   `json:"source"`
	Command           []string `json:"command"`
}

type runIncompletePayload struct {
	Reason string `json:"reason"`
}

type childExitError struct {
	code int
}

type runProvider struct {
	Adapter primitives.AdapterName
	Target  adapters.Target
	Name    string
}

func (err childExitError) Error() string {
	return fmt.Sprintf("wrapped command exited with status %d", err.code)
}

func (err childExitError) ExitCode() int {
	if err.code < 0 {
		return 1
	}
	return err.code
}

func runCmd() *cobra.Command {
	var skipHookInstall bool
	var bypassHookTrust bool
	var quiet bool

	cmd := &cobra.Command{
		Use:          "run -- <claude|codex|copilot|cursor|agent|opencode|pi> [args...]",
		Short:        "Run a supported agent with turnal safety checkpoints",
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (resultErr error) {
			provider, ok := resolveRunProvider(args[0])
			if !ok {
				return fmt.Errorf("turnal run supports claude, codex, copilot, cursor (or agent), opencode, and pi; unsupported command %q", args[0])
			}
			if provider.Target != adapters.TargetCodex && cmd.Flags().Changed("bypass-hook-trust") {
				return fmt.Errorf("--bypass-hook-trust applies only to Codex")
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if err := runs.RecoverAbandoned(repo); err != nil {
				return err
			}

			overrides := agentconfig.Overrides{}
			if cmd.Flags().Changed("skip-hook-install") {
				installHooks := !skipHookInstall
				overrides.RunInstallHooks = &installHooks
			}
			if cmd.Flags().Changed("bypass-hook-trust") {
				overrides.RunBypassHookTrust = &bypassHookTrust
			}
			if cmd.Flags().Changed("quiet") {
				overrides.RunQuiet = &quiet
			}
			effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), overrides)
			if err != nil {
				return err
			}

			if effective.Run.InstallHooks {
				if _, err := adapters.InstallWithOptions(repo.WorkspaceRoot.String(), []adapters.Target{provider.Target}, adapters.InstallOptions{
					HookCommand: effective.Hooks.Command,
				}); err != nil {
					return err
				}
			}

			beforeRawCount, err := adapterRawRecordCount(repo.MetadataDir, provider.Adapter)
			if err != nil {
				return err
			}
			runID, err := primitives.NewRunID()
			if err != nil {
				return err
			}
			sessionID, err := newRunSessionID(provider.Adapter, time.Now())
			if err != nil {
				return err
			}
			unlockSession, err := adapters.AcquireSessionLock(repo, sessionID)
			if err != nil {
				return err
			}
			defer unlockSession()
			releaseLifecycle, err := runs.Begin(repo, runID, sessionID, args)
			if err != nil {
				return err
			}
			defer releaseLifecycle()
			runOpen := true
			defer func() {
				if !runOpen || resultErr == nil {
					return
				}
				if finishErr := runs.Finish(repo, runID, sessionID, runs.StatusIncomplete, resultErr.Error()); finishErr != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("finalize incomplete run %s: %w", runID, finishErr))
				} else {
					runOpen = false
				}
			}()
			if err := runs.LinkCapture(repo, runID, runs.CaptureWrapper, sessionID, provider.Adapter); err != nil {
				return err
			}

			started, err := startRunTurn(repo, provider.Adapter, sessionID, args)
			if err != nil {
				return err
			}

			childArgs := args
			if provider.Target == adapters.TargetCodex {
				childArgs = prepareCodexCommand(args, effective.Run.BypassHookTrust)
			}
			if provider.Target == adapters.TargetCopilot && !containsOption(args[1:], "-C") {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve GitHub Copilot CLI working directory: %w", err)
				}
				childArgs = append([]string{args[0], "-C", cwd}, args[1:]...)
			}
			childEnvironment := map[string]string{}
			if provider.Target == adapters.TargetCopilot && !environmentContains(os.Environ(), "GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS") {
				childEnvironment["GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS"] = "true"
			}
			postExitFinalize := providerUsesSingleTurnPromptMode(provider.Target, args[1:])
			if postExitFinalize {
				childEnvironment[runs.EnvPostExitFinalize] = "1"
			}
			childErr := runChildCommand(cmd, childArgs, runID, childEnvironment)
			var providerFinalizeErr error
			if postExitFinalize && provider.Target == adapters.TargetCopilot {
				providerFinalizeErr = adapters.FinalizeCopilotRun(repo, runID)
			}
			if postExitFinalize && provider.Target == adapters.TargetCursor {
				providerFinalizeErr = adapters.FinalizeCursorRun(repo, runID)
			}

			finishErr := finishRunTurn(repo, provider.Adapter, sessionID, started.TurnID)
			afterRawCount, countErr := adapterRawRecordCount(repo.MetadataDir, provider.Adapter)

			if !effective.Run.Quiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "turnal: recorded run %s with wrapper checkpoints for %s:%s\n", runID, sessionID, started.TurnID)
				if countErr == nil && afterRawCount == beforeRawCount {
					fmt.Fprintf(cmd.ErrOrStderr(), "turnal: no %s hook payloads were observed; wrapper checkpoints are available, but prompt/tool/assistant capture depends on the provider loading Turnal hooks\n", provider.Name)
				}
			}
			if finishErr != nil {
				_, _ = eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
					SessionID: sessionID,
					TurnID:    &started.TurnID,
					Type:      primitives.EventTypeError,
					Adapter:   provider.Adapter,
					SourceID:  fmt.Sprintf("%s-run:%s:%s:incomplete", provider.Adapter, sessionID, started.TurnID),
					Payload:   mustJSON(runIncompletePayload{Reason: finishErr.Error()}),
				})
				return finishErr
			}
			if childErr != nil {
				combined := errors.Join(childErr, providerFinalizeErr, countErr)
				if err := runs.Finish(repo, runID, sessionID, runs.StatusFailed, combined.Error()); err != nil {
					return errors.Join(combined, err)
				}
				runOpen = false
				return combined
			}
			if countErr != nil {
				return countErr
			}
			if providerFinalizeErr != nil {
				return providerFinalizeErr
			}
			if err := runs.Finish(repo, runID, sessionID, runs.StatusSucceeded, ""); err != nil {
				return err
			}
			runOpen = false
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipHookInstall, "skip-hook-install", false, "Do not install the selected provider's Turnal integration")
	cmd.Flags().BoolVar(&bypassHookTrust, "bypass-hook-trust", false, "Pass --dangerously-bypass-hook-trust to Codex for this invocation")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress turnal wrapper status messages")
	return cmd
}

func isCodexCommand(command string) bool {
	provider, ok := resolveRunProvider(command)
	return ok && provider.Target == adapters.TargetCodex
}

func resolveRunProvider(command string) (runProvider, bool) {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(command), ".exe"))
	switch base {
	case "claude":
		return runProvider{Adapter: primitives.AdapterClaudeCode, Target: adapters.TargetClaude, Name: "Claude Code"}, true
	case "codex":
		return runProvider{Adapter: primitives.AdapterCodex, Target: adapters.TargetCodex, Name: "Codex"}, true
	case "copilot":
		return runProvider{Adapter: primitives.AdapterCopilotCLI, Target: adapters.TargetCopilot, Name: "GitHub Copilot CLI"}, true
	case "cursor", "agent":
		return runProvider{Adapter: primitives.AdapterCursor, Target: adapters.TargetCursor, Name: "Cursor"}, true
	case "opencode":
		return runProvider{Adapter: primitives.AdapterOpenCode, Target: adapters.TargetOpenCode, Name: "OpenCode"}, true
	case "pi":
		return runProvider{Adapter: primitives.AdapterPi, Target: adapters.TargetPi, Name: "Pi"}, true
	default:
		return runProvider{}, false
	}
}

func prepareCodexCommand(args []string, bypassHookTrust bool) []string {
	prepared := []string{args[0]}
	rest := args[1:]
	if !codexArgsMentionHooksFeature(rest) {
		prepared = append(prepared, "--enable", "hooks")
	}
	if bypassHookTrust && !containsArg(rest, "--dangerously-bypass-hook-trust") {
		prepared = append(prepared, "--dangerously-bypass-hook-trust")
	}
	return append(prepared, rest...)
}

func codexArgsMentionHooksFeature(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--enable" || arg == "--disable":
			if i+1 < len(args) && args[i+1] == "hooks" {
				return true
			}
			i++
		case strings.HasPrefix(arg, "--enable=") || strings.HasPrefix(arg, "--disable="):
			_, value, _ := strings.Cut(arg, "=")
			if value == "hooks" {
				return true
			}
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) && strings.Contains(args[i+1], "features.hooks") {
				return true
			}
			i++
		case strings.HasPrefix(arg, "-c=") || strings.HasPrefix(arg, "--config="):
			_, value, _ := strings.Cut(arg, "=")
			if strings.Contains(value, "features.hooks") {
				return true
			}
		}
	}
	return false
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsOption(args []string, want string) bool {
	for _, arg := range args {
		if arg == want || strings.HasPrefix(arg, want+"=") {
			return true
		}
	}
	return false
}

func providerUsesSingleTurnPromptMode(target adapters.Target, args []string) bool {
	switch target {
	case adapters.TargetCursor:
		return containsOption(args, "--print") || containsOption(args, "-p")
	case adapters.TargetCopilot:
		return containsOption(args, "--prompt") || containsOption(args, "-p")
	default:
		return false
	}
}

func runChildCommand(cmd *cobra.Command, args []string, runID primitives.RunID, environment map[string]string) error {
	child := exec.Command(args[0], args[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Dir = ""
	child.Env = runEnvironment(os.Environ(), runID)
	for name, value := range environment {
		child.Env = replaceEnvironment(child.Env, name, value)
	}

	if err := child.Start(); err != nil {
		return err
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	err := waitForChild(done, signals, child.Process.Signal, child.Process.Kill)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return childExitError{code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func replaceEnvironment(existing []string, name, value string) []string {
	result := make([]string, 0, len(existing)+1)
	for _, entry := range existing {
		entryName, _, found := strings.Cut(entry, "=")
		if found && environmentNamesEqual(entryName, name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}

func environmentContains(existing []string, name string) bool {
	for _, entry := range existing {
		entryName, _, found := strings.Cut(entry, "=")
		if found && environmentNamesEqual(entryName, name) {
			return true
		}
	}
	return false
}

func runEnvironment(existing []string, runID primitives.RunID) []string {
	result := make([]string, 0, len(existing)+1)
	for _, entry := range existing {
		name, _, found := strings.Cut(entry, "=")
		if found && environmentNamesEqual(name, runs.EnvRunID) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, runs.EnvRunID+"="+runID.String())
}

func waitForChild(done <-chan error, signals <-chan os.Signal, forward func(os.Signal) error, kill func() error) error {
	forwarded := false
	for {
		select {
		case err := <-done:
			return err
		case received := <-signals:
			if !forwarded {
				forwarded = true
				_ = forward(received)
				continue
			}
			// A repeated interrupt is an explicit escalation request. This also
			// prevents an uncooperative child from trapping the wrapper forever.
			_ = kill()
		}
	}
}

func newRunSessionID(adapter primitives.AdapterName, now time.Time) (primitives.SessionID, error) {
	return primitives.ParseSessionID(fmt.Sprintf("%s-run-%d", adapter, now.UTC().UnixNano()))
}

func startRunTurn(repo *checkpoint.Repo, adapter primitives.AdapterName, sessionID primitives.SessionID, command []string) (turns.StartResult, error) {
	log := repo.EventLog()
	if err := turnevents.RecoverCheckpointJournals(log, repo); err != nil {
		return turns.StartResult{}, err
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s-run:%s:session", adapter, sessionID),
		Payload: mustJSON(runSessionPayload{
			ProviderSessionID: sessionID.String(),
			Source:            "turnal run",
			Command:           command,
		}),
	}); err != nil {
		return turns.StartResult{}, err
	}

	manager := turns.NewManager(repo).WithCheckpointEvents(adapter, "")
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		return turns.StartResult{}, err
	}
	if err := turnevents.AppendTurnStart(log, adapter, sessionID, started.TurnID, ""); err != nil {
		return turns.StartResult{}, err
	}
	if err := turnevents.AppendCheckpointWithGitSync(log, adapter, sessionID, started.TurnID, primitives.CheckpointPhasePre, started.Pre, started.GitSync, ""); err != nil {
		return turns.StartResult{}, err
	}
	return started, nil
}

func finishRunTurn(repo *checkpoint.Repo, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID) error {
	log := repo.EventLog()
	if err := turnevents.RecoverCheckpointJournals(log, repo); err != nil {
		return err
	}
	manager := turns.NewManager(repo).WithCheckpointEvents(adapter, "")
	finished, err := manager.Finish(sessionID, turnID)
	if err != nil {
		return err
	}
	if err := turnevents.AppendTurnFinish(log, adapter, sessionID, finished.TurnID, ""); err != nil {
		return err
	}
	if err := turnevents.AppendCheckpointWithGitSync(log, adapter, sessionID, finished.TurnID, primitives.CheckpointPhasePost, finished.Post, finished.GitSync, ""); err != nil {
		return err
	}
	return nil
}

func codexRawRecordCount(metadataDir string) (int, error) {
	return adapterRawRecordCount(metadataDir, primitives.AdapterCodex)
}

func adapterRawRecordCount(metadataDir string, adapter primitives.AdapterName) (int, error) {
	filename := adapter.String() + ".jsonl"
	paths := []string{filepath.Join(metadataDir, "log", "adapter", filename)}
	rawRoot := filepath.Join(metadataDir, "log", "raw")
	_ = filepath.WalkDir(rawRoot, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == filename {
			paths = append(paths, path)
		}
		return nil
	})
	count := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("read %s raw adapter log: %w", adapter, err)
		}
		count += strings.Count(string(data), "\n")
	}
	return count, nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
