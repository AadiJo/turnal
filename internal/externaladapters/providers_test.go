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
		name     string
		hook     string
		raw      string
		want     []adaptersdk.EventType
		wantText string
	}{
		{name: "copilot-cli", hook: "postToolUse", raw: `{"sessionId":"copilot-session","cwd":"/workspace","toolName":"edit","toolArgs":{"path":"a.go"},"toolResult":{"resultType":"success","textResultForLlm":"ok"}}`, want: []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
		{name: "copilot-cli", hook: "UserPromptSubmit", raw: `{"session_id":"copilot-session","cwd":"/workspace","prompt":"fix it"}`, want: []adaptersdk.EventType{adaptersdk.EventPromptUser}},
		{name: "gemini-cli", hook: "AfterAgent", raw: `{"session_id":"gemini-session","cwd":"/workspace","prompt_response":"done"}`, want: []adaptersdk.EventType{adaptersdk.EventAssistantMessage}},
		{name: "gemini-cli", hook: "AfterTool", raw: `{"session_id":"gemini-session","cwd":"/workspace","tool_name":"write_file","tool_input":{"path":"a.go"},"tool_response":{"ok":true}}`, want: []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
		{name: "opencode", hook: "event", raw: `{"directory":"/workspace","event":{"type":"session.created","properties":{"info":{"id":"opencode-session"}}}}`, want: []adaptersdk.EventType{adaptersdk.EventSessionStart}},
		{name: "opencode", hook: "event", raw: `{"directory":"/workspace","event":{"type":"message.updated","properties":{"info":{"id":"message-1","sessionID":"opencode-session","role":"user","text":"fix it"}}}}`, want: []adaptersdk.EventType{adaptersdk.EventPromptUser}, wantText: "fix it"},
		{name: "opencode", hook: "tool.execute.after", raw: `{"sessionID":"opencode-session","directory":"/workspace","tool":"bash","callID":"call-1","args":{"command":"true"},"output":"ok"}`, want: []adaptersdk.EventType{adaptersdk.EventToolCall, adaptersdk.EventToolResult}},
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
			if test.wantText != "" && events[0].Text != test.wantText {
				t.Fatalf("event text = %q, want %q", events[0].Text, test.wantText)
			}
			if len(events) == 2 && len(events[0].Input) > 0 && !bytes.Equal(events[1].Input, events[0].Input) {
				t.Fatalf("tool result did not retain paired call input: call=%s result=%s", events[0].Input, events[1].Input)
			}
		})
	}
}
