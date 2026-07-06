package cli

import (
	"fmt"
	"strings"

	"agent-vcs-again/internal/primitives"
)

func parseTurnTarget(value string) (primitives.SessionID, primitives.TurnID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, fmt.Errorf("target is required")
	}

	if strings.Contains(value, ":turn:") {
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return "", 0, err
		}
		if _, hasPhase := target.Phase(); hasPhase {
			return "", 0, fmt.Errorf("turn target must not include a checkpoint phase")
		}
		return target.SessionID(), target.TurnID(), nil
	}

	sessionText, turnText, ok := strings.Cut(value, ":")
	if !ok || strings.Contains(turnText, ":") {
		return "", 0, fmt.Errorf("target must be <session>:<turn>")
	}
	sessionID, err := primitives.ParseSessionID(sessionText)
	if err != nil {
		return "", 0, err
	}
	turnID, err := primitives.ParseTurnID(turnText)
	if err != nil {
		return "", 0, err
	}
	return sessionID, turnID, nil
}
