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
	SessionID SessionID
	TurnID    TurnID
	Phase     CheckpointPhase
	HasPhase  bool
}

const checkpointRefPrefix = "refs/agent-vcs/checkpoints"

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

func ParseCheckpointRef(value string) (CheckpointRef, error) {
	parts, err := parseCheckpointRefParts(value)
	if err != nil {
		return "", err
	}

	expected := buildCheckpointRef(parts.SessionID, parts.TurnID, parts.Phase, parts.HasPhase)
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
	if len(segments) != 6 && len(segments) != 7 {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "must be refs/agent-vcs/checkpoints/<session>/turn/<turn>[/<phase>]")
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

	turnSegment := segments[5]
	if len(turnSegment) < 6 || !isAllDigits(turnSegment) {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "turn ref segment must be at least six digits")
	}
	turnNumber, err := strconv.ParseUint(turnSegment, 10, 64)
	if err != nil {
		return CheckpointRefParts{}, invalid("checkpoint ref", value, "turn ref segment overflows uint64")
	}
	turnID, err := NewTurnID(turnNumber)
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

func buildCheckpointRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase, hasPhase bool) string {
	ref := fmt.Sprintf("%s/%s/turn/%s", checkpointRefPrefix, sessionID, turnID.RefSegment())
	if hasPhase {
		ref += "/" + phase.String()
	}
	return ref
}
