package viewer

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

const resourceKeyVersion = 1

type resourceKind string

const (
	resourceSession resourceKind = "session"
	resourceTurn    resourceKind = "turn"
)

type resourceIdentity struct {
	Version    int    `json:"v"`
	Kind       string `json:"k"`
	StoreID    string `json:"s"`
	WorktreeID string `json:"w,omitempty"`
	StreamID   string `json:"r"`
	SessionID  string `json:"i"`
	TurnID     uint64 `json:"t,omitempty"`
}

type keyCodec struct {
	storeID primitives.StoreID
}

func newKeyCodec(repo *checkpoint.Repo) keyCodec {
	return keyCodec{storeID: repo.StoreID}
}

func (codec keyCodec) encode(kind resourceKind, worktreeID primitives.WorktreeID, streamID primitives.EventStreamID, sessionID primitives.SessionID, turnID primitives.TurnID) (string, error) {
	identity := resourceIdentity{
		Version:    resourceKeyVersion,
		Kind:       string(kind),
		StoreID:    codec.storeID.String(),
		WorktreeID: worktreeID.String(),
		StreamID:   streamID.String(),
		SessionID:  sessionID.String(),
		TurnID:     turnID.Uint64(),
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("marshal viewer resource key: %w", err)
	}
	digest := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(digest[:10]), nil
}

func (codec keyCodec) decode(value string, expected resourceKind) (resourceIdentity, error) {
	encodedPayload, encodedDigest, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok || encodedPayload == "" || encodedDigest == "" {
		return resourceIdentity{}, fmt.Errorf("malformed resource key")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return resourceIdentity{}, fmt.Errorf("malformed resource key payload")
	}
	digest, err := base64.RawURLEncoding.DecodeString(encodedDigest)
	if err != nil || len(digest) != 10 {
		return resourceIdentity{}, fmt.Errorf("malformed resource key checksum")
	}
	want := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(digest, want[:10]) != 1 {
		return resourceIdentity{}, fmt.Errorf("resource key checksum mismatch")
	}
	var identity resourceIdentity
	if err := json.Unmarshal(payload, &identity); err != nil {
		return resourceIdentity{}, fmt.Errorf("malformed resource key identity")
	}
	if identity.Version != resourceKeyVersion {
		return resourceIdentity{}, fmt.Errorf("unsupported resource key version %d", identity.Version)
	}
	if identity.Kind != string(expected) {
		return resourceIdentity{}, fmt.Errorf("resource key kind %q cannot be used as %s", identity.Kind, expected)
	}
	if identity.StoreID != codec.storeID.String() {
		return resourceIdentity{}, fmt.Errorf("resource key belongs to a different Turnal store")
	}
	if _, err := primitives.ParseEventStreamID(identity.StreamID); err != nil {
		return resourceIdentity{}, fmt.Errorf("invalid resource stream identity")
	}
	if _, err := primitives.ParseSessionID(identity.SessionID); err != nil {
		return resourceIdentity{}, fmt.Errorf("invalid resource session identity")
	}
	if identity.WorktreeID != "" {
		if _, err := primitives.ParseWorktreeID(identity.WorktreeID); err != nil {
			return resourceIdentity{}, fmt.Errorf("invalid resource worktree identity")
		}
	}
	if expected == resourceTurn {
		if _, err := primitives.NewTurnID(identity.TurnID); err != nil {
			return resourceIdentity{}, fmt.Errorf("invalid resource turn identity")
		}
	}
	return identity, nil
}
