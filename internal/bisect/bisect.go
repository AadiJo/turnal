// Package bisect finds the recorded checkpoint that first made repository
// checks fail. It walks completed turns in blame's chronological order, runs
// the verifier contract against materialized checkpoints, and reports either
// the culprit turn or, when the break sits between two turns, that the change
// happened outside recorded turns.
package bisect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/blame"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/verifier"
)

const SchemaVersion = 1

type Kind string

const (
	// KindTurn means a turn's post checkpoint is the first failing state.
	KindTurn Kind = "turn"
	// KindOutsideRecordedTurns means a turn's pre checkpoint is the first
	// failing state, so the change landed between recorded turns.
	KindOutsideRecordedTurns Kind = "outside_recorded_turns"
	// KindNotBisectable means the endpoints do not bracket a break.
	KindNotBisectable Kind = "not_bisectable"
)

type Outcome string

const (
	OutcomePassed Outcome = "passed"
	OutcomeFailed Outcome = "failed"
)

type TurnRef struct {
	SessionID primitives.SessionID `json:"session_id"`
	TurnID    primitives.TurnID    `json:"turn_id"`
}

func (ref TurnRef) String() string {
	return ref.SessionID.String() + ":" + ref.TurnID.String()
}

// State is one distinct workspace content in chronological order. Consecutive
// checkpoints with the same tree collapse into one state; the later
// identities are kept as aliases so users can refer to either.
type State struct {
	SessionID primitives.SessionID       `json:"session_id"`
	TurnID    primitives.TurnID          `json:"turn_id"`
	Phase     primitives.CheckpointPhase `json:"phase"`
	Ref       primitives.CheckpointRef   `json:"ref"`
	Commit    primitives.CommitSHA       `json:"commit"`
	Tree      string                     `json:"tree"`
	Time      time.Time                  `json:"time"`
	Aliases   []string                   `json:"aliases,omitempty"`
	turnIndex int
}

func (state State) Display() string {
	return fmt.Sprintf("%s:%s:%s", state.SessionID, state.TurnID, state.Phase)
}

type Change struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

type Culprit struct {
	SessionID primitives.SessionID       `json:"session_id"`
	TurnID    primitives.TurnID          `json:"turn_id"`
	Adapter   string                     `json:"adapter,omitempty"`
	Model     string                     `json:"model,omitempty"`
	Prompt    string                     `json:"prompt,omitempty"`
	ToolNames []string                   `json:"tool_names,omitempty"`
	Intents   []provenance.IntentPayload `json:"intents,omitempty"`
}

type Run struct {
	State      State           `json:"state"`
	Outcome    Outcome         `json:"outcome"`
	DurationMS int64           `json:"duration_ms"`
	Report     verifier.Report `json:"report"`
}

type Result struct {
	Kind     Kind   `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	LastGood *State `json:"last_good,omitempty"`
	FirstBad *State `json:"first_bad,omitempty"`
	// Changed lists the files that differ between the last good and first bad
	// states. For a culprit turn this is exactly what the turn changed.
	Changed      []Change `json:"changed,omitempty"`
	FailedChecks []string `json:"failed_checks,omitempty"`
	Culprit      *Culprit `json:"culprit,omitempty"`
	// SkippedBetween lists turns a path filter excluded that sit between the
	// last good and first bad states. They changed the workspace inside the
	// window but were never verified.
	SkippedBetween []TurnRef `json:"skipped_between,omitempty"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Session       string    `json:"session,omitempty"`
	Paths         []string  `json:"paths,omitempty"`
	Checks        []string  `json:"checks"`
	States        []State   `json:"states"`
	ExcludedTurns []TurnRef `json:"excluded_turns,omitempty"`
	Good          State     `json:"good"`
	Bad           State     `json:"bad"`
	Runs          []Run     `json:"runs"`
	Result        Result    `json:"result"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	DurationMS    int64     `json:"duration_ms"`
}

// Plan is the ordered search space built from completed turns.
type Plan struct {
	States        []State
	ExcludedTurns []TurnRef
	turns         []blame.CompletedTurn
	excludedIndex []int
}

// NewPlan orders checkpoints chronologically and collapses identical
// neighbours. When paths are given, only turns whose pre-to-post diff touches
// one of them contribute states; the rest are listed as excluded.
func NewPlan(repo *checkpoint.Repo, turns []blame.CompletedTurn, paths []string) (Plan, error) {
	if repo == nil {
		return Plan{}, fmt.Errorf("bisect plan requires checkpoint repo")
	}
	plan := Plan{turns: turns}
	for index, turn := range turns {
		if len(paths) > 0 {
			touched, err := turnTouches(repo, turn, paths)
			if err != nil {
				return Plan{}, err
			}
			if !touched {
				plan.ExcludedTurns = append(plan.ExcludedTurns, TurnRef{SessionID: turn.SessionID, TurnID: turn.TurnID})
				plan.excludedIndex = append(plan.excludedIndex, index)
				continue
			}
		}
		for _, info := range []checkpoint.CheckpointRefInfo{turn.Pre, turn.Post} {
			state, err := stateFromInfo(repo, turn, info, index)
			if err != nil {
				return Plan{}, err
			}
			plan.add(state)
		}
	}
	return plan, nil
}

func stateFromInfo(repo *checkpoint.Repo, turn blame.CompletedTurn, info checkpoint.CheckpointRefInfo, index int) (State, error) {
	tree, err := repo.CommitTree(info.Commit)
	if err != nil {
		return State{}, fmt.Errorf("resolve tree for %s:%s:%s: %w", turn.SessionID, turn.TurnID, info.Phase, err)
	}
	return State{
		SessionID: turn.SessionID,
		TurnID:    turn.TurnID,
		Phase:     info.Phase,
		Ref:       info.Ref,
		Commit:    info.Commit,
		Tree:      tree,
		Time:      info.Time,
		turnIndex: index,
	}, nil
}

func (plan *Plan) add(state State) {
	if last := len(plan.States) - 1; last >= 0 && plan.States[last].Tree == state.Tree {
		plan.States[last].Aliases = append(plan.States[last].Aliases, state.Display())
		return
	}
	plan.States = append(plan.States, state)
}

func turnTouches(repo *checkpoint.Repo, turn blame.CompletedTurn, paths []string) (bool, error) {
	changes, err := repo.DiffNameStatusCommits(turn.Pre.Commit, turn.Post.Commit)
	if err != nil {
		return false, fmt.Errorf("diff turn %s:%s for path filter: %w", turn.SessionID, turn.TurnID, err)
	}
	for _, change := range changes {
		if provenance.ScopeMatches(paths, change.Path) || (change.OldPath != "" && provenance.ScopeMatches(paths, change.OldPath)) {
			return true, nil
		}
	}
	return false, nil
}

// Find locates a state by its own identity or one of its aliases.
func (plan Plan) Find(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (int, bool) {
	display := State{SessionID: sessionID, TurnID: turnID, Phase: phase}.Display()
	for index, state := range plan.States {
		if state.Display() == display {
			return index, true
		}
		for _, alias := range state.Aliases {
			if alias == display {
				return index, true
			}
		}
	}
	return 0, false
}

// Probe evaluates one state and returns the verifier report for it.
type Probe func(ctx context.Context, state State) (verifier.Report, error)

type Request struct {
	Repo      *checkpoint.Repo
	Plan      Plan
	GoodIndex int
	BadIndex  int
	Probe     Probe
	Checks    []string
	Session   string
	Paths     []string
	Now       func() time.Time
}

// Search verifies both endpoints, then binary searches the states between
// them. A check that cannot launch or hits an infrastructure error aborts the
// search instead of counting as a failing state.
func Search(ctx context.Context, request Request) (Report, error) {
	if ctx == nil {
		return Report{}, fmt.Errorf("bisect context is required")
	}
	if request.Repo == nil || request.Probe == nil {
		return Report{}, fmt.Errorf("bisect requires checkpoint repo and probe")
	}
	states := request.Plan.States
	if request.GoodIndex < 0 || request.BadIndex >= len(states) || request.GoodIndex >= request.BadIndex {
		return Report{}, fmt.Errorf("bisect endpoints must be ordered states in the plan")
	}
	now := request.Now
	if now == nil {
		now = time.Now
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		Session:       request.Session,
		Paths:         request.Paths,
		Checks:        request.Checks,
		States:        states,
		ExcludedTurns: request.Plan.ExcludedTurns,
		Good:          states[request.GoodIndex],
		Bad:           states[request.BadIndex],
		Runs:          make([]Run, 0),
		StartedAt:     now().UTC(),
	}
	finish := func() {
		report.FinishedAt = now().UTC()
		report.DurationMS = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	}

	runsByIndex := make(map[int]int)
	probe := func(index int) (Outcome, error) {
		state := states[index]
		started := now()
		verified, err := request.Probe(ctx, state)
		if err != nil {
			return "", err
		}
		outcome, err := classify(state, verified)
		if err != nil {
			return "", err
		}
		report.Runs = append(report.Runs, Run{
			State:      state,
			Outcome:    outcome,
			DurationMS: now().Sub(started).Milliseconds(),
			Report:     verified,
		})
		runsByIndex[index] = len(report.Runs) - 1
		return outcome, nil
	}

	good := states[request.GoodIndex]
	bad := states[request.BadIndex]
	outcome, err := probe(request.GoodIndex)
	if err != nil {
		return Report{}, err
	}
	if outcome != OutcomePassed {
		report.Result = Result{Kind: KindNotBisectable, Reason: fmt.Sprintf("good state %s fails the checks", good.Display())}
		finish()
		return report, nil
	}
	outcome, err = probe(request.BadIndex)
	if err != nil {
		return Report{}, err
	}
	if outcome != OutcomeFailed {
		report.Result = Result{Kind: KindNotBisectable, Reason: fmt.Sprintf("bad state %s passes the checks", bad.Display())}
		finish()
		return report, nil
	}

	low, high := request.GoodIndex, request.BadIndex
	for high-low > 1 {
		middle := low + (high-low)/2
		outcome, err := probe(middle)
		if err != nil {
			return Report{}, err
		}
		if outcome == OutcomePassed {
			low = middle
		} else {
			high = middle
		}
	}

	lastGood, firstBad := states[low], states[high]
	changes, err := request.Repo.DiffNameStatusCommits(lastGood.Commit, firstBad.Commit)
	if err != nil {
		return Report{}, fmt.Errorf("diff %s..%s: %w", lastGood.Display(), firstBad.Display(), err)
	}
	result := Result{
		LastGood: &lastGood,
		FirstBad: &firstBad,
	}
	for _, change := range changes {
		result.Changed = append(result.Changed, Change{Status: change.Status, Path: change.Path, OldPath: change.OldPath})
	}
	for _, check := range report.Runs[runsByIndex[high]].Report.Checks {
		if check.Status != verifier.StatusPassed {
			result.FailedChecks = append(result.FailedChecks, check.Name)
		}
	}
	for position, index := range request.Plan.excludedIndex {
		if index > lastGood.turnIndex && index < firstBad.turnIndex {
			result.SkippedBetween = append(result.SkippedBetween, request.Plan.ExcludedTurns[position])
		}
	}
	if firstBad.Phase == primitives.CheckpointPhasePost {
		turn := request.Plan.turns[firstBad.turnIndex]
		result.Kind = KindTurn
		result.Culprit = &Culprit{
			SessionID: turn.SessionID,
			TurnID:    turn.TurnID,
			Adapter:   turn.Adapter,
			Model:     turn.Model,
			Prompt:    turn.Prompt,
			ToolNames: turn.ToolNames,
			Intents:   turn.Intents,
		}
	} else {
		result.Kind = KindOutsideRecordedTurns
	}
	report.Result = result
	finish()
	return report, nil
}

func classify(state State, report verifier.Report) (Outcome, error) {
	for _, check := range report.Checks {
		if check.Status == verifier.StatusLaunchError {
			detail := strings.TrimSpace(check.LaunchError)
			if detail == "" {
				detail = "unknown launch error"
			}
			return "", fmt.Errorf("cannot classify %s: check %q could not start: %s", state.Display(), check.Name, detail)
		}
		if len(check.InfrastructureErrors) > 0 {
			first := check.InfrastructureErrors[0]
			return "", fmt.Errorf("cannot classify %s: check %q infrastructure error at %s: %s", state.Display(), check.Name, first.Stage, first.Message)
		}
	}
	if report.Successful() {
		return OutcomePassed, nil
	}
	return OutcomeFailed, nil
}
