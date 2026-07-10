package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateStartsUnidentifiedUntilExplicitOptIn(t *testing.T) {
	store := testStateStore(t)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Preference != PreferenceUnset || state.AnonymousID != nil {
		t.Fatalf("new state = %#v", state)
	}
	assertMode(t, filepath.Dir(store.Path), 0o700)
	assertMode(t, store.Path, 0o600)

	state, err = store.SetPreference(PreferenceOn)
	if err != nil {
		t.Fatal(err)
	}
	if state.Preference != PreferenceOn || state.AnonymousID == nil || !state.AnonymousID.Valid() {
		t.Fatalf("enabled state = %#v", state)
	}
	firstID := state.AnonymousID.String()

	state, err = store.SetPreference(PreferenceOff)
	if err != nil {
		t.Fatal(err)
	}
	if state.Preference != PreferenceOff || state.AnonymousID == nil || state.AnonymousID.String() != firstID {
		t.Fatalf("disabled state = %#v", state)
	}

	state, err = store.RotateAndDisable()
	if err != nil {
		t.Fatal(err)
	}
	if state.AnonymousID == nil || state.AnonymousID.String() == firstID {
		t.Fatalf("reset did not rotate ID: %#v", state)
	}
}

func TestStateNoticeDoesNotCreateInstallationID(t *testing.T) {
	store := testStateStore(t)
	state, err := store.MarkNotice(time.Date(2026, 7, 10, 12, 34, 56, 999, time.FixedZone("test", -5*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	if state.NoticeAt != "2026-07-10T17:34:56Z" || state.AnonymousID != nil || state.Preference != PreferenceUnset {
		t.Fatalf("notice state = %#v", state)
	}
}

func TestStateRecoversCorruptAndFutureFiles(t *testing.T) {
	for name, data := range map[string]string{
		"malformed": `{not-json}`,
		"future":    `{"version":2,"preference":"off","created_at":"2026-07-10T12:00:00Z","updated_at":"2026-07-10T12:00:00Z"}`,
		"unknown":   `{"version":1,"preference":"off","workspace":"secret","created_at":"2026-07-10T12:00:00Z","updated_at":"2026-07-10T12:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := testStateStore(t)
			if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Preference != PreferenceUnset || state.AnonymousID != nil {
				t.Fatalf("recovered state = %#v", state)
			}
			matches, err := filepath.Glob(store.Path + ".corrupt-*")
			if err != nil || len(matches) != 1 {
				t.Fatalf("quarantine files = %v, %v", matches, err)
			}
			quarantined, err := os.ReadFile(matches[0])
			if err != nil || string(quarantined) != data {
				t.Fatalf("quarantined data = %q, %v", quarantined, err)
			}
		})
	}
}

func TestStateRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows hosts")
	}
	store := testStateStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "sensitive")
	if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("symlink state was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "do not replace" {
		t.Fatalf("symlink target = %q, %v", data, err)
	}
}

func TestStateConcurrentUpdatesRemainValid(t *testing.T) {
	store := testStateStore(t)
	store.LockTimeout = 2 * time.Second
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(second int) {
			defer wait.Done()
			_, err := store.MarkNotice(time.Date(2026, 7, 10, 12, 0, second, 0, time.UTC))
			errors <- err
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStateSerializationContainsNoEndpointOrWorkspaceFields(t *testing.T) {
	store := testStateStore(t)
	if _, err := store.SetPreference(PreferenceOn); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"workspace", "endpoint", "hostname", "username", "repository"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("state contains forbidden field %q: %s", forbidden, data)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 5 {
		t.Fatalf("enabled state has unexpected fields: %#v", decoded)
	}
}

func TestAutomaticFlushWaitUsesLongestActiveDeadline(t *testing.T) {
	state := State{
		LastFlushAttemptAt:  "2026-07-10T12:00:00Z",
		NetworkBackoffUntil: "2026-07-11T12:00:00Z",
	}
	now := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	if got := state.AutomaticFlushWait(now); got != 23*time.Hour+30*time.Minute {
		t.Fatalf("AutomaticFlushWait() = %s", got)
	}
	state.NetworkBackoffUntil = ""
	if got := state.AutomaticFlushWait(now); got != 5*time.Hour+30*time.Minute {
		t.Fatalf("attempt wait = %s", got)
	}
	if got := state.AutomaticFlushWait(now.Add(6 * time.Hour)); got != 0 {
		t.Fatalf("expired wait = %s", got)
	}
}

func testStateStore(t *testing.T) StateStore {
	t.Helper()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	return StateStore{
		Path: filepath.Join(t.TempDir(), "config", "turnal", "telemetry.json"),
		Now:  func() time.Time { return now },
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}
