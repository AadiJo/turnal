package sharedhistory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

type gitStore struct {
	repo *checkpoint.Repo
	root string
}

type remoteRef struct {
	DeviceID string
	Ref      string
	Head     string
}

func openGitStore(ctx context.Context, repo *checkpoint.Repo) (*gitStore, error) {
	root := filepath.Join(sharedRoot(repo), "repository")
	store := &gitStore{repo: repo, root: root}
	gitDir := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect shared history Git repository: %w", err)
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("shared history Git metadata is not a regular directory: %s", gitDir)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	// Reinitialization is idempotent and repairs a crash that left a partial
	// .git directory before HEAD, objects, or configuration were installed.
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

func (store *gitStore) localHead(ctx context.Context) (string, error) {
	output, err := store.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		if isGitExitCode(err, 1) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (store *gitStore) commitBatch(ctx context.Context, batch Batch, bundles []builtBundle) (string, error) {
	if len(bundles) == 0 {
		return store.localHead(ctx)
	}
	batchBytes := 0
	for _, bundle := range bundles {
		batchBytes += len(bundle.Manifest) + len(bundle.EventsJSON)
	}
	if len(bundles) > MaxBundlesPerBatch || batchBytes > MaxBatchBytes {
		return "", fmt.Errorf("shared history batch exceeds %d bundles or %d bytes", MaxBundlesPerBatch, MaxBatchBytes)
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
		manifestPath := filepath.Join(dir, "manifest.json")
		eventsPath := filepath.Join(dir, "events.jsonl")
		manifestTracked, err := store.fileExistsAtCommit(ctx, head, bundle.Path+"/manifest.json")
		if err != nil {
			return "", err
		}
		eventsTracked, err := store.fileExistsAtCommit(ctx, head, bundle.Path+"/events.jsonl")
		if err != nil {
			return "", err
		}
		if err := ensureImmutableFile(manifestPath, bundle.Manifest, !manifestTracked); err != nil {
			return "", err
		}
		if err := ensureImmutableFile(eventsPath, bundle.EventsJSON, !eventsTracked); err != nil {
			return "", err
		}
	}
	if err := writeJSONAtomic(filepath.Join(store.root, "batch.json"), batch, 0o600); err != nil {
		return "", err
	}
	addArgs := []string{"add", "--", "batch.json"}
	for _, bundle := range bundles {
		addArgs = append(addArgs, bundle.Path+"/manifest.json", bundle.Path+"/events.jsonl")
	}
	if _, err := store.run(ctx, addArgs...); err != nil {
		return "", err
	}
	tracked, err := store.run(ctx, "ls-files", "-z")
	if err != nil {
		return "", err
	}
	if err := validateSharedTreePaths(splitNULPaths(tracked)); err != nil {
		return "", err
	}
	message := fmt.Sprintf("turnal shared history: %d bundle", len(bundles))
	if len(bundles) != 1 {
		message += "s"
	}
	if _, err := store.run(ctx, "commit", "--no-verify", "-m", message); err != nil {
		return "", err
	}
	return store.localHead(ctx)
}

func ensureImmutableFile(path string, data []byte, replaceUntracked bool) error {
	if replaceUntracked {
		return writeFileAtomic(path, data, 0o600)
	}
	existing, err := readRegularFile(path, DefaultBundleLimit)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("shared history immutable file changed: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

func historyRef(deviceID string) string {
	return "refs/turnal/v1/history/" + deviceID
}

func incomingRef(deviceID string) string {
	return "refs/turnal/v1/incoming/" + deviceID + "/tip"
}

func trackingRefPrefix(remote string, repoID primitives.RepoID) string {
	digest := strings.TrimPrefix(sha256Bytes([]byte(publicRemoteIdentity(remote)+"\x00"+repoID.String())), "sha256:")
	return "refs/turnal/v1/tracking/" + digest[:32] + "/"
}

func trackingRef(remote string, repoID primitives.RepoID, deviceID string) string {
	return trackingRefPrefix(remote, repoID) + deviceID
}

func (store *gitStore) recoverTrackingState(ctx context.Context, remote string, repoID primitives.RepoID, state *stateFile) (bool, error) {
	prefix := trackingRefPrefix(remote, repoID)
	output, err := store.run(ctx, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return false, err
	}
	changed := false
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], prefix) {
			return false, fmt.Errorf("shared history tracking ref is malformed")
		}
		deviceID := strings.TrimPrefix(fields[1], prefix)
		if !validDeviceID(deviceID) {
			return false, fmt.Errorf("shared history tracking ref has invalid device id")
		}
		head, err := primitives.ParseCommitSHA(fields[0])
		if err != nil {
			return false, err
		}
		tracked := head.String()
		observed := state.LastSeen[deviceID]
		if observed == tracked {
			continue
		}
		if observed != "" {
			if _, err := store.run(ctx, "merge-base", "--is-ancestor", observed, tracked); err != nil {
				return false, fmt.Errorf("shared history tracking ref for device %s does not extend remembered head %s", deviceID, observed)
			}
		}
		state.LastSeen[deviceID] = tracked
		changed = true
	}
	return changed, nil
}

func (store *gitStore) remoteHead(ctx context.Context, remote, ref string) (string, error) {
	output, err := store.run(ctx, "ls-remote", "--refs", remote, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != ref {
		return "", fmt.Errorf("unexpected shared history ls-remote response")
	}
	if _, err := primitives.ParseCommitSHA(fields[0]); err != nil {
		return "", err
	}
	return strings.ToLower(fields[0]), nil
}

func (store *gitStore) push(ctx context.Context, remote, deviceID, lastSeen string) (string, error) {
	local, err := store.localHead(ctx)
	if err != nil || local == "" {
		return local, err
	}
	ref := historyRef(deviceID)
	remoteHead, err := store.remoteHead(ctx, remote, ref)
	if err != nil {
		return "", err
	}
	if remoteHead == "" && lastSeen != "" {
		return "", fmt.Errorf("shared history remote ref disappeared after head %s was observed", lastSeen)
	}
	// A previous attempt may have completed the network push and crashed before
	// promoting the durable outbox in state.json. The remote already matching the
	// local tip is the idempotent success case, not a ref rewrite.
	if remoteHead == local {
		return local, nil
	}
	if remoteHead != "" && lastSeen != "" && remoteHead != lastSeen {
		return "", fmt.Errorf("shared history remote ref rewound or was replaced after head %s was observed; current head is %s", lastSeen, remoteHead)
	}
	if remoteHead != "" && remoteHead != local {
		incoming := incomingRef(deviceID)
		if _, err := store.run(ctx, "fetch", "--no-tags", remote, "+"+ref+":"+incoming); err != nil {
			return "", err
		}
		if _, err := store.run(ctx, "merge-base", "--is-ancestor", remoteHead, local); err != nil {
			return "", fmt.Errorf("shared history remote head %s is not an ancestor of local head %s", remoteHead, local)
		}
	}
	if remoteHead != local {
		if _, err := store.run(ctx, "push", remote, "HEAD:"+ref); err != nil {
			return "", err
		}
	}
	confirmed, err := store.remoteHead(ctx, remote, ref)
	if err != nil {
		return "", err
	}
	if confirmed != local {
		return "", fmt.Errorf("shared history push verification failed: remote=%s local=%s", confirmed, local)
	}
	return local, nil
}

func (store *gitStore) listRemoteHistory(ctx context.Context, remote string) ([]remoteRef, error) {
	prefix := "refs/turnal/v1/history/"
	output, err := store.run(ctx, "ls-remote", "--refs", remote, prefix+"*")
	if err != nil {
		return nil, err
	}
	var refs []remoteRef
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], prefix) {
			return nil, fmt.Errorf("unexpected shared history remote ref response")
		}
		deviceID := strings.TrimPrefix(fields[1], prefix)
		if !validDeviceID(deviceID) {
			return nil, fmt.Errorf("invalid shared history device ref %q", fields[1])
		}
		if _, err := primitives.ParseCommitSHA(fields[0]); err != nil {
			return nil, err
		}
		refs = append(refs, remoteRef{DeviceID: deviceID, Ref: fields[1], Head: strings.ToLower(fields[0])})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].DeviceID < refs[j].DeviceID })
	return refs, nil
}

func (store *gitStore) fetchAndIngest(ctx context.Context, remote string, remoteRef remoteRef, lastSeen string, repoID primitives.RepoID) ([]StoredBundle, string, error) {
	if remoteRef.Head == lastSeen {
		return nil, lastSeen, nil
	}
	incoming := incomingRef(remoteRef.DeviceID)
	if _, err := store.run(ctx, "fetch", "--no-tags", remote, "+"+remoteRef.Ref+":"+incoming); err != nil {
		return nil, lastSeen, err
	}
	if lastSeen != "" {
		if _, err := store.run(ctx, "merge-base", "--is-ancestor", lastSeen, remoteRef.Head); err != nil {
			return nil, lastSeen, fmt.Errorf("shared history ref %s rewound from %s to %s", remoteRef.Ref, lastSeen, remoteRef.Head)
		}
	}
	rangeArg := remoteRef.Head
	if lastSeen != "" {
		rangeArg = lastSeen + ".." + remoteRef.Head
	}
	output, err := store.run(ctx, "rev-list", "--reverse", rangeArg)
	if err != nil {
		return nil, lastSeen, err
	}
	commits := strings.Fields(output)
	if len(commits) == 0 {
		return nil, lastSeen, fmt.Errorf("shared history ref %s has no commits after observed head %s", remoteRef.Ref, lastSeen)
	}
	// A pull advances each device by one bounded publication batch. This keeps
	// validation memory bounded and leaves the tracking ref as a durable cursor.
	if len(commits) > 1 {
		commits = commits[:1]
	}
	var ingested []StoredBundle
	batchBytes := 0
	previous := lastSeen
	for _, commit := range commits {
		treeOutput, err := store.run(ctx, "ls-tree", "-rz", "--name-only", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		if err := validateSharedTreePaths(splitNULPaths(treeOutput)); err != nil {
			return nil, lastSeen, fmt.Errorf("shared history commit %s: %w", commit, err)
		}
		parents, err := store.run(ctx, "show", "-s", "--format=%P", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		parentFields := strings.Fields(parents)
		if len(parentFields) > 1 {
			return nil, lastSeen, fmt.Errorf("shared history commit %s is a merge; device history must be linear", commit)
		}
		actualPrevious := ""
		if len(parentFields) > 0 {
			actualPrevious = parentFields[0]
		}
		if previous != "" && actualPrevious != previous {
			return nil, lastSeen, fmt.Errorf("shared history commit %s does not extend observed head %s", commit, previous)
		}
		if previous == "" && actualPrevious != "" {
			return nil, lastSeen, fmt.Errorf("shared history commit %s does not begin at the device history root", commit)
		}
		batchData, err := store.showFile(ctx, commit, "batch.json", 8<<20)
		if err != nil {
			return nil, lastSeen, err
		}
		var batch Batch
		if err := decodeStrictJSON(batchData, &batch); err != nil {
			return nil, lastSeen, fmt.Errorf("parse shared history batch at %s: %w", commit, err)
		}
		public, err := verifyBatch(batch)
		if err != nil {
			return nil, lastSeen, err
		}
		if batch.SchemaVersion != SchemaVersion || len(batch.Bundles) == 0 || len(batch.Bundles) > MaxBundlesPerBatch {
			return nil, lastSeen, fmt.Errorf("shared history batch %s has an invalid schema or bundle count", commit)
		}
		if batch.DeviceID != remoteRef.DeviceID || batch.PreviousHead != actualPrevious {
			return nil, lastSeen, fmt.Errorf("shared history batch %s has invalid device or previous head", commit)
		}
		changedOutput, err := store.run(ctx, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit)
		if err != nil {
			return nil, lastSeen, err
		}
		if err := validateBatchChanges(splitNULPaths(changedOutput), batch); err != nil {
			return nil, lastSeen, fmt.Errorf("shared history batch %s: %w", commit, err)
		}
		seenBundles := make(map[primitives.BundleID]struct{}, len(batch.Bundles))
		for _, item := range batch.Bundles {
			if _, exists := seenBundles[item.BundleID]; exists {
				return nil, lastSeen, fmt.Errorf("shared history batch %s repeats bundle %s", commit, item.BundleID)
			}
			seenBundles[item.BundleID] = struct{}{}
			if item.Path != bundlePath(item.BundleID) {
				return nil, lastSeen, fmt.Errorf("shared history bundle %s has non-canonical path %s", item.BundleID, item.Path)
			}
			if actualPrevious != "" {
				for _, name := range []string{"manifest.json", "events.jsonl"} {
					exists, err := store.fileExistsAtCommit(ctx, actualPrevious, item.Path+"/"+name)
					if err != nil {
						return nil, lastSeen, err
					}
					if exists {
						return nil, lastSeen, fmt.Errorf("shared history bundle %s rewrites an existing immutable path", item.BundleID)
					}
				}
			}
			manifestData, err := store.showFile(ctx, commit, item.Path+"/manifest.json", DefaultBundleLimit)
			if err != nil {
				return nil, lastSeen, err
			}
			eventsData, err := store.showFile(ctx, commit, item.Path+"/events.jsonl", DefaultBundleLimit)
			if err != nil {
				return nil, lastSeen, err
			}
			var manifest Manifest
			if err := decodeStrictJSON(manifestData, &manifest); err != nil {
				return nil, lastSeen, fmt.Errorf("parse shared history manifest %s: %w", item.BundleID, err)
			}
			if err := validateManifest(repoID, remoteRef.DeviceID, item, manifest); err != nil {
				return nil, lastSeen, fmt.Errorf("validate shared history manifest %s: %w", item.BundleID, err)
			}
			if len(manifestData)+len(eventsData) > DefaultBundleLimit {
				return nil, lastSeen, fmt.Errorf("shared history bundle %s exceeds %d bytes", item.BundleID, DefaultBundleLimit)
			}
			batchBytes += len(manifestData) + len(eventsData)
			if batchBytes > MaxBatchBytes {
				return nil, lastSeen, fmt.Errorf("shared history batch %s exceeds %d bytes", commit, MaxBatchBytes)
			}
			if manifest.ContentHashes["events.jsonl"] != sha256Bytes(eventsData) {
				return nil, lastSeen, fmt.Errorf("shared history event content hash mismatch for %s", item.BundleID)
			}
			if err := verifyManifest(public, manifest); err != nil {
				return nil, lastSeen, err
			}
			events, err := decodeEventsJSONL(eventsData)
			if err != nil {
				return nil, lastSeen, fmt.Errorf("decode shared history bundle %s: %w", item.BundleID, err)
			}
			if err := validateProjectedEvents(manifest, events); err != nil {
				return nil, lastSeen, fmt.Errorf("validate shared history bundle %s events: %w", item.BundleID, err)
			}
			stored := StoredBundle{Manifest: manifest, Events: events, PublicKey: batch.PublicKey}
			ingested = append(ingested, stored)
		}
		previous = commit
	}
	for _, bundle := range ingested {
		if err := materializePulled(store.repo, bundle); err != nil {
			return nil, lastSeen, err
		}
	}
	observedHead := lastSeen
	if len(commits) > 0 {
		observedHead = commits[len(commits)-1]
	}
	oldTracking, err := store.refValue(ctx, trackingRef(remote, repoID, remoteRef.DeviceID))
	if err != nil {
		return nil, lastSeen, err
	}
	args := []string{"update-ref", trackingRef(remote, repoID, remoteRef.DeviceID), observedHead}
	if oldTracking != "" {
		args = append(args, oldTracking)
	}
	if _, err := store.run(ctx, args...); err != nil {
		return nil, lastSeen, err
	}
	return ingested, observedHead, nil
}

// recoverCommittedState reconstructs the outbox after a crash between the Git
// commit and state.json update. A normal sync creates at most one unacknowledged
// batch, so the current local tip is sufficient recovery evidence.
func (store *gitStore) recoverCommittedState(ctx context.Context, identity deviceIdentity, state *stateFile) error {
	if len(state.Committed) > 0 {
		return nil
	}
	head, err := store.localHead(ctx)
	if err != nil || head == "" || state.LastSeen[identity.DeviceID] == head {
		return err
	}
	data, err := store.showFile(ctx, head, "batch.json", 8<<20)
	if err != nil {
		return fmt.Errorf("recover shared history outbox: %w", err)
	}
	var batch Batch
	if err := decodeStrictJSON(data, &batch); err != nil {
		return fmt.Errorf("recover shared history outbox batch: %w", err)
	}
	if _, err := verifyBatch(batch); err != nil {
		return fmt.Errorf("recover shared history outbox: %w", err)
	}
	if batch.SchemaVersion != SchemaVersion || batch.DeviceID != identity.DeviceID || batch.PublicKey != identity.PublicKey || len(batch.Bundles) == 0 || len(batch.Bundles) > MaxBundlesPerBatch {
		return fmt.Errorf("recover shared history outbox: local tip has an invalid batch")
	}
	parents, err := store.run(ctx, "show", "-s", "--format=%P", head)
	if err != nil {
		return err
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) > 1 {
		return fmt.Errorf("recover shared history outbox: local tip is a merge")
	}
	actualPrevious := ""
	if len(parentFields) == 1 {
		actualPrevious = parentFields[0]
	}
	if batch.PreviousHead != actualPrevious {
		return fmt.Errorf("recover shared history outbox: batch does not extend its Git parent")
	}
	for _, item := range batch.Bundles {
		if _, published := state.Published[item.BundleID.String()]; !published {
			state.Committed[item.BundleID.String()] = head
		}
	}
	return nil
}

func (store *gitStore) validateCommittedPolicy(ctx context.Context, state stateFile, policyHash string) error {
	if len(state.Committed) == 0 {
		return nil
	}
	head := ""
	for bundleID, committedHead := range state.Committed {
		if head != "" && committedHead != head {
			return fmt.Errorf("shared history outbox contains bundles from multiple heads")
		}
		head = committedHead
		if strings.TrimSpace(bundleID) == "" || strings.TrimSpace(committedHead) == "" {
			return fmt.Errorf("shared history outbox contains an invalid committed entry")
		}
	}
	data, err := store.showFile(ctx, head, "batch.json", 8<<20)
	if err != nil {
		return fmt.Errorf("validate shared history outbox policy: %w", err)
	}
	var batch Batch
	if err := decodeStrictJSON(data, &batch); err != nil {
		return fmt.Errorf("validate shared history outbox policy: %w", err)
	}
	for _, item := range batch.Bundles {
		if _, committed := state.Committed[item.BundleID.String()]; !committed {
			continue
		}
		manifestData, err := store.showFile(ctx, head, item.Path+"/manifest.json", DefaultBundleLimit)
		if err != nil {
			return fmt.Errorf("validate shared history outbox policy: %w", err)
		}
		var manifest Manifest
		if err := decodeStrictJSON(manifestData, &manifest); err != nil {
			return fmt.Errorf("validate shared history outbox policy: %w", err)
		}
		if manifest.PolicyHash != policyHash {
			return fmt.Errorf("shared history outbox bundle %s was approved under policy %s, not current policy %s; restore the prior configuration and publish it before changing policy", item.BundleID, manifest.PolicyHash, policyHash)
		}
	}
	return nil
}

func (store *gitStore) refValue(ctx context.Context, ref string) (string, error) {
	output, err := store.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if isGitExitCode(err, 1) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (store *gitStore) fileExistsAtCommit(ctx context.Context, commit, path string) (bool, error) {
	_, err := store.run(ctx, "cat-file", "-e", commit+":"+path)
	if err == nil {
		return true, nil
	}
	if isGitExitCode(err, 128) {
		return false, nil
	}
	return false, err
}

func (store *gitStore) showFile(ctx context.Context, commit, path string, limit int64) ([]byte, error) {
	data, err := store.runBytesLimit(ctx, limit, "show", commit+":"+path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func materializePulled(repo *checkpoint.Repo, bundle StoredBundle) error {
	path := pulledBundlePath(repo, bundle.Manifest.RepoID, bundle.Manifest.DeviceID, bundle.Manifest.BundleID)
	if existingData, err := readRegularFile(path, MaxMaterializedLimit); err == nil {
		var existing StoredBundle
		if err := decodeStrictJSON(existingData, &existing); err != nil {
			return fmt.Errorf("parse existing shared history bundle %s: %w", bundle.Manifest.BundleID, err)
		}
		existingCanonical, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		incomingCanonical, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		if !bytes.Equal(existingCanonical, incomingCanonical) {
			return fmt.Errorf("shared history bundle %s changed after it was observed", bundle.Manifest.BundleID)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(bundle); err != nil {
		return err
	}
	if encoded.Len() > MaxMaterializedLimit {
		return fmt.Errorf("shared history materialized bundle %s exceeds %d bytes", bundle.Manifest.BundleID, MaxMaterializedLimit)
	}
	return writeFileAtomic(path, encoded.Bytes(), 0o600)
}

func pulledBundlePath(repo *checkpoint.Repo, repoID primitives.RepoID, deviceID string, bundleID primitives.BundleID) string {
	return filepath.Join(sharedRoot(repo), "pulled", repoID.String(), deviceID, bundleID.String()+".json")
}

func validateManifest(repoID primitives.RepoID, deviceID string, item BatchBundle, manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion || manifest.EvidenceClass != EvidencePublisherClaim {
		return fmt.Errorf("unsupported schema or evidence class")
	}
	if err := validatePromptMode(manifest.PromptMode); err != nil {
		return err
	}
	if manifest.BundleID != item.BundleID || manifest.DeviceID != deviceID || item.RepoID != repoID || manifest.RepoID != item.RepoID {
		return fmt.Errorf("manifest identity does not match its batch and repository")
	}
	if manifest.SessionID != item.SessionID || manifest.TurnID != item.TurnID || manifest.SourceSequence != item.Sequence {
		return fmt.Errorf("manifest turn identity does not match its batch")
	}
	wantBundleID, err := primitives.DeriveBundleID(manifest.RepoID, manifest.StreamID, manifest.TurnID)
	if err != nil || wantBundleID != manifest.BundleID {
		return fmt.Errorf("bundle id is not derived from its repository, stream, and turn")
	}
	if len(manifest.SourceRefs) == 0 || manifest.SourceSequence.First != manifest.SourceRefs[0].Seq || manifest.SourceSequence.Last != manifest.SourceRefs[len(manifest.SourceRefs)-1].Seq {
		return fmt.Errorf("source sequence does not match source references")
	}
	previous := primitives.EventSeq(0)
	for _, ref := range manifest.SourceRefs {
		if ref.StreamID != manifest.StreamID || ref.Seq <= previous || ref.Hash == "" {
			return fmt.Errorf("source references are not one strictly ordered stream")
		}
		previous = ref.Seq
	}
	if len(manifest.ContentHashes) != 1 || manifest.ContentHashes["events.jsonl"] == "" {
		return fmt.Errorf("manifest must hash exactly events.jsonl")
	}
	if !validSHA256(manifest.PolicyHash) || !validSHA256(manifest.ContentHashes["events.jsonl"]) {
		return fmt.Errorf("manifest contains an invalid SHA-256 digest")
	}
	if manifest.CreatedAt.IsZero() || manifest.Truncations.Count < 0 || manifest.Truncations.OriginalBytes < 0 {
		return fmt.Errorf("manifest contains invalid creation or truncation metadata")
	}
	for reason, count := range manifest.Omissions {
		if strings.TrimSpace(reason) == "" || len(reason) > 256 || count <= 0 {
			return fmt.Errorf("manifest contains invalid omission metadata")
		}
	}
	for _, link := range manifest.SourceLinks {
		if link.CommitSHA == "" && link.Checkpoint == "" {
			return fmt.Errorf("manifest contains an empty source link")
		}
		if link.CommitSHA != "" {
			if _, err := primitives.ParseCommitSHA(link.CommitSHA); err != nil {
				return fmt.Errorf("manifest contains an invalid source commit")
			}
		}
		if link.Checkpoint != "" {
			if _, err := primitives.ParseCheckpointID(link.Checkpoint); err != nil {
				return fmt.Errorf("manifest contains an invalid checkpoint link")
			}
		}
	}
	return nil
}

func validateProjectedEvents(manifest Manifest, events []ContextEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("bundle has no projected events")
	}
	refs := make(map[primitives.EventSeq]SourceRef, len(manifest.SourceRefs))
	for _, ref := range manifest.SourceRefs {
		refs[ref.Seq] = ref
	}
	previous := primitives.EventSeq(0)
	for _, event := range events {
		if err := validateContextEvent(event); err != nil {
			return err
		}
		if event.Prompt != nil && event.Prompt.Omitted != (manifest.PromptMode == PromptModeOmit) {
			return fmt.Errorf("prompt projection does not match manifest prompt mode")
		}
		ref, exists := refs[event.Seq]
		if !exists || ref != event.Source {
			return fmt.Errorf("event %s is not bound to its manifest source reference", event.Seq)
		}
		if event.Seq <= previous {
			return fmt.Errorf("projected events are not strictly ordered")
		}
		previous = event.Seq
	}
	return nil
}

func validateSharedTreePaths(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("shared history tree is empty")
	}
	for _, path := range paths {
		if path == "batch.json" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		if len(parts) != 4 || parts[0] != "bundles" || (parts[3] != "manifest.json" && parts[3] != "events.jsonl") {
			return fmt.Errorf("shared history tree contains forbidden path %q", path)
		}
		bundleID, err := primitives.ParseBundleID(parts[2])
		if err != nil || filepath.ToSlash(filepath.Join(parts[0], parts[1], parts[2])) != bundlePath(bundleID) {
			return fmt.Errorf("shared history tree contains non-canonical bundle path %q", path)
		}
	}
	return nil
}

func validateBatchChanges(paths []string, batch Batch) error {
	expected := map[string]struct{}{"batch.json": {}}
	for _, item := range batch.Bundles {
		expected[item.Path+"/manifest.json"] = struct{}{}
		expected[item.Path+"/events.jsonl"] = struct{}{}
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

func splitNULPaths(value string) []string {
	value = strings.TrimSuffix(value, "\x00")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\x00")
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func decodeEventsJSONL(data []byte) ([]ContextEvent, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), DefaultBundleLimit)
	var events []ContextEvent
	for scanner.Scan() {
		var event ContextEvent
		if err := decodeStrictJSON(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		if err := validateContextEvent(event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func validateContextEvent(event ContextEvent) error {
	if event.SchemaVersion != SchemaVersion || !event.Type.Valid() || event.Seq == 0 || event.Source.StreamID == "" || event.Source.Seq != event.Seq || event.Source.Hash == "" {
		return fmt.Errorf("invalid shared history context event identity")
	}
	payloads := 0
	for _, exists := range []bool{event.Lifecycle != nil, event.Prompt != nil, event.Intent != nil, event.Assistant != nil, event.Tool != nil, event.Checkpoint != nil, event.CaptureError != nil} {
		if exists {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("shared history context event must contain exactly one typed payload")
	}
	matchingPayload := false
	switch event.Type {
	case primitives.EventTypeTurnStart, primitives.EventTypeTurnFinish:
		matchingPayload = event.Lifecycle != nil
	case primitives.EventTypePromptUser:
		matchingPayload = event.Prompt != nil
	case primitives.EventTypeAgentIntent:
		matchingPayload = event.Intent != nil
	case primitives.EventTypeAssistantMessage:
		matchingPayload = event.Assistant != nil
	case primitives.EventTypeToolCall, primitives.EventTypeToolResult:
		matchingPayload = event.Tool != nil
	case primitives.EventTypeCheckpoint:
		matchingPayload = event.Checkpoint != nil
	case primitives.EventTypeError:
		matchingPayload = event.CaptureError != nil
	}
	if !matchingPayload {
		return fmt.Errorf("shared history event type %s does not match its typed payload", event.Type)
	}
	return validateContextPayload(event)
}

func validateContextPayload(event ContextEvent) error {
	switch event.Type {
	case primitives.EventTypeTurnStart:
		if event.Lifecycle.State != "started" {
			return fmt.Errorf("invalid turn-start lifecycle projection")
		}
	case primitives.EventTypeTurnFinish:
		if event.Lifecycle.State != "finished" {
			return fmt.Errorf("invalid turn-finish lifecycle projection")
		}
	case primitives.EventTypePromptUser:
		if event.Prompt.Omitted {
			if event.Prompt.Text != "" || event.Prompt.Truncated || event.Prompt.Bytes != 0 {
				return fmt.Errorf("omitted prompt projection contains text metadata")
			}
		} else if err := validateProjectedText(event.Prompt.Text, event.Prompt.Truncated, event.Prompt.Bytes); err != nil {
			return fmt.Errorf("invalid prompt projection: %w", err)
		}
	case primitives.EventTypeAgentIntent:
		if err := validateTextProjection(event.Intent.Problem); err != nil {
			return fmt.Errorf("invalid intent problem: %w", err)
		}
		if len(event.Intent.AgentType) > 256 || containsControl(event.Intent.AgentType) {
			return fmt.Errorf("invalid intent agent type")
		}
		for _, values := range [][]string{event.Intent.Scope, event.Intent.Evidence} {
			for _, value := range values {
				if len(value) > DefaultFieldLimit {
					return fmt.Errorf("intent list field exceeds %d bytes", DefaultFieldLimit)
				}
			}
		}
	case primitives.EventTypeAssistantMessage:
		if err := validateTextProjection(*event.Assistant); err != nil {
			return fmt.Errorf("invalid assistant projection: %w", err)
		}
	case primitives.EventTypeToolCall, primitives.EventTypeToolResult:
		if event.Tool.Name == "" || len(event.Tool.Name) > 256 || containsControl(event.Tool.Name) {
			return fmt.Errorf("invalid tool name projection")
		}
		validCategory := event.Tool.Category == "mutation" || event.Tool.Category == "search" || event.Tool.Category == "read" || event.Tool.Category == "command" || event.Tool.Category == "other"
		if !validCategory {
			return fmt.Errorf("invalid tool category projection")
		}
		if event.Type == primitives.EventTypeToolCall && event.Tool.Status != "started" {
			return fmt.Errorf("invalid tool-call status projection")
		}
		if event.Type == primitives.EventTypeToolResult && (event.Tool.Status != "completed" || event.Tool.MutationCandidate) {
			return fmt.Errorf("invalid tool-result status projection")
		}
	case primitives.EventTypeCheckpoint:
		if _, err := primitives.ParseCheckpointPhase(event.Checkpoint.Phase); err != nil {
			return fmt.Errorf("invalid checkpoint phase projection")
		}
		if _, err := primitives.ParseCheckpointID(event.Checkpoint.CheckpointID); err != nil {
			return fmt.Errorf("invalid checkpoint id projection")
		}
		if event.Checkpoint.SourceCommit != "" {
			if _, err := primitives.ParseCommitSHA(event.Checkpoint.SourceCommit); err != nil {
				return fmt.Errorf("invalid checkpoint source commit projection")
			}
		}
	case primitives.EventTypeError:
		if event.CaptureError.Kind != "capture_error" {
			return fmt.Errorf("invalid capture-error projection")
		}
	}
	return nil
}

func validateTextProjection(text TextProjection) error {
	return validateProjectedText(text.Text, text.Truncated, text.Bytes)
}

func validateProjectedText(text string, truncated bool, originalBytes int) error {
	if len(text) > DefaultFieldLimit || originalBytes < 0 {
		return fmt.Errorf("text field exceeds limits")
	}
	if truncated && originalBytes <= len(text) {
		return fmt.Errorf("truncation metadata is inconsistent")
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func (store *gitStore) run(ctx context.Context, args ...string) (string, error) {
	data, err := store.runBytes(ctx, args...)
	return string(data), err
}

func (store *gitStore) runBytes(ctx context.Context, args ...string) ([]byte, error) {
	return store.runBytesLimit(ctx, 16<<20, args...)
}

func (store *gitStore) runBytesLimit(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", store.root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = scrubGitEnv(os.Environ())
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		return nil, gitCommandError{args: args, output: strings.TrimSpace(redactGitOutput(stderr.String(), args)), err: err}
	}
	if stdout.overflow {
		return nil, fmt.Errorf("git %s output exceeds %d bytes", formatGitArgs(args), limit)
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	data     bytes.Buffer
	limit    int64
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - int64(buffer.data.Len())
	if remaining > 0 {
		keep := int64(len(value))
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.data.Write(value[:keep])
	}
	if int64(written) > remaining {
		buffer.overflow = true
	}
	return written, nil
}

func (buffer *limitedBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *limitedBuffer) String() string { return buffer.data.String() }

type gitCommandError struct {
	args   []string
	output string
	err    error
}

func (err gitCommandError) Error() string {
	if err.output == "" {
		return fmt.Sprintf("git %s: %v", formatGitArgs(err.args), err.err)
	}
	return fmt.Sprintf("git %s: %s", formatGitArgs(err.args), err.output)
}

func (err gitCommandError) Unwrap() error { return err.err }

func formatGitArgs(args []string) string {
	redacted := make([]string, len(args))
	for index, arg := range args {
		redacted[index] = redactRemote(arg)
	}
	return strings.Join(redacted, " ")
}

func redactGitOutput(output string, args []string) string {
	for _, arg := range args {
		redacted := redactRemote(arg)
		if redacted != arg {
			output = strings.ReplaceAll(output, arg, redacted)
		}
	}
	return output
}

func isGitExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

func scrubGitEnv(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		name, _, found := strings.Cut(value, "=")
		if found && !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			result = append(result, value)
		}
	}
	return result
}

func decodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			canonicalKey := strings.ToLower(key)
			if key != canonicalKey {
				return fmt.Errorf("non-canonical JSON field %q", key)
			}
			if _, duplicate := seen[canonicalKey]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[canonicalKey] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func validDeviceID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
