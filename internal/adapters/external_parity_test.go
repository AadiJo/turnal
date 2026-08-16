package adapters

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/externaladapters"
	"github.com/AadiJo/turnal/internal/primitives"
)

// TestAdapterLifecycleLogParity is deliberately field-level. Adapter unit
// tests can all pass while one capture path silently omits durable action
// snapshots or emits a different lifecycle ordering.
func TestAdapterLifecycleLogParity(t *testing.T) {
	type harness struct {
		name string
		run  func(t *testing.T, root primitives.WorkspaceRoot, mutate func())
	}

	harnesses := []harness{
		{name: "claude-code", run: runBuiltInParityLifecycle(primitives.AdapterClaudeCode)},
		{name: "codex", run: runBuiltInParityLifecycle(primitives.AdapterCodex)},
		{name: "cursor", run: runExternalParityLifecycle("cursor", []parityHook{
			{"sessionStart", map[string]any{"conversation_id": paritySessionID, "model": parityModel}},
			{"beforeSubmitPrompt", map[string]any{"conversation_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"preToolUse", map[string]any{"conversation_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"postToolUse", map[string]any{"conversation_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_output": parityOutput}},
			{"afterAgentResponse", map[string]any{"conversation_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
			{"stop", map[string]any{"conversation_id": paritySessionID}},
		})},
		{name: "pi", run: runExternalParityLifecycle("pi", []parityHook{
			{"session_start", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"before_agent_start", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"tool_execution_start", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_call_id": parityToolID, "args": parityInput}},
			{"tool_execution_end", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_call_id": parityToolID, "result": parityOutput, "is_error": false}},
			{"agent_settled", map[string]any{"session_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
		})},
		{name: "gemini-cli", run: runExternalParityLifecycle("gemini-cli", []parityHook{
			{"SessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"BeforeAgent", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"BeforeTool", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"AfterTool", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_response": parityOutput}},
			{"AfterAgent", map[string]any{"session_id": paritySessionID, "prompt_response": parityAssistant, "model": parityModel}},
			{"SessionEnd", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "opencode", run: runExternalParityLifecycle("opencode", []parityHook{
			{"session.created", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"message.updated", map[string]any{"session_id": paritySessionID, "role": "user", "text": parityPrompt, "model": parityModel}},
			{"tool.execute.before", map[string]any{"session_id": paritySessionID, "tool": parityTool, "call_id": parityToolID, "args": parityInput}},
			{"tool.execute.after", map[string]any{"session_id": paritySessionID, "tool": parityTool, "call_id": parityToolID, "args": parityInput, "output": parityOutput}},
			{"assistant.completed", map[string]any{"session_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
			{"session.idle", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "copilot-cli", run: runExternalParityLifecycle("copilot-cli", []parityHook{
			{"sessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"userPromptSubmitted", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"preToolUse", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"postToolUse", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_result": parityOutput}},
			{"agentStop", map[string]any{"session_id": paritySessionID, "response": parityAssistant, "model": parityModel}},
		})},
	}

	var baseline []comparableLogEvent
	for _, harness := range harnesses {
		t.Run(harness.name, func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, "app.txt", "before\n")

			harness.run(t, root, func() {
				writeFile(t, root, "app.txt", "after\n")
			})

			events := readEvents(t, repo, sessionID(t, paritySessionID))
			got := comparableAdapterLog(t, events)
			if baseline == nil {
				baseline = got
				return
			}
			if !reflect.DeepEqual(got, baseline) {
				wantJSON, _ := json.MarshalIndent(baseline, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("durable log differs from built-in harness\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

func TestAdapterFailureLogParity(t *testing.T) {
	harnesses := []struct {
		name string
		run  func(*testing.T, primitives.WorkspaceRoot, func())
	}{
		{name: "claude-code", run: runBuiltInFailureParityLifecycle()},
		{name: "cursor", run: runExternalParityLifecycle("cursor", []parityHook{
			{"sessionStart", map[string]any{"conversation_id": paritySessionID, "model": parityModel}},
			{"beforeSubmitPrompt", map[string]any{"conversation_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"preToolUse", map[string]any{"conversation_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"postToolUseFailure", map[string]any{"conversation_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "error_message": "boom"}},
			{"afterAgentResponse", map[string]any{"conversation_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
			{"stop", map[string]any{"conversation_id": paritySessionID}},
		})},
		{name: "pi", run: runExternalParityLifecycle("pi", []parityHook{
			{"session_start", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"before_agent_start", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"tool_execution_start", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_call_id": parityToolID, "args": parityInput}},
			{"tool_execution_end", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_call_id": parityToolID, "result": map[string]any{"error": "boom"}, "is_error": true}},
			{"agent_settled", map[string]any{"session_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
		})},
		{name: "gemini-cli", run: runExternalParityLifecycle("gemini-cli", []parityHook{
			{"SessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"BeforeAgent", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"BeforeTool", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"AfterTool", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_response": map[string]any{"error": "boom"}, "is_error": true}},
			{"AfterAgent", map[string]any{"session_id": paritySessionID, "prompt_response": parityAssistant, "model": parityModel}},
			{"SessionEnd", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "opencode", run: runExternalParityLifecycle("opencode", []parityHook{
			{"session.created", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"user.completed", map[string]any{"session_id": paritySessionID, "text": parityPrompt, "model": parityModel}},
			{"tool.execute.before", map[string]any{"session_id": paritySessionID, "tool": parityTool, "call_id": parityToolID, "args": parityInput}},
			{"tool.execute.after", map[string]any{"session_id": paritySessionID, "tool": parityTool, "call_id": parityToolID, "args": parityInput, "output": map[string]any{"error": "boom"}, "is_error": true}},
			{"assistant.completed", map[string]any{"session_id": paritySessionID, "text": parityAssistant, "model": parityModel}},
			{"session.idle", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "copilot-cli", run: runExternalParityLifecycle("copilot-cli", []parityHook{
			{"sessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"userPromptSubmitted", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"preToolUse", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}},
			{"postToolUse", map[string]any{"session_id": paritySessionID, "tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_result": map[string]any{"error": "boom"}, "is_error": true}},
			{"agentStop", map[string]any{"session_id": paritySessionID, "response": parityAssistant, "model": parityModel}},
		})},
	}
	assertAdapterLogParity(t, harnesses, true)
}

func TestAdapterEmptyAssistantLogParity(t *testing.T) {
	harnesses := []struct {
		name string
		run  func(*testing.T, primitives.WorkspaceRoot, func())
	}{
		{name: "claude-code", run: runBuiltInEmptyAssistantParityLifecycle()},
		{name: "cursor", run: runExternalParityLifecycle("cursor", []parityHook{
			{"sessionStart", map[string]any{"conversation_id": paritySessionID, "model": parityModel}},
			{"beforeSubmitPrompt", map[string]any{"conversation_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"afterAgentResponse", map[string]any{"conversation_id": paritySessionID, "text": "", "model": parityModel}},
			{"stop", map[string]any{"conversation_id": paritySessionID}},
		})},
		{name: "pi", run: runExternalParityLifecycle("pi", []parityHook{
			{"session_start", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"before_agent_start", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"agent_settled", map[string]any{"session_id": paritySessionID, "text": "", "model": parityModel}},
		})},
		{name: "gemini-cli", run: runExternalParityLifecycle("gemini-cli", []parityHook{
			{"SessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"BeforeAgent", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"AfterAgent", map[string]any{"session_id": paritySessionID, "prompt_response": "", "model": parityModel}},
			{"SessionEnd", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "opencode", run: runExternalParityLifecycle("opencode", []parityHook{
			{"session.created", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"user.completed", map[string]any{"session_id": paritySessionID, "text": parityPrompt, "model": parityModel}},
			{"assistant.completed", map[string]any{"session_id": paritySessionID, "text": "", "model": parityModel}},
			{"session.idle", map[string]any{"session_id": paritySessionID}},
		})},
		{name: "copilot-cli", run: runExternalParityLifecycle("copilot-cli", []parityHook{
			{"sessionStart", map[string]any{"session_id": paritySessionID, "model": parityModel}},
			{"userPromptSubmitted", map[string]any{"session_id": paritySessionID, "prompt": parityPrompt, "model": parityModel}},
			{"agentStop", map[string]any{"session_id": paritySessionID, "response": "", "model": parityModel}},
		})},
	}
	assertAdapterLogParity(t, harnesses, false)
}

func assertAdapterLogParity(t *testing.T, harnesses []struct {
	name string
	run  func(*testing.T, primitives.WorkspaceRoot, func())
}, mutate bool) {
	t.Helper()
	var baseline []comparableLogEvent
	for _, harness := range harnesses {
		t.Run(harness.name, func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, "app.txt", "before\n")
			harness.run(t, root, func() {
				if mutate {
					writeFile(t, root, "app.txt", "after\n")
				}
			})
			got := comparableAdapterLog(t, readEvents(t, repo, sessionID(t, paritySessionID)))
			if baseline == nil {
				baseline = got
				return
			}
			if !reflect.DeepEqual(got, baseline) {
				wantJSON, _ := json.MarshalIndent(baseline, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("durable log differs from built-in harness\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

const (
	paritySessionID = "adapter-parity"
	parityModel     = "test-model"
	parityPrompt    = "change app.txt"
	parityAssistant = "done"
	parityTool      = "write"
	parityToolID    = "tool-1"
)

var (
	parityInput  = map[string]any{"path": "app.txt", "content": "after\n"}
	parityOutput = map[string]any{"ok": true}
)

type parityHook struct {
	name   string
	fields map[string]any
}

func runBuiltInParityLifecycle(adapter primitives.AdapterName) func(*testing.T, primitives.WorkspaceRoot, func()) {
	return func(t *testing.T, root primitives.WorkspaceRoot, mutate func()) {
		send := func(hook string, fields map[string]any) {
			fields["cwd"] = root.String()
			fields["session_id"] = paritySessionID
			hookName := hook
			if adapter == primitives.AdapterCodex {
				fields["hook_event_name"] = hook
				hookName = "CodexHook"
			}
			handlePayload(t, adapter, hookName, fields)
		}

		send("SessionStart", map[string]any{"model": parityModel})
		send("UserPromptSubmit", map[string]any{"prompt": parityPrompt, "model": parityModel})
		send("PreToolUse", map[string]any{
			"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput,
		})
		mutate()
		send("PostToolUse", map[string]any{
			"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_response": parityOutput,
		})
		send("Stop", map[string]any{"last_assistant_message": parityAssistant, "model": parityModel})
	}
}

func runBuiltInFailureParityLifecycle() func(*testing.T, primitives.WorkspaceRoot, func()) {
	return func(t *testing.T, root primitives.WorkspaceRoot, mutate func()) {
		send := func(hook string, fields map[string]any) {
			fields["cwd"] = root.String()
			fields["session_id"] = paritySessionID
			handlePayload(t, primitives.AdapterClaudeCode, hook, fields)
		}
		send("SessionStart", map[string]any{"model": parityModel})
		send("UserPromptSubmit", map[string]any{"prompt": parityPrompt, "model": parityModel})
		send("PreToolUse", map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput})
		mutate()
		send("PostToolUseFailure", map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "error": "boom"})
		send("Stop", map[string]any{"last_assistant_message": parityAssistant, "model": parityModel})
	}
}

func runBuiltInEmptyAssistantParityLifecycle() func(*testing.T, primitives.WorkspaceRoot, func()) {
	return func(t *testing.T, root primitives.WorkspaceRoot, _ func()) {
		send := func(hook string, fields map[string]any) {
			fields["cwd"] = root.String()
			fields["session_id"] = paritySessionID
			handlePayload(t, primitives.AdapterClaudeCode, hook, fields)
		}
		send("SessionStart", map[string]any{"model": parityModel})
		send("UserPromptSubmit", map[string]any{"prompt": parityPrompt, "model": parityModel})
		send("Stop", map[string]any{"last_assistant_message": "", "model": parityModel})
	}
}

func runExternalParityLifecycle(name string, hooks []parityHook) func(*testing.T, primitives.WorkspaceRoot, func()) {
	return func(t *testing.T, root primitives.WorkspaceRoot, mutate func()) {
		normalize, ok := externaladapters.Normalizer(name)
		if !ok {
			t.Fatalf("normalizer %q not found", name)
		}
		for index, hook := range hooks {
			fields := make(map[string]any, len(hook.fields)+1)
			for key, value := range hook.fields {
				fields[key] = value
			}
			fields["cwd"] = root.String()
			if name == "cursor" {
				fields["workspace_roots"] = []string{root.String()}
				delete(fields, "cwd")
			}
			raw := rawPayload(t, fields)
			events, err := normalize(hook.name, raw)
			if err != nil {
				t.Fatalf("normalize %s: %v", hook.name, err)
			}
			if err := HandleNormalizedEvents(primitives.AdapterName(name), hook.name, raw, events); err != nil {
				t.Fatalf("capture %s: %v", hook.name, err)
			}
			if index == 2 {
				mutate()
			}
		}
	}
}

type comparableLogEvent struct {
	Turn    uint64         `json:"turn,omitempty"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func comparableAdapterLog(t *testing.T, events []eventlog.Event) []comparableLogEvent {
	t.Helper()
	result := make([]comparableLogEvent, 0, len(events))
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", event.Type, err)
		}
		switch event.Type {
		case primitives.EventTypeCheckpoint:
			payload = map[string]any{
				"turn":            payload["turn"],
				"phase":           payload["phase"],
				"event_seq_start": payload["event_seq_start"],
				"event_seq_end":   payload["event_seq_end"],
			}
		case primitives.EventTypeToolCall:
			canonicalizeSnapshot(payload, "pre_snapshot")
		case primitives.EventTypeToolResult:
			canonicalizeSnapshot(payload, "post_snapshot")
		}
		var turn uint64
		if event.TurnID != nil {
			turn = event.TurnID.Uint64()
		}
		result = append(result, comparableLogEvent{Turn: turn, Type: event.Type.String(), Payload: payload})
	}
	return result
}

func canonicalizeSnapshot(payload map[string]any, key string) {
	if _, ok := payload[key]; ok {
		payload[key] = map[string]any{"captured": true}
	}
}

func TestNewAdapterToolRetriesAreIdempotent(t *testing.T) {
	for _, name := range []string{"cursor", "pi", "gemini-cli", "opencode", "copilot-cli"} {
		t.Run(name, func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, "app.txt", "before\n")
			normalize, _ := externaladapters.Normalizer(name)

			capture := func(hook string, fields map[string]any) {
				fields["session_id"] = paritySessionID + "-retry-" + name
				fields["cwd"] = root.String()
				raw := rawPayload(t, fields)
				normalized, normalizeErr := normalize(hook, raw)
				if normalizeErr != nil {
					t.Fatalf("normalize %s: %v", hook, normalizeErr)
				}
				if captureErr := HandleNormalizedEvents(primitives.AdapterName(name), hook, raw, normalized); captureErr != nil {
					t.Fatalf("capture %s: %v", hook, captureErr)
				}
			}

			promptHook, callHook, resultHook := "before_agent_start", "tool_execution_start", "tool_execution_end"
			promptFields := map[string]any{"prompt": parityPrompt}
			callFields := map[string]any{"tool_name": parityTool, "tool_call_id": parityToolID, "args": parityInput}
			resultFields := map[string]any{"tool_name": parityTool, "tool_call_id": parityToolID, "result": parityOutput}
			switch name {
			case "cursor":
				promptHook, callHook, resultHook = "beforeSubmitPrompt", "preToolUse", "postToolUse"
				callFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}
				resultFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_output": parityOutput}
			case "gemini-cli":
				promptHook, callHook, resultHook = "BeforeAgent", "BeforeTool", "AfterTool"
				callFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}
				resultFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_response": parityOutput}
			case "opencode":
				promptHook, callHook, resultHook = "user.completed", "tool.execute.before", "tool.execute.after"
				promptFields = map[string]any{"text": parityPrompt}
				callFields = map[string]any{"tool": parityTool, "call_id": parityToolID, "args": parityInput}
				resultFields = map[string]any{"tool": parityTool, "call_id": parityToolID, "args": parityInput, "output": parityOutput}
			case "copilot-cli":
				promptHook, callHook, resultHook = "userPromptSubmitted", "preToolUse", "postToolUse"
				callFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}
				resultFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_result": parityOutput}
			}

			capture(promptHook, promptFields)
			capture(callHook, callFields)
			writeFile(t, root, "app.txt", "after\n")
			capture(resultHook, resultFields)
			callFields["retry_attempt"] = 2
			resultFields["retry_attempt"] = 2
			capture(callHook, callFields)
			capture(resultHook, resultFields)

			session := sessionID(t, paritySessionID+"-retry-"+name)
			events := readEvents(t, repo, session)
			if got := countEvents(events, primitives.EventTypeToolCall); got != 1 {
				t.Fatalf("tool calls = %d, want 1; events=%s", got, fmt.Sprint(eventTypes(events)))
			}
			if got := countEvents(events, primitives.EventTypeToolResult); got != 1 {
				t.Fatalf("tool results = %d, want 1; events=%s", got, fmt.Sprint(eventTypes(events)))
			}
		})
	}
}

func TestNewAdapterRawLogsHonorSecretsPolicy(t *testing.T) {
	tests := []struct {
		name  string
		hooks []parityHook
	}{
		{name: "cursor", hooks: []parityHook{
			{"beforeSubmitPrompt", map[string]any{"conversation_id": "cursor-private", "prompt": "prompt-secret"}},
			{"preToolUse", map[string]any{"conversation_id": "cursor-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "input-secret"}}},
			{"postToolUse", map[string]any{"conversation_id": "cursor-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_output": "output-secret"}},
			{"afterAgentResponse", map[string]any{"conversation_id": "cursor-private", "text": "assistant-secret"}},
		}},
		{name: "pi", hooks: []parityHook{
			{"before_agent_start", map[string]any{"session_id": "pi-private", "prompt": "prompt-secret"}},
			{"tool_execution_start", map[string]any{"session_id": "pi-private", "tool_name": "read", "tool_call_id": "tool-1", "args": map[string]any{"path": "input-secret"}}},
			{"tool_execution_end", map[string]any{"session_id": "pi-private", "tool_name": "read", "tool_call_id": "tool-1", "result": "output-secret"}},
			{"agent_settled", map[string]any{"session_id": "pi-private", "text": "assistant-secret"}},
		}},
		{name: "gemini-cli", hooks: []parityHook{
			{"BeforeAgent", map[string]any{"session_id": "gemini-cli-private", "prompt": "prompt-secret"}},
			{"BeforeTool", map[string]any{"session_id": "gemini-cli-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "input-secret"}}},
			{"AfterTool", map[string]any{"session_id": "gemini-cli-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "input-secret"}, "tool_response": "output-secret"}},
			{"AfterAgent", map[string]any{"session_id": "gemini-cli-private", "prompt_response": "assistant-secret"}},
		}},
		{name: "opencode", hooks: []parityHook{
			{"user.completed", map[string]any{"session_id": "opencode-private", "text": "prompt-secret"}},
			{"tool.execute.before", map[string]any{"session_id": "opencode-private", "tool": "read", "call_id": "tool-1", "args": map[string]any{"path": "input-secret"}}},
			{"tool.execute.after", map[string]any{"session_id": "opencode-private", "tool": "read", "call_id": "tool-1", "args": map[string]any{"path": "input-secret"}, "output": "output-secret"}},
			{"assistant.completed", map[string]any{"session_id": "opencode-private", "text": "assistant-secret"}},
		}},
		{name: "copilot-cli", hooks: []parityHook{
			{"userPromptSubmitted", map[string]any{"session_id": "copilot-cli-private", "prompt": "prompt-secret"}},
			{"preToolUse", map[string]any{"session_id": "copilot-cli-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "input-secret"}}},
			{"postToolUse", map[string]any{"session_id": "copilot-cli-private", "tool_name": "read", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "input-secret"}, "tool_result": "output-secret"}},
			{"agentStop", map[string]any{"session_id": "copilot-cli-private", "response": "assistant-secret"}},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, ".turnal/config.toml", "version = 1\n\n[secrets]\nstore_prompts = false\nstore_tool_io = false\n")
			normalize, _ := externaladapters.Normalizer(test.name)
			for _, hook := range test.hooks {
				fields := make(map[string]any, len(hook.fields)+1)
				for key, value := range hook.fields {
					fields[key] = value
				}
				fields["cwd"] = root.String()
				if test.name == "cursor" {
					fields["workspace_roots"] = []string{root.String()}
					delete(fields, "cwd")
				}
				raw := rawPayload(t, fields)
				normalized, normalizeErr := normalize(hook.name, raw)
				if normalizeErr != nil {
					t.Fatalf("normalize %s: %v", hook.name, normalizeErr)
				}
				if captureErr := HandleNormalizedEvents(primitives.AdapterName(test.name), hook.name, raw, normalized); captureErr != nil {
					t.Fatalf("capture %s: %v", hook.name, captureErr)
				}
			}

			session := sessionID(t, test.name+"-private")
			events := readEvents(t, repo, session)
			seenRawRefs := map[string]bool{}
			for _, event := range events {
				if event.RawRef == "" || seenRawRefs[event.RawRef] {
					continue
				}
				seenRawRefs[event.RawRef] = true
				record, readErr := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
				if readErr != nil {
					t.Fatal(readErr)
				}
				stored := string(record.Payload)
				for _, secret := range []string{"prompt-secret", "input-secret", "output-secret", "assistant-secret"} {
					if strings.Contains(stored, secret) {
						t.Fatalf("%s raw %s retained %q: %s", test.name, record.Hook, secret, stored)
					}
				}
			}
			if len(seenRawRefs) != len(test.hooks) {
				t.Fatalf("raw records = %d, want %d", len(seenRawRefs), len(test.hooks))
			}
		})
	}
}
