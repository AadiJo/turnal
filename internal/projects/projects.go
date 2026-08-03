// Package projects maintains the machine-wide index that backs the global
// Turnal viewer. It answers two questions the per-project CLI cannot: which
// projects are recorded on this machine, and what did agents do across all of
// them recently.
//
// The registry that turnal init writes (see checkpoint.ListRegisteredStores)
// stays authoritative for which projects exist. Everything in this database is
// derived from it plus each store's durable records, so the file is disposable:
// delete it and Refresh rebuilds it.
package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	_ "modernc.org/sqlite"
)

// Project is one recorded project as shown in the global index.
type Project struct {
	StoreID      string
	RepoID       string
	StorePath    string
	Name         string
	Root         string
	Branch       string
	Present      bool
	IndexState   string
	HistoryState string
	SessionCount int
	TurnCount    int
	Additions    int
	Deletions    int
	LastActivity time.Time
	LastPrompt   string
	LastAdapter  string
	AddedAt      time.Time
	Worktrees    []Worktree
}

// Worktree is one workspace attached to a project's store.
type Worktree struct {
	Root       string
	GitDir     string
	LastSeenAt string
}

// Activity is one recorded session, carrying enough project identity to route
// a click in the cross-project feed back to its owner.
type Activity struct {
	StoreID     string
	ProjectName string
	SessionKey  string
	SessionID   string
	Title       string
	Adapter     string
	Model       string
	Branch      string
	Status      string
	TurnCount   int
	FileCount   int
	Additions   int
	Deletions   int
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Summary is the per-store aggregate the viewer supplies during Refresh. It is
// passed in rather than computed here so this package does not depend on the
// viewer, which depends on it.
type Summary struct {
	Branch       string
	IndexState   string
	HistoryState string
	SessionCount int
	TurnCount    int
	Additions    int
	Deletions    int
	LastActivity time.Time
	LastPrompt   string
	LastAdapter  string
	Sessions     []Activity
}

// Summarizer produces a Summary for one registered store. Refresh tolerates an
// error for a single store: one unreadable project must not blank the index.
type Summarizer func(ctx context.Context, store checkpoint.RegisteredStore) (Summary, error)

// DB is the machine-wide project index.
type DB struct {
	db   *sql.DB
	path string
}

// Path returns the database file location, honoring TURNAL_STATE_DIR.
func Path() (string, error) {
	dir, err := checkpoint.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DBFileName), nil
}

// Open opens the index, creating or rebuilding it when missing, corrupt, or
// written by a different schema version. Rebuilding is always safe here.
func Open() (*DB, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create turnal state directory: %w", err)
	}
	db, err := openAt(path)
	if err != nil {
		return nil, err
	}
	healthy, err := db.healthy()
	if err != nil || !healthy {
		if closeErr := db.Close(); closeErr != nil {
			return nil, closeErr
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("discard unusable project index: %w", err)
		}
		if db, err = openAt(path); err != nil {
			return nil, err
		}
		if err := db.initialize(); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func openAt(path string) (*DB, error) {
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open project index: %w", err)
	}
	// Single connection: writes are serialized and the index is small.
	handle.SetMaxOpenConns(1)
	return &DB{db: handle, path: path}, nil
}

func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// Path reports the file backing this index.
func (d *DB) Path() string { return d.path }

func (d *DB) healthy() (bool, error) {
	var quickCheck string
	if err := d.db.QueryRow("PRAGMA quick_check").Scan(&quickCheck); err != nil {
		return false, nil
	}
	if quickCheck != "ok" {
		return false, nil
	}
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return false, nil
	}
	return version == SchemaVersion, nil
}

func (d *DB) initialize() error {
	if _, err := d.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("create project index schema: %w", err)
	}
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("set project index schema version: %w", err)
	}
	return nil
}

// Refresh reconciles the index against the registry and re-summarizes each
// store. Stores no longer in the registry are dropped; stores whose directory
// has disappeared are kept and marked absent. A per-store summarize failure is
// recorded on that row and does not abort the refresh.
func (d *DB) Refresh(ctx context.Context, summarize Summarizer) error {
	stores, err := checkpoint.ListRegisteredStores()
	if err != nil {
		return err
	}
	registered := make(map[string]struct{}, len(stores))
	for _, store := range stores {
		registered[store.StoreID.String()] = struct{}{}
	}
	if err := d.pruneMissing(registered); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, store := range stores {
		if err := ctx.Err(); err != nil {
			return err
		}
		present := checkpoint.StoreExists(store.StorePath)
		root := primaryRoot(store)
		summary := Summary{HistoryState: "ready"}
		if present {
			produced, summarizeErr := summarize(ctx, store)
			if summarizeErr != nil {
				summary.HistoryState = "attention"
			} else {
				summary = produced
			}
		} else {
			summary.HistoryState = "absent"
		}
		if err := d.upsert(ctx, store, root, present, summary, now); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) pruneMissing(registered map[string]struct{}) error {
	rows, err := d.db.Query(`SELECT store_id FROM projects`)
	if err != nil {
		return fmt.Errorf("read indexed projects: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan indexed project: %w", err)
		}
		if _, ok := registered[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read indexed projects: %w", err)
	}
	rows.Close()
	for _, id := range stale {
		if _, err := d.db.Exec(`DELETE FROM projects WHERE store_id = ?`, id); err != nil {
			return fmt.Errorf("drop deregistered project: %w", err)
		}
	}
	return nil
}

func (d *DB) upsert(ctx context.Context, store checkpoint.RegisteredStore, root string, present bool, summary Summary, now time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project index write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	storeID := store.StoreID.String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (
			store_id, repo_id, store_path, git_common_dir, name, root, branch,
			present, index_state, history_state, session_count, turn_count,
			additions, deletions, last_activity, last_prompt, last_adapter,
			added_at, refreshed_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(store_id) DO UPDATE SET
			repo_id = excluded.repo_id,
			store_path = excluded.store_path,
			git_common_dir = excluded.git_common_dir,
			name = excluded.name,
			root = excluded.root,
			branch = excluded.branch,
			present = excluded.present,
			index_state = excluded.index_state,
			history_state = excluded.history_state,
			session_count = excluded.session_count,
			turn_count = excluded.turn_count,
			additions = excluded.additions,
			deletions = excluded.deletions,
			last_activity = excluded.last_activity,
			last_prompt = excluded.last_prompt,
			last_adapter = excluded.last_adapter,
			refreshed_at = excluded.refreshed_at`,
		storeID, store.RepoID.String(), store.StorePath, store.GitCommonDir,
		filepath.Base(root), root, summary.Branch, boolToInt(present),
		summary.IndexState, summary.HistoryState, summary.SessionCount, summary.TurnCount,
		summary.Additions, summary.Deletions, timeText(summary.LastActivity),
		summary.LastPrompt, summary.LastAdapter, timeText(now), timeText(now),
	); err != nil {
		return fmt.Errorf("write indexed project: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM project_worktrees WHERE store_id = ?`, storeID); err != nil {
		return fmt.Errorf("reset project worktrees: %w", err)
	}
	for _, worktree := range store.Worktrees {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_worktrees (store_id, root, git_dir, last_seen_at)
			VALUES (?,?,?,?)`, storeID, worktree.Root, worktree.GitDir, worktree.LastSeenAt,
		); err != nil {
			return fmt.Errorf("write project worktree: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM activity WHERE store_id = ?`, storeID); err != nil {
		return fmt.Errorf("reset project activity: %w", err)
	}
	for _, session := range summary.Sessions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO activity (
				store_id, session_key, session_id, title, adapter, model, branch,
				status, turn_count, file_count, additions, deletions, started_at, finished_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			storeID, session.SessionKey, session.SessionID, session.Title, session.Adapter,
			session.Model, session.Branch, session.Status, session.TurnCount, session.FileCount,
			session.Additions, session.Deletions, timeText(session.StartedAt), timeText(session.FinishedAt),
		); err != nil {
			return fmt.Errorf("write project activity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project index write: %w", err)
	}
	return nil
}

// Projects lists indexed projects, most recently active first. Projects with no
// recorded activity sort after those that have some, then by name.
func (d *DB) Projects(ctx context.Context) ([]Project, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT store_id, repo_id, store_path, name, root, COALESCE(branch, ''), present,
		       COALESCE(index_state, ''), COALESCE(history_state, ''), session_count, turn_count,
		       additions, deletions, COALESCE(last_activity, ''), COALESCE(last_prompt, ''),
		       COALESCE(last_adapter, ''), added_at
		FROM projects
		ORDER BY last_activity IS NULL, last_activity DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list indexed projects: %w", err)
	}
	defer rows.Close()
	var list []Project
	for rows.Next() {
		var project Project
		var present int
		var lastActivity, addedAt string
		if err := rows.Scan(
			&project.StoreID, &project.RepoID, &project.StorePath, &project.Name, &project.Root,
			&project.Branch, &present, &project.IndexState, &project.HistoryState,
			&project.SessionCount, &project.TurnCount, &project.Additions, &project.Deletions,
			&lastActivity, &project.LastPrompt, &project.LastAdapter, &addedAt,
		); err != nil {
			return nil, fmt.Errorf("scan indexed project: %w", err)
		}
		project.Present = present == 1
		project.LastActivity = parseTime(lastActivity)
		project.AddedAt = parseTime(addedAt)
		list = append(list, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list indexed projects: %w", err)
	}
	for i := range list {
		worktrees, err := d.worktrees(ctx, list[i].StoreID)
		if err != nil {
			return nil, err
		}
		list[i].Worktrees = worktrees
	}
	return list, nil
}

func (d *DB) worktrees(ctx context.Context, storeID string) ([]Worktree, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT root, COALESCE(git_dir, ''), COALESCE(last_seen_at, '')
		FROM project_worktrees WHERE store_id = ? ORDER BY root`, storeID)
	if err != nil {
		return nil, fmt.Errorf("list project worktrees: %w", err)
	}
	defer rows.Close()
	var list []Worktree
	for rows.Next() {
		var worktree Worktree
		if err := rows.Scan(&worktree.Root, &worktree.GitDir, &worktree.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan project worktree: %w", err)
		}
		list = append(list, worktree)
	}
	return list, rows.Err()
}

// Project reads one indexed project by store id.
func (d *DB) Project(ctx context.Context, storeID string) (Project, error) {
	list, err := d.Projects(ctx)
	if err != nil {
		return Project{}, err
	}
	for _, project := range list {
		if project.StoreID == storeID {
			return project, nil
		}
	}
	return Project{}, fmt.Errorf("project %s is not indexed", storeID)
}

// Activity returns the newest sessions across every project.
func (d *DB) Activity(ctx context.Context, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT a.store_id, p.name, a.session_key, a.session_id, COALESCE(a.title, ''),
		       COALESCE(a.adapter, ''), COALESCE(a.model, ''), COALESCE(a.branch, ''),
		       COALESCE(a.status, ''), a.turn_count, a.file_count, a.additions, a.deletions,
		       COALESCE(a.started_at, ''), COALESCE(a.finished_at, '')
		FROM activity a
		JOIN projects p ON p.store_id = a.store_id
		ORDER BY COALESCE(a.finished_at, a.started_at) DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()
	var list []Activity
	for rows.Next() {
		var item Activity
		var started, finished string
		if err := rows.Scan(
			&item.StoreID, &item.ProjectName, &item.SessionKey, &item.SessionID, &item.Title,
			&item.Adapter, &item.Model, &item.Branch, &item.Status, &item.TurnCount,
			&item.FileCount, &item.Additions, &item.Deletions, &started, &finished,
		); err != nil {
			return nil, fmt.Errorf("scan activity: %w", err)
		}
		item.StartedAt = parseTime(started)
		item.FinishedAt = parseTime(finished)
		list = append(list, item)
	}
	return list, rows.Err()
}

// Deregister removes a project from the registry and from this index. No files
// are deleted: the store keeps its recorded history and can be re-registered.
func (d *DB) Deregister(ctx context.Context, storeID string) error {
	parsed, err := primitives.ParseStoreID(storeID)
	if err != nil {
		return err
	}
	if err := checkpoint.DeregisterStore(parsed); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM projects WHERE store_id = ?`, storeID); err != nil {
		return fmt.Errorf("drop project from index: %w", err)
	}
	return nil
}

func primaryRoot(store checkpoint.RegisteredStore) string {
	for _, worktree := range store.Worktrees {
		if worktree.Root != "" {
			return worktree.Root
		}
	}
	// A store always lives at <root>/.turnal, so the parent is the workspace.
	return filepath.Dir(store.StorePath)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// ErrNotIndexed reports a project the index does not know about.
var ErrNotIndexed = errors.New("project is not indexed")
