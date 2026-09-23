// Package bisect finds a recorded checkpoint transition where repository
// checks go from passing to failing. It orders checkpoints by capture time,
// runs the verifier contract against materialized checkpoints, and reports
// the culprit turn when exactly one turn was active in that window. When
// several turns were active, or none, it says so instead of guessing.
package bisect

import (
	"context"
	"fmt"
	"sort"
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
	// KindTurn means the first bad state is a turn's post checkpoint and no
	// other turn was active between the last good and first bad states.
	KindTurn Kind = "turn"
	// KindOutsideRecordedTurns means no recorded turn was active between the
	// last good and first bad states; the change came from elsewhere.
	KindOutsideRecordedTurns Kind = "outside_recorded_turns"
	// KindAmbiguous means more than one turn, an excluded turn, or an
	// unfinished turn was active in the window, so no single turn is blamed.
	KindAmbiguous Kind = "ambiguous"
	// KindNotBisectable means the endpoints do not bracket a break.
	KindNotBisectable Kind = "not_bisectable"
)

type Outcome string

const (
	OutcomePassed Outcome = "passed"
	OutcomeFailed Outcome = "failed"
)

const (
	ExcludedBySession = "session"
	ExcludedByPath    = "path"
)

// TurnRef names a turn and, when relevant, why it was not a search candidate.
type TurnRef struct {
	SessionID  primitives.SessionID `json:"session_id"`
	TurnID     primitives.TurnID    `json:"turn_id"`
	ExcludedBy string               `json:"excluded_by,omitempty"`
	Incomplete bool                 `json:"incomplete,omitempty"`
}

func (ref TurnRef) String() string {
	return ref.SessionID.String() + ":" + ref.TurnID.String()
}

// State is one distinct workspace content, ordered by capture time.
// Consecutive checkpoints with the same tree collapse into one state; the
// later identities are kept as aliases so users can refer to either.
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
	// Participants are the turns active between the last good and first bad
	// states, including turns the filters excluded and unfinished turns.
	Participants []TurnRef `json:"participants,omitempty"`
	Culprit      *Culprit  `json:"culprit,omitempty"`
}

type Filters struct {
	Session primitives.SessionID  `json:"session,omitempty"`
	Paths   []primitives.RepoPath `json:"paths,omitempty"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Filters       Filters   `json:"filters"`
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

// Plan is the ordered search space built from the worktree's turn history.
type Plan struct {
	States        []State
	ExcludedTurns []TurnRef
	Filters       Filters
	turns         []blame.CompletedTurn
	excludedBy    []string
	incomplete    []blame.IncompleteTurn
}

// NewPlan orders every completed turn's checkpoints by capture time and
// collapses identical neighbours. Filters remove turns from the candidates
// but not from the history, so an excluded turn active in the final window
// is still disclosed.
func NewPlan(repo *checkpoint.Repo, history blame.History, filters Filters) (Plan, error) {
	if repo == nil {
		return Plan{}, fmt.Errorf("bisect plan requires checkpoint repo")
	}
	plan := Plan{
		Filters:    filters,
		turns:      history.Completed,
		excludedBy: make([]string, len(history.Completed)),
		incomplete: history.Incomplete,
	}
	commits := make([]primitives.CommitSHA, 0, 2*len(history.Completed))
	for _, turn := range history.Completed {
		commits = append(commits, turn.Pre.Commit, turn.Post.Commit)
	}
	trees, err := repo.CommitTrees(commits)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve checkpoint trees: %w", err)
	}

	var candidates []State
	for index, turn := range history.Completed {
		reason, err := exclusionReason(repo, turn, filters)
		if err != nil {
			return Plan{}, err
		}
		plan.excludedBy[index] = reason
		if reason != "" {
			plan.ExcludedTurns = append(plan.ExcludedTurns, TurnRef{SessionID: turn.SessionID, TurnID: turn.TurnID, ExcludedBy: reason})
			continue
		}
		candidates = append(candidates,
			newState(turn, turn.Pre, trees[2*index], turn.Start, index),
			newState(turn, turn.Post, trees[2*index+1], turn.End, index),
		)
	}
	// Stable: ties keep blame's turn order, and a turn's pre before its post.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Time.Before(candidates[j].Time)
	})
	for _, state := range candidates {
		if last := len(plan.States) - 1; last >= 0 && plan.States[last].Tree == state.Tree {
			plan.States[last].Aliases = append(plan.States[last].Aliases, state.Display())
			continue
		}
		plan.States = append(plan.States, state)
	}
	return plan, nil
}

func newState(turn blame.CompletedTurn, info checkpoint.CheckpointRefInfo, tree string, at time.Time, index int) State {
	return State{
		SessionID: turn.SessionID,
		TurnID:    turn.TurnID,
		Phase:     info.Phase,
		Ref:       info.Ref,
		Commit:    info.Commit,
		Tree:      tree,
		Time:      at,
		turnIndex: index,
	}
}

func exclusionReason(repo *checkpoint.Repo, turn blame.CompletedTurn, filters Filters) (string, error) {
	if filters.Session != "" && turn.SessionID != filters.Session {
		return ExcludedBySession, nil
	}
	if len(filters.Paths) == 0 {
		return "", nil
	}
	changes, err := repo.DiffNameStatusCommits(turn.Pre.Commit, turn.Post.Commit)
	if err != nil {
		return "", fmt.Errorf("diff turn %s:%s for path filter: %w", turn.SessionID, turn.TurnID, err)
	}
	scope := make([]string, 0, len(filters.Paths))
	for _, path := range filters.Paths {
		scope = append(scope, path.String())
	}
	for _, change := range changes {
		if provenance.ScopeMatches(scope, change.Path) || (change.OldPath != "" && provenance.ScopeMatches(scope, change.OldPath)) {
			return "", nil
		}
	}
	return ExcludedByPath, nil
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
	Now       func() time.Time
}

// Search verifies both endpoints, then binary searches the states between
// them. Like git bisect, it assumes checks stay failing once broken: with a
// flaky or non-monotonic history it still returns an adjacent, verified
// pass-to-fail transition, but not necessarily the earliest one. A check that
// cannot launch or hits an infrastructure error aborts the search instead of
// counting as a failing state.
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

	started := now()
	report := Report{
		SchemaVersion: SchemaVersion,
		Filters:       request.Plan.Filters,
		Checks:        request.Checks,
		States:        states,
		ExcludedTurns: request.Plan.ExcludedTurns,
		Good:          states[request.GoodIndex],
		Bad:           states[request.BadIndex],
		Runs:          make([]Run, 0),
		StartedAt:     started.UTC(),
	}
	finish := func() {
		finished := now()
		report.FinishedAt = finished.UTC()
		report.DurationMS = finished.Sub(started).Milliseconds()
	}

	runsByIndex := make(map[int]int)
	probe := func(index int) (Outcome, error) {
		state := states[index]
		runStarted := now()
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
			DurationMS: now().Sub(runStarted).Milliseconds(),
			Report:     verified,
		})
		runsByIndex[index] = len(report.Runs) - 1
		return outcome, nil
	}

	outcome, err := probe(request.GoodIndex)
	if err != nil {
		return Report{}, err
	}
	if outcome != OutcomePassed {
		report.Result = Result{Kind: KindNotBisectable, Reason: fmt.Sprintf("good state %s fails the checks", report.Good.Display())}
		finish()
		return report, nil
	}
	outcome, err = probe(request.BadIndex)
	if err != nil {
		return Report{}, err
	}
	if outcome != OutcomeFailed {
		report.Result = Result{Kind: KindNotBisectable, Reason: fmt.Sprintf("bad state %s passes the checks", report.Bad.Display())}
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
	result := Result{LastGood: &lastGood, FirstBad: &firstBad}
	for _, change := range changes {
		result.Changed = append(result.Changed, Change{Status: change.Status, Path: change.Path, OldPath: change.OldPath})
	}
	for _, check := range report.Runs[runsByIndex[high]].Report.Checks {
		if check.Status != verifier.StatusPassed {
			result.FailedChecks = append(result.FailedChecks, check.Name)
		}
	}
	result.Participants = request.Plan.participants(lastGood, firstBad)
	result.Kind = classifyWindow(firstBad, result.Participants)
	if result.Kind == KindTurn {
		turn := request.Plan.turns[firstBad.turnIndex]
		result.Culprit = &Culprit{
			SessionID: turn.SessionID,
			TurnID:    turn.TurnID,
			Adapter:   turn.Adapter,
			Model:     turn.Model,
			Prompt:    turn.Prompt,
			ToolNames: turn.ToolNames,
			Intents:   turn.Intents,
		}
	}
	report.Result = result
	finish()
	return report, nil
}

// participants lists every turn that could have changed the workspace
// between two adjacent states: the first bad state's own turn when it is a
// post checkpoint, any turn whose recorded span overlaps the open window,
// and any unfinished turn that started before the window closed. Boundary
// comparisons are strict so the sequential turn ending exactly at the last
// good state is not counted.
func (plan Plan) participants(lastGood, firstBad State) []TurnRef {
	var refs []TurnRef
	seen := make(map[string]bool)
	add := func(ref TurnRef) {
		if !seen[ref.String()] {
			seen[ref.String()] = true
			refs = append(refs, ref)
		}
	}
	if firstBad.Phase == primitives.CheckpointPhasePost {
		add(TurnRef{SessionID: firstBad.SessionID, TurnID: firstBad.TurnID, ExcludedBy: plan.excludedBy[firstBad.turnIndex]})
	}
	for index, turn := range plan.turns {
		if turn.Start.Before(firstBad.Time) && turn.End.After(lastGood.Time) {
			add(TurnRef{SessionID: turn.SessionID, TurnID: turn.TurnID, ExcludedBy: plan.excludedBy[index]})
		}
	}
	for _, turn := range plan.incomplete {
		if !turn.Start.IsZero() && turn.Start.Before(firstBad.Time) {
			add(TurnRef{SessionID: turn.SessionID, TurnID: turn.TurnID, Incomplete: true})
		}
	}
	return refs
}

func classifyWindow(firstBad State, participants []TurnRef) Kind {
	switch {
	case len(participants) == 0:
		return KindOutsideRecordedTurns
	case len(participants) == 1 && firstBad.Phase == primitives.CheckpointPhasePost &&
		participants[0].ExcludedBy == "" && !participants[0].Incomplete &&
		participants[0].SessionID == firstBad.SessionID && participants[0].TurnID == firstBad.TurnID:
		return KindTurn
	default:
		return KindAmbiguous
	}
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
