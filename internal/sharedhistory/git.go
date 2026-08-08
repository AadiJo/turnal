package sharedhistory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return store, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect shared history Git repository: %w", err)
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

func (store *gitStore) localHead(ctx context.Context) (string, error) {
	output, err := store.run(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if isGitExit(err) {
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
	for _, bundle := range bundles {
		dir := filepath.Join(store.root, filepath.FromSlash(bundle.Path))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		manifestPath := filepath.Join(dir, "manifest.json")
		eventsPath := filepath.Join(dir, "events.jsonl")
		if err := ensureImmutableFile(manifestPath, bundle.Manifest); err != nil {
			return "", err
		}
		if err := ensureImmutableFile(eventsPath, bundle.EventsJSON); err != nil {
			return "", err
		}
	}
	if err := writeJSONAtomic(filepath.Join(store.root, "batch.json"), batch, 0o600); err != nil {
		return "", err
	}
	if _, err := store.run(ctx, "add", "--", "batch.json", "bundles"); err != nil {
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

func ensureImmutableFile(path string, data []byte) error {
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

func trackingRef(deviceID string) string {
	return "refs/turnal/v1/tracking/" + deviceID
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
	if remoteHead != "" && remoteHead != local {
		incoming := fmt.Sprintf("refs/turnal/v1/incoming/%s/%s", deviceID, remoteHead)
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

func (store *gitStore) fetchAndIngest(ctx context.Context, remote string, remoteRef remoteRef, lastSeen string) ([]StoredBundle, error) {
	if remoteRef.Head == lastSeen {
		return nil, nil
	}
	incoming := fmt.Sprintf("refs/turnal/v1/incoming/%s/%s", remoteRef.DeviceID, remoteRef.Head)
	if _, err := store.run(ctx, "fetch", "--no-tags", remote, "+"+remoteRef.Ref+":"+incoming); err != nil {
		return nil, err
	}
	if lastSeen != "" {
		if _, err := store.run(ctx, "merge-base", "--is-ancestor", lastSeen, remoteRef.Head); err != nil {
			return nil, fmt.Errorf("shared history ref %s rewound from %s to %s", remoteRef.Ref, lastSeen, remoteRef.Head)
		}
	}
	rangeArg := remoteRef.Head
	if lastSeen != "" {
		rangeArg = lastSeen + ".." + remoteRef.Head
	}
	output, err := store.run(ctx, "rev-list", "--reverse", rangeArg)
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(output)
	var ingested []StoredBundle
	previous := lastSeen
	for _, commit := range commits {
		parents, err := store.run(ctx, "show", "-s", "--format=%P", commit)
		if err != nil {
			return nil, err
		}
		parentFields := strings.Fields(parents)
		if len(parentFields) > 1 {
			return nil, fmt.Errorf("shared history commit %s is a merge; device history must be linear", commit)
		}
		actualPrevious := ""
		if len(parentFields) > 0 {
			actualPrevious = parentFields[0]
		}
		if previous != "" && actualPrevious != previous {
			return nil, fmt.Errorf("shared history commit %s does not extend observed head %s", commit, previous)
		}
		batchData, err := store.showFile(ctx, commit, "batch.json", 8<<20)
		if err != nil {
			return nil, err
		}
		var batch Batch
		if err := json.Unmarshal(batchData, &batch); err != nil {
			return nil, fmt.Errorf("parse shared history batch at %s: %w", commit, err)
		}
		public, err := verifyBatch(batch)
		if err != nil {
			return nil, err
		}
		if batch.SchemaVersion != SchemaVersion || len(batch.Bundles) == 0 {
			return nil, fmt.Errorf("shared history batch %s has an invalid schema or no bundles", commit)
		}
		if err := validateChainAnchor(batch.ChainAnchor); err != nil {
			return nil, fmt.Errorf("shared history batch %s: %w", commit, err)
		}
		if batch.DeviceID != remoteRef.DeviceID || batch.PreviousHead != actualPrevious {
			return nil, fmt.Errorf("shared history batch %s has invalid device or previous head", commit)
		}
		seenBundles := make(map[primitives.BundleID]struct{}, len(batch.Bundles))
		for _, item := range batch.Bundles {
			if _, exists := seenBundles[item.BundleID]; exists {
				return nil, fmt.Errorf("shared history batch %s repeats bundle %s", commit, item.BundleID)
			}
			seenBundles[item.BundleID] = struct{}{}
			if item.Path != bundlePath(item.BundleID) {
				return nil, fmt.Errorf("shared history bundle %s has non-canonical path %s", item.BundleID, item.Path)
			}
			manifestData, err := store.showFile(ctx, commit, item.Path+"/manifest.json", DefaultBundleLimit)
			if err != nil {
				return nil, err
			}
			eventsData, err := store.showFile(ctx, commit, item.Path+"/events.jsonl", DefaultBundleLimit)
			if err != nil {
				return nil, err
			}
			var manifest Manifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				return nil, fmt.Errorf("parse shared history manifest %s: %w", item.BundleID, err)
			}
			if err := validateManifest(store.repo, remoteRef.DeviceID, item, manifest); err != nil {
				return nil, fmt.Errorf("validate shared history manifest %s: %w", item.BundleID, err)
			}
			if len(manifestData)+len(eventsData) > DefaultBundleLimit {
				return nil, fmt.Errorf("shared history bundle %s exceeds %d bytes", item.BundleID, DefaultBundleLimit)
			}
			if manifest.ContentHashes["events.jsonl"] != sha256Bytes(eventsData) {
				return nil, fmt.Errorf("shared history event content hash mismatch for %s", item.BundleID)
			}
			if err := verifyManifest(public, manifest); err != nil {
				return nil, err
			}
			events, err := decodeEventsJSONL(eventsData)
			if err != nil {
				return nil, fmt.Errorf("decode shared history bundle %s: %w", item.BundleID, err)
			}
			if err := validateProjectedEvents(manifest, events); err != nil {
				return nil, fmt.Errorf("validate shared history bundle %s events: %w", item.BundleID, err)
			}
			stored := StoredBundle{Manifest: manifest, Events: events, PublicKey: batch.PublicKey}
			if err := materializePulled(store.repo, stored); err != nil {
				return nil, err
			}
			ingested = append(ingested, stored)
		}
		previous = commit
	}
	oldTracking, _ := store.refValue(ctx, trackingRef(remoteRef.DeviceID))
	args := []string{"update-ref", trackingRef(remoteRef.DeviceID), remoteRef.Head}
	if oldTracking != "" {
		args = append(args, oldTracking)
	}
	if _, err := store.run(ctx, args...); err != nil {
		return nil, err
	}
	return ingested, nil
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
	if err := json.Unmarshal(data, &batch); err != nil {
		return fmt.Errorf("recover shared history outbox batch: %w", err)
	}
	if _, err := verifyBatch(batch); err != nil {
		return fmt.Errorf("recover shared history outbox: %w", err)
	}
	if batch.SchemaVersion != SchemaVersion || batch.DeviceID != identity.DeviceID || batch.PublicKey != identity.PublicKey || len(batch.Bundles) == 0 {
		return fmt.Errorf("recover shared history outbox: local tip has an invalid batch")
	}
	if err := validateChainAnchor(batch.ChainAnchor); err != nil {
		return fmt.Errorf("recover shared history outbox: %w", err)
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

func (store *gitStore) refValue(ctx context.Context, ref string) (string, error) {
	output, err := store.run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		if isGitExit(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (store *gitStore) showFile(ctx context.Context, commit, path string, limit int64) ([]byte, error) {
	data, err := store.runBytes(ctx, "show", commit+":"+path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("shared history object %s exceeds %d bytes", path, limit)
	}
	return data, nil
}

func materializePulled(repo *checkpoint.Repo, bundle StoredBundle) error {
	path := filepath.Join(sharedRoot(repo), "pulled", bundle.Manifest.DeviceID, bundle.Manifest.BundleID.String()+".json")
	if existingData, err := readRegularFile(path, DefaultBundleLimit*2); err == nil {
		var existing StoredBundle
		if err := json.Unmarshal(existingData, &existing); err != nil {
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
	return writeJSONAtomic(path, bundle, 0o600)
}

func validateManifest(repo *checkpoint.Repo, deviceID string, item BatchBundle, manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion || manifest.EvidenceClass != EvidencePublisherClaim {
		return fmt.Errorf("unsupported schema or evidence class")
	}
	if err := validatePromptMode(manifest.PromptMode); err != nil {
		return err
	}
	if manifest.BundleID != item.BundleID || manifest.DeviceID != deviceID || manifest.RepoID != repo.RepoID {
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

func validateChainAnchor(anchor map[string]string) error {
	for stream, hash := range anchor {
		if strings.TrimSpace(stream) == "" || strings.ContainsAny(stream, "\r\n\x00") || !validSHA256(hash) {
			return fmt.Errorf("chain anchor contains an invalid stream or hash")
		}
	}
	return nil
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
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
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
	return nil
}

func (store *gitStore) run(ctx context.Context, args ...string) (string, error) {
	data, err := store.runBytes(ctx, args...)
	return string(data), err
}

func (store *gitStore) runBytes(ctx context.Context, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", store.root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = scrubGitEnv(os.Environ())
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, gitCommandError{args: args, output: strings.TrimSpace(string(data)), err: err}
	}
	return data, nil
}

type gitCommandError struct {
	args   []string
	output string
	err    error
}

func (err gitCommandError) Error() string {
	if err.output == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(err.args, " "), err.err)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(err.args, " "), err.output)
}

func (err gitCommandError) Unwrap() error { return err.err }

func isGitExit(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func scrubGitEnv(values []string) []string {
	blocked := map[string]struct{}{
		"GIT_DIR": {}, "GIT_WORK_TREE": {}, "GIT_INDEX_FILE": {}, "GIT_OBJECT_DIRECTORY": {},
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {}, "GIT_COMMON_DIR": {}, "GIT_PREFIX": {},
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		name := value
		if index := strings.IndexByte(value, '='); index >= 0 {
			name = value[:index]
		}
		if _, found := blocked[name]; !found {
			result = append(result, value)
		}
	}
	return result
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
