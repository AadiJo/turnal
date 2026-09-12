package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

func TestExternalPromptCaptureOutputsProviderIntentGuidance(t *testing.T) {
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionID(t, "external-intent-output")
	if _, err := turns.NewManager(repo).Start(sessionID, 0); err != nil {
		t.Fatal(err)
	}
	event := adaptersdk.Event{
		Type:      adaptersdk.EventPromptUser,
		SessionID: sessionID.String(),
		CWD:       root.String(),
		Text:      "fix retries",
	}

	for _, test := range []struct {
		name    string
		adapter primitives.AdapterName
		cursor  bool
	}{
		{name: "cursor", adapter: primitives.AdapterCursor, cursor: true},
		{name: "pi", adapter: primitives.AdapterPi},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writeAdapterCaptureOutput(&output, test.adapter, []adaptersdk.Event{event})
			var decoded adapterCaptureOutput
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatalf("decode output: %v\n%s", err, output.String())
			}
			if test.cursor {
				if decoded.Continue == nil || !*decoded.Continue || decoded.AdditionalContext != "" || !bytes.Contains([]byte(decoded.UserMessage), []byte("turnal intent --session "+sessionID.String()+" --turn 1")) {
					t.Fatalf("Cursor output = %#v", decoded)
				}
			} else if decoded.Continue != nil || decoded.UserMessage != "" || !bytes.Contains([]byte(decoded.AdditionalContext), []byte("turnal intent --session "+sessionID.String()+" --turn 1")) {
				t.Fatalf("Pi output = %#v", decoded)
			}
		})
	}

	if instruction, ok := adapters.IntentInstructionForSession(root.String(), sessionID.String()); !ok || instruction == "" {
		t.Fatalf("shared instruction unavailable: %q, %t", instruction, ok)
	}
}
