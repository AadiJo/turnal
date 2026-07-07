package index

const schemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE sessions (
	session_id      TEXT PRIMARY KEY,
	first_event_at  TEXT,
	last_event_at   TEXT,
	primary_adapter TEXT,
	event_count     INTEGER NOT NULL DEFAULT 0,
	turn_count      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE events (
	session_id   TEXT    NOT NULL,
	seq          INTEGER NOT NULL,
	turn_id      INTEGER,
	event_type   TEXT    NOT NULL,
	adapter      TEXT,
	event_time   TEXT    NOT NULL,
	source_id    TEXT,
	raw_ref      TEXT,
	prev_hash    TEXT    NOT NULL,
	event_hash   TEXT    NOT NULL,
	payload_json TEXT    NOT NULL,
	PRIMARY KEY (session_id, seq),
	FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE TABLE turns (
	session_id              TEXT    NOT NULL,
	turn_id                 INTEGER NOT NULL,
	status                  TEXT    NOT NULL,
	event_count             INTEGER NOT NULL DEFAULT 0,
	adapter                 TEXT,
	prompt_preview          TEXT,
	assistant_preview       TEXT,
	tool_names_json         TEXT    NOT NULL DEFAULT '[]',
	event_type_counts_json  TEXT    NOT NULL DEFAULT '{}',
	events_first_at         TEXT,
	events_last_at          TEXT,
	diff_loaded             INTEGER NOT NULL DEFAULT 0,
	diff_file_count         INTEGER NOT NULL DEFAULT 0,
	diff_additions          INTEGER NOT NULL DEFAULT 0,
	diff_deletions          INTEGER NOT NULL DEFAULT 0,
	diff_binary_files       INTEGER NOT NULL DEFAULT 0,
	warnings_json           TEXT    NOT NULL DEFAULT '[]',
	PRIMARY KEY (session_id, turn_id),
	FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE TABLE checkpoints (
	ref          TEXT PRIMARY KEY,
	session_id   TEXT    NOT NULL,
	turn_id      INTEGER NOT NULL,
	phase        TEXT    NOT NULL,
	commit_sha   TEXT    NOT NULL,
	committed_at TEXT    NOT NULL,
	UNIQUE (session_id, turn_id, phase),
	FOREIGN KEY (session_id, turn_id) REFERENCES turns(session_id, turn_id) ON DELETE CASCADE
);

CREATE TABLE file_touches (
	session_id TEXT    NOT NULL,
	turn_id    INTEGER NOT NULL,
	path       TEXT    NOT NULL,
	additions  INTEGER NOT NULL,
	deletions  INTEGER NOT NULL,
	binary     INTEGER NOT NULL,
	PRIMARY KEY (session_id, turn_id, path),
	FOREIGN KEY (session_id, turn_id) REFERENCES turns(session_id, turn_id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE turn_search USING fts5(
	session_id UNINDEXED,
	turn_id UNINDEXED,
	first_at UNINDEXED,
	last_at UNINDEXED,
	adapter,
	prompt,
	assistant,
	tools,
	paths,
	event_text,
	tokenize = 'unicode61'
);

CREATE INDEX idx_turns_session ON turns(session_id, turn_id DESC);
CREATE INDEX idx_events_turn ON events(session_id, turn_id, seq);
CREATE INDEX idx_checkpoints_session ON checkpoints(session_id, turn_id, phase);
`
