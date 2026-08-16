package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubCopilotCLITranscriptRequiresMatchingSessionAndWorkspace(t *testing.T) {
	root := t.TempDir()
	const session = "copilot-session"
	path := filepath.Join(root, "session-state", session, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session.start","data":{"sessionId":"copilot-session","context":{"cwd":` + quote(root) + `}}}
{"type":"assistant.message","data":{"content":"turnal-edit-complete","model":"claude-haiku-4.5"}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	text, model, ok := copilotCompletedAssistant(path, session, root)
	if !ok || text != "turnal-edit-complete" || model != "claude-haiku-4.5" {
		t.Fatalf("completed assistant = (%q, %q, %v)", text, model, ok)
	}
	if _, _, ok := copilotCompletedAssistant(path, "other-session", root); ok {
		t.Fatal("transcript accepted for a different session")
	}
	if _, _, ok := copilotCompletedAssistant(path, session, filepath.Join(root, "other")); ok {
		t.Fatal("transcript accepted for a different workspace")
	}
}
