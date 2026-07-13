package cases

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestCreateNewTaskAndSiblingCaseFromRecordedTurns(t *testing.T) {
	repo, root := caseRepo(t)
	sessionOne, turnOne := recordCaseTurn(t, repo, root, "source-one", "Fix the parser", false)
	before, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read workspace before create: %v", err)
	}

	created, err := Create(repo, CreateRequest{SessionID: sessionOne, TurnID: turnOne})
	if err != nil {
		t.Fatalf("Create new task: %v", err)
	}
	if !created.TaskCreated || len(created.Task.Revisions) != 1 || created.Task.Revisions[0].Instruction.Text != "Fix the parser" {
		t.Fatalf("created task = %#v", created.Task)
	}
	if created.Case.TaskID != created.Task.ID || created.Case.TaskRevision != 1 || created.Case.Readiness.Base.Status != "available" {
		t.Fatalf("created case = %#v", created.Case)
	}

	sessionTwo, turnTwo := recordCaseTurn(t, repo, root, "source-two", "Fix the parser", false)
	sibling, err := Create(repo, CreateRequest{SessionID: sessionTwo, TurnID: turnTwo, TaskID: created.Task.ID})
	if err != nil {
		t.Fatalf("Create sibling: %v", err)
	}
	if sibling.TaskCreated || sibling.Case.TaskID != created.Task.ID || sibling.Case.ID == created.Case.ID || len(sibling.Task.Revisions) != 1 {
		t.Fatalf("sibling result = %#v", sibling)
	}

	after, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read workspace after create: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("case creation changed active workspace: before=%q after=%q", before, after)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("case creation unexpectedly created user .git: %v", err)
	}
}

func TestCreatePreservesRedactedAndMissingInstructionState(t *testing.T) {
	for name, prompt := range map[string]*string{
		"redacted": ptr(primitives.SecretsRedactionText),
		"missing":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			repo, root := caseRepo(t)
			sessionID, turnID := recordCaseTurn(t, repo, root, "source", value(prompt), prompt == nil)
			created, err := Create(repo, CreateRequest{SessionID: sessionID, TurnID: turnID})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			want := fork.InstructionRedacted
			if prompt == nil {
				want = fork.InstructionMissing
			}
			instruction := created.Case.Readiness.Instruction
			if instruction.Status != want || instruction.Text != "" {
				t.Fatalf("instruction = %#v", instruction)
			}
			encoded, _ := json.Marshal(created)
			if strings.Contains(string(encoded), primitives.SecretsRedactionText) {
				t.Fatalf("created record exposes redaction marker: %s", encoded)
			}
		})
	}
}

func TestCreateFreezesVerifierContract(t *testing.T) {
	repo, root := caseRepo(t)
	configPath := filepath.Join(repo.MetadataDir, "config.toml")
	writeCaseFile(t, configPath, "version = 1\n[[verify]]\nname = \"tests\"\ncommand = \"go\"\nargs = [\"test\", \"./...\"]\ntimeout = \"2m\"\n")
	sessionID, turnID := recordCaseTurn(t, repo, root, "source", "Fix it", false)
	created, err := Create(repo, CreateRequest{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Case.Verifiers) != 1 || created.Case.Verifiers[0].Name != "tests" {
		t.Fatalf("verifiers = %#v", created.Case.Verifiers)
	}
	writeCaseFile(t, configPath, "version = 1\n[[verify]]\nname = \"lint\"\ncommand = \"go\"\nargs = [\"vet\", \"./...\"]\ntimeout = \"5m\"\n")

	rebuilt, err := Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	frozen, ok := rebuilt.Case(created.Case.ID)
	if !ok || !reflect.DeepEqual(frozen.Verifiers, created.Case.Verifiers) {
		t.Fatalf("frozen verifiers changed: %#v", frozen.Verifiers)
	}
}

func TestCreateRejectsCheckpointRefMismatchBeforeWritingTask(t *testing.T) {
	repo, root := caseRepo(t)
	sessionID, turnID := recordCaseTurn(t, repo, root, "source", "Fix it", false)
	ref, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CheckpointRefFor: %v", err)
	}
	if _, err := repo.CreateSyntheticSnapshotRef(ref.String(), "move ref", []checkpoint.SyntheticTreeEntry{{Path: "other.txt", Mode: primitives.GitFileModeRegular, Content: []byte("other\n")}}); err != nil {
		t.Fatalf("move ref: %v", err)
	}
	if _, err := Create(repo, CreateRequest{SessionID: sessionID, TurnID: turnID}); err == nil || !strings.Contains(err.Error(), "checkpoint ref") {
		t.Fatalf("Create error = %v", err)
	}
	projection, err := Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(projection.Tasks) != 0 || len(projection.Cases) != 0 {
		t.Fatalf("failed creation wrote task/case history: %#v", projection)
	}
}

func caseRepo(t *testing.T) (*checkpoint.Repo, primitives.WorkspaceRoot) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	return repo, root
}

func recordCaseTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, sessionText, prompt string, omitPrompt bool) (primitives.SessionID, primitives.TurnID) {
	t.Helper()
	writeCaseFile(t, filepath.Join(root.String(), "app.txt"), "before\n")
	sessionID, _ := primitives.ParseSessionID(sessionText)
	turnID, _ := primitives.NewTurnID(1)
	log := repo.EventLog()
	if _, err := log.Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, Payload: json.RawMessage(`{"provider_session_id":"source","model":"test-model","permission_mode":"workspace"}`)}); err != nil {
		t.Fatalf("append session start: %v", err)
	}
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if !omitPrompt {
		payload, _ := json.Marshal(map[string]string{"text": prompt})
		if _, err := log.Append(eventlog.AppendInput{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, Payload: payload}); err != nil {
			t.Fatalf("append prompt: %v", err)
		}
	}
	return sessionID, turnID
}

func writeCaseFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func ptr(value string) *string { return &value }
func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
