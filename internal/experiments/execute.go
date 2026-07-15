// Package experiments executes and evaluates durable Case attempts.
package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

const (
	EnvCaseID      = "TURNAL_FORK_CASE_ID"
	EnvAttemptID   = "TURNAL_FORK_ATTEMPT_ID"
	EnvRunID       = "TURNAL_FORK_RUN_ID"
	EnvSource      = "TURNAL_FORK_SOURCE"
	EnvBaseCommit  = "TURNAL_FORK_BASE_COMMIT"
	EnvInstruction = "TURNAL_FORK_INSTRUCTION"
)

type Request struct {
	Case    cases.Case
	Command []string
	Keep    bool
	Runner  Runner
}

type Result struct {
	CaseID        primitives.CaseID        `json:"case_id"`
	RunID         primitives.RunID         `json:"run_id"`
	AttemptID     primitives.AttemptID     `json:"attempt_id"`
	SessionID     primitives.SessionID     `json:"session_id"`
	TurnID        primitives.TurnID        `json:"turn_id"`
	BaseRef       primitives.CheckpointRef `json:"base_ref"`
	BaseCommit    primitives.CommitSHA     `json:"base_commit"`
	PostRef       primitives.CheckpointRef `json:"post_ref"`
	PostCommit    primitives.CommitSHA     `json:"post_commit"`
	Status        string                   `json:"status"`
	ExitCode      *int                     `json:"exit_code,omitempty"`
	Error         string                   `json:"error,omitempty"`
	Workspace     string                   `json:"workspace,omitempty"`
	WorkspaceKept bool                     `json:"workspace_kept"`
}

type Runner interface {
	Run(context.Context, string, []string, []string) (int, error)
}

type ExecRunner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    []string
}

func (runner ExecRunner) Run(ctx context.Context, root string, command, environment []string) (int, error) {
	if len(command) == 0 {
		return -1, fmt.Errorf("fork command is required")
	}
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Dir = root
	child.Stdin = runner.Stdin
	child.Stdout = runner.Stdout
	child.Stderr = runner.Stderr
	baseEnvironment := runner.Env
	if baseEnvironment == nil {
		baseEnvironment = os.Environ()
	}
	child.Env = forkEnvironment(baseEnvironment, root, environment)
	err := child.Run()
	if err == nil {
		return 0, nil
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	return -1, err
}

func Execute(ctx context.Context, repo *checkpoint.Repo, request Request) (result Result, resultErr error) {
	if repo == nil {
		return Result{}, fmt.Errorf("fork execution requires checkpoint repo")
	}
	if len(request.Command) == 0 || strings.TrimSpace(request.Command[0]) == "" {
		return Result{}, fmt.Errorf("fork execution requires a command after --")
	}
	if request.Runner == nil {
		request.Runner = ExecRunner{}
	}
	definition, err := currentCase(repo, request.Case.ID)
	if err != nil {
		return Result{}, err
	}
	if definition.Scope.RepoID != repo.RepoID || definition.Scope.StoreID != repo.StoreID || definition.Scope.WorktreeID != repo.WorktreeID {
		return Result{}, fmt.Errorf("case %s belongs to a different repository, store, or worktree", definition.ID)
	}
	if definition.Readiness.Base.Status != "available" || definition.Readiness.Base.Ref == "" || definition.Readiness.Base.CommitSHA == "" {
		return Result{}, fmt.Errorf("case %s has no executable pre-turn checkpoint", definition.ID)
	}
	baseCommit, err := repo.CheckpointCommit(definition.Readiness.Base.Ref)
	if err != nil {
		return Result{}, fmt.Errorf("resolve case base checkpoint: %w", err)
	}
	if baseCommit != definition.Readiness.Base.CommitSHA {
		return Result{}, fmt.Errorf("case base checkpoint invariant failed: ref %s points to %s, case records %s", definition.Readiness.Base.Ref, baseCommit, definition.Readiness.Base.CommitSHA)
	}

	root, err := os.MkdirTemp("", "turnal-fork-*")
	if err != nil {
		return Result{}, fmt.Errorf("create isolated fork workspace: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return Result{}, fmt.Errorf("secure isolated fork workspace: %w", err)
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		if err := os.RemoveAll(root); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove isolated fork workspace: %w", err))
		}
	}()
	if err := repo.MaterializeCommit(baseCommit, root, checkpoint.MaterializeOptions{ApplyCurrentSecretDenyGlobs: true}); err != nil {
		return Result{}, fmt.Errorf("materialize case base: %w", err)
	}
	captureRepo, err := repo.ForCaptureRoot(root)
	if err != nil {
		return Result{}, err
	}

	runID, err := primitives.NewRunID()
	if err != nil {
		return Result{}, err
	}
	sessionID, err := forkSessionID(runID)
	if err != nil {
		return Result{}, err
	}
	result = Result{CaseID: definition.ID, RunID: runID, SessionID: sessionID, BaseRef: definition.Readiness.Base.Ref, BaseCommit: baseCommit, Workspace: root, WorkspaceKept: request.Keep}
	if !request.Keep {
		result.Workspace = ""
	}

	unlockSession, err := adapters.AcquireSessionLock(repo, sessionID)
	if err != nil {
		return result, err
	}
	defer unlockSession()
	releaseLifecycle, err := runs.Begin(repo, runID, sessionID, request.Command)
	if err != nil {
		return result, err
	}
	defer releaseLifecycle()
	runOpen := true
	defer func() {
		if !runOpen {
			return
		}
		message := "fork execution ended before durable completion"
		if resultErr != nil {
			message = resultErr.Error()
		}
		if err := runs.Finish(repo, runID, sessionID, runs.StatusIncomplete, message); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finalize incomplete fork run %s: %w", runID, err))
		}
	}()
	if err := runs.LinkCapture(repo, runID, runs.CaptureWrapper, sessionID, primitives.AdapterCodex); err != nil {
		return result, err
	}
	if err := appendForkSession(repo, definition, runID, sessionID, request.Command); err != nil {
		return result, err
	}
	gitSync := false
	recorder := turnevents.Recorder{Log: captureRepo.EventLog(), Manager: turns.NewManager(captureRepo), Adapter: primitives.AdapterCodex}
	recorder.Manager.GitSyncEnabled = &gitSync
	started, err := recorder.Start(sessionID, 1)
	if err != nil {
		return result, err
	}
	result.TurnID = started.TurnID
	attemptID, err := runs.EnsureWrapperAttempt(repo, runID, sessionID, started.TurnID, primitives.AdapterCodex)
	if err != nil {
		return result, err
	}
	result.AttemptID = attemptID
	if _, err := cases.LinkAttempt(repo, cases.LinkAttemptRequest{CaseID: definition.ID, RunID: runID, AttemptID: attemptID, Command: request.Command}); err != nil {
		return result, err
	}

	environment := []string{
		EnvCaseID + "=" + definition.ID.String(),
		EnvAttemptID + "=" + attemptID.String(),
		EnvRunID + "=" + runID.String(),
		EnvSource + "=" + fmt.Sprintf("%s:%s", definition.Source.SessionID, definition.Source.TurnID),
		EnvBaseCommit + "=" + baseCommit.String(),
		EnvInstruction + "=" + definition.Readiness.Instruction.Text,
	}
	exitCode, commandErr := request.Runner.Run(ctx, root, append([]string(nil), request.Command...), environment)
	status := cases.AttemptStatusSucceeded
	var durableExitCode *int
	var durableError string
	switch {
	case commandErr != nil:
		status = cases.AttemptStatusIncomplete
		durableError = commandErr.Error()
	case exitCode != 0:
		status = cases.AttemptStatusFailed
		durableExitCode = &exitCode
		durableError = fmt.Sprintf("command exited with status %d", exitCode)
	default:
		durableExitCode = &exitCode
	}
	finished, finishErr := recorder.Finish(sessionID, started.TurnID)
	if finishErr != nil {
		return result, errors.Join(commandErr, fmt.Errorf("capture fork result checkpoint: %w", finishErr))
	}
	result.PostRef, result.PostCommit = finished.Post.Ref, finished.Post.Commit
	result.Status, result.ExitCode, result.Error = status, cloneInt(durableExitCode), durableError
	if _, err := cases.RecordAttemptResult(repo, cases.RecordAttemptResultRequest{CaseID: definition.ID, RunID: runID, AttemptID: attemptID, PostRef: finished.Post.Ref, PostCommit: finished.Post.Commit, Status: status, ExitCode: durableExitCode, Error: durableError}); err != nil {
		return result, err
	}
	runStatus := runs.StatusSucceeded
	if status == cases.AttemptStatusFailed {
		runStatus = runs.StatusFailed
	} else if status == cases.AttemptStatusIncomplete {
		runStatus = runs.StatusIncomplete
	}
	if err := runs.Finish(repo, runID, sessionID, runStatus, durableError); err != nil {
		return result, err
	}
	runOpen = false
	if request.Keep {
		cleanup = false
	}
	if commandErr != nil {
		return result, commandErr
	}
	return result, nil
}

func currentCase(repo *checkpoint.Repo, caseID primitives.CaseID) (cases.Case, error) {
	projection, err := cases.Rebuild(repo)
	if err != nil {
		return cases.Case{}, err
	}
	definition, ok := projection.Case(caseID)
	if !ok {
		return cases.Case{}, fmt.Errorf("case %s does not exist in this Turnal store", caseID)
	}
	return definition, nil
}

func appendForkSession(repo *checkpoint.Repo, definition cases.Case, runID primitives.RunID, sessionID primitives.SessionID, command []string) error {
	payload, err := json.Marshal(map[string]any{
		"provider_session_id": sessionID,
		"source":              "turnal fork",
		"case_id":             definition.ID,
		"source_turn":         fmt.Sprintf("%s:%s", definition.Source.SessionID, definition.Source.TurnID),
		"run_id":              runID,
		"command":             command,
	})
	if err != nil {
		return err
	}
	_, err = repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, SourceID: fmt.Sprintf("fork:%s:session", runID), Payload: payload})
	return err
}

func forkSessionID(runID primitives.RunID) (primitives.SessionID, error) {
	return primitives.ParseSessionID("fork-" + strings.TrimPrefix(runID.String(), "run_"))
}

func forkEnvironment(existing []string, root string, values []string) []string {
	replacements := make(map[string]string, len(values)+2)
	for _, entry := range values {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			replacements[environmentKey(name)] = name + "=" + value
		}
	}
	replacements[environmentKey(runs.EnvRunID)] = runs.EnvRunID + "=" + strings.TrimPrefix(replacements[environmentKey(EnvRunID)], EnvRunID+"=")
	replacements[environmentKey("PWD")] = "PWD=" + root
	result := make([]string, 0, len(existing)+len(replacements))
	for _, entry := range existing {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		if _, replaced := replacements[environmentKey(name)]; replaced {
			continue
		}
		result = append(result, entry)
	}
	for _, entry := range replacements {
		result = append(result, entry)
	}
	return result
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
