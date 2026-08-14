package notes

import (
	"sort"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

// Query narrows a note projection. A zero Query returns every surviving note.
type Query struct {
	Target    *Target
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	// Path restricts results to notes anchored to one file.
	Path primitives.RepoPath
}

func (query Query) matches(note Note) bool {
	if query.Target != nil && note.Target.key() != query.Target.key() {
		return false
	}
	if query.SessionID != "" && note.Target.SessionID != query.SessionID {
		return false
	}
	if query.TurnID != 0 && note.Target.TurnID != query.TurnID {
		return false
	}
	if query.Path != "" && (note.Anchor == nil || note.Anchor.Path != query.Path) {
		return false
	}
	return true
}

// List projects surviving notes from the store's durable note events.
func List(repo *checkpoint.Repo, query Query) ([]Note, error) {
	events, err := ReadEvents(repo)
	if err != nil {
		return nil, err
	}
	return Project(events, query), nil
}

// ListFromMetadata projects surviving notes for readers that hold only a
// metadata directory.
func ListFromMetadata(metadataDir string, query Query) ([]Note, error) {
	events, err := ReadEventsFromMetadata(metadataDir)
	if err != nil {
		return nil, err
	}
	return Project(events, query), nil
}

// Project folds note create and delete events into surviving notes.
//
// A tombstone only hides a note created in the same stream. Notes are authored,
// so allowing a cross-stream tombstone would let one reviewer retract another
// reviewer's note by supplying its id. Malformed payloads are skipped rather
// than failing the projection: a note is commentary, and one unreadable comment
// must not make the rest of a reviewer's history unreadable.
func Project(events []eventlog.Event, query Query) []Note {
	type streamNote struct {
		streamID primitives.EventStreamID
		note     Note
	}
	created := make(map[primitives.NoteID]streamNote)
	tombstoned := make(map[primitives.NoteID]map[primitives.EventStreamID]struct{})

	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeNoteCreate:
			payload, err := ParseCreatePayload(event.Payload)
			if err != nil {
				continue
			}
			if _, exists := created[payload.NoteID]; exists {
				continue
			}
			created[payload.NoteID] = streamNote{streamID: event.StreamID, note: noteFromCreate(event, payload)}
		case primitives.EventTypeNoteDelete:
			payload, err := ParseDeletePayload(event.Payload)
			if err != nil {
				continue
			}
			if tombstoned[payload.NoteID] == nil {
				tombstoned[payload.NoteID] = make(map[primitives.EventStreamID]struct{})
			}
			tombstoned[payload.NoteID][event.StreamID] = struct{}{}
		}
	}

	var result []Note
	for noteID, entry := range created {
		if _, hidden := tombstoned[noteID][entry.streamID]; hidden {
			continue
		}
		if !query.matches(entry.note) {
			continue
		}
		result = append(result, entry.note)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Time.Equal(result[j].CreatedAt.Time) {
			return result[i].CreatedAt.Time.Before(result[j].CreatedAt.Time)
		}
		if result[i].StreamID != result[j].StreamID {
			return result[i].StreamID.String() < result[j].StreamID.String()
		}
		return result[i].Seq.Uint64() < result[j].Seq.Uint64()
	})
	return result
}

// ForTurn returns surviving notes about one recorded turn.
func ForTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID) ([]Note, error) {
	return List(repo, Query{SessionID: sessionID, TurnID: turnID})
}
