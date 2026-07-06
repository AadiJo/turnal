package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"agent-vcs-again/internal/adapters"
	"agent-vcs-again/internal/primitives"
	"github.com/spf13/cobra"
)

func claudeHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "claude-hook [user|assistant|tool-use|tool-batch]",
		Short:        "Internal: Claude Code hook adapter",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			hookName, err := claudeHookName(args[0])
			if err != nil {
				return
			}
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return
			}
			_ = adapters.HandleHookPayload(primitives.AdapterClaudeCode, hookName, raw)
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
		Run: func(cmd *cobra.Command, args []string) {
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return
			}
			_ = adapters.HandleHookPayload(primitives.AdapterCodex, codexHookName(raw), raw)
		},
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
