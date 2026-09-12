package viewer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

type cachedSessionRecord struct {
	fingerprint string
	record      sessionRecord
}

// fingerprintPaths detects ordinary capture, import, ref packing, and removal
// without launching Git or reading event payloads. Like event tail state, cached
// reads rely on file size and modification time; caches are process-local only.
func fingerprintPaths(paths ...string) (string, error) {
	hash := sha256.New()
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				_, _ = fmt.Fprintln(hash, path, "missing")
				return nil
			}
			if err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("history path must not be a symlink: %s", path)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano(), info.Mode())
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (service *Service) recordForIdentity(ctx context.Context, identity resourceIdentity) (sessionRecord, error) {
	// Serializing cache fills also prevents concurrent patch requests from doing
	// the same history reconstruction before the first request publishes it.
	service.recordMu.Lock()
	defer service.recordMu.Unlock()
	if err := ctx.Err(); err != nil {
		return sessionRecord{}, err
	}
	metadata := service.Repo.MetadataDir
	refPaths := []string{
		filepath.Join(service.Repo.GitDir, "refs", "agent-vcs"),
		filepath.Join(service.Repo.GitDir, "packed-refs"),
		filepath.Join(metadata, "worktrees"),
		filepath.Join(metadata, "identity.json"),
	}
	refKey, err := fingerprintPaths(refPaths...)
	if err != nil {
		return sessionRecord{}, err
	}
	infos := service.refCache
	if refKey != service.refFingerprint {
		infos, err = service.Repo.ListAllCheckpointRefInfos()
		if err != nil {
			return sessionRecord{}, err
		}
		after, err := fingerprintPaths(refPaths...)
		if err != nil {
			return sessionRecord{}, err
		}
		if after == refKey {
			service.refCache, service.refFingerprint = infos, refKey
		}
	}
	session, _ := primitives.ParseSessionID(identity.SessionID)
	stream, _ := primitives.ParseEventStreamID(identity.StreamID)
	eventPaths := []string{
		filepath.Join(metadata, "log", "events", session.String()),
		filepath.Join(metadata, "log", "events", session.String()+".jsonl"),
		filepath.Join(metadata, "log", "streams", stream.String()+".json"),
	}
	eventKey, err := fingerprintPaths(eventPaths...)
	if err != nil {
		return sessionRecord{}, err
	}
	key := refKey + eventKey
	if cached, ok := service.recordCache[identity]; ok && cached.fingerprint == key {
		return cached.record, nil
	}
	var streams []eventlog.DurableStream
	for attempt := 0; ; attempt++ {
		value, found, err := eventlog.ReadDurableStream(metadata, session, stream)
		if err == nil {
			if found {
				streams = append(streams, value)
			}
			break
		}
		if attempt >= 4 || !strings.Contains(err.Error(), "trailing partial line") {
			return sessionRecord{}, err
		}
		held, lockErr := activeEventWriter(metadata)
		if lockErr != nil {
			return sessionRecord{}, lockErr
		}
		if !held {
			return sessionRecord{}, err
		}
		timer := time.NewTimer(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return sessionRecord{}, ctx.Err()
		case <-timer.C:
		}
	}
	scoped := make([]checkpoint.CheckpointRefInfo, 0)
	for _, info := range infos {
		if info.SessionID == session && info.StreamID == stream {
			scoped = append(scoped, info)
		}
	}
	records, err := service.buildRecords(ctx, streams, scoped)
	if err != nil {
		return sessionRecord{}, err
	}
	for _, record := range records {
		if record.stream.SessionID != session || record.stream.StreamID != stream ||
			identity.WorktreeID != "" && record.stream.WorktreeID.String() != identity.WorktreeID {
			continue
		}
		after, err := fingerprintPaths(eventPaths...)
		if err != nil {
			return sessionRecord{}, err
		}
		refsAfter, err := fingerprintPaths(refPaths...)
		if err != nil {
			return sessionRecord{}, err
		}
		if after == eventKey && refsAfter == refKey {
			if len(service.recordCache) >= 64 {
				clear(service.recordCache)
			}
			service.recordCache[identity] = cachedSessionRecord{fingerprint: key, record: record}
		}
		return record, nil
	}
	return sessionRecord{}, fmt.Errorf("resource no longer exists in this Turnal store")
}
