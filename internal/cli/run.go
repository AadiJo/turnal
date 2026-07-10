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
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/spf13/cobra"
)

type runSessionPayload struct {
	ProviderSessionID string   `json:"provider_session_id"`
	Source            string   `json:"source"`
	Command           []string `json:"command"`
}

type runTurnPayload struct {
	Turn uint64 `json:"turn"`
}

type runCheckpointPayload struct {
	Turn       uint64 `json:"turn"`
	Phase      string `json:"phase"`
	CommitSHA  string `json:"commit_sha"`
	Ref        string `json:"ref"`
	GitSyncRef string `json:"git_sync_ref,omitempty"`
}

type runIncompletePayload struct {
	Reason string `json:"reason"`
}

type childExitError struct {
	code int
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
		Use:          "run -- codex [args...]",
		Short:        "Run Codex with turnal safety checkpoints",
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isCodexCommand(args[0]) {
				return fmt.Errorf("turnal run currently supports Codex only; expected command %q to be codex", args[0])
			}

			repo, err := openCheckpointRepo()
			if err != nil {
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
				if _, err := adapters.InstallCodexHookWithOptions(repo.WorkspaceRoot.String(), adapters.InstallOptions{
					HookCommand: effective.Hooks.Command,
				}); err != nil {
					return err
				}
			}

			beforeRawCount, err := codexRawRecordCount(repo.MetadataDir)
			if err != nil {
				return err
			}
			sessionID, err := newRunSessionID(time.Now())
			if err != nil {
				return err
			}
			unlockSession, err := adapters.AcquireSessionLock(repo, sessionID)
			if err != nil {
				return err
			}
			defer unlockSession()

			started, err := startRunTurn(repo, sessionID, args)
			if err != nil {
				return err
			}

			childArgs := prepareCodexCommand(args, effective.Run.BypassHookTrust)
			childErr := runChildCommand(cmd, childArgs)

			finishErr := finishRunTurn(repo, sessionID, started.TurnID)
			afterRawCount, countErr := codexRawRecordCount(repo.MetadataDir)

			if !effective.Run.Quiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "turnal: recorded wrapper checkpoints for %s:%s\n", sessionID, started.TurnID)
				if countErr == nil && afterRawCount == beforeRawCount {
					fmt.Fprintln(cmd.ErrOrStderr(), "turnal: no Codex hook payloads were observed; wrapper checkpoints are available, but prompt/tool/assistant capture depends on Codex hooks. Review /hooks in Codex, or rerun with --bypass-hook-trust after reviewing hook sources.")
				}
			}
			if finishErr != nil {
				_, _ = eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
					SessionID: sessionID,
					TurnID:    &started.TurnID,
					Type:      primitives.EventTypeError,
					Adapter:   primitives.AdapterCodex,
					SourceID:  fmt.Sprintf("codex-run:%s:%s:incomplete", sessionID, started.TurnID),
					Payload:   mustJSON(runIncompletePayload{Reason: finishErr.Error()}),
				})
				return finishErr
			}
			if countErr != nil {
				return countErr
			}
			if childErr != nil {
				return childErr
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipHookInstall, "skip-hook-install", false, "Do not update .codex/config.toml before running Codex")
	cmd.Flags().BoolVar(&bypassHookTrust, "bypass-hook-trust", false, "Pass --dangerously-bypass-hook-trust to Codex for this invocation")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress turnal wrapper status messages")
	return cmd
}

func isCodexCommand(command string) bool {
	base := filepath.Base(command)
	base = strings.TrimSuffix(base, ".exe")
	return base == "codex"
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

func runChildCommand(cmd *cobra.Command, args []string) error {
	child := exec.Command(args[0], args[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Dir = ""
	child.Env = os.Environ()

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

func newRunSessionID(now time.Time) (primitives.SessionID, error) {
	return primitives.ParseSessionID(fmt.Sprintf("codex-run-%d", now.UTC().UnixNano()))
}

func startRunTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, command []string) (turns.StartResult, error) {
	log := repo.EventLog()
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:session", sessionID),
		Payload: mustJSON(runSessionPayload{
			ProviderSessionID: sessionID.String(),
			Source:            "turnal run",
			Command:           command,
		}),
	}); err != nil {
		return turns.StartResult{}, err
	}

	started, err := turns.NewManager(repo).Start(sessionID, 0)
	if err != nil {
		return turns.StartResult{}, err
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &started.TurnID,
		Type:      primitives.EventTypeTurnStart,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:%s:start", sessionID, started.TurnID),
		Payload:   mustJSON(runTurnPayload{Turn: started.TurnID.Uint64()}),
	}); err != nil {
		return turns.StartResult{}, err
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &started.TurnID,
		Type:      primitives.EventTypeCheckpoint,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:%s:checkpoint:pre", sessionID, started.TurnID),
		Payload: mustJSON(runCheckpointPayload{
			Turn:       started.TurnID.Uint64(),
			Phase:      primitives.CheckpointPhasePre.String(),
			CommitSHA:  started.Pre.Commit.String(),
			Ref:        started.Pre.Ref.String(),
			GitSyncRef: snapshotRef(started.GitSync),
		}),
	}); err != nil {
		return turns.StartResult{}, err
	}
	return started, nil
}

func finishRunTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID) error {
	log := repo.EventLog()
	finished, err := turns.NewManager(repo).Finish(sessionID, turnID)
	if err != nil {
		return err
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &finished.TurnID,
		Type:      primitives.EventTypeTurnFinish,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:%s:finish", sessionID, finished.TurnID),
		Payload:   mustJSON(runTurnPayload{Turn: finished.TurnID.Uint64()}),
	}); err != nil {
		return err
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &finished.TurnID,
		Type:      primitives.EventTypeCheckpoint,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:%s:checkpoint:post", sessionID, finished.TurnID),
		Payload: mustJSON(runCheckpointPayload{
			Turn:       finished.TurnID.Uint64(),
			Phase:      primitives.CheckpointPhasePost.String(),
			CommitSHA:  finished.Post.Commit.String(),
			Ref:        finished.Post.Ref.String(),
			GitSyncRef: snapshotRef(finished.GitSync),
		}),
	}); err != nil {
		return err
	}
	return nil
}

func snapshotRef(snapshot *checkpoint.Snapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.Ref
}

func codexRawRecordCount(metadataDir string) (int, error) {
	paths := []string{filepath.Join(metadataDir, "log", "adapter", "codex.jsonl")}
	rawRoot := filepath.Join(metadataDir, "log", "raw")
	_ = filepath.WalkDir(rawRoot, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == "codex.jsonl" {
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
			return 0, fmt.Errorf("read Codex raw adapter log: %w", err)
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
