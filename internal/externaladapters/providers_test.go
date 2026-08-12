package externaladapters

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/AadiJo/turnal/internal/buildinfo"
	"github.com/AadiJo/turnal/internal/upgrade"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

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
		{"copilot-cli", "postToolUse", `{"sessionId":"copilot-session","cwd":"/workspace","toolName":"edit","toolArgs":{"path":"a.go"},"toolResult":{"resultType":"success","textResultForLlm":"ok"}}`, []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
		{"copilot-cli", "UserPromptSubmit", `{"session_id":"copilot-session","cwd":"/workspace","prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"gemini-cli", "AfterAgent", `{"session_id":"gemini-session","cwd":"/workspace","prompt_response":"done"}`, []adaptersdk.EventType{adaptersdk.EventAssistantMessage}},
		{"gemini-cli", "AfterTool", `{"session_id":"gemini-session","cwd":"/workspace","tool_name":"write_file","tool_input":{"path":"a.go"},"tool_response":{"ok":true}}`, []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
		{"opencode", "event", `{"directory":"/workspace","event":{"type":"session.created","properties":{"info":{"id":"opencode-session"}}}}`, []adaptersdk.EventType{adaptersdk.EventSessionStart}},
		{"opencode", "tool.execute.after", `{"sessionID":"opencode-session","directory":"/workspace","tool":"bash","callID":"call-1","args":{"command":"true"},"output":"ok"}`, []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
		{"cursor", "beforeSubmitPrompt", `{"conversation_id":"cursor-session","generation_id":"turn-1","workspace_roots":["/workspace"],"prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"cursor", "postToolUse", `{"conversation_id":"cursor-session","workspace_roots":["/workspace"],"tool_name":"Shell","tool_use_id":"call-1","tool_input":{"command":"true"},"tool_output":"{\"exitCode\":0}"}`, []adaptersdk.EventType{adaptersdk.EventToolResult}},
		{"pi", "before_agent_start", `{"session_id":"pi-session","cwd":"/workspace","prompt":"fix it"}`, []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{"pi", "tool_execution_start", `{"session_id":"pi-session","cwd":"/workspace","tool_name":"bash","tool_call_id":"call-1","args":{"command":"true"}}`, []adaptersdk.EventType{adaptersdk.EventToolCall}},
		{"pi", "agent_settled", `{"session_id":"pi-session","cwd":"/workspace","text":"done"}`, []adaptersdk.EventType{adaptersdk.EventAssistantMessage}},
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
