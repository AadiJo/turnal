package externaladapters

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/AadiJo/turnal/internal/buildinfo"
	"github.com/AadiJo/turnal/internal/upgrade"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

type providerHookFixture struct {
	name    string
	payload map[string]any
}

func TestNewProviderProtocolHarnessMatchesDirectNormalizer(t *testing.T) {
	tests := []struct {
		name  string
		hooks []providerHookFixture
	}{
		{name: "cursor", hooks: []providerHookFixture{
			{"sessionStart", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "model": "model-1"}},
			{"beforeSubmitPrompt", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "generation_id": "turn-1", "prompt": "fix it"}},
			{"preToolUse", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "generation_id": "turn-1", "tool_name": "write", "tool_use_id": "tool-1", "tool_input": map[string]any{"path": "app.go"}}},
			{"postToolUse", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "generation_id": "turn-1", "tool_name": "write", "tool_use_id": "tool-1", "tool_output": map[string]any{"ok": true}}},
			{"afterAgentResponse", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "generation_id": "turn-1", "text": "done"}},
			{"stop", map[string]any{"conversation_id": "session-1", "workspace_roots": []string{"/workspace"}, "generation_id": "turn-1"}},
		}},
		{name: "pi", hooks: []providerHookFixture{
			{"session_start", map[string]any{"session_id": "session-1", "cwd": "/workspace", "model": "model-1"}},
			{"before_agent_start", map[string]any{"session_id": "session-1", "cwd": "/workspace", "prompt": "fix it"}},
			{"tool_execution_start", map[string]any{"session_id": "session-1", "cwd": "/workspace", "tool_name": "write", "tool_call_id": "tool-1", "args": map[string]any{"path": "app.go"}}},
			{"tool_execution_end", map[string]any{"session_id": "session-1", "cwd": "/workspace", "tool_name": "write", "tool_call_id": "tool-1", "result": map[string]any{"ok": true}}},
			{"agent_settled", map[string]any{"session_id": "session-1", "cwd": "/workspace", "text": "done"}},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalize, ok := Normalizer(test.name)
			if !ok {
				t.Fatalf("normalizer %q not found", test.name)
			}
			var input bytes.Buffer
			encoder := json.NewEncoder(&input)
			var direct []adaptersdk.Event
			for index, hook := range test.hooks {
				payload, err := json.Marshal(hook.payload)
				if err != nil {
					t.Fatal(err)
				}
				events, err := normalize(hook.name, payload)
				if err != nil {
					t.Fatalf("direct normalize %s: %v", hook.name, err)
				}
				direct = append(direct, events...)
				request := adaptersdk.NewRequest(string(rune('a'+index)), adaptersdk.MethodNormalize)
				request.Hook = hook.name
				request.Payload = payload
				if err := encoder.Encode(request); err != nil {
					t.Fatal(err)
				}
			}

			var output bytes.Buffer
			if err := Run(test.name, &input, &output); err != nil {
				t.Fatalf("protocol harness: %v", err)
			}
			decoder := json.NewDecoder(&output)
			var protocol []adaptersdk.Event
			for {
				var response adaptersdk.Response
				if err := decoder.Decode(&response); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if response.Type != adaptersdk.ResponseEvent || response.Event == nil {
					t.Fatalf("response = %+v", response)
				}
				protocol = append(protocol, *response.Event)
			}
			if !reflect.DeepEqual(protocol, direct) {
				t.Fatalf("protocol events = %#v, direct events = %#v", protocol, direct)
			}
		})
	}
}

func TestPiForkLifecycleKeepsTopologyOnSessionStartOnly(t *testing.T) {
	normalize, _ := Normalizer("pi")
	for _, hook := range []string{
		"session_start",
		"before_agent_start",
		"tool_execution_start",
		"tool_execution_end",
		"agent_settled",
	} {
		payload := json.RawMessage(`{
			"session_id":"child-session",
			"parent_session_id":"parent-session",
			"cwd":"/workspace",
			"prompt":"fix it",
			"tool_name":"write",
			"tool_call_id":"tool-1",
			"args":{"path":"app.go"},
			"result":{"ok":true},
			"text":"done"
		}`)
		events, err := normalize(hook, payload)
		if err != nil {
			t.Fatalf("normalize %s: %v", hook, err)
		}
		for _, event := range events {
			if err := adaptersdk.ValidateEvent(event); err != nil {
				t.Fatalf("%s produced invalid event %+v: %v", hook, event, err)
			}
			if hook == "session_start" && event.ParentSessionID != "parent-session" {
				t.Fatalf("session start lost parent topology: %+v", event)
			}
			if hook != "session_start" && event.ParentSessionID != "" {
				t.Fatalf("%s repeated session topology: %+v", hook, event)
			}
		}
	}
}

func TestRunCommandPrintsBuildMetadata(t *testing.T) {
	oldVersion := buildinfo.Version
	oldChannel := buildinfo.Channel
	oldCommit := buildinfo.Commit
	oldInstallSource := buildinfo.InstallSource
	buildinfo.Version = "1.2.3"
	buildinfo.Channel = upgrade.ChannelStable
	buildinfo.Commit = "abc123"
	buildinfo.InstallSource = upgrade.InstallSourceStandalone
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Channel = oldChannel
		buildinfo.Commit = oldCommit
		buildinfo.InstallSource = oldInstallSource
	})

	var output bytes.Buffer
	if err := RunCommand("opencode", []string{"version", "--json"}, bytes.NewReader(nil), &output); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	var metadata upgrade.Metadata
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata != buildinfo.Current() {
		t.Fatalf("metadata = %+v, want %+v", metadata, buildinfo.Current())
	}
}

func TestBundledProviderNormalization(t *testing.T) {
	tests := []struct {
		name string
		hook string
		raw  string
		want []adaptersdk.EventType
	}{
		{"copilot-cli", "postToolUse", `{"sessionId":"copilot-session","cwd":"/workspace","toolName":"edit","toolArgs":{"path":"a.go"},"toolResult":{"resultType":"success","textResultForLlm":"ok"}}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"copilot-cli", "UserPromptSubmit", `{"session_id":"copilot-session","cwd":"/workspace","prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"opencode", "event", `{"directory":"/workspace","event":{"type":"session.created","properties":{"info":{"id":"opencode-session"}}}}`, []adaptersdk.EventType{adaptersdk.EventSessionStart}},
		{"opencode", "tool.execute.after", `{"sessionID":"opencode-session","directory":"/workspace","tool":"bash","callID":"call-1","args":{"command":"true"},"output":"ok"}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"cursor", "beforeSubmitPrompt", `{"conversation_id":"cursor-session","generation_id":"turn-1","workspace_roots":["/workspace"],"prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"cursor", "postToolUse", `{"conversation_id":"cursor-session","workspace_roots":["/workspace"],"tool_name":"Shell","tool_use_id":"call-1","tool_input":{"command":"true"},"tool_output":"{\"exitCode\":0}"}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"cursor", "postToolUseFailure", `{"conversation_id":"cursor-session","workspace_roots":["/workspace"],"tool_name":"Shell","tool_use_id":"call-1","tool_input":{"command":"false"},"error_message":"exit 1"}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"cursor", "stop", `{"conversation_id":"cursor-session","workspace_roots":["/workspace"]}`, []adaptersdk.EventType{adaptersdk.EventTurnFinish}},
		{"pi", "before_agent_start", `{"session_id":"pi-session","cwd":"/workspace","prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"pi", "tool_execution_start", `{"session_id":"pi-session","cwd":"/workspace","tool_name":"bash","tool_call_id":"call-1","args":{"command":"true"}}`, []adaptersdk.EventType{adaptersdk.EventToolCall}},
		{"pi", "tool_execution_end", `{"session_id":"pi-session","cwd":"/workspace","tool_name":"bash","tool_call_id":"call-1","result":{"content":[{"type":"text","text":"exit 1"}]},"is_error":true}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"pi", "agent_settled", `{"session_id":"pi-session","cwd":"/workspace","text":"done"}`, []adaptersdk.EventType{adaptersdk.EventAssistantMessage}},
		{"pi", "agent_settled", `{"session_id":"pi-session","cwd":"/workspace","text":""}`, []adaptersdk.EventType{adaptersdk.EventAssistantMessage}},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.hook, func(t *testing.T) {
			normalize, _ := Normalizer(test.name)
			events, err := normalize(test.hook, json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != len(test.want) {
				t.Fatalf("events = %#v, want types %#v", events, test.want)
			}
			for index, event := range events {
				if event.Type != test.want[index] {
					t.Fatalf("event %d type = %s, want %s", index, event.Type, test.want[index])
				}
				if err := adaptersdk.ValidateEvent(event); err != nil {
					t.Fatalf("event %d invalid: %v", index, err)
				}
			}
			if len(events) == 2 && len(events[0].Input) > 0 && !bytes.Equal(events[1].Input, events[0].Input) {
				t.Fatalf("tool result did not retain paired call input: call=%s result=%s", events[0].Input, events[1].Input)
			}
		})
	}
}

func TestCursorSubagentNormalizationPreservesSessionTopology(t *testing.T) {
	normalize, _ := Normalizer("cursor")
	events, err := normalize("subagentStart", json.RawMessage(`{
		"conversation_id":"parent-session",
		"workspace_roots":["/workspace"],
		"subagent_id":"child-session",
		"parent_conversation_id":"parent-session",
		"tool_call_id":"task-1",
		"subagent_model":"composer-worker"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.SessionID != "child-session" || event.ParentSessionID != "parent-session" || event.ParentToolUseID != "task-1" {
		t.Fatalf("topology = %+v", event)
	}
	if event.CWD != "/workspace" || event.Model != "composer-worker" {
		t.Fatalf("metadata = %+v", event)
	}
	if err := adaptersdk.ValidateEvent(event); err != nil {
		t.Fatalf("event invalid: %v", err)
	}
}

func TestCursorAndPiPreserveStructuredToolFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		hook string
		raw  string
	}{
		{name: "cursor", hook: "postToolUseFailure", raw: `{"conversation_id":"session-1","cwd":"/workspace","tool_name":"shell","tool_call_id":"call-1","error_message":"boom"}`},
		{name: "pi", hook: "tool_execution_end", raw: `{"session_id":"session-1","cwd":"/workspace","tool_name":"shell","tool_call_id":"call-1","result":"boom","is_error":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalize, ok := Normalizer(test.name)
			if !ok {
				t.Fatal("normalizer missing")
			}
			events, err := normalize(test.hook, json.RawMessage(test.raw))
			if err != nil || len(events) != 1 || events[0].Type != adaptersdk.EventToolResult || !events[0].IsError {
				t.Fatalf("failure events = %#v err=%v", events, err)
			}
			if test.name == "cursor" && string(events[0].Output) != `{"error":"boom"}` {
				t.Fatalf("Cursor failure output = %s, want Claude-compatible error object", events[0].Output)
			}
		})
	}
}

func TestCursorAfterFileEditNormalizesObservedMutationPair(t *testing.T) {
	normalize, _ := Normalizer("cursor")
	events, err := normalize("afterFileEdit", json.RawMessage(`{
		"conversation_id":"session-1",
		"generation_id":"turn-1",
		"workspace_roots":["/workspace"],
		"file_path":"/workspace/app.go",
		"edits":[{"old_string":"before","new_string":"after"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != adaptersdk.EventToolCall || events[1].Type != adaptersdk.EventToolResult {
		t.Fatalf("events = %#v", events)
	}
	if events[0].ToolUseID == "" || events[0].ToolUseID != events[1].ToolUseID {
		t.Fatalf("tool IDs = %q, %q", events[0].ToolUseID, events[1].ToolUseID)
	}
	if events[0].ToolName != "Write" || !events[0].MutationAlreadyApplied {
		t.Fatalf("call = %+v", events[0])
	}
	for _, event := range events {
		if err := adaptersdk.ValidateEvent(event); err != nil {
			t.Fatalf("invalid event %+v: %v", event, err)
		}
	}
}

func TestCursorTopologyIsLimitedToSessionStarts(t *testing.T) {
	normalize, _ := Normalizer("cursor")
	for _, hook := range []string{"sessionStart", "beforeSubmitPrompt", "preToolUse", "postToolUse", "afterAgentResponse", "stop"} {
		events, err := normalize(hook, json.RawMessage(`{
			"conversation_id":"child-session",
			"parent_session_id":"parent-session",
			"parent_tool_use_id":"task-1",
			"workspace_roots":["/workspace"],
			"prompt":"fix it",
			"tool_name":"write",
			"tool_use_id":"tool-1",
			"tool_input":{"path":"app.go"},
			"tool_output":{"ok":true},
			"text":"done"
		}`))
		if err != nil {
			t.Fatalf("normalize %s: %v", hook, err)
		}
		for _, event := range events {
			if err := adaptersdk.ValidateEvent(event); err != nil {
				t.Fatalf("%s produced invalid event %+v: %v", hook, event, err)
			}
			if hook == "sessionStart" && (event.ParentSessionID != "parent-session" || event.ParentToolUseID != "task-1") {
				t.Fatalf("session start lost parent topology: %+v", event)
			}
			if hook != "sessionStart" && (event.ParentSessionID != "" || event.ParentToolUseID != "") {
				t.Fatalf("%s repeated session topology: %+v", hook, event)
			}
		}
	}
}

func TestSharedProviderMetadataDoesNotLeakTopology(t *testing.T) {
	for _, test := range []struct {
		name string
		hook string
		raw  string
	}{
		{name: "copilot-cli", hook: "postToolUse", raw: `{"session_id":"child-session","parent_session_id":"parent-session","parent_tool_use_id":"task-1","cwd":"/workspace","toolName":"write","toolArgs":{},"toolResult":{}}`},
		{name: "opencode", hook: "tool.execute.after", raw: `{"sessionID":"child-session","parent_session_id":"parent-session","parent_tool_use_id":"task-1","directory":"/workspace","tool":"write","callID":"tool-1","args":{},"output":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalize, _ := Normalizer(test.name)
			events, err := normalize(test.hook, json.RawMessage(test.raw))
			if err != nil || len(events) == 0 {
				t.Fatalf("events = %#v err=%v", events, err)
			}
			for _, event := range events {
				if event.ParentSessionID != "" || event.ParentToolUseID != "" {
					t.Fatalf("non-session event retained topology: %+v", event)
				}
				if err := adaptersdk.ValidateEvent(event); err != nil {
					t.Fatalf("event invalid: %v", err)
				}
			}
		})
	}
}

func TestCursorSubagentOmitsOrphanedParentToolUseID(t *testing.T) {
	normalize, _ := Normalizer("cursor")
	events, err := normalize("subagentStart", json.RawMessage(`{
		"subagent_id":"child-session",
		"tool_call_id":"task-1",
		"workspace_roots":["/workspace"]
	}`))
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v err=%v", events, err)
	}
	if events[0].ParentToolUseID != "" {
		t.Fatalf("orphaned parent tool id retained: %+v", events[0])
	}
	if err := adaptersdk.ValidateEvent(events[0]); err != nil {
		t.Fatalf("event invalid: %v", err)
	}
}
