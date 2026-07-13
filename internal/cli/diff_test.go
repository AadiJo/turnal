package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffJSONIncludesNativeEditorDocuments(t *testing.T) {
	root, _, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	output := runRootStdout(t, "diff", sessionID.String()+":"+turnID.String(), "--json")
	var result diffDocumentsOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("unmarshal diff json: %v\n%s", err, output)
	}
	if result.Kind != "turn" || result.SessionID != sessionID.String() || result.TurnID != turnID.Uint64() {
		t.Fatalf("diff identity = %#v", result)
	}
	if len(result.Files) != 1 {
		t.Fatalf("diff files = %#v, want one", result.Files)
	}
	file := result.Files[0]
	if file.Status != "M" || file.Path != "app.txt" || !file.BeforeExists || !file.AfterExists {
		t.Fatalf("diff file = %#v", file)
	}
	if decodeDiffTestContent(t, file.BeforeBase64) != "before\n" || decodeDiffTestContent(t, file.AfterBase64) != "after\n" {
		t.Fatalf("diff content = before %q after %q", file.BeforeBase64, file.AfterBase64)
	}
}

func TestRollbackPreviewJSONComparesWorkspaceToPreTurn(t *testing.T) {
	root, _, sessionID, turnID := createTurnWithDiff(t)
	if err := os.WriteFile(filepath.Join(root.String(), "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later file: %v", err)
	}
	t.Chdir(root.String())

	output := runRootStdout(t, "diff", sessionID.String()+":"+turnID.String(), "--json", "--rollback-preview")
	var result diffDocumentsOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("unmarshal rollback diff json: %v\n%s", err, output)
	}
	if result.Kind != "rollback" {
		t.Fatalf("kind = %q, want rollback", result.Kind)
	}
	if result.WorkspaceTree == "" {
		t.Fatal("rollback preview workspace_tree is empty")
	}
	files := make(map[string]diffDocumentJSON, len(result.Files))
	for _, file := range result.Files {
		files[file.Path] = file
	}
	app := files["app.txt"]
	if app.Status != "M" || !app.BeforeExists || !app.AfterExists || decodeDiffTestContent(t, app.AfterBase64) != "before\n" {
		t.Fatalf("app rollback file = %#v", app)
	}
	later := files["later.txt"]
	if later.Status != "D" || !later.BeforeExists || later.AfterExists {
		t.Fatalf("later rollback file = %#v", later)
	}
}

func decodeDiffTestContent(t *testing.T, encoded string) string {
	t.Helper()
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode diff content: %v", err)
	}
	return string(content)
}
