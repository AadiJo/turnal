package replay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

const (
	sessionVersion = 1
	markerFileName = ".agent-vcs-replay.json"
)

type Manager struct {
	Repo *checkpoint.Repo
}

type Checkpoint struct {
	Target    string `json:"target"`
	Ref       string `json:"ref"`
	Commit    string `json:"commit"`
	SessionID string `json:"session_id"`
	Turn      uint64 `json:"turn"`
	Phase     string `json:"phase"`
}

type Session struct {
	Version       int          `json:"version"`
	ID            string       `json:"id"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
	WorkspaceRoot string       `json:"workspace_root"`
	Path          string       `json:"path"`
	Sequence      []Checkpoint `json:"sequence"`
	Current       int          `json:"current"`
	Kept          bool         `json:"kept,omitempty"`
}

type Result struct {
	Session Session
	Current Checkpoint
}

type DiffMode int

const (
	DiffPrevious DiffMode = iota
	DiffNext
	DiffWorkspace
)

type marker struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	WorkspaceRoot string `json:"workspace_root"`
	Current       string `json:"current"`
	Kept          bool   `json:"kept,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

func New(repo *checkpoint.Repo) Manager {
	return Manager{Repo: repo}
}

func MarkerFileName() string {
	return markerFileName
}

func (manager Manager) Checkout(selectionText string, pathText string) (Result, error) {
	if err := manager.requireRepo(); err != nil {
		return Result{}, err
	}
	selection, err := manager.resolveSelection(selectionText)
	if err != nil {
		return Result{}, err
	}
	current := selection.Current()
	id := newSessionID(current)
	path, err := manager.resolveCheckoutPath(id, pathText)
	if err != nil {
		return Result{}, err
	}
	cleanup, err := manager.prepareCheckoutPath(path)
	if err != nil {
		return Result{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	session := Session{
		Version:       sessionVersion,
		ID:            id,
		CreatedAt:     now,
		UpdatedAt:     now,
		WorkspaceRoot: manager.Repo.WorkspaceRoot.String(),
		Path:          path,
		Sequence:      selection.Entries,
		Current:       selection.CurrentIndex,
	}
	if err := manager.materialize(session); err != nil {
		cleanup()
		return Result{}, err
	}
	if err := manager.writeSession(session); err != nil {
		cleanup()
		return Result{}, err
	}
	if err := manager.setActive(session.ID); err != nil {
		cleanup()
		_ = os.Remove(manager.sessionPath(session.ID))
		return Result{}, err
	}
	return Result{Session: session, Current: session.current()}, nil
}

func (manager Manager) Move(delta int) (Result, error) {
	session, err := manager.Active()
	if err != nil {
		return Result{}, err
	}
	nextIndex := session.Current + delta
	if nextIndex < 0 {
		return Result{}, fmt.Errorf("already at first replay checkpoint")
	}
	if nextIndex >= len(session.Sequence) {
		return Result{}, fmt.Errorf("already at last replay checkpoint")
	}
	session.Current = nextIndex
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.ensureSessionPathOwned(session); err != nil {
		return Result{}, err
	}
	if err := manager.materialize(session); err != nil {
		return Result{}, err
	}
	if err := manager.writeSession(session); err != nil {
		return Result{}, err
	}
	return Result{Session: session, Current: session.current()}, nil
}

func (manager Manager) Goto(selectionText string) (Result, error) {
	session, err := manager.Active()
	if err != nil {
		return Result{}, err
	}
	selection, err := manager.resolveSelection(selectionText)
	if err != nil {
		return Result{}, err
	}
	session.Sequence = selection.Entries
	session.Current = selection.CurrentIndex
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.ensureSessionPathOwned(session); err != nil {
		return Result{}, err
	}
	if err := manager.materialize(session); err != nil {
		return Result{}, err
	}
	if err := manager.writeSession(session); err != nil {
		return Result{}, err
	}
	return Result{Session: session, Current: session.current()}, nil
}

func (manager Manager) Diff(mode DiffMode) ([]byte, error) {
	session, err := manager.Active()
	if err != nil {
		return nil, err
	}
	current := session.current()
	switch mode {
	case DiffWorkspace:
		commit, err := primitives.ParseCommitSHA(current.Commit)
		if err != nil {
			return nil, err
		}
		return manager.Repo.DiffCommitToWorkspace(commit)
	case DiffNext:
		if session.Current+1 >= len(session.Sequence) {
			return nil, fmt.Errorf("no next replay checkpoint")
		}
		return manager.diffCheckpoints(current, session.Sequence[session.Current+1])
	default:
		if session.Current == 0 {
			return nil, fmt.Errorf("no previous replay checkpoint")
		}
		return manager.diffCheckpoints(session.Sequence[session.Current-1], current)
	}
}

func (manager Manager) Keep(pathText string) (Result, string, error) {
	session, err := manager.Active()
	if err != nil {
		return Result{}, "", err
	}
	current := session.current()
	if strings.TrimSpace(pathText) != "" {
		path, err := manager.resolveUserPath(pathText)
		if err != nil {
			return Result{}, "", err
		}
		if err := manager.ensureKeepPath(path); err != nil {
			return Result{}, "", err
		}
		commit, err := primitives.ParseCommitSHA(current.Commit)
		if err != nil {
			return Result{}, "", err
		}
		if err := manager.Repo.MaterializeCommit(commit, path, checkpoint.MaterializeOptions{}); err != nil {
			return Result{}, "", err
		}
		return Result{Session: session, Current: current}, path, nil
	}

	session.Kept = true
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := manager.writeMarker(session); err != nil {
		return Result{}, "", err
	}
	if err := manager.writeSession(session); err != nil {
		return Result{}, "", err
	}
	return Result{Session: session, Current: current}, session.Path, nil
}

func (manager Manager) Stop() (Session, bool, error) {
	session, err := manager.Active()
	if err != nil {
		return Session{}, false, err
	}
	removed, err := manager.removeSession(session, !session.Kept)
	return session, removed, err
}

func (manager Manager) Remove(selector string) (Session, bool, error) {
	session, err := manager.findSession(selector)
	if err != nil {
		return Session{}, false, err
	}
	removed, err := manager.removeSession(session, true)
	return session, removed, err
}

func (manager Manager) Active() (Session, error) {
	if err := manager.requireRepo(); err != nil {
		return Session{}, err
	}
	idBytes, err := os.ReadFile(manager.activePath())
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, fmt.Errorf("no active replay session")
		}
		return Session{}, fmt.Errorf("read active replay session: %w", err)
	}
	id := strings.TrimSpace(string(idBytes))
	if id == "" {
		return Session{}, fmt.Errorf("active replay session is empty")
	}
	return manager.readSession(id)
}

func (manager Manager) List() ([]Session, string, error) {
	if err := manager.requireRepo(); err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(manager.sessionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read replay sessions: %w", err)
	}
	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		session, err := manager.readSession(id)
		if err != nil {
			return nil, "", err
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt != sessions[j].UpdatedAt {
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		}
		return sessions[i].ID < sessions[j].ID
	})
	activeID := ""
	if data, err := os.ReadFile(manager.activePath()); err == nil {
		activeID = strings.TrimSpace(string(data))
	}
	return sessions, activeID, nil
}

func (manager Manager) diffCheckpoints(left Checkpoint, right Checkpoint) ([]byte, error) {
	leftRef, err := primitives.ParseCheckpointRef(left.Ref)
	if err != nil {
		return nil, err
	}
	rightRef, err := primitives.ParseCheckpointRef(right.Ref)
	if err != nil {
		return nil, err
	}
	return manager.Repo.DiffRefs(leftRef, rightRef)
}

func (manager Manager) materialize(session Session) error {
	current := session.current()
	commit, err := primitives.ParseCommitSHA(current.Commit)
	if err != nil {
		return err
	}
	if err := manager.Repo.MaterializeCommit(commit, session.Path, checkpoint.MaterializeOptions{
		PreservePaths: []string{markerFileName},
	}); err != nil {
		return err
	}
	return manager.writeMarker(session)
}

func (manager Manager) removeSession(session Session, removeWorktree bool) (bool, error) {
	if err := manager.requireRepo(); err != nil {
		return false, err
	}
	removedWorktree := false
	if removeWorktree {
		if err := manager.ensureSessionPathOwned(session); err != nil {
			return false, err
		}
		if err := os.RemoveAll(session.Path); err != nil {
			return false, fmt.Errorf("remove replay worktree %s: %w", session.Path, err)
		}
		removedWorktree = true
	} else {
		_ = os.Remove(filepath.Join(session.Path, markerFileName))
	}
	if err := os.Remove(manager.sessionPath(session.ID)); err != nil && !os.IsNotExist(err) {
		return removedWorktree, fmt.Errorf("remove replay session metadata: %w", err)
	}
	activeID := ""
	if data, err := os.ReadFile(manager.activePath()); err == nil {
		activeID = strings.TrimSpace(string(data))
	}
	if activeID == session.ID {
		if err := os.Remove(manager.activePath()); err != nil && !os.IsNotExist(err) {
			return removedWorktree, fmt.Errorf("clear active replay session: %w", err)
		}
	}
	return removedWorktree, nil
}

func (manager Manager) writeSession(session Session) error {
	if err := os.MkdirAll(manager.sessionsDir(), 0o755); err != nil {
		return fmt.Errorf("create replay sessions dir: %w", err)
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal replay session: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(manager.sessionPath(session.ID), data, 0o644); err != nil {
		return fmt.Errorf("write replay session: %w", err)
	}
	return nil
}

func (manager Manager) readSession(id string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsRune(id, os.PathSeparator) {
		return Session{}, fmt.Errorf("invalid replay session id %q", id)
	}
	data, err := os.ReadFile(manager.sessionPath(id))
	if err != nil {
		return Session{}, fmt.Errorf("read replay session %s: %w", id, err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, fmt.Errorf("parse replay session %s: %w", id, err)
	}
	if err := validateSession(session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (manager Manager) findSession(selector string) (Session, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return manager.Active()
	}
	if session, err := manager.readSession(selector); err == nil {
		return session, nil
	}
	path, err := filepath.Abs(selector)
	if err != nil {
		return Session{}, fmt.Errorf("resolve replay selector: %w", err)
	}
	sessions, _, err := manager.List()
	if err != nil {
		return Session{}, err
	}
	var matches []Session
	for _, session := range sessions {
		if session.Path == path || filepath.Base(session.Path) == selector {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Session{}, fmt.Errorf("replay session selector %q is ambiguous", selector)
	}
	return Session{}, fmt.Errorf("replay session %q not found", selector)
}

func (manager Manager) setActive(id string) error {
	if err := os.MkdirAll(manager.replayDir(), 0o755); err != nil {
		return fmt.Errorf("create replay dir: %w", err)
	}
	if err := os.WriteFile(manager.activePath(), []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("write active replay session: %w", err)
	}
	return nil
}

func (manager Manager) writeMarker(session Session) error {
	current := session.current()
	data, err := json.MarshalIndent(marker{
		Version:       sessionVersion,
		ID:            session.ID,
		WorkspaceRoot: session.WorkspaceRoot,
		Current:       current.Target,
		Kept:          session.Kept,
		UpdatedAt:     session.UpdatedAt,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal replay marker: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(session.Path, markerFileName), data, 0o644); err != nil {
		return fmt.Errorf("write replay marker: %w", err)
	}
	return nil
}

func (manager Manager) prepareCheckoutPath(path string) (func(), error) {
	existed := true
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat replay worktree path: %w", err)
		}
		existed = false
	}
	if err := manager.ensureCheckoutPath(path, ""); err != nil {
		return nil, err
	}
	cleanup := func() {
		if existed {
			_ = removeDirContents(path)
			return
		}
		_ = os.RemoveAll(path)
	}
	return cleanup, nil
}

func (manager Manager) ensureCheckoutPath(path string, sessionID string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(path, 0o755)
		}
		return fmt.Errorf("stat replay worktree path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("replay worktree path is not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read replay worktree path: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	existingMarker, ok, err := readMarker(path)
	if err != nil {
		return err
	}
	if ok {
		if sessionID != "" && existingMarker.ID != sessionID {
			return fmt.Errorf("replay worktree path belongs to session %s", existingMarker.ID)
		}
		if sessionID == "" {
			return fmt.Errorf("replay worktree path is already managed by session %s", existingMarker.ID)
		}
		return nil
	}
	return fmt.Errorf("replay worktree path must be empty or an existing replay worktree: %s", path)
}

func removeDirContents(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (manager Manager) ensureKeepPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(path, 0o755)
		}
		return fmt.Errorf("stat keep path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("keep path is not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read keep path: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("keep path must be empty: %s", path)
	}
	return nil
}

func (manager Manager) ensureSessionPathOwned(session Session) error {
	if err := manager.ensureCheckoutPath(session.Path, session.ID); err != nil {
		return err
	}
	existingMarker, ok, err := readMarker(session.Path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("replay worktree marker missing: %s", filepath.Join(session.Path, markerFileName))
	}
	if existingMarker.WorkspaceRoot != manager.Repo.WorkspaceRoot.String() {
		return fmt.Errorf("replay worktree belongs to workspace %s", existingMarker.WorkspaceRoot)
	}
	return nil
}

func readMarker(path string) (marker, bool, error) {
	data, err := os.ReadFile(filepath.Join(path, markerFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return marker{}, false, nil
		}
		return marker{}, false, fmt.Errorf("read replay marker: %w", err)
	}
	var parsed marker
	if err := json.Unmarshal(data, &parsed); err != nil {
		return marker{}, false, fmt.Errorf("parse replay marker: %w", err)
	}
	if parsed.ID == "" {
		return marker{}, false, fmt.Errorf("replay marker missing id")
	}
	return parsed, true, nil
}

func (manager Manager) resolveCheckoutPath(id string, pathText string) (string, error) {
	if strings.TrimSpace(pathText) == "" {
		return filepath.Join(manager.worktreesDir(), id), nil
	}
	return manager.resolveUserPath(pathText)
}

func (manager Manager) resolveUserPath(pathText string) (string, error) {
	if strings.ContainsRune(pathText, 0) {
		return "", fmt.Errorf("replay worktree path must not contain NUL")
	}
	path, err := filepath.Abs(strings.TrimSpace(pathText))
	if err != nil {
		return "", fmt.Errorf("resolve replay worktree path: %w", err)
	}
	path = filepath.Clean(path)
	guardPath, err := resolvePathForGuard(path)
	if err != nil {
		return "", err
	}
	workspaceRoot, err := resolvePathForGuard(manager.Repo.WorkspaceRoot.String())
	if err != nil {
		return "", err
	}
	replayRoot, err := resolvePathForGuard(manager.replayDir())
	if err != nil {
		return "", err
	}
	if guardPath == workspaceRoot {
		return "", fmt.Errorf("replay worktree path cannot be the source workspace root")
	}
	if pathWithin(guardPath, workspaceRoot) && !pathWithin(guardPath, replayRoot) {
		return "", fmt.Errorf("replay worktree path must be outside the source workspace or under %s", manager.replayDir())
	}
	return path, nil
}

func resolvePathForGuard(path string) (string, error) {
	path = filepath.Clean(path)
	var suffix []string
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve symlinks for %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithin(path string, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (manager Manager) replayDir() string {
	return filepath.Join(manager.Repo.TmpDir, "replay")
}

func (manager Manager) sessionsDir() string {
	return filepath.Join(manager.replayDir(), "sessions")
}

func (manager Manager) worktreesDir() string {
	return filepath.Join(manager.replayDir(), "worktrees")
}

func (manager Manager) activePath() string {
	return filepath.Join(manager.replayDir(), "active")
}

func (manager Manager) sessionPath(id string) string {
	return filepath.Join(manager.sessionsDir(), id+".json")
}

func (manager Manager) requireRepo() error {
	if manager.Repo == nil {
		return fmt.Errorf("replay repo is required")
	}
	return nil
}

func (session Session) current() Checkpoint {
	if len(session.Sequence) == 0 || session.Current < 0 || session.Current >= len(session.Sequence) {
		return Checkpoint{}
	}
	return session.Sequence[session.Current]
}

func validateSession(session Session) error {
	if session.Version != sessionVersion {
		return fmt.Errorf("unsupported replay session version %d", session.Version)
	}
	if session.ID == "" {
		return fmt.Errorf("replay session missing id")
	}
	if session.Path == "" {
		return fmt.Errorf("replay session %s missing path", session.ID)
	}
	if len(session.Sequence) == 0 {
		return fmt.Errorf("replay session %s has no checkpoints", session.ID)
	}
	if session.Current < 0 || session.Current >= len(session.Sequence) {
		return fmt.Errorf("replay session %s current index out of range", session.ID)
	}
	return nil
}

func newSessionID(current Checkpoint) string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Sprintf("%s-turn-%06d-%s-%d", current.SessionID, current.Turn, current.Phase, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-turn-%06d-%s-%s", current.SessionID, current.Turn, current.Phase, hex.EncodeToString(suffix))
}
