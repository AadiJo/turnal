package cli

import (
	"encoding/json"
	"strings"
	"testing"

	caseengine "github.com/AadiJo/turnal/internal/cases"
)

func TestCaseCreateAndShowHumanAndVersionedJSON(t *testing.T) {
	root, sessionID, _ := createForkReadyTurn(t, "Fix the parser", true)
	t.Chdir(root.String())

	output := runRootStdout(t, "case", "create", sessionID.String()+":1", "--json")
	var created caseCreateJSON
	if err := json.Unmarshal([]byte(output), &created); err != nil {
		t.Fatalf("decode case create JSON: %v\n%s", err, output)
	}
	if created.Version != caseengine.JSONVersion || !created.TaskCreated || created.Task.ID == "" || created.Case.ID == "" {
		t.Fatalf("created = %#v", created)
	}
	if created.Case.TaskID != created.Task.ID || created.Case.Readiness.Instruction.Text != "Fix the parser" {
		t.Fatalf("created task/case = %#v / %#v", created.Task, created.Case)
	}

	human := runRootStdout(t, "case", "show", created.Case.ID.String())
	for _, want := range []string{
		"case:           " + created.Case.ID.String(),
		"task:           " + created.Task.ID.String() + " revision 1",
		"source turn:    " + sessionID.String() + ":1",
		"base ref:       refs/agent-vcs/",
		"base commit:",
		"instruction:    available",
		"readiness:      needs_context",
		"fidelity:       L1",
		"verifiers:      none",
		"attempts:       none linked",
		"Secrets-denied path patterns",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("case show missing %q:\n%s", want, human)
		}
	}

	caseOutput := runRootStdout(t, "case", "show", created.Case.ID.String(), "--json")
	var shown caseJSON
	if err := json.Unmarshal([]byte(caseOutput), &shown); err != nil {
		t.Fatalf("decode case show JSON: %v", err)
	}
	if shown.Version != caseengine.JSONVersion || shown.Case.ID != created.Case.ID {
		t.Fatalf("shown case = %#v", shown)
	}

	taskHuman := runRootStdout(t, "task", "show", created.Task.ID.String())
	if !strings.Contains(taskHuman, "revisions:\n  1  available") || !strings.Contains(taskHuman, created.Case.ID.String()) {
		t.Fatalf("task show = %s", taskHuman)
	}
	taskOutput := runRootStdout(t, "task", "show", created.Task.ID.String(), "--json")
	var shownTask taskJSON
	if err := json.Unmarshal([]byte(taskOutput), &shownTask); err != nil {
		t.Fatalf("decode task show JSON: %v", err)
	}
	if shownTask.Version != caseengine.JSONVersion || shownTask.Task.ID != created.Task.ID || len(shownTask.Cases) != 1 || shownTask.Cases[0] != created.Case.ID {
		t.Fatalf("shown task = %#v", shownTask)
	}
}

func TestCaseCreateRejectsMalformedTaskID(t *testing.T) {
	root, sessionID, _ := createForkReadyTurn(t, "Fix the parser", true)
	t.Chdir(root.String())
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"case", "create", sessionID.String() + ":1", "--task", "task_bad"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid task id") {
		t.Fatalf("case create error = %v", err)
	}
}
