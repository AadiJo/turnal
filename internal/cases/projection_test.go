package cases

import (
	"encoding/json"
	"strings"
	"testing"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestProjectRejectsDuplicateCreationAndRevisionGaps(t *testing.T) {
	fixture := projectionFixture(t)
	t.Run("duplicate task", func(t *testing.T) {
		events := append([]eventlog.Event(nil), fixture.events...)
		events = append(events, fixture.events[0])
		events[len(events)-1].Seq = 4
		_, err := project([]eventlog.DurableStream{{Events: events}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "duplicate creation") {
			t.Fatalf("project error = %v", err)
		}
	})
	t.Run("revision gap", func(t *testing.T) {
		payload := taskRevisionPayload{TaskID: fixture.taskID, revisionDefinition: revisionDefinition{Revision: 3, Instruction: fixture.instruction, Source: fixture.source}}
		event := fixture.event(t, 4, primitives.EventTypeTaskRevision, payload)
		_, err := project([]eventlog.DurableStream{{Events: append(fixture.events, event)}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "skip number 2") {
			t.Fatalf("project error = %v", err)
		}
	})
	t.Run("revision reuse", func(t *testing.T) {
		payload := taskRevisionPayload{TaskID: fixture.taskID, revisionDefinition: revisionDefinition{Revision: 1, Instruction: fixture.instruction, Source: fixture.source}}
		event := fixture.event(t, 4, primitives.EventTypeTaskRevision, payload)
		_, err := project([]eventlog.DurableStream{{Events: append(fixture.events, event)}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "duplicated or reused") {
			t.Fatalf("project error = %v", err)
		}
	})
}

func TestProjectRejectsCaseAndAttemptRelationshipViolations(t *testing.T) {
	fixture := projectionFixture(t)
	t.Run("nonexistent task", func(t *testing.T) {
		foreignTask, _ := primitives.ParseTaskID("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		payload := fixture.casePayload
		payload.TaskID = foreignTask
		event := fixture.event(t, 3, primitives.EventTypeCaseCreate, payload)
		_, err := project([]eventlog.DurableStream{{Events: []eventlog.Event{fixture.events[0], event}}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "nonexistent task") {
			t.Fatalf("project error = %v", err)
		}
	})
	t.Run("conflicting case", func(t *testing.T) {
		duplicate := fixture.events[1]
		duplicate.Seq = 3
		_, err := project([]eventlog.DurableStream{{Events: append(fixture.events, duplicate)}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "duplicate or conflicting") {
			t.Fatalf("project error = %v", err)
		}
	})
	t.Run("nonexistent attempt", func(t *testing.T) {
		attemptID, _ := primitives.ParseAttemptID("attempt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		runID, _ := primitives.ParseRunID("run_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		link := fixture.event(t, 3, primitives.EventTypeCaseAttemptLink, caseAttemptLinkPayload{CaseID: fixture.caseID, Scope: fixture.scope, RunID: runID, AttemptID: attemptID, Source: fixture.source})
		_, err := project([]eventlog.DurableStream{{Events: append(fixture.events, link)}}, fixture.scope, nil)
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("project error = %v", err)
		}
	})
	t.Run("foreign attempt scope", func(t *testing.T) {
		attemptID, _ := primitives.ParseAttemptID("attempt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		runID, _ := primitives.ParseRunID("run_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		foreign := fixture.scope
		foreign.WorktreeID, _ = primitives.ParseWorktreeID("wt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		link := fixture.event(t, 3, primitives.EventTypeCaseAttemptLink, caseAttemptLinkPayload{CaseID: fixture.caseID, Scope: fixture.scope, RunID: runID, AttemptID: attemptID, Source: fixture.source})
		attempts := map[primitives.AttemptID]attemptRecord{attemptID: {RunID: runID, AttemptID: attemptID, Scope: foreign, Source: fixture.source}}
		_, err := project([]eventlog.DurableStream{{Events: append(fixture.events, link)}}, fixture.scope, attempts)
		if err == nil || !strings.Contains(err.Error(), "different repository, store, or worktree") {
			t.Fatalf("project error = %v", err)
		}
	})
}

func TestProjectExistingHistoryWithoutTaskOrCaseRecords(t *testing.T) {
	fixture := projectionFixture(t)
	event := fixture.event(t, 1, primitives.EventTypePromptUser, map[string]string{"text": "old history"})
	projection, err := project([]eventlog.DurableStream{{Events: []eventlog.Event{event}}}, fixture.scope, nil)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if projection.Version != JSONVersion || len(projection.Tasks) != 0 || len(projection.Cases) != 0 {
		t.Fatalf("projection = %#v", projection)
	}
}

type projectionTestFixture struct {
	scope       Scope
	source      SourceTurn
	taskID      primitives.TaskID
	caseID      primitives.CaseID
	instruction fork.Instruction
	casePayload caseCreatePayload
	events      []eventlog.Event
}

func projectionFixture(t *testing.T) projectionTestFixture {
	t.Helper()
	repoID, _ := primitives.ParseRepoID("repo_11111111111111111111111111111111")
	storeID, _ := primitives.ParseStoreID("store_22222222222222222222222222222222")
	worktreeID, _ := primitives.ParseWorktreeID("wt_33333333333333333333333333333333")
	streamID, _ := primitives.ParseEventStreamID("stream_44444444444444444444444444444444")
	sessionID, _ := primitives.ParseSessionID("session")
	turnID, _ := primitives.NewTurnID(1)
	taskID, _ := primitives.ParseTaskID("task_55555555555555555555555555555555")
	caseID, _ := primitives.ParseCaseID("case_66666666666666666666666666666666")
	commit, _ := primitives.ParseCommitSHA(strings.Repeat("a", 40))
	ref, _ := primitives.ParseCheckpointRef("refs/agent-vcs/checkpoints/session/turn/000001/pre")
	scope := Scope{RepoID: repoID, StoreID: storeID, WorktreeID: worktreeID}
	source := SourceTurn{SessionID: sessionID, TurnID: turnID, StreamID: streamID}
	instruction := fork.Instruction{Status: fork.InstructionAvailable, Text: "Fix it", Adapter: primitives.AdapterCodex}
	readiness := fork.Report{Version: 1, Source: fork.Source{SessionID: sessionID, TurnID: turnID, WorktreeID: worktreeID, StreamID: streamID}, Base: fork.Base{Status: "available", Phase: primitives.CheckpointPhasePre, Ref: ref, CommitSHA: commit}, Instruction: instruction}
	fixture := projectionTestFixture{scope: scope, source: source, taskID: taskID, caseID: caseID, instruction: instruction}
	fixture.casePayload = caseCreatePayload{CaseID: caseID, TaskID: taskID, TaskRevision: 1, Scope: scope, Source: source, Readiness: readiness}
	fixture.events = []eventlog.Event{
		fixture.event(t, 1, primitives.EventTypeTaskCreate, taskCreatePayload{TaskID: taskID, Scope: scope, InitialRevision: revisionDefinition{Revision: 1, Instruction: instruction, Source: source}}),
		fixture.event(t, 2, primitives.EventTypeCaseCreate, fixture.casePayload),
	}
	return fixture
}

func (fixture projectionTestFixture) event(t *testing.T, seq uint64, eventType primitives.EventType, payload any) eventlog.Event {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	parsedSeq, _ := primitives.NewEventSeq(seq)
	turnID := fixture.source.TurnID
	return eventlog.Event{Version: 2, RepoID: fixture.scope.RepoID, WorktreeID: fixture.scope.WorktreeID, StreamID: fixture.source.StreamID, Seq: parsedSeq, SessionID: fixture.source.SessionID, TurnID: &turnID, Type: eventType, Adapter: primitives.AdapterCodex, Payload: encoded}
}
