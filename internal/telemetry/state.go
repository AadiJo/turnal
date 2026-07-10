package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
)

const (
	StateVersion     = 1
	maxStateFileSize = 32 * 1024
	stateLockTimeout = 25 * time.Millisecond
)

type State struct {
	Version             int        `json:"version"`
	Preference          Preference `json:"preference"`
	AnonymousID         *UUID      `json:"anonymous_id,omitempty"`
	NoticeAt            string     `json:"notice_at,omitempty"`
	LastFlushAttemptAt  string     `json:"last_flush_attempt_at,omitempty"`
	LastFlushSuccessAt  string     `json:"last_flush_success_at,omitempty"`
	NetworkBackoffUntil string     `json:"network_backoff_until,omitempty"`
	CreatedAt           string     `json:"created_at"`
	UpdatedAt           string     `json:"updated_at"`
}

func (state State) Validate() error {
	if state.Version != StateVersion {
		return fmt.Errorf("unsupported telemetry state version %d", state.Version)
	}
	if !state.Preference.Valid() {
		return fmt.Errorf("invalid telemetry preference %q", state.Preference)
	}
	if state.Preference == PreferenceOn && state.AnonymousID == nil {
		return errors.New("enabled telemetry state has no installation ID")
	}
	if state.AnonymousID != nil && !state.AnonymousID.Valid() {
		return errors.New("invalid telemetry installation ID")
	}
	for name, value := range map[string]string{
		"notice_at":             state.NoticeAt,
		"last_flush_attempt_at": state.LastFlushAttemptAt,
		"last_flush_success_at": state.LastFlushSuccessAt,
		"network_backoff_until": state.NetworkBackoffUntil,
		"created_at":            state.CreatedAt,
		"updated_at":            state.UpdatedAt,
	} {
		if value == "" {
			if name == "created_at" || name == "updated_at" {
				return fmt.Errorf("telemetry state %s is required", name)
			}
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil || parsed.UTC().Format(time.RFC3339) != value {
			return fmt.Errorf("telemetry state %s is not canonical UTC RFC3339", name)
		}
	}
	return nil
}

type StateStore struct {
	Path        string
	Now         func() time.Time
	NewUUID     func() (UUID, error)
	LockTimeout time.Duration
}

func DefaultStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "turnal", "telemetry.json"), nil
}

func DefaultStateStore() (StateStore, error) {
	path, err := DefaultStatePath()
	if err != nil {
		return StateStore{}, err
	}
	return StateStore{Path: path}, nil
}

func (store StateStore) Load() (State, error) {
	var result State
	err := store.withLock(func() error {
		state, err := store.loadOrRecoverUnlocked()
		if err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func (store StateStore) Update(update func(*State) error) (State, error) {
	if update == nil {
		return State{}, errors.New("telemetry state update is required")
	}
	var result State
	err := store.withLock(func() error {
		state, err := store.loadOrRecoverUnlocked()
		if err != nil {
			return err
		}
		if err := update(&state); err != nil {
			return err
		}
		state.UpdatedAt = store.now().Format(time.RFC3339)
		if err := state.Validate(); err != nil {
			return err
		}
		if err := store.writeUnlocked(state); err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func (store StateStore) SetPreference(preference Preference) (State, error) {
	if preference != PreferenceOn && preference != PreferenceOff {
		return State{}, fmt.Errorf("preference must be %q or %q", PreferenceOn, PreferenceOff)
	}
	return store.Update(func(state *State) error {
		if preference == PreferenceOn && state.AnonymousID == nil {
			id, err := store.newUUID()()
			if err != nil {
				return err
			}
			state.AnonymousID = &id
		}
		state.Preference = preference
		return nil
	})
}

func (store StateStore) Enable(at time.Time) (State, error) {
	return store.Update(func(state *State) error {
		if state.AnonymousID == nil {
			id, err := store.newUUID()()
			if err != nil {
				return err
			}
			state.AnonymousID = &id
		}
		state.Preference = PreferenceOn
		if state.NoticeAt == "" {
			state.NoticeAt = at.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		return nil
	})
}

func (store StateStore) MarkNotice(at time.Time) (State, error) {
	return store.Update(func(state *State) error {
		state.NoticeAt = at.UTC().Truncate(time.Second).Format(time.RFC3339)
		return nil
	})
}

func (store StateStore) RotateAndDisable() (State, error) {
	return store.Update(func(state *State) error {
		id, err := store.newUUID()()
		if err != nil {
			return err
		}
		state.Preference = PreferenceOff
		state.AnonymousID = &id
		state.LastFlushAttemptAt = ""
		state.LastFlushSuccessAt = ""
		return nil
	})
}

func (store StateStore) MarkFlush(at time.Time, successful bool) (State, error) {
	return store.Update(func(state *State) error {
		stamp := at.UTC().Truncate(time.Second).Format(time.RFC3339)
		state.LastFlushAttemptAt = stamp
		if successful {
			state.LastFlushSuccessAt = stamp
			state.NetworkBackoffUntil = ""
		}
		return nil
	})
}

func (store StateStore) MarkNetworkBackoff(until time.Time) (State, error) {
	return store.Update(func(state *State) error {
		state.NetworkBackoffUntil = until.UTC().Truncate(time.Second).Format(time.RFC3339)
		return nil
	})
}

func (state State) NetworkBackoff(at time.Time) time.Duration {
	if state.NetworkBackoffUntil == "" {
		return 0
	}
	until, err := time.Parse(time.RFC3339, state.NetworkBackoffUntil)
	if err != nil || !at.UTC().Before(until) {
		return 0
	}
	return until.Sub(at.UTC())
}

func (state State) AutomaticFlushWait(at time.Time) time.Duration {
	wait := state.NetworkBackoff(at)
	if state.LastFlushAttemptAt == "" {
		return wait
	}
	attempt, err := time.Parse(time.RFC3339, state.LastFlushAttemptAt)
	if err != nil {
		return wait
	}
	until := attempt.Add(FlushInterval)
	if remaining := until.Sub(at.UTC()); remaining > wait {
		return remaining
	}
	return wait
}

func (store StateStore) withLock(action func() error) error {
	if strings.TrimSpace(store.Path) == "" {
		return errors.New("telemetry state path is required")
	}
	timeout := store.LockTimeout
	if timeout == 0 {
		timeout = stateLockTimeout
	}
	lock, err := filelock.Acquire(store.Path+".lock", timeout)
	if err != nil {
		return fmt.Errorf("lock telemetry state: %w", err)
	}
	defer lock.Release()
	return action()
}

func (store StateStore) loadOrRecoverUnlocked() (State, error) {
	state, err := store.readUnlocked()
	if err == nil {
		return state, nil
	}
	if !os.IsNotExist(err) && !errors.Is(err, errCorruptState) {
		return State{}, err
	}
	if errors.Is(err, errCorruptState) {
		if err := store.quarantineUnlocked(); err != nil {
			return State{}, err
		}
	}
	state = store.newState()
	if err := store.writeUnlocked(state); err != nil {
		return State{}, err
	}
	return state, nil
}

var errCorruptState = errors.New("corrupt telemetry state")

func (store StateStore) readUnlocked() (State, error) {
	info, err := os.Lstat(store.Path)
	if err != nil {
		return State{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return State{}, fmt.Errorf("telemetry state is not a regular file: %s", store.Path)
	}
	if info.Size() > maxStateFileSize {
		return State{}, fmt.Errorf("%w: file exceeds %d bytes", errCorruptState, maxStateFileSize)
	}
	if err := os.Chmod(store.Path, 0o600); err != nil {
		return State{}, fmt.Errorf("secure telemetry state: %w", err)
	}
	file, err := os.Open(store.Path)
	if err != nil {
		return State{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateFileSize+1))
	if err != nil {
		return State{}, fmt.Errorf("read telemetry state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("%w: %v", errCorruptState, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, fmt.Errorf("%w: trailing JSON", errCorruptState)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("%w: %v", errCorruptState, err)
	}
	return state, nil
}

func (store StateStore) quarantineUnlocked() error {
	stamp := store.now().Format("20060102T150405.000000000Z")
	quarantinePath := store.Path + ".corrupt-" + stamp
	if err := replaceFile(store.Path, quarantinePath); err != nil {
		return fmt.Errorf("quarantine telemetry state: %w", err)
	}
	return nil
}

func (store StateStore) writeUnlocked(state State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode telemetry state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxStateFileSize {
		return fmt.Errorf("telemetry state exceeds %d bytes", maxStateFileSize)
	}
	return atomicWriteFile(store.Path, data, 0o600)
}

func (store StateStore) newState() State {
	now := store.now().Format(time.RFC3339)
	return State{
		Version:    StateVersion,
		Preference: PreferenceUnset,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func (store StateStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func (store StateStore) newUUID() func() (UUID, error) {
	if store.NewUUID != nil {
		return store.NewUUID
	}
	return NewUUID
}
