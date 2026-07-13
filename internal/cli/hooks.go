package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/spf13/cobra"
)

func claudeHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "claude-hook [user|assistant|tool-use|tool-batch]",
		Short:        "Internal: Claude Code hook adapter",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readHookPayload(cmd.InOrStdin())
			if err != nil {
				reportHookFailure(cmd, primitives.AdapterClaudeCode, "UnknownClaudeHook", raw, err)
				return nil
			}
			hookName, err := claudeHookName(args[0])
			if err != nil {
				reportHookFailure(cmd, primitives.AdapterClaudeCode, "UnknownClaudeHook", raw, err)
				return nil
			}
			handleHookFailure(cmd, primitives.AdapterClaudeCode, hookName, raw)
			return nil
		},
	}
	return cmd
}

func codexHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codex-hook",
		Short:        "Internal: Codex hook adapter",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readHookPayload(cmd.InOrStdin())
			if err != nil {
				reportHookFailure(cmd, primitives.AdapterCodex, codexHookName(raw), raw, err)
				return nil
			}
			handleHookFailure(cmd, primitives.AdapterCodex, codexHookName(raw), raw)
			return nil
		},
	}
}

func readHookPayload(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, adapters.MaxHookPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read hook payload: %w", err)
	}
	if len(raw) > adapters.MaxHookPayloadBytes {
		return raw[:adapters.MaxHookPayloadBytes], fmt.Errorf("hook payload exceeds %d-byte limit", adapters.MaxHookPayloadBytes)
	}
	return raw, nil
}

func handleHookFailure(cmd *cobra.Command, adapter primitives.AdapterName, hookName string, raw []byte) {
	err := adapters.HandleHookPayloadWithRunID(adapter, hookName, raw, os.Getenv(runs.EnvRunID))
	if err == nil {
		return
	}
	reportHookFailure(cmd, adapter, hookName, raw, err)
}

func reportHookFailure(cmd *cobra.Command, adapter primitives.AdapterName, hookName string, raw []byte, err error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "turnal: %s hook capture failed (%s): %v\n", adapter, hookName, err)
	if ledgerErr := adapters.RecordHookFailure(adapter, hookName, raw, err); ledgerErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "turnal: could not persist hook failure diagnostic: %v\n", ledgerErr)
	}
}

func claudeHookName(value string) (string, error) {
	switch value {
	case "user":
		return "UserPromptSubmit", nil
	case "assistant":
		return "Stop", nil
	case "tool-use":
		return "PostToolUse", nil
	case "tool-batch":
		return "PostToolBatch", nil
	default:
		return "", fmt.Errorf("invalid Claude hook %q; expected user, assistant, tool-use, or tool-batch", value)
	}
}

func codexHookName(raw []byte) string {
	var payload struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.HookEventName == "" {
		return "CodexHook"
	}
	return payload.HookEventName
}
