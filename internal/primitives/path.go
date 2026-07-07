package primitives

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// WorkspaceRoot is an absolute, cleaned filesystem path for the project root.
type WorkspaceRoot string

func ParseWorkspaceRoot(value string) (WorkspaceRoot, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", invalid("workspace root", value, "must not be empty")
	}
	if strings.ContainsRune(value, 0) {
		return "", invalid("workspace root", value, "must not contain NUL")
	}

	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) {
		return "", invalid("workspace root", value, "must be absolute")
	}

	return WorkspaceRoot(cleaned), nil
}

func (root WorkspaceRoot) String() string {
	return string(root)
}

func (root WorkspaceRoot) Join(repoPath RepoPath) string {
	return filepath.Join(root.String(), filepath.FromSlash(repoPath.String()))
}

// RepoPath is a normalized slash-separated path relative to a workspace root.
type RepoPath string

func ParseRepoPath(value string) (RepoPath, error) {
	if value == "" {
		return "", invalid("repo path", value, "must not be empty")
	}
	if strings.ContainsRune(value, 0) {
		return "", invalid("repo path", value, "must not contain NUL")
	}
	if filepath.IsAbs(value) || hasWindowsVolumePrefix(value) {
		return "", invalid("repo path", value, "must be relative")
	}
	if strings.Contains(value, "\\") {
		return "", invalid("repo path", value, "must use slash separators")
	}

	normalized := value
	if path.IsAbs(normalized) {
		return "", invalid("repo path", value, "must be relative")
	}

	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "", invalid("repo path", value, "must name a file or directory")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", invalid("repo path", value, "must not escape the workspace root")
	}

	for _, segment := range strings.Split(cleaned, "/") {
		lowerSegment := strings.ToLower(segment)
		switch segment {
		case "", ".", "..":
			return "", invalid("repo path", value, "must not contain empty, . or .. segments")
		}
		switch lowerSegment {
		case ".git", ".turnal":
			return "", invalid("repo path", value, "must not point inside tool metadata directories")
		}
	}

	return RepoPath(cleaned), nil
}

func (repoPath RepoPath) String() string {
	return string(repoPath)
}

func (repoPath RepoPath) OSPath() string {
	return filepath.FromSlash(repoPath.String())
}

func hasWindowsVolumePrefix(value string) bool {
	if filepath.VolumeName(value) != "" {
		return true
	}
	if len(value) >= 2 && isASCIIAlpha(rune(value[0])) && value[1] == ':' {
		return true
	}
	return false
}

// TargetRef is a user-facing rollback/replay target such as demo:turn:1:pre.
type TargetRef struct {
	sessionID SessionID
	turnID    TurnID
	phase     CheckpointPhase
	hasPhase  bool
}

func NewTargetRef(sessionID SessionID, turnID TurnID, phase CheckpointPhase) (TargetRef, error) {
	parsedSessionID, err := ParseSessionID(sessionID.String())
	if err != nil {
		return TargetRef{}, err
	}
	parsedTurnID, err := NewTurnID(turnID.Uint64())
	if err != nil {
		return TargetRef{}, err
	}
	target := TargetRef{
		sessionID: parsedSessionID,
		turnID:    parsedTurnID,
	}
	if phase != "" {
		parsed, err := ParseCheckpointPhase(phase.String())
		if err != nil {
			return TargetRef{}, err
		}
		target.phase = parsed
		target.hasPhase = true
	}
	return target, nil
}

func ParseTargetRef(value string) (TargetRef, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return TargetRef{}, invalid("target ref", value, "must be <session>:turn:<turn>[:pre|post]")
	}
	if parts[1] != "turn" {
		return TargetRef{}, invalid("target ref", value, "only turn targets are supported")
	}

	sessionID, err := ParseSessionID(parts[0])
	if err != nil {
		return TargetRef{}, err
	}
	turnID, err := ParseTurnID(parts[2])
	if err != nil {
		return TargetRef{}, err
	}

	if len(parts) == 3 {
		return NewTargetRef(sessionID, turnID, "")
	}
	phase, err := ParseCheckpointPhase(parts[3])
	if err != nil {
		return TargetRef{}, err
	}
	return NewTargetRef(sessionID, turnID, phase)
}

func (target TargetRef) SessionID() SessionID {
	return target.sessionID
}

func (target TargetRef) TurnID() TurnID {
	return target.turnID
}

func (target TargetRef) Phase() (CheckpointPhase, bool) {
	return target.phase, target.hasPhase
}

func (target TargetRef) CheckpointRef() (CheckpointRef, error) {
	if target.hasPhase {
		return NewCheckpointRef(target.sessionID, target.turnID, target.phase)
	}
	return NewCheckpointRef(target.sessionID, target.turnID, "")
}

func (target TargetRef) String() string {
	base := fmt.Sprintf("%s:turn:%s", target.sessionID, target.turnID)
	if target.hasPhase {
		return base + ":" + target.phase.String()
	}
	return base
}
