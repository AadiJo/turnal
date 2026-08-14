package sharedhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func notesRef(deviceID string) string {
	return NotesRefPrefix + deviceID
}

func notesIncomingRef(deviceID string) string {
	return "refs/turnal/v1/notes-incoming/" + deviceID + "/tip"
}

// openNotesGitStore opens the note channel's own Git repository.
//
// Notes get a separate repository, not just a separate ref, so a note bundle
// can never enter a turn-context tree. A receiver that predates notes enumerates
// only refs/turnal/v1/history/, so this channel is invisible to it rather than
// being an undecodable event that quarantines the publisher.
func openNotesGitStore(ctx context.Context, repo *checkpoint.Repo) (*gitStore, error) {
	root := filepath.Join(sharedRoot(repo), "notes-repository")
	store := &gitStore{repo: repo, root: root, networkTimeout: DefaultNetworkTimeout}
	gitDir := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect note sharing Git repository: %w", err)
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("note sharing Git metadata is not a regular directory: %s", gitDir)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if _, err := store.run(ctx, "init", "--initial-branch=local"); err != nil {
		return nil, err
	}
	for _, setting := range [][2]string{
		{"user.name", "Turnal Shared History"},
		{"user.email", "shared-history@turnal.local"},
		{"commit.gpgsign", "false"},
		{"core.filemode", "true"},
		{"fetch.writeCommitGraph", "false"},
	} {
		if _, err := store.run(ctx, "config", "--local", setting[0], setting[1]); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func validateNotesTreePaths(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("note sharing tree is empty")
	}
	for _, path := range paths {
		if path == "batch.json" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		if len(parts) != 4 || parts[0] != "notes" || parts[3] != "note.json" && parts[3] != "manifest.json" {
			return fmt.Errorf("note sharing tree contains forbidden path %q", path)
		}
		bundleID, err := primitives.ParseBundleID(parts[2])
		if err != nil || filepath.ToSlash(filepath.Join(parts[0], parts[1], parts[2])) != noteBundlePath(bundleID) {
			return fmt.Errorf("note sharing tree contains non-canonical bundle path %q", path)
		}
	}
	return nil
}

func validateNoteBatchChanges(paths []string, batch NoteBatch) error {
	expected := map[string]struct{}{"batch.json": {}}
	for _, item := range batch.Bundles {
		expected[item.Path+"/manifest.json"] = struct{}{}
		expected[item.Path+"/note.json"] = struct{}{}
	}
	if len(paths) != len(expected) {
		return fmt.Errorf("commit changes %d paths, expected %d closed-schema paths", len(paths), len(expected))
	}
	for _, path := range paths {
		if _, allowed := expected[filepath.ToSlash(path)]; !allowed {
			return fmt.Errorf("commit changes forbidden path %q", path)
		}
	}
	return nil
}

func (store *gitStore) commitNoteBatch(ctx context.Context, batch NoteBatch, bundles []builtNoteBundle) (string, error) {
	if len(bundles) == 0 {
		return store.localHead(ctx)
	}
	if len(bundles) > MaxBundlesPerBatch {
		return "", fmt.Errorf("note batch exceeds %d bundles", MaxBundlesPerBatch)
	}
	head, err := store.localHead(ctx)
	if err != nil {
		return "", err
	}
	if head == "" {
		if _, err := store.run(ctx, "read-tree", "--empty"); err != nil {
			return "", err
		}
	} else if _, err := store.run(ctx, "read-tree", head); err != nil {
		return "", err
	}
	for _, bundle := range bundles {
		dir := filepath.Join(store.root, filepath.FromSlash(bundle.Path))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		manifestTracked, err := store.fileExistsAtCommit(ctx, head, bundle.Path+"/manifest.json")
		if err != nil {
			return "", err
		}
		noteTracked, err := store.fileExistsAtCommit(ctx, head, bundle.Path+"/note.json")
		if err != nil {
			return "", err
		}
		if err := ensureImmutableFile(filepath.Join(dir, "manifest.json"), bundle.Manifest, !manifestTracked); err != nil {
			return "", err
		}
		if err := ensureImmutableFile(filepath.Join(dir, "note.json"), bundle.NoteJSON, !noteTracked); err != nil {
			return "", err
		}
	}
	if err := writeJSONAtomic(filepath.Join(store.root, "batch.json"), batch, 0o600); err != nil {
		return "", err
	}
	addArgs := []string{"add", "--", "batch.json"}
	for _, bundle := range bundles {
		addArgs = append(addArgs, bundle.Path+"/manifest.json", bundle.Path+"/note.json")
	}
	if _, err := store.run(ctx, addArgs...); err != nil {
		return "", err
	}
	tracked, err := store.run(ctx, "ls-files", "-z")
	if err != nil {
		return "", err
	}
	if err := validateNotesTreePaths(splitNULPaths(tracked)); err != nil {
		return "", err
	}
	message := fmt.Sprintf("turnal notes: %d bundle", len(bundles))
	if len(bundles) != 1 {
		message += "s"
	}
	if _, err := store.run(ctx, "commit", "--no-verify", "-m", message); err != nil {
		return "", err
	}
	return store.localHead(ctx)
}

// recoverCommittedNoteState rebuilds outbox bookkeeping from the durable local
// tip after an interruption between committing a note batch and saving state.
//
// Without it the next push sees no committed bundles, rebuilds the same note
// operations, and commits them again at paths the previous commit already
// occupies. Receivers reject that as a rewrite of an immutable path and
// quarantine the publisher, so the local crash becomes a permanent remote
// failure. The batch at the tip is verified before it is trusted, because it is
// read back from disk.
func (store *gitStore) recoverCommittedNoteState(ctx context.Context, identity deviceIdentity, state *notesStateFile) error {
	if len(state.Committed) > 0 {
		return nil
	}
	head, err := store.localHead(ctx)
	if err != nil || head == "" || state.LastSeen[identity.DeviceID] == head {
		return err
	}
	data, err := store.showFile(ctx, head, "batch.json", 8<<20)
	if err != nil {
		return fmt.Errorf("recover note outbox: %w", err)
	}
	var batch NoteBatch
	if err := decodeStrictJSON(data, &batch); err != nil {
		return fmt.Errorf("recover note outbox batch: %w", err)
	}
	if _, err := verifyNoteBatch(batch); err != nil {
		return fmt.Errorf("recover note outbox: %w", err)
	}
	if batch.SchemaVersion != NotesSchemaVersion || batch.DeviceID != identity.DeviceID ||
		batch.PublicKey != identity.PublicKey || len(batch.Bundles) == 0 || len(batch.Bundles) > MaxBundlesPerBatch {
		return fmt.Errorf("recover note outbox: local tip has an invalid batch")
	}
	parents, err := store.run(ctx, "show", "-s", "--format=%P", head)
	if err != nil {
		return err
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) > 1 {
		return fmt.Errorf("recover note outbox: local tip is a merge")
	}
	actualPrevious := ""
	if len(parentFields) == 1 {
		actualPrevious = parentFields[0]
	}
	if batch.PreviousHead != actualPrevious {
		return fmt.Errorf("recover note outbox: batch does not extend its Git parent")
	}
	for _, item := range batch.Bundles {
		if _, published := state.Published[item.BundleID.String()]; !published {
			state.Committed[item.BundleID.String()] = head
		}
	}
	return nil
}

func (store *gitStore) pushNotes(ctx context.Context, remote, deviceID, lastSeen string) (string, error) {
	local, err := store.localHead(ctx)
	if err != nil || local == "" {
		return local, err
	}
	ref := notesRef(deviceID)
	remoteHead, err := store.remoteHead(ctx, remote, ref)
	if err != nil {
		return "", err
	}
	if remoteHead == "" && lastSeen != "" {
		return "", fmt.Errorf("note ref disappeared after head %s was observed", lastSeen)
	}
	if remoteHead == local {
		return local, nil
	}
	if remoteHead != "" && lastSeen != "" && remoteHead != lastSeen {
		return "", fmt.Errorf("note ref rewound or was replaced after head %s was observed; current head is %s", lastSeen, remoteHead)
	}
	if remoteHead != "" && remoteHead != local {
		if _, err := store.runNetwork(ctx, "fetch", "--no-tags", remote, "+"+ref+":"+notesIncomingRef(deviceID)); err != nil {
			return "", err
		}
		if _, err := store.run(ctx, "merge-base", "--is-ancestor", remoteHead, local); err != nil {
			return "", fmt.Errorf("note remote head %s is not an ancestor of local head %s", remoteHead, local)
		}
	}
	if _, err := store.runNetwork(ctx, "push", remote, "HEAD:"+ref); err != nil {
		return "", err
	}
	confirmed, err := store.remoteHead(ctx, remote, ref)
	if err != nil {
		return "", err
	}
	if confirmed != local {
		return "", fmt.Errorf("note push verification failed: remote=%s local=%s", confirmed, local)
	}
	return local, nil
}

func (store *gitStore) listRemoteNotes(ctx context.Context, remote string) ([]remoteRef, []string, error) {
	output, err := store.runNetwork(ctx, "ls-remote", "--refs", remote, NotesRefPrefix+"*")
	if err != nil {
		return nil, nil, err
	}
	var refs []remoteRef
	var warnings []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			warnings = append(warnings, "ignored malformed note remote ref response")
			continue
		}
		if !strings.HasPrefix(fields[1], NotesRefPrefix) {
			warnings = append(warnings, fmt.Sprintf("ignored suffix-matching ref %s outside the note namespace", fields[1]))
			continue
		}
		deviceID := strings.TrimPrefix(fields[1], NotesRefPrefix)
		if !validDeviceID(deviceID) {
			warnings = append(warnings, fmt.Sprintf("ignored invalid note device ref %s", fields[1]))
			continue
		}
		if _, err := primitives.ParseCommitSHA(fields[0]); err != nil {
			warnings = append(warnings, fmt.Sprintf("ignored note device ref %s with invalid commit id", fields[1]))
			continue
		}
		refs = append(refs, remoteRef{DeviceID: deviceID, Ref: fields[1], Head: strings.ToLower(fields[0])})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].DeviceID < refs[j].DeviceID })
	return refs, warnings, nil
}

// fetchAndIngestNotes advances one publishing device by one bounded batch,
// applying the same tamper-evidence rules the turn-context channel uses: linear
// history, no rewinds, no merges, and no rewrite of an existing immutable path.
func (store *gitStore) fetchAndIngestNotes(ctx context.Context, remote string, ref remoteRef, lastSeen string, repoID primitives.RepoID) ([]StoredNoteBundle, string, error) {
	if ref.Head == lastSeen {
		return nil, lastSeen, nil
	}
	incoming := notesIncomingRef(ref.DeviceID)
	if _, err := store.runNetwork(ctx, "fetch", "--no-tags", remote, "+"+ref.Ref+":"+incoming); err != nil {
		return nil, lastSeen, retryablePullError{err: err}
	}
	if lastSeen != "" {
		if _, err := store.run(ctx, "merge-base", "--is-ancestor", lastSeen, ref.Head); err != nil {
			return nil, lastSeen, fmt.Errorf("note ref %s rewound from %s to %s", ref.Ref, lastSeen, ref.Head)
		}
	}
	rangeArg := ref.Head
	if lastSeen != "" {
		rangeArg = lastSeen + ".." + ref.Head
	}
	output, err := store.run(ctx, "rev-list", "--reverse", rangeArg)
	if err != nil {
		return nil, lastSeen, err
	}
	commits := strings.Fields(output)
	if len(commits) == 0 {
		return nil, lastSeen, fmt.Errorf("note ref %s has no commits after observed head %s", ref.Ref, lastSeen)
	}
	commits = commits[:1]

	var ingested []StoredNoteBundle
	previous := lastSeen
	for _, commit := range commits {
		treeOutput, err := store.run(ctx, "ls-tree", "-rz", "--name-only", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		if err := validateNotesTreePaths(splitNULPaths(treeOutput)); err != nil {
			return nil, lastSeen, fmt.Errorf("note commit %s: %w", commit, err)
		}
		parents, err := store.run(ctx, "show", "-s", "--format=%P", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		parentFields := strings.Fields(parents)
		if len(parentFields) > 1 {
			return nil, lastSeen, fmt.Errorf("note commit %s is a merge; device history must be linear", commit)
		}
		actualPrevious := ""
		if len(parentFields) > 0 {
			actualPrevious = parentFields[0]
		}
		if previous != "" && actualPrevious != previous {
			return nil, lastSeen, fmt.Errorf("note commit %s does not extend observed head %s", commit, previous)
		}
		if previous == "" && actualPrevious != "" {
			return nil, lastSeen, fmt.Errorf("note commit %s does not begin at the device history root", commit)
		}
		batchData, err := store.showFile(ctx, commit, "batch.json", 8<<20)
		if err != nil {
			return nil, lastSeen, err
		}
		var batch NoteBatch
		if err := decodeStrictJSON(batchData, &batch); err != nil {
			return nil, lastSeen, fmt.Errorf("parse note batch at %s: %w", commit, err)
		}
		public, err := verifyNoteBatch(batch)
		if err != nil {
			return nil, lastSeen, err
		}
		if batch.SchemaVersion != NotesSchemaVersion || len(batch.Bundles) == 0 || len(batch.Bundles) > MaxBundlesPerBatch {
			return nil, lastSeen, fmt.Errorf("note batch %s has an invalid schema or bundle count", commit)
		}
		if batch.DeviceID != ref.DeviceID || batch.PreviousHead != actualPrevious {
			return nil, lastSeen, fmt.Errorf("note batch %s has invalid device or previous head", commit)
		}
		changedOutput, err := store.run(ctx, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		if err := validateNoteBatchChanges(splitNULPaths(changedOutput), batch); err != nil {
			return nil, lastSeen, fmt.Errorf("note batch %s: %w", commit, err)
		}
		seen := make(map[primitives.BundleID]struct{}, len(batch.Bundles))
		for _, item := range batch.Bundles {
			if _, exists := seen[item.BundleID]; exists {
				return nil, lastSeen, fmt.Errorf("note batch %s repeats bundle %s", commit, item.BundleID)
			}
			seen[item.BundleID] = struct{}{}
			if item.Path != noteBundlePath(item.BundleID) {
				return nil, lastSeen, fmt.Errorf("note bundle %s has non-canonical path %s", item.BundleID, item.Path)
			}
			if actualPrevious != "" {
				for _, name := range []string{"manifest.json", "note.json"} {
					exists, err := store.fileExistsAtCommit(ctx, actualPrevious, item.Path+"/"+name)
					if err != nil {
						return nil, lastSeen, err
					}
					if exists {
						return nil, lastSeen, fmt.Errorf("note bundle %s rewrites an existing immutable path", item.BundleID)
					}
				}
			}
			manifestData, err := store.showFile(ctx, commit, item.Path+"/manifest.json", DefaultBundleLimit)
			if err != nil {
				return nil, lastSeen, err
			}
			noteData, err := store.showFile(ctx, commit, item.Path+"/note.json", DefaultBundleLimit)
			if err != nil {
				return nil, lastSeen, err
			}
			var manifest NoteManifest
			if err := decodeStrictJSON(manifestData, &manifest); err != nil {
				return nil, lastSeen, fmt.Errorf("parse note manifest %s: %w", item.BundleID, err)
			}
			if err := validateNoteManifest(repoID, ref.DeviceID, item, manifest); err != nil {
				return nil, lastSeen, fmt.Errorf("note bundle %s: %w", item.BundleID, err)
			}
			if err := verifyNoteManifest(public, manifest); err != nil {
				return nil, lastSeen, fmt.Errorf("note bundle %s: %w", item.BundleID, err)
			}
			if manifest.ContentHashes["note.json"] != sha256Bytes(noteData) {
				return nil, lastSeen, fmt.Errorf("note bundle %s content hash does not match its manifest", item.BundleID)
			}
			var projection NoteProjection
			if err := decodeStrictJSON(noteData, &projection); err != nil {
				return nil, lastSeen, fmt.Errorf("parse note projection %s: %w", item.BundleID, err)
			}
			if err := validateNoteProjection(manifest, projection); err != nil {
				return nil, lastSeen, fmt.Errorf("note bundle %s: %w", item.BundleID, err)
			}
			stored := StoredNoteBundle{Manifest: manifest, Note: projection, PublicKey: batch.PublicKey}
			if err := materializePulledNote(store.repo, stored); err != nil {
				return nil, lastSeen, err
			}
			ingested = append(ingested, stored)
		}
		previous = commit
	}
	return ingested, previous, nil
}

func materializePulledNote(repo *checkpoint.Repo, bundle StoredNoteBundle) error {
	path := notesPulledPath(repo, bundle.Manifest.RepoID, bundle.Manifest.DeviceID, bundle.Manifest.BundleID)
	if existingData, err := readRegularFile(path, MaxMaterializedLimit); err == nil {
		var existing StoredNoteBundle
		if err := decodeStrictJSON(existingData, &existing); err != nil {
			return fmt.Errorf("parse existing note bundle %s: %w", bundle.Manifest.BundleID, err)
		}
		existingCanonical, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		incomingCanonical, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		if string(existingCanonical) != string(incomingCanonical) {
			return fmt.Errorf("note bundle %s changed after it was observed", bundle.Manifest.BundleID)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}
