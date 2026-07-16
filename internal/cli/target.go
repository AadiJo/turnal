package cli

import (
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
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

func parseVerifyTarget(value string) (primitives.SessionID, primitives.TurnID, primitives.CheckpointPhase, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, "", fmt.Errorf("verify target is required")
	}
	if strings.Contains(value, ":turn:") {
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return "", 0, "", err
		}
		phase, ok := target.Phase()
		if !ok {
			return "", 0, "", fmt.Errorf("verify target must include :pre or :post")
		}
		return target.SessionID(), target.TurnID(), phase, nil
	}

	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return "", 0, "", fmt.Errorf("verify target must be <session>:<turn>:<pre|post>")
	}
	sessionID, err := primitives.ParseSessionID(parts[0])
	if err != nil {
		return "", 0, "", err
	}
	turnID, err := primitives.ParseTurnID(parts[1])
	if err != nil {
		return "", 0, "", err
	}
	phase, err := primitives.ParseCheckpointPhase(parts[2])
	if err != nil {
		return "", 0, "", err
	}
	return sessionID, turnID, phase, nil
}
