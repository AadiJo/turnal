package primitives

import (
	"fmt"
	"strconv"
	"strings"
)

// CommitSHA is a full Git object id. Both SHA-1 and SHA-256 repositories are supported.
type CommitSHA string

func ParseCommitSHA(value string) (CommitSHA, error) {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return "", invalid("commit sha", value, "must be a full 40-character SHA-1 or 64-character SHA-256 hex id")
	}
	if !isHex(value) {
		return "", invalid("commit sha", value, "must be hex encoded")
	}
	return CommitSHA(strings.ToLower(value)), nil
}

func (sha CommitSHA) String() string {
	return string(sha)
}

func (sha CommitSHA) MarshalText() ([]byte, error) {
	parsed, err := ParseCommitSHA(sha.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (sha *CommitSHA) UnmarshalText(text []byte) error {
	parsed, err := ParseCommitSHA(string(text))
	if err != nil {
		return err
	}
	*sha = parsed
	return nil
}

// GitObjectID is a full Git object id for any object type.
type GitObjectID string

func ParseGitObjectID(value string) (GitObjectID, error) {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return "", invalid("git object id", value, "must be a full 40-character SHA-1 or 64-character SHA-256 hex id")
	}
	if !isHex(value) {
		return "", invalid("git object id", value, "must be hex encoded")
	}
	return GitObjectID(strings.ToLower(value)), nil
}

func (id GitObjectID) String() string {
	return string(id)
}

func (id GitObjectID) MarshalText() ([]byte, error) {
	parsed, err := ParseGitObjectID(id.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (id *GitObjectID) UnmarshalText(text []byte) error {
	parsed, err := ParseGitObjectID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type GitFileMode string

const (
	GitFileModeRegular    GitFileMode = "100644"
	GitFileModeExecutable GitFileMode = "100755"
	GitFileModeSymlink    GitFileMode = "120000"
)

func ParseGitFileMode(value string) (GitFileMode, error) {
	value = strings.TrimSpace(value)
	switch GitFileMode(value) {
	case GitFileModeRegular, GitFileModeExecutable, GitFileModeSymlink:
		return GitFileMode(value), nil
	default:
		return "", invalid("git file mode", value, "must be 100644, 100755, or 120000")
	}
}

func (mode GitFileMode) String() string {
	return string(mode)
}

func (mode GitFileMode) MarshalText() ([]byte, error) {
	parsed, err := ParseGitFileMode(mode.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (mode *GitFileMode) UnmarshalText(text []byte) error {
	parsed, err := ParseGitFileMode(string(text))
	if err != nil {
		return err
	}
	*mode = parsed
	return nil
}

type RollbackMode string

const (
	RollbackModeCheckpoint   RollbackMode = "checkpoint"
	RollbackModeWorkspaceGit RollbackMode = "workspace-git"
)

func ParseRollbackMode(value string) (RollbackMode, error) {
	value = strings.TrimSpace(value)
	switch RollbackMode(value) {
	case "", RollbackModeCheckpoint:
		return RollbackModeCheckpoint, nil
	case RollbackModeWorkspaceGit:
		return RollbackModeWorkspaceGit, nil
	default:
		return "", invalid("rollback mode", value, "must be checkpoint or workspace-git")
	}
}

func (mode RollbackMode) String() string {
	return string(mode)
}

func (mode RollbackMode) MarshalText() ([]byte, error) {
	parsed, err := ParseRollbackMode(mode.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (mode *RollbackMode) UnmarshalText(text []byte) error {
	parsed, err := ParseRollbackMode(string(text))
	if err != nil {
		return err
	}
	*mode = parsed
	return nil
}

// CheckpointPhase identifies whether a checkpoint is before or after a turn.
type CheckpointPhase string

const (
	CheckpointPhasePre  CheckpointPhase = "pre"
	CheckpointPhasePost CheckpointPhase = "post"
)

func ParseCheckpointPhase(value string) (CheckpointPhase, error) {
	value = strings.TrimSpace(value)
	switch CheckpointPhase(value) {
	case CheckpointPhasePre, CheckpointPhasePost:
		return CheckpointPhase(value), nil
	default:
		return "", invalid("checkpoint phase", value, "must be pre or post")
	}
}

func (phase CheckpointPhase) String() string {
	return string(phase)
}

// CheckpointRef is a private Git ref pointing at a synthetic checkpoint commit.
type CheckpointRef string

type CheckpointRefParts struct {
	SessionID    SessionID
	TurnID       TurnID
	Phase        CheckpointPhase
	HasPhase     bool
	WorktreeID   WorktreeID
	StreamID     EventStreamID
	CheckpointID CheckpointID
	Scoped       bool
	Canonical    bool
}

const checkpointRefPrefix = "refs/agent-vcs/checkpoints"

func CheckpointRefsPrefix() string {
	return checkpointRefPrefix
}

func CheckpointSessionRefPrefix(sessionID SessionID) (string, error) {
	parsedSessionID, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	refPrefix := fmt.Sprintf("%s/%s/turn", checkpointRefPrefix, parsedSessionID)
	if err := validateGitRefName(refPrefix); err != nil {
		return "", err
	}
	return refPrefix, nil
}

func NewCheckpointRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase) (CheckpointRef, error) {
	parsedSessionID, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	parsedTurnID, err := NewTurnID(turnID.Uint64())
	if err != nil {
		return "", err
	}
	parsedPhase := phase
	if phase != "" {
		parsedPhase, err = ParseCheckpointPhase(phase.String())
		if err != nil {
			return "", err
		}
	}

	ref := buildCheckpointRef(parsedSessionID, parsedTurnID, parsedPhase, parsedPhase != "")
	if err := validateGitRefName(ref); err != nil {
		return "", err
	}
	return CheckpointRef(ref), nil
}

func NewScopedCheckpointRef(worktreeID WorktreeID, streamID EventStreamID, sessionID SessionID, turnID TurnID, phase CheckpointPhase) (CheckpointRef, error) {
	parsedWorktreeID, err := ParseWorktreeID(worktreeID.String())
	if err != nil {
		return "", err
	}
	parsedStreamID, err := ParseEventStreamID(streamID.String())
	if err != nil {
		return "", err
	}
	parsedSessionID, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	parsedTurnID, err := NewTurnID(turnID.Uint64())
	if err != nil {
		return "", err
	}
	parsedPhase := phase
	if phase != "" {
		parsedPhase, err = ParseCheckpointPhase(phase.String())
		if err != nil {
			return "", err
		}
	}
	ref := buildScopedCheckpointRef(parsedWorktreeID, parsedStreamID, parsedSessionID, parsedTurnID, parsedPhase, parsedPhase != "")
	if err := validateGitRefName(ref); err != nil {
		return "", err
	}
	return CheckpointRef(ref), nil
}

func NewCheckpointIDRef(checkpointID CheckpointID) (CheckpointRef, error) {
	parsed, err := ParseCheckpointID(checkpointID.String())
	if err != nil {
		return "", err
	}
	ref := checkpointRefPrefix + "/by-id/" + parsed.String()
	if err := validateGitRefName(ref); err != nil {
		return "", err
	}
	return CheckpointRef(ref), nil
}

func ParseCheckpointRef(value string) (CheckpointRef, error) {
	parts, err := parseCheckpointRefParts(value)
	if err != nil {
		return "", err
	}

	expected := buildCheckpointRef(parts.SessionID, parts.TurnID, parts.Phase, parts.HasPhase)
	if parts.Scoped {
		expected = buildScopedCheckpointRef(parts.WorktreeID, parts.StreamID, parts.SessionID, parts.TurnID, parts.Phase, parts.HasPhase)
	}
	if parts.Canonical {
		expected = checkpointRefPrefix + "/by-id/" + parts.CheckpointID.String()
	}
	if value = strings.TrimSpace(value); value != expected {
		return "", invalid("checkpoint ref", value, fmt.Sprintf("must be canonical %q", expected))
	}
	return CheckpointRef(expected), nil
}

func (ref CheckpointRef) String() string {
	return string(ref)
}

func (ref CheckpointRef) Parts() (CheckpointRefParts, error) {
	parsed, err := ParseCheckpointRef(ref.String())
	if err != nil {
		return CheckpointRefParts{}, err
	}
	return parseCheckpointRefParts(parsed.String())
}

func (ref CheckpointRef) MarshalText() ([]byte, error) {
	parsed, err := ParseCheckpointRef(ref.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (ref *CheckpointRef) UnmarshalText(text []byte) error {
	parsed, err := ParseCheckpointRef(string(text))
	if err != nil {
		return err
	}
	*ref = parsed
	return nil
}

func parseCheckpointRefParts(value string) (CheckpointRefParts, error) {
	value = strings.TrimSpace(value)
	if err := validateGitRefName(value); err != nil {
		return CheckpointRefParts{}, err
	}

	segments := strings.Split(value, "/")
	if len(segments) == 5 && strings.Join(segments[:4], "/") == checkpointRefPrefix+"/by-id" {
		checkpointID, err := ParseCheckpointID(segments[4])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		return CheckpointRefParts{CheckpointID: checkpointID, Canonical: true}, nil
	}
	if (len(segments) == 9 || len(segments) == 10) && strings.Join(segments[:4], "/") == checkpointRefPrefix+"/by-worktree" {
		worktreeID, err := ParseWorktreeID(segments[4])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		streamID, err := ParseEventStreamID(segments[5])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		sessionID, err := ParseSessionID(segments[6])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		if segments[7] != "turn" {
			return CheckpointRefParts{}, invalid("checkpoint ref", value, "scoped checkpoint ref must contain /turn/")
		}
		turnID, err := parseCheckpointTurnSegment(value, segments[8])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		parts := CheckpointRefParts{SessionID: sessionID, TurnID: turnID, WorktreeID: worktreeID, StreamID: streamID, Scoped: true}
		if len(segments) == 10 {
			phase, err := ParseCheckpointPhase(segments[9])
			if err != nil {
				return CheckpointRefParts{}, err
			}
			parts.Phase = phase
			parts.HasPhase = true
		}
		return parts, nil
	}
	if len(segments) != 6 && len(segments) != 7 {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "must be a legacy, scoped, or by-id checkpoint ref")
	}
	if strings.Join(segments[:3], "/") != checkpointRefPrefix {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "must be under refs/agent-vcs/checkpoints")
	}
	if segments[4] != "turn" {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "must contain /turn/ after the session id")
	}

	sessionID, err := ParseSessionID(segments[3])
	if err != nil {
		return CheckpointRefParts{}, err
	}

	turnID, err := parseCheckpointTurnSegment(value, segments[5])
	if err != nil {
		return CheckpointRefParts{}, err
	}

	refParts := CheckpointRefParts{
		SessionID: sessionID,
		TurnID:    turnID,
	}
	if len(segments) == 7 {
		phase, err := ParseCheckpointPhase(segments[6])
		if err != nil {
			return CheckpointRefParts{}, err
		}
		refParts.Phase = phase
		refParts.HasPhase = true
	}

	return refParts, nil
}

func parseCheckpointTurnSegment(value string, turnSegment string) (TurnID, error) {
	if len(turnSegment) < 6 || !isAllDigits(turnSegment) {
		return 0, invalid("checkpoint ref", value, "turn ref segment must be at least six digits")
	}
	turnNumber, err := strconv.ParseUint(turnSegment, 10, 64)
	if err != nil {
		return 0, invalid("checkpoint ref", value, "turn ref segment overflows uint64")
	}
	turnID, err := NewTurnID(turnNumber)
	if err != nil {
		return 0, err
	}
	return turnID, nil
}

func buildCheckpointRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase, hasPhase bool) string {
	ref := fmt.Sprintf("%s/%s/turn/%s", checkpointRefPrefix, sessionID, turnID.RefSegment())
	if hasPhase {
		ref += "/" + phase.String()
	}
	return ref
}

func buildScopedCheckpointRef(worktreeID WorktreeID, streamID EventStreamID, sessionID SessionID, turnID TurnID, phase CheckpointPhase, hasPhase bool) string {
	ref := fmt.Sprintf("%s/by-worktree/%s/%s/%s/turn/%s", checkpointRefPrefix, worktreeID, streamID, sessionID, turnID.RefSegment())
	if hasPhase {
		ref += "/" + phase.String()
	}
	return ref
}

// GitSyncRef is a private Git ref pointing at captured workspace Git state.
type GitSyncRef string

type GitSyncRefParts struct {
	SessionID SessionID
	TurnID    TurnID
	Phase     CheckpointPhase
}

const gitSyncRefPrefix = "refs/agent-vcs/git-sync"

func GitSyncRefsPrefix() string {
	return gitSyncRefPrefix
}

func NewGitSyncRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase) (GitSyncRef, error) {
	parsedSessionID, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	parsedTurnID, err := NewTurnID(turnID.Uint64())
	if err != nil {
		return "", err
	}
	parsedPhase, err := ParseCheckpointPhase(phase.String())
	if err != nil {
		return "", err
	}

	ref := buildGitSyncRef(parsedSessionID, parsedTurnID, parsedPhase)
	if err := validateGitRefName(ref); err != nil {
		return "", err
	}
	return GitSyncRef(ref), nil
}

func ParseGitSyncRef(value string) (GitSyncRef, error) {
	parts, err := parseGitSyncRefParts(value)
	if err != nil {
		return "", err
	}
	expected := buildGitSyncRef(parts.SessionID, parts.TurnID, parts.Phase)
	if value = strings.TrimSpace(value); value != expected {
		return "", invalid("git-sync ref", value, fmt.Sprintf("must be canonical %q", expected))
	}
	return GitSyncRef(expected), nil
}

func (ref GitSyncRef) String() string {
	return string(ref)
}

func (ref GitSyncRef) Parts() (GitSyncRefParts, error) {
	parsed, err := ParseGitSyncRef(ref.String())
	if err != nil {
		return GitSyncRefParts{}, err
	}
	return parseGitSyncRefParts(parsed.String())
}

func (ref GitSyncRef) MarshalText() ([]byte, error) {
	parsed, err := ParseGitSyncRef(ref.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (ref *GitSyncRef) UnmarshalText(text []byte) error {
	parsed, err := ParseGitSyncRef(string(text))
	if err != nil {
		return err
	}
	*ref = parsed
	return nil
}

func parseGitSyncRefParts(value string) (GitSyncRefParts, error) {
	value = strings.TrimSpace(value)
	if err := validateGitRefName(value); err != nil {
		return GitSyncRefParts{}, err
	}
	segments := strings.Split(value, "/")
	if len(segments) != 7 {
		return GitSyncRefParts{}, invalid("git-sync ref", value, "must be refs/agent-vcs/git-sync/<session>/turn/<turn>/<phase>")
	}
	if strings.Join(segments[:3], "/") != gitSyncRefPrefix {
		return GitSyncRefParts{}, invalid("git-sync ref", value, "must be under refs/agent-vcs/git-sync")
	}
	if segments[4] != "turn" {
		return GitSyncRefParts{}, invalid("git-sync ref", value, "must contain /turn/ after the session id")
	}

	sessionID, err := ParseSessionID(segments[3])
	if err != nil {
		return GitSyncRefParts{}, err
	}
	turnSegment := segments[5]
	if len(turnSegment) < 6 || !isAllDigits(turnSegment) {
		return GitSyncRefParts{}, invalid("git-sync ref", value, "turn ref segment must be at least six digits")
	}
	turnNumber, err := strconv.ParseUint(turnSegment, 10, 64)
	if err != nil {
		return GitSyncRefParts{}, invalid("git-sync ref", value, "turn ref segment overflows uint64")
	}
	turnID, err := NewTurnID(turnNumber)
	if err != nil {
		return GitSyncRefParts{}, err
	}
	phase, err := ParseCheckpointPhase(segments[6])
	if err != nil {
		return GitSyncRefParts{}, err
	}
	return GitSyncRefParts{SessionID: sessionID, TurnID: turnID, Phase: phase}, nil
}

func buildGitSyncRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase) string {
	return fmt.Sprintf("%s/%s/turn/%s/%s", gitSyncRefPrefix, sessionID, turnID.RefSegment(), phase)
}
