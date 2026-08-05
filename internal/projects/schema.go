package projects

// SchemaVersion is stored in PRAGMA user_version. Bumping it makes an existing
// database structurally unhealthy, which Open resolves by rebuilding from the
// registry rather than migrating: every row here is derived state.
const (
	SchemaVersion = 1
	DBFileName    = "projects.sqlite"
)

const schemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- One row per registered Turnal store. store_id is the durable identity; the
-- path can move. "present" is 0 when the store directory is gone, which keeps
-- the project visible instead of silently vanishing from the index.
CREATE TABLE projects (
	store_id        TEXT PRIMARY KEY,
	repo_id         TEXT,
	store_path      TEXT NOT NULL,
	git_common_dir  TEXT,
	name            TEXT NOT NULL,
	root            TEXT NOT NULL,
	branch          TEXT,
	present         INTEGER NOT NULL DEFAULT 1,
	index_state     TEXT,
	history_state   TEXT,
	session_count   INTEGER NOT NULL DEFAULT 0,
	turn_count      INTEGER NOT NULL DEFAULT 0,
	additions       INTEGER NOT NULL DEFAULT 0,
	deletions       INTEGER NOT NULL DEFAULT 0,
	last_activity   TEXT,
	last_prompt     TEXT,
	last_adapter    TEXT,
	added_at        TEXT NOT NULL,
	refreshed_at    TEXT
);

CREATE TABLE project_worktrees (
	store_id     TEXT NOT NULL,
	root         TEXT NOT NULL,
	git_dir      TEXT,
	last_seen_at TEXT,
	PRIMARY KEY (store_id, root),
	FOREIGN KEY (store_id) REFERENCES projects(store_id) ON DELETE CASCADE
);

-- Denormalized cross-project feed. This is what makes "what did my agents do
-- today, anywhere" answerable without opening every store on every page load.
CREATE TABLE activity (
	store_id     TEXT NOT NULL,
	session_key  TEXT NOT NULL,
	session_id   TEXT NOT NULL,
	title        TEXT,
	adapter      TEXT,
	model        TEXT,
	branch       TEXT,
	status       TEXT,
	turn_count   INTEGER NOT NULL DEFAULT 0,
	file_count   INTEGER NOT NULL DEFAULT 0,
	additions    INTEGER NOT NULL DEFAULT 0,
	deletions    INTEGER NOT NULL DEFAULT 0,
	started_at   TEXT,
	finished_at  TEXT,
	PRIMARY KEY (store_id, session_key),
	FOREIGN KEY (store_id) REFERENCES projects(store_id) ON DELETE CASCADE
);

CREATE INDEX activity_recent ON activity(finished_at DESC);
CREATE INDEX projects_recent ON projects(last_activity DESC);
`
