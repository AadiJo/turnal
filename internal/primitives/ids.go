package primitives

import "strings"

const (
	maxSessionIDLength  = 128
	maxAdapterNameBytes = 64
)

// SessionID is the canonical, ref-safe identifier for one captured agent session.
type SessionID string

func ParseSessionID(value string) (SessionID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if err := validateRefSafeComponent("session id", value, maxSessionIDLength, false); err != nil {
		return "", err
	}
	return SessionID(value), nil
}

func (id SessionID) String() string {
	return string(id)
}

func (id SessionID) MarshalText() ([]byte, error) {
	parsed, err := ParseSessionID(id.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (id *SessionID) UnmarshalText(text []byte) error {
	parsed, err := ParseSessionID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// AdapterName identifies the agent adapter that produced normalized events.
// Adapter names are an open set so plugins can add adapters without changing
// the primitive schema.
type AdapterName string

const (
	AdapterClaudeCode AdapterName = "claude-code"
	AdapterCodex      AdapterName = "codex"
	AdapterCopilotCLI AdapterName = "copilot-cli"
	AdapterCursor     AdapterName = "cursor"
	AdapterManual     AdapterName = "manual"
	AdapterOpenCode   AdapterName = "opencode"
	AdapterPi         AdapterName = "pi"
)

func ParseAdapterName(value string) (AdapterName, error) {
	value = strings.TrimSpace(value)
	if err := validateRefSafeComponent("adapter name", value, maxAdapterNameBytes, false); err != nil {
		return "", err
	}
	return AdapterName(value), nil
}

func (name AdapterName) String() string {
	return string(name)
}

func (name AdapterName) MarshalText() ([]byte, error) {
	parsed, err := ParseAdapterName(name.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (name *AdapterName) UnmarshalText(text []byte) error {
	parsed, err := ParseAdapterName(string(text))
	if err != nil {
		return err
	}
	*name = parsed
	return nil
}
