package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
)

type DurableStream struct {
	SessionID  primitives.SessionID
	StreamID   primitives.EventStreamID
	RepoID     primitives.RepoID
	WorktreeID primitives.WorktreeID
	ProducerID primitives.EventProducerID
	Path       string
	Legacy     bool
	ByteSHA256 string
	ByteCount  int64
	Events     []Event
}

func ListDurableStreams(metadataDir string) ([]DurableStream, error) {
	log := Open(metadataDir)
	entries, err := os.ReadDir(log.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read event log dir: %w", err)
	}
	var streams []DurableStream
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("event stream path invariant failed: symlink is not allowed: %s", filepath.Join(log.Dir, entry.Name()))
		}
		if !entry.IsDir() {
			if filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			sessionID, err := primitives.ParseSessionID(strings.TrimSuffix(entry.Name(), ".jsonl"))
			if err != nil {
				return nil, err
			}
			if log.StoreID == "" {
				return nil, fmt.Errorf("legacy event stream %s requires a valid store identity", entry.Name())
			}
			streamID, err := primitives.DeriveLegacyEventStreamID(log.StoreID, sessionID)
			if err != nil {
				return nil, err
			}
			stream, err := inspectDurableStream(log, sessionID, streamID, filepath.Join(log.Dir, entry.Name()), true)
			if err != nil {
				return nil, err
			}
			streams = append(streams, stream)
			continue
		}

		sessionID, err := primitives.ParseSessionID(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("event stream session directory invariant failed for %s: %w", entry.Name(), err)
		}
		dir := filepath.Join(log.Dir, entry.Name())
		streamEntries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read event stream session dir: %w", err)
		}
		for _, streamEntry := range streamEntries {
			path := filepath.Join(dir, streamEntry.Name())
			if streamEntry.Type()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("event stream path invariant failed: symlink is not allowed: %s", path)
			}
			if streamEntry.IsDir() || filepath.Ext(streamEntry.Name()) != ".jsonl" {
				continue
			}
			streamID, err := primitives.ParseEventStreamID(strings.TrimSuffix(streamEntry.Name(), ".jsonl"))
			if err != nil {
				return nil, err
			}
			stream, err := inspectDurableStream(log, sessionID, streamID, path, false)
			if err != nil {
				return nil, err
			}
			streams = append(streams, stream)
		}
	}
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].SessionID != streams[j].SessionID {
			return streams[i].SessionID.String() < streams[j].SessionID.String()
		}
		return streams[i].StreamID.String() < streams[j].StreamID.String()
	})
	return streams, nil
}

func inspectDurableStream(log Log, sessionID primitives.SessionID, streamID primitives.EventStreamID, path string, legacy bool) (DurableStream, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return DurableStream{}, fmt.Errorf("stat event stream %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return DurableStream{}, fmt.Errorf("event stream path invariant failed: regular file required: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DurableStream{}, fmt.Errorf("read event stream %s: %w", path, err)
	}
	digest := sha256.Sum256(data)
	events, err := log.readPath(sessionID, path, func() primitives.EventStreamID {
		if legacy {
			return ""
		}
		return streamID
	}())
	if err != nil {
		return DurableStream{}, err
	}
	var worktreeID primitives.WorktreeID
	repoID := log.RepoID
	producerID := log.ProducerID
	for _, event := range events {
		if event.WorktreeID == "" {
			continue
		}
		if worktreeID != "" && worktreeID != event.WorktreeID {
			return DurableStream{}, fmt.Errorf("event stream invariant failed for %s: multiple worktree ids", path)
		}
		worktreeID = event.WorktreeID
		if event.RepoID != "" {
			repoID = event.RepoID
		}
	}
	if metadata, ok := log.readStreamMetadata(streamID); ok {
		if worktreeID == "" {
			worktreeID = metadata.WorktreeID
		}
		if repoID == "" {
			repoID = metadata.RepoID
		}
		producerID = metadata.ProducerID
	}
	return DurableStream{
		SessionID:  sessionID,
		StreamID:   streamID,
		RepoID:     repoID,
		WorktreeID: worktreeID,
		ProducerID: producerID,
		Path:       path,
		Legacy:     legacy,
		ByteSHA256: "sha256:" + hex.EncodeToString(digest[:]),
		ByteCount:  info.Size(),
		Events:     events,
	}, nil
}
