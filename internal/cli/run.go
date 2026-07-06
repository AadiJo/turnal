package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-vcs-again/internal/adapters"
	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
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
	Turn      uint64 `json:"turn"`
	Phase     string `json:"phase"`
	CommitSHA string `json:"commit_sha"`
	Ref       string `json:"ref"`
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
		Short:        "Run Codex with agent-vcs safety checkpoints",
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isCodexCommand(args[0]) {
				return fmt.Errorf("agent-vcs run currently supports Codex only; expected command %q to be codex", args[0])
			}

			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if !skipHookInstall {
				if _, err := adapters.InstallCodexHook(repo.WorkspaceRoot.String()); err != nil {
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

			started, err := startRunTurn(repo, sessionID, args)
			if err != nil {
				return err
			}

			childArgs := prepareCodexCommand(args, bypassHookTrust)
			childErr := runChildCommand(cmd, childArgs)

			finishErr := finishRunTurn(repo, sessionID, started.TurnID)
			afterRawCount, countErr := codexRawRecordCount(repo.MetadataDir)

			if !quiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "agent-vcs: recorded wrapper checkpoints for %s:%s\n", sessionID, started.TurnID)
				if countErr == nil && afterRawCount == beforeRawCount {
					fmt.Fprintln(cmd.ErrOrStderr(), "agent-vcs: no Codex hook payloads were observed; wrapper checkpoints are available, but prompt/tool/assistant capture depends on Codex hooks. Review /hooks in Codex, or rerun with --bypass-hook-trust after reviewing hook sources.")
				}
			}
			if finishErr != nil {
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
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress agent-vcs wrapper status messages")
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

	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return childExitError{code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func newRunSessionID(now time.Time) (primitives.SessionID, error) {
	return primitives.ParseSessionID(fmt.Sprintf("codex-run-%d", now.UTC().UnixNano()))
}

func startRunTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, command []string) (turns.StartResult, error) {
	log := eventlog.Open(repo.MetadataDir)
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterCodex,
		SourceID:  fmt.Sprintf("codex-run:%s:session", sessionID),
		Payload: mustJSON(runSessionPayload{
			ProviderSessionID: sessionID.String(),
			Source:            "agent-vcs run",
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
			Turn:      started.TurnID.Uint64(),
			Phase:     primitives.CheckpointPhasePre.String(),
			CommitSHA: started.Pre.Commit.String(),
			Ref:       started.Pre.Ref.String(),
		}),
	}); err != nil {
		return turns.StartResult{}, err
	}
	return started, nil
}

func finishRunTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID) error {
	log := eventlog.Open(repo.MetadataDir)
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
			Turn:      finished.TurnID.Uint64(),
			Phase:     primitives.CheckpointPhasePost.String(),
			CommitSHA: finished.Post.Commit.String(),
			Ref:       finished.Post.Ref.String(),
		}),
	}); err != nil {
		return err
	}
	return nil
}

func codexRawRecordCount(metadataDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(metadataDir, "log", "adapter", "codex.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read Codex raw adapter log: %w", err)
	}
	return strings.Count(string(data), "\n"), nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
