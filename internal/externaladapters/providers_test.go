package externaladapters

import (
	"encoding/json"
	"testing"

	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

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
		})
	}
}
