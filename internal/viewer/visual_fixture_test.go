package viewer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

// TestCreateVisualFixture creates a real disposable Turnal store for browser
// screenshot checks when TURNAL_VISUAL_FIXTURE is set. It is skipped in normal
// test runs and none of its sample content is included in release binaries.
func TestCreateVisualFixture(t *testing.T) {
	rootPath := os.Getenv("TURNAL_VISUAL_FIXTURE")
	if rootPath == "" {
		t.Skip("TURNAL_VISUAL_FIXTURE is not set")
	}
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-b", "main", rootPath).Run(); err != nil {
		t.Fatal(err)
	}
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := bootstrapped.Repo

	files := map[string]string{
		"internal/viewer/server.go":   "package viewer\n\nfunc allowedHost(host string) bool {\n\treturn host == \"127.0.0.1\"\n}\n",
		"internal/viewer/service.go":  "package viewer\n\nfunc payloadLimit() int {\n\treturn 1024\n}\n",
		"internal/viewer/identity.go": "package viewer\n\nfunc resourceVersion() int {\n\treturn 1\n}\n",
		"web/src/App.tsx":             "export function App() {\n  return <main>History</main>;\n}\n",
		"web/src/styles.css":          ":root {\n  --accent: #73d7b8;\n}\n",
		"README.md":                   "# Turnal\n\nA local-first flight recorder for coding agents.\n",
	}
	for name, content := range files {
		writeVisualFile(t, rootPath, name, content)
	}

	createVisualSession(t, repo, "secure-local-viewer", primitives.AdapterCodex, []visualTurn{
		{
			Prompt: "Harden fragment bootstrap and scope the viewer session to its random launch path.",
			Tool:   "apply_patch", Path: "internal/viewer/server.go",
			Content:   "package viewer\n\nfunc allowedHost(host, expected string) bool {\n\tif host == \"\" {\n\t\treturn false\n\t}\n\treturn host == expected\n}\n",
			Assistant: "Added strict host matching and a scoped one-time bootstrap exchange.",
		},
		{
			Prompt: "Add bounded payloads and make cancelled browser requests stop checkpoint work.",
			Tool:   "apply_patch", Path: "internal/viewer/service.go",
			Content:   "package viewer\n\nimport \"context\"\n\nconst maxPayload = 512 << 10\n\nfunc payloadLimit(ctx context.Context) (int, error) {\n\tif err := ctx.Err(); err != nil {\n\t\treturn 0, err\n\t}\n\treturn maxPayload, nil\n}\n",
			Assistant: "Bounded diff responses and propagated cancellation through the read service.",
		},
		{
			Prompt: "Use versioned opaque keys so duplicate turn IDs cannot resolve across streams.",
			Tool:   "apply_patch", Path: "internal/viewer/identity.go",
			Content:   "package viewer\n\nconst resourceKeyVersion = 1\n\ntype resourceIdentity struct {\n\tStore string\n\tWorktree string\n\tStream string\n\tSession string\n\tTurn uint64\n}\n",
			Assistant: "Canonical keys now carry the full store, worktree, stream, session, and turn identity.",
		},
	})

	createVisualSession(t, repo, "provenance-workflow", primitives.AdapterClaudeCode, []visualTurn{
		{
			Prompt: "Build the checkpoint graph and keep the selected turn context visible.",
			Tool:   "apply_patch", Path: "web/src/App.tsx",
			Content:   "export function App() {\n  return (\n    <main data-view=\"timeline\">\n      <nav>Sessions</nav>\n      <section>Checkpoint graph</section>\n      <aside>Turn evidence</aside>\n    </main>\n  );\n}\n",
			Assistant: "Built a navigable topology with a persistent evidence inspector.",
		},
		{
			Prompt: "Refine the dark theme with calmer contrast and spring-based selection motion.",
			Tool:   "apply_patch", Path: "web/src/styles.css",
			Content:   ":root {\n  --accent: #73d7b8;\n  --surface: #181c1b;\n  --surface-raised: #1d2221;\n  --text: #edf2ef;\n}\n\n.turn-selection {\n  transition: transform 240ms cubic-bezier(.16, 1, .3, 1);\n}\n",
			Assistant: "Tuned hierarchy, contrast, and selection feedback across all three panes.",
		},
	})

	createVisualSession(t, repo, "release-readiness", primitives.AdapterManual, []visualTurn{
		{
			Prompt: "Document the local-only viewer command and its read-only security boundary.",
			Tool:   "apply_patch", Path: "README.md",
			Content:   "# Turnal\n\nA local-first flight recorder for coding agents.\n\n## Local viewer\n\nRun `turnal ui` to inspect sessions, checkpoint diffs, and line origins. The server binds to loopback and never changes durable history.\n",
			Assistant: "Documented launch behavior, the local trust boundary, and the non-mutating workflow.",
		},
	})

	if _, err := exec.Command("git", "-C", rootPath, "status", "--short").Output(); err != nil {
		t.Fatal(err)
	}
	t.Logf("visual fixture created at %s", rootPath)
}

type visualTurn struct {
	Prompt    string
	Tool      string
	Path      string
	Content   string
	Assistant string
}

func createVisualSession(t *testing.T, repo *checkpoint.Repo, id string, adapter primitives.AdapterName, turnsToCreate []visualTurn) {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(id)
	if err != nil {
		t.Fatal(err)
	}
	appendVisualEvent(t, repo.EventLog(), sessionID, nil, primitives.EventTypeSessionStart, adapter, map[string]any{
		"model": map[primitives.AdapterName]string{
			primitives.AdapterCodex:      "gpt-5-codex",
			primitives.AdapterClaudeCode: "claude-sonnet",
			primitives.AdapterManual:     "manual",
		}[adapter],
	})
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: adapter}
	for _, fixture := range turnsToCreate {
		started, err := recorder.Start(sessionID, 0)
		if err != nil {
			t.Fatal(err)
		}
		appendVisualEvent(t, repo.EventLog(), sessionID, &started.TurnID, primitives.EventTypePromptUser, adapter, map[string]any{"text": fixture.Prompt})
		appendVisualEvent(t, repo.EventLog(), sessionID, &started.TurnID, primitives.EventTypeAssistantMessage, adapter, map[string]any{"text": "I will inspect the current implementation and make a focused change."})
		appendVisualEvent(t, repo.EventLog(), sessionID, &started.TurnID, primitives.EventTypeToolCall, adapter, map[string]any{
			"tool_name": fixture.Tool, "path": fixture.Path, "operation": "update",
		})
		writeVisualFile(t, repo.WorkspaceRoot.String(), fixture.Path, fixture.Content)
		appendVisualEvent(t, repo.EventLog(), sessionID, &started.TurnID, primitives.EventTypeToolResult, adapter, map[string]any{
			"tool_name": fixture.Tool, "result": fmt.Sprintf("Updated %s successfully", fixture.Path),
		})
		appendVisualEvent(t, repo.EventLog(), sessionID, &started.TurnID, primitives.EventTypeAssistantMessage, adapter, map[string]any{"text": fixture.Assistant})
		if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func appendVisualEvent(t *testing.T, log eventlog.Log, sessionID primitives.SessionID, turnID *primitives.TurnID, eventType primitives.EventType, adapter primitives.AdapterName, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID, TurnID: turnID, Type: eventType, Adapter: adapter,
		Time: primitives.NowTimestamp(), Payload: data,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeVisualFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
