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
	for _, name := range []string{"cursor", "pi"} {
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
			callFields := map[string]any{"tool_name": parityTool, "tool_call_id": parityToolID, "args": parityInput}
			resultFields := map[string]any{"tool_name": parityTool, "tool_call_id": parityToolID, "result": parityOutput}
			if name == "cursor" {
				promptHook, callHook, resultHook = "beforeSubmitPrompt", "preToolUse", "postToolUse"
				callFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput}
				resultFields = map[string]any{"tool_name": parityTool, "tool_use_id": parityToolID, "tool_input": parityInput, "tool_output": parityOutput}
			}

			capture(promptHook, map[string]any{"prompt": parityPrompt})
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
