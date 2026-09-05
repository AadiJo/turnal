package events

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AadiJo/turnal/internal/primitives"
	_ "modernc.org/sqlite"
)

// Source index mutations hold the stream append lock. Covered reads can avoid
// the lock by checking that the durable tail stays unchanged across the lookup.
func (log Log) sourceIndexPath(session primitives.SessionID, stream primitives.EventStreamID) string {
	name := stream.String()
	if name == "" {
		name = "legacy"
	}
	return filepath.Join(filepath.Dir(log.Dir), "source", session.String(), "index", name+".sqlite")
}

func openSourceIndex(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if err := file.Close(); err != nil {
		return nil, err
	}
	if statErr != nil {
		return nil, statErr
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Only the event log needs durable writes. Lost cache transactions are
	// detected by coverage validation and rebuilt after a crash.
	if info.Size() == 0 {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS source_events (source TEXT PRIMARY KEY, event BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS coverage (id INTEGER PRIMARY KEY CHECK(id = 1), state BLOB NOT NULL)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func readSourceCoverage(db *sql.DB) (tailState, error) {
	var data []byte
	if err := db.QueryRow(`SELECT state FROM coverage WHERE id = 1`).Scan(&data); err != nil {
		return tailState{}, err
	}
	var state tailState
	err := json.Unmarshal(data, &state)
	return state, err
}

func writeSourceCoverage(tx *sql.Tx, state tailState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO coverage(id, state) VALUES(1, ?)`, data)
	return err
}

func indexSourceEvent(tx *sql.Tx, event Event) error {
	if event.SourceID == "" {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO source_events(source, event) VALUES(?, ?)`, event.SourceID, data)
	return err
}

func (log Log) findSourceInStream(db *sql.DB, session primitives.SessionID, path string, stream primitives.EventStreamID, source string) (Event, bool, error) {
	if db != nil {
		if event, found, err := log.querySourceIndex(db, session, path, stream, source); err == nil {
			return event, found, nil
		}
	}
	// A missing, stale, or damaged cache must never hide durable events or make
	// recording unavailable. Verify the log before returning an uncached result.
	records, err := log.readPath(session, path, stream)
	if err != nil {
		return Event{}, false, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].SourceID == source {
			return records[i], true, nil
		}
	}
	return Event{}, false, nil
}

func (log Log) querySourceIndex(db *sql.DB, session primitives.SessionID, path string, stream primitives.EventStreamID, source string) (Event, bool, error) {
	identity := Event{SessionID: session, StreamID: stream}
	current, valid, err := log.currentTailState(identity)
	if err != nil {
		return Event{}, false, err
	}
	covered, coverageErr := readSourceCoverage(db)
	if !valid || coverageErr != nil || covered != current {
		records, err := log.readPath(session, path, stream)
		if err != nil {
			return Event{}, false, err
		}
		current = tailState{}
		if len(records) > 0 {
			last := records[len(records)-1]
			if err := log.writeTailState(path, last); err != nil {
				return Event{}, false, err
			}
			var valid bool
			current, valid, err = log.currentTailState(identity)
			if err != nil {
				return Event{}, false, err
			}
			if !valid {
				return Event{}, false, fmt.Errorf("source index tail changed during rebuild")
			}
		}
		tx, err := db.Begin()
		if err != nil {
			return Event{}, false, err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM source_events`); err != nil {
			return Event{}, false, err
		}
		for _, event := range records {
			if err := indexSourceEvent(tx, event); err != nil {
				return Event{}, false, err
			}
		}
		if err := writeSourceCoverage(tx, current); err != nil {
			return Event{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Event{}, false, err
		}
	}
	return readIndexedSource(db, session, stream, source, current)
}

func readIndexedSource(db *sql.DB, session primitives.SessionID, stream primitives.EventStreamID, source string, current tailState) (Event, bool, error) {
	var data []byte
	err := db.QueryRow(`SELECT event FROM source_events WHERE source = ?`, source).Scan(&data)
	if err == sql.ErrNoRows {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, err
	}
	event, err := parseEventLine(data)
	if err != nil {
		return Event{}, false, err
	}
	hash, err := eventHash(event)
	if err != nil {
		return Event{}, false, err
	}
	if event.SessionID != session || event.StreamID != stream || event.SourceID != source || event.Hash != hash || event.Seq.Uint64() > current.Sequence {
		return Event{}, false, fmt.Errorf("source index event does not match its coverage")
	}
	return event, true, nil
}

// Advance only an index covering the immediately preceding event. Otherwise the
// next lookup rebuilds it from the durable log, including after interrupted writes.
func (log Log) advanceSourceIndex(event Event, db *sql.DB) {
	if db == nil {
		return
	}
	previous, err := readSourceCoverage(db)
	if err != nil || previous.Sequence+1 != event.Seq.Uint64() {
		return
	}
	current, valid, err := log.currentTailState(event)
	if err != nil || !valid {
		return
	}
	if previous.Sequence > 0 && (previous.Hash != event.PrevHash.String() || previous.Generation != current.Generation) {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if err := indexSourceEvent(tx, event); err != nil {
		return
	}
	if err := writeSourceCoverage(tx, current); err != nil {
		return
	}
	_ = tx.Commit()
}

// A covered read can avoid the append lock. Rechecking the durable tail after
// the query prevents a concurrent append from turning a stale absence into a hit.
func (log Log) cachedSource(session primitives.SessionID, stream primitives.EventStreamID, source string) (Event, bool, bool) {
	identity := Event{SessionID: session, StreamID: stream}
	before, valid, err := log.currentTailState(identity)
	if err != nil || !valid {
		return Event{}, false, false
	}
	path := log.sourceIndexPath(session, stream)
	if _, err := os.Stat(path); err != nil {
		return Event{}, false, false
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return Event{}, false, false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	covered, err := readSourceCoverage(db)
	if err != nil || covered != before {
		return Event{}, false, false
	}
	event, found, err := readIndexedSource(db, session, stream, source, before)
	if err != nil {
		return Event{}, false, false
	}
	after, valid, err := log.currentTailState(identity)
	if err != nil || !valid || after != before {
		return Event{}, false, false
	}
	return event, found, true
}

func (log Log) openSourceIndexForAppend(session primitives.SessionID, stream primitives.EventStreamID, hasSource bool) *sql.DB {
	path := log.sourceIndexPath(session, stream)
	if _, err := os.Stat(path); err != nil {
		if !hasSource || !log.useSourceIndex(session, stream) {
			return nil
		}
	}
	db, err := openSourceIndex(path)
	if err != nil {
		return nil
	}
	return db
}

// Small logs cost less to scan than opening and checkpointing SQLite. Start the
// derived index at 1 MiB, then retain it through later appends and truncations.
func (log Log) useSourceIndex(session primitives.SessionID, stream primitives.EventStreamID) bool {
	if _, err := os.Stat(log.sourceIndexPath(session, stream)); err == nil {
		return true
	}
	info, err := os.Stat(log.eventPath(Event{SessionID: session, StreamID: stream}))
	return err == nil && info.Size() >= 1<<20
}
