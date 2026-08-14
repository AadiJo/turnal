package notes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

// Resolved is a note target plus the local evidence that made it resolvable.
//
// PostCommit is present only when the target turn's post checkpoint exists in
// this store. Anchoring a note to a line range requires it; a note about a
// turn known only from a teammate's published bundle can still be recorded, but
// it cannot be line-anchored against local file state.
type Resolved struct {
	Target     Target
	PostCommit primitives.CommitSHA
	Local      bool
}

// ResolveLocalTurn resolves a recorded turn in this store to a canonical target.
//
// The turn does not have to belong to the current worktree: notes are authored
// separately from the turns they discuss, so any turn this store recorded can be
// noted. A turn id that appears in more than one stream is ambiguous and must be
// narrowed with an explicit stream.
func ResolveLocalTurn(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, streamID primitives.EventStreamID) (Resolved, error) {
	if repo == nil {
		return Resolved{}, fmt.Errorf("note target resolution requires checkpoint repo")
	}
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return Resolved{}, err
	}
	parsedTurnID, err := primitives.NewTurnID(turnID.Uint64())
	if err != nil {
		return Resolved{}, err
	}

	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return Resolved{}, err
	}
	matches := map[primitives.EventStreamID]struct{}{}
	for _, stream := range streams {
		if stream.Workspace || stream.SessionID != parsedSessionID {
			continue
		}
		if streamID != "" && stream.StreamID != streamID {
			continue
		}
		for _, event := range stream.Events {
			if event.TurnID != nil && *event.TurnID == parsedTurnID {
				matches[stream.StreamID] = struct{}{}
				break
			}
		}
	}
	if len(matches) == 0 {
		return Resolved{}, fmt.Errorf("turn %s:%s does not exist in this Turnal store", parsedSessionID, parsedTurnID)
	}
	if len(matches) > 1 {
		candidates := make([]string, 0, len(matches))
		for candidate := range matches {
			candidates = append(candidates, candidate.String())
		}
		sort.Strings(candidates)
		return Resolved{}, fmt.Errorf("turn %s:%s is ambiguous; rerun with --stream followed by one of: %s", parsedSessionID, parsedTurnID, strings.Join(candidates, ", "))
	}
	var resolvedStream primitives.EventStreamID
	for candidate := range matches {
		resolvedStream = candidate
	}

	resolved := Resolved{
		Target: Target{
			RepoID:    repo.RepoID,
			StreamID:  resolvedStream,
			SessionID: parsedSessionID,
			TurnID:    parsedTurnID,
		},
		Local: true,
	}

	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		return Resolved{}, err
	}
	for _, info := range infos {
		if info.SessionID != parsedSessionID || info.TurnID != parsedTurnID {
			continue
		}
		if info.StreamID != "" && info.StreamID != resolvedStream {
			continue
		}
		if info.Phase == primitives.CheckpointPhasePost {
			resolved.PostCommit = info.Commit
			break
		}
	}
	return resolved, nil
}
