package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/bisect"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

// Ways bisect can go wrong, each covered below through the real command:
//
//   - blaming a turn when the break happened between turns (human edits);
//   - reporting a culprit when the good endpoint fails or the bad endpoint
//     passes, instead of refusing to bisect;
//   - counting a check that could not launch as a failed state;
//   - hiding turns that --path excluded from the window where the break sits;
//   - selecting or launching a check that --check did not name;
//   - wasting runs on identical consecutive checkpoints;
//   - touching the workspace, the private refs, the user's Git state, or
//     leaving evaluation directories behind.

func TestBisectFindsTheTurnThatBrokeACheck(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	fixture.turn(func() { fixture.write("other.txt", "2\n") })
	fixture.turn(func() {
		fixture.intent("Retry loop resets the counter on every attempt", "app.txt")
		fixture.write("app.txt", "broken\n")
	})
	fixture.turn(func() { fixture.write("other.txt", "4\n") })
	fixture.turn(func() { fixture.write("other.txt", "5\n") })
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})

	refsBefore := fixture.privateRefs()
	gitBefore := captureUserGitState(t, fixture.root())

	output, err := executeVerifyCommand(t, fixture.root(), "bisect", "--json")
	if err != nil {
		t.Fatalf("bisect: %v\n%s", err, output)
	}
	report := decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindTurn {
		t.Fatalf("result = %#v", report.Result)
	}
	if got := report.Result.FirstBad.Display(); got != fixture.session.String()+":3:post" {
		t.Fatalf("first bad = %s", got)
	}
	if got := report.Result.LastGood.Display(); got != fixture.session.String()+":2:post" {
		t.Fatalf("last good = %s", got)
	}
	culprit := report.Result.Culprit
	if culprit == nil || culprit.TurnID.Uint64() != 3 || culprit.SessionID != fixture.session {
		t.Fatalf("culprit = %#v", culprit)
	}
	if len(culprit.Intents) != 1 || culprit.Intents[0].Problem != "Retry loop resets the counter on every attempt" {
		t.Fatalf("intents = %#v", culprit.Intents)
	}
	if len(report.Result.Changed) != 1 || report.Result.Changed[0].Path != "app.txt" {
		t.Fatalf("changed = %#v", report.Result.Changed)
	}
	if len(report.Result.FailedChecks) != 1 || report.Result.FailedChecks[0] != "content" {
		t.Fatalf("failed checks = %#v", report.Result.FailedChecks)
	}
	// Six distinct states: two endpoints plus a binary search over four.
	if len(report.States) != 6 || len(report.Runs) != 4 {
		t.Fatalf("states = %d runs = %d\n%s", len(report.States), len(report.Runs), output)
	}
	if report.Runs[0].Outcome != bisect.OutcomePassed || report.Runs[1].Outcome != bisect.OutcomeFailed {
		t.Fatalf("endpoint runs = %#v", report.Runs[:2])
	}

	if got := readCLIFile(t, fixture.root(), "app.txt"); got != "broken\n" {
		t.Fatalf("workspace changed: %q", got)
	}
	if after := fixture.privateRefs(); after != refsBefore {
		t.Fatalf("private refs changed:\n%s\n%s", refsBefore, after)
	}
	if after := captureUserGitState(t, fixture.root()); after != gitBefore {
		t.Fatal("user Git state changed")
	}
	assertVerifyTempEmpty(t, fixture.repo)

	human, err := executeVerifyCommand(t, fixture.root(), "bisect")
	if err != nil {
		t.Fatalf("bisect human: %v\n%s", err, human)
	}
	for _, want := range []string{
		"culprit: turn " + fixture.session.String() + ":3",
		"first bad: " + fixture.session.String() + ":3:post",
		"last good: " + fixture.session.String() + ":2:post",
		"Retry loop resets the counter on every attempt",
		"app.txt",
		"content",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output missing %q:\n%s", want, human)
		}
	}
}

func TestBisectReportsBreaksOutsideRecordedTurns(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	fixture.turn(func() { fixture.write("other.txt", "2\n") })
	fixture.write("app.txt", "broken\n")
	fixture.turn(func() { fixture.write("other.txt", "3\n") })
	fixture.turn(func() { fixture.write("other.txt", "4\n") })
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})

	output, err := executeVerifyCommand(t, fixture.root(), "bisect", "--json")
	if err != nil {
		t.Fatalf("bisect: %v\n%s", err, output)
	}
	report := decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindOutsideRecordedTurns || report.Result.Culprit != nil {
		t.Fatalf("result = %#v", report.Result)
	}
	if got := report.Result.LastGood.Display(); got != fixture.session.String()+":2:post" {
		t.Fatalf("last good = %s", got)
	}
	if got := report.Result.FirstBad.Display(); got != fixture.session.String()+":3:pre" {
		t.Fatalf("first bad = %s", got)
	}
	if len(report.Result.Changed) != 1 || report.Result.Changed[0].Path != "app.txt" {
		t.Fatalf("changed = %#v", report.Result.Changed)
	}

	human, err := executeVerifyCommand(t, fixture.root(), "bisect")
	if err != nil {
		t.Fatalf("bisect human: %v\n%s", err, human)
	}
	if !strings.Contains(human, "outside recorded turns") {
		t.Fatalf("human output missing outside-turn explanation:\n%s", human)
	}
}

func TestBisectPathFilterNarrowsCandidatesAndDisclosesSkippedTurns(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	fixture.turn(func() { fixture.write("app.txt", "broken\n") })
	fixture.turn(func() { fixture.write("other.txt", "3\n") })
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})

	output, err := executeVerifyCommand(t, fixture.root(), "bisect", "--path", "app.txt", "--json")
	if err != nil {
		t.Fatalf("bisect --path app.txt: %v\n%s", err, output)
	}
	report := decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindTurn || report.Result.Culprit == nil || report.Result.Culprit.TurnID.Uint64() != 2 {
		t.Fatalf("result = %#v", report.Result)
	}
	if len(report.States) != 2 || len(report.Runs) != 2 {
		t.Fatalf("states = %d runs = %d", len(report.States), len(report.Runs))
	}
	if len(report.ExcludedTurns) != 2 {
		t.Fatalf("excluded = %#v", report.ExcludedTurns)
	}

	// Excluding the real culprit must not silently blame something else.
	output, err = executeVerifyCommand(t, fixture.root(), "bisect", "--path", "other.txt", "--json")
	if err != nil {
		t.Fatalf("bisect --path other.txt: %v\n%s", err, output)
	}
	report = decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindOutsideRecordedTurns {
		t.Fatalf("result = %#v", report.Result)
	}
	if len(report.Result.SkippedBetween) != 1 || report.Result.SkippedBetween[0].TurnID.Uint64() != 2 {
		t.Fatalf("skipped between = %#v", report.Result.SkippedBetween)
	}
	human, err := executeVerifyCommand(t, fixture.root(), "bisect", "--path", "other.txt")
	if err != nil {
		t.Fatalf("bisect human: %v\n%s", err, human)
	}
	if !strings.Contains(human, "excluded by --path") {
		t.Fatalf("human output does not disclose skipped turns:\n%s", human)
	}
}

func TestBisectRefusesEndpointsThatDoNotBracketTheBreak(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	fixture.turn(func() { fixture.write("other.txt", "2\n") })
	fixture.turn(func() { fixture.write("app.txt", "broken\n") })
	fixture.turn(func() { fixture.write("other.txt", "4\n") })
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "bad passes", args: []string{"--bad", fixture.session.String() + ":2"}, want: "passes"},
		{name: "good fails", args: []string{"--good", fixture.session.String() + ":3"}, want: "fails"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"bisect", "--json"}, test.args...)
			output, err := executeVerifyCommand(t, fixture.root(), args...)
			code, ok := commandExitCode(err)
			if !ok || code != verifyFailureExitCode {
				t.Fatalf("exit = %d %t err = %v\n%s", code, ok, err, output)
			}
			report := decodeBisectReport(t, output)
			if report.Result.Kind != bisect.KindNotBisectable || !strings.Contains(report.Result.Reason, test.want) {
				t.Fatalf("result = %#v", report.Result)
			}
			assertVerifyTempEmpty(t, fixture.repo)
		})
	}

	_, err := executeVerifyCommand(t, fixture.root(), "bisect", "--good", fixture.session.String()+":3", "--bad", fixture.session.String()+":1")
	if err == nil || !strings.Contains(err.Error(), "before") {
		t.Fatalf("reversed endpoints error = %v", err)
	}
	_, err = executeVerifyCommand(t, fixture.root(), "bisect", "--good", fixture.session.String()+":9")
	if err == nil || !strings.Contains(err.Error(), "not a bisect state") {
		t.Fatalf("unknown endpoint error = %v", err)
	}
}

func TestBisectCheckFlagSelectsVerifiersAndRejectsUnknownNames(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	fixture.turn(func() { fixture.write("app.txt", "broken\n") })
	fixture.turn(func() { fixture.write("other.txt", "3\n") })
	sentinel := filepath.Join(t.TempDir(), "launched")
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{
		{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}},
		{Name: "always-fails", Mode: "fail"},
		{Name: "marker", Mode: "touch", Args: []string{sentinel}},
	})

	_, err := executeVerifyCommand(t, fixture.root(), "bisect", "--check", "missing")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unknown check error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatal("a verifier launched before the check names were validated")
	}

	output, err := executeVerifyCommand(t, fixture.root(), "bisect", "--check", "content", "--json")
	if err != nil {
		t.Fatalf("bisect --check content: %v\n%s", err, output)
	}
	report := decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindTurn || report.Result.Culprit == nil || report.Result.Culprit.TurnID.Uint64() != 2 {
		t.Fatalf("result = %#v", report.Result)
	}
	if len(report.Checks) != 1 || report.Checks[0] != "content" {
		t.Fatalf("checks = %#v", report.Checks)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatal("an unselected verifier ran")
	}

	// Every check, including one that always fails, makes the good endpoint fail.
	output, err = executeVerifyCommand(t, fixture.root(), "bisect", "--json")
	if code, ok := commandExitCode(err); !ok || code != verifyFailureExitCode {
		t.Fatalf("exit = %d %t err = %v\n%s", code, ok, err, output)
	}
	if report := decodeBisectReport(t, output); report.Result.Kind != bisect.KindNotBisectable {
		t.Fatalf("result = %#v", report.Result)
	}
}

func TestBisectAbortsWhenACheckCannotLaunch(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("app.txt", "broken\n") })
	missing := filepath.Join(t.TempDir(), "missing-verifier")
	config := fmt.Sprintf("version = 1\n\n[[verify]]\nname = \"broken-tool\"\ncommand = %q\nargs = []\ntimeout = \"5s\"\n", missing)
	if err := os.WriteFile(filepath.Join(fixture.repo.MetadataDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := executeVerifyCommand(t, fixture.root(), "bisect")
	if err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("launch error = %v", err)
	}
	if _, ok := commandExitCode(err); ok {
		t.Fatalf("launch failure reported as a classified outcome: %v", err)
	}
	assertVerifyTempEmpty(t, fixture.repo)
}

func TestBisectRequiresVerifiersAndCompletedTurns(t *testing.T) {
	fixture := bisectFixture(t)
	_, err := executeVerifyCommand(t, fixture.root(), "bisect")
	if err == nil || !strings.Contains(err.Error(), "no repository verifiers") {
		t.Fatalf("missing verifier error = %v", err)
	}

	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})
	_, err = executeVerifyCommand(t, fixture.root(), "bisect")
	if err == nil || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("no history error = %v", err)
	}
}

func TestBisectSessionFlagLimitsHistory(t *testing.T) {
	fixture := bisectFixture(t)
	fixture.turn(func() { fixture.write("other.txt", "1\n") })
	other, _ := primitives.ParseSessionID("other-session")
	fixture.turnIn(other, func() { fixture.write("app.txt", "broken\n") })
	fixture.turn(func() { fixture.write("other.txt", "3\n") })
	writeVerifyConfig(t, fixture.repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", "ok\n"}}})

	output, err := executeVerifyCommand(t, fixture.root(), "bisect", "--json")
	if err != nil {
		t.Fatalf("bisect: %v\n%s", err, output)
	}
	report := decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindTurn || report.Result.Culprit == nil || report.Result.Culprit.SessionID != other {
		t.Fatalf("result = %#v", report.Result)
	}

	output, err = executeVerifyCommand(t, fixture.root(), "bisect", "--session", fixture.session.String(), "--json")
	if err != nil {
		t.Fatalf("bisect --session: %v\n%s", err, output)
	}
	report = decodeBisectReport(t, output)
	if report.Result.Kind != bisect.KindOutsideRecordedTurns {
		t.Fatalf("result = %#v", report.Result)
	}
}

type bisectTestFixture struct {
	t        *testing.T
	repo     *checkpoint.Repo
	session  primitives.SessionID
	active   primitives.SessionID
	recorder turnevents.Recorder
	next     map[primitives.SessionID]uint64
}

func bisectFixture(t *testing.T) *bisectTestFixture {
	t.Helper()
	repo := cliVerifyRepoWithUserGit(t)
	initializeUserGitFixture(t, repo.WorkspaceRoot.String())
	writeCLIFile(t, repo.WorkspaceRoot.String(), "app.txt", "ok\n")
	writeCLIFile(t, repo.WorkspaceRoot.String(), "other.txt", "0\n")
	sessionID, _ := primitives.ParseSessionID("bisect-cli")
	log := eventlog.OpenFor(repo.MetadataDir, repo.WorkspaceRoot.String(), repo.RepoID, repo.StoreID, repo.WorktreeID, repo.EventProducerID)
	return &bisectTestFixture{
		t:        t,
		repo:     repo,
		session:  sessionID,
		recorder: turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual},
		next:     make(map[primitives.SessionID]uint64),
	}
}

func (fixture *bisectTestFixture) root() string {
	return fixture.repo.WorkspaceRoot.String()
}

func (fixture *bisectTestFixture) write(relative, content string) {
	writeCLIFile(fixture.t, fixture.root(), relative, content)
}

func (fixture *bisectTestFixture) turn(during func()) {
	fixture.turnIn(fixture.session, during)
}

func (fixture *bisectTestFixture) turnIn(sessionID primitives.SessionID, during func()) {
	fixture.t.Helper()
	fixture.next[sessionID]++
	turnID, err := primitives.NewTurnID(fixture.next[sessionID])
	if err != nil {
		fixture.t.Fatalf("NewTurnID: %v", err)
	}
	if _, err := fixture.recorder.Start(sessionID, turnID); err != nil {
		fixture.t.Fatalf("start turn %s:%s: %v", sessionID, turnID, err)
	}
	fixture.active = sessionID
	during()
	if _, err := fixture.recorder.Finish(sessionID, turnID); err != nil {
		fixture.t.Fatalf("finish turn %s:%s: %v", sessionID, turnID, err)
	}
}

func (fixture *bisectTestFixture) intent(problem string, scope ...string) {
	fixture.t.Helper()
	turnID, _ := primitives.NewTurnID(fixture.next[fixture.active])
	if _, err := provenance.Record(fixture.repo, provenance.RecordInput{
		SessionID: fixture.active,
		TurnID:    turnID,
		Problem:   problem,
		Scope:     scope,
	}); err != nil {
		fixture.t.Fatalf("record intent: %v", err)
	}
}

func (fixture *bisectTestFixture) privateRefs() string {
	fixture.t.Helper()
	refs, err := fixture.repo.ListAllPrivateRefs()
	if err != nil {
		fixture.t.Fatalf("ListAllPrivateRefs: %v", err)
	}
	return strings.Join(refs, "\n")
}

func decodeBisectReport(t *testing.T, output string) bisect.Report {
	t.Helper()
	var report bisect.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode bisect report: %v\n%s", err, output)
	}
	if report.SchemaVersion != bisect.SchemaVersion {
		t.Fatalf("schema version = %d", report.SchemaVersion)
	}
	return report
}
