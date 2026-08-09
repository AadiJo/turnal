package sharedhistory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

type Manager struct {
	repo *checkpoint.Repo
	now  func() time.Time
}

func New(repo *checkpoint.Repo) *Manager {
	return &Manager{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if manager == nil || manager.repo == nil {
		return Status{}, fmt.Errorf("shared history status requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "inspect shared history", func() (Status, error) {
		return manager.statusLocked(ctx)
	})
}

func (manager *Manager) statusLocked(ctx context.Context) (Status, error) {
	ctx = nonNilContext(ctx)
	if _, err := os.Lstat(policyPath(manager.repo)); err != nil {
		if os.IsNotExist(err) {
			return Status{Configured: false}, nil
		}
		return Status{}, err
	}
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return Status{}, err
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Status{}, err
	}
	identity, err := loadOrCreateDevice(manager.repo)
	if err != nil {
		return Status{}, err
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Status{}, err
	}
	alignStateRemote(&state, policy.Remote)
	turns, err := listCompletedTurns(manager.repo)
	if err != nil {
		return Status{}, err
	}
	pending := 0
	for _, turn := range turns {
		bundleID, err := primitives.DeriveBundleID(policy.RepoID, turn.Stream.StreamID, turn.TurnID)
		if err != nil {
			return Status{}, err
		}
		if _, ok := state.Published[bundleID.String()]; ok {
			continue
		}
		pending++
	}
	pulled, err := countPulled(manager.repo)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Configured: true,
		Remote:     redactRemote(policy.Remote),
		RepoID:     policy.RepoID,
		PromptMode: policy.PromptMode,
		PolicyHash: digest,
		Approved:   policy.ApprovedHash == digest,
		DeviceID:   identity.DeviceID,
		Pending:    pending,
		Blocked:    cloneStringMap(state.Blocked),
		Published:  len(state.Published),
		Pulled:     pulled,
		LastSeen:   cloneStringMap(state.LastSeen),
	}
	store, err := openGitStore(ctx, manager.repo)
	if err != nil {
		return Status{}, err
	}
	local, err := store.localHead(ctx)
	if err != nil {
		return Status{}, err
	}
	if local != "" {
		remote, err := store.remoteHead(ctx, policy.Remote, historyRef(identity.DeviceID))
		if err != nil {
			status.UnpushedLocalTip = true
			status.RemoteError = err.Error()
			return status, nil
		}
		status.UnpushedLocalTip = local != remote
	}
	return status, nil
}

func (manager *Manager) Preview(ctx context.Context, options PreviewOptions) (Plan, error) {
	if manager == nil || manager.repo == nil {
		return Plan{}, fmt.Errorf("shared history preview requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "preview shared history", func() (Plan, error) {
		return manager.previewLocked(ctx, options)
	})
}

func (manager *Manager) previewLocked(ctx context.Context, options PreviewOptions) (Plan, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return Plan{}, err
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Plan{}, err
	}
	identity, err := loadOrCreateDevice(manager.repo)
	if err != nil {
		return Plan{}, err
	}
	source, err := findCompletedTurn(manager.repo, options.SessionID, options.TurnID, options.StreamID)
	if err != nil {
		return Plan{}, err
	}
	bundle, err := buildBundle(manager.repo, identity, policy, digest, source)
	if err != nil {
		return Plan{}, err
	}
	if options.Approve {
		if err := approvePolicy(manager.repo, policy, digest); err != nil {
			return Plan{}, err
		}
		policy.ApprovedHash = digest
	}
	return Plan{
		Locator:          locator(identity.DeviceID, bundle.Stored.Manifest.BundleID),
		PolicyHash:       digest,
		ApprovalRequired: policy.ApprovedHash != digest,
		Bytes:            len(bundle.Manifest) + len(bundle.EventsJSON),
		Manifest:         bundle.Stored.Manifest,
		Events:           bundle.Stored.Events,
	}, nil
}

func (manager *Manager) Sync(ctx context.Context, direction Direction) (Result, error) {
	if manager == nil || manager.repo == nil {
		return Result{}, fmt.Errorf("shared history sync requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "synchronize shared history", func() (Result, error) {
		return manager.syncLocked(ctx, direction)
	})
}

func (manager *Manager) syncLocked(ctx context.Context, direction Direction) (Result, error) {
	ctx = nonNilContext(ctx)
	switch direction {
	case DirectionPush:
		return manager.syncPush(ctx)
	case DirectionPull:
		return manager.syncPull(ctx)
	default:
		return Result{}, fmt.Errorf("invalid shared history sync direction %q", direction)
	}
}

func (manager *Manager) syncPush(ctx context.Context) (Result, error) {
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return Result{}, err
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Result{}, err
	}
	if policy.ApprovedHash != digest {
		return Result{}, fmt.Errorf("shared history policy is not approved; preview a completed turn with --approve")
	}
	identity, err := loadOrCreateDevice(manager.repo)
	if err != nil {
		return Result{}, err
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignStateRemote(&state, policy.Remote)
	store, err := openGitStore(ctx, manager.repo)
	if err != nil {
		return Result{}, err
	}
	if err := store.recoverCommittedState(ctx, identity, &state); err != nil {
		return Result{}, err
	}
	if err := store.validateCommittedPolicy(ctx, state, digest); err != nil {
		return Result{}, err
	}

	// Durable local commits are the outbox. Push them before scanning for more
	// turns so a network failure never causes a bundle to be regenerated.
	localHead, err := store.localHead(ctx)
	if err != nil {
		return Result{}, err
	}
	for bundleID, committedHead := range state.Committed {
		if localHead == "" || committedHead != localHead {
			return Result{}, fmt.Errorf("shared history outbox state for %s does not match local head %s", bundleID, localHead)
		}
	}
	publishedThisRun := 0
	if localHead != "" {
		head, err := store.push(ctx, policy.Remote, identity.DeviceID, state.LastSeen[identity.DeviceID])
		if err != nil {
			return Result{Direction: DirectionPush}, err
		}
		publishedThisRun += promoteCommitted(&state, head)
		state.LastSeen[identity.DeviceID] = head
		if err := saveState(manager.repo, state); err != nil {
			return Result{}, err
		}
	}

	turns, err := listCompletedTurns(manager.repo)
	if err != nil {
		return Result{}, err
	}
	var bundles []builtBundle
	batchBytes := 0
	blockedThisRun := 0
	for _, source := range turns {
		bundleID, err := primitives.DeriveBundleID(policy.RepoID, source.Stream.StreamID, source.TurnID)
		if err != nil {
			return Result{}, err
		}
		if _, ok := state.Published[bundleID.String()]; ok {
			continue
		}
		if _, ok := state.Committed[bundleID.String()]; ok {
			continue
		}
		bundle, err := buildBundle(manager.repo, identity, policy, digest, source)
		if err != nil {
			state.Blocked[bundleID.String()] = err.Error()
			blockedThisRun++
			continue
		}
		bundleBytes := len(bundle.Manifest) + len(bundle.EventsJSON)
		if len(bundles) > 0 && batchBytes+bundleBytes > MaxBatchBytes {
			break
		}
		delete(state.Blocked, bundleID.String())
		bundles = append(bundles, bundle)
		batchBytes += bundleBytes
		if len(bundles) == MaxBundlesPerBatch {
			break
		}
	}
	if len(bundles) == 0 {
		if err := saveState(manager.repo, state); err != nil {
			return Result{}, err
		}
		return Result{Direction: DirectionPush, Published: publishedThisRun, Blocked: blockedThisRun, Head: localHead}, nil
	}

	previousHead, err := store.localHead(ctx)
	if err != nil {
		return Result{}, err
	}
	batch := Batch{
		SchemaVersion: SchemaVersion,
		DeviceID:      identity.DeviceID,
		PublicKey:     identity.PublicKey,
		PreviousHead:  previousHead,
		CreatedAt:     manager.now(),
	}
	for _, bundle := range bundles {
		manifest := bundle.Stored.Manifest
		batch.Bundles = append(batch.Bundles, BatchBundle{BundleID: manifest.BundleID, Path: bundle.Path, RepoID: manifest.RepoID, SessionID: manifest.SessionID, TurnID: manifest.TurnID, Sequence: manifest.SourceSequence})
	}
	batch, err = signBatch(identity, batch)
	if err != nil {
		return Result{}, err
	}
	head, err := store.commitBatch(ctx, batch, bundles)
	if err != nil {
		return Result{}, err
	}
	for _, bundle := range bundles {
		state.Committed[bundle.Stored.Manifest.BundleID.String()] = head
	}
	if err := saveState(manager.repo, state); err != nil {
		return Result{}, err
	}
	confirmed, err := store.push(ctx, policy.Remote, identity.DeviceID, state.LastSeen[identity.DeviceID])
	if err != nil {
		return Result{Direction: DirectionPush, Blocked: blockedThisRun, Head: head}, err
	}
	publishedThisRun += promoteCommitted(&state, confirmed)
	state.LastSeen[identity.DeviceID] = confirmed
	if err := saveState(manager.repo, state); err != nil {
		return Result{}, err
	}
	return Result{Direction: DirectionPush, Published: publishedThisRun, Blocked: blockedThisRun, Head: confirmed}, nil
}

func (manager *Manager) syncPull(ctx context.Context) (Result, error) {
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return Result{}, err
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignStateRemote(&state, policy.Remote)
	store, err := openGitStore(ctx, manager.repo)
	if err != nil {
		return Result{}, err
	}
	recovered, err := store.recoverTrackingState(ctx, policy.Remote, &state)
	if err != nil {
		return Result{}, err
	}
	if recovered {
		if err := saveState(manager.repo, state); err != nil {
			return Result{}, err
		}
	}
	refs, err := store.listRemoteHistory(ctx, policy.Remote)
	if err != nil {
		return Result{}, err
	}
	present := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		present[ref.DeviceID] = struct{}{}
	}
	for deviceID, head := range state.LastSeen {
		if head == "" {
			continue
		}
		if _, exists := present[deviceID]; !exists {
			return Result{}, fmt.Errorf("shared history remote ref disappeared after head %s was observed for device %s", head, deviceID)
		}
	}
	pulled := 0
	for _, ref := range refs {
		bundles, observedHead, err := store.fetchAndIngest(ctx, policy.Remote, ref, state.LastSeen[ref.DeviceID], policy.RepoID)
		if err != nil {
			return Result{Direction: DirectionPull, Pulled: pulled}, err
		}
		pulled += len(bundles)
		state.LastSeen[ref.DeviceID] = observedHead
		if err := saveState(manager.repo, state); err != nil {
			return Result{Direction: DirectionPull, Pulled: pulled}, err
		}
	}
	return Result{Direction: DirectionPull, Pulled: pulled}, nil
}

func (manager *Manager) Read(ctx context.Context, value string) (StoredBundle, error) {
	if manager == nil || manager.repo == nil {
		return StoredBundle{}, fmt.Errorf("read shared history requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "read shared history", func() (StoredBundle, error) {
		return manager.readLocked(ctx, value)
	})
}

func (manager *Manager) readLocked(ctx context.Context, value string) (StoredBundle, error) {
	ctx = nonNilContext(ctx)
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return StoredBundle{}, err
	}
	deviceID, bundleID, err := parseLocator(value)
	if err != nil {
		return StoredBundle{}, err
	}
	identity, err := loadOrCreateDevice(manager.repo)
	if err != nil {
		return StoredBundle{}, err
	}
	if deviceID == identity.DeviceID {
		store, err := openGitStore(ctx, manager.repo)
		if err != nil {
			return StoredBundle{}, err
		}
		manifestData, err := readRegularFile(filepath.Join(store.root, filepath.FromSlash(bundlePath(bundleID)), "manifest.json"), DefaultBundleLimit)
		if err != nil {
			return StoredBundle{}, err
		}
		eventsData, err := readRegularFile(filepath.Join(store.root, filepath.FromSlash(bundlePath(bundleID)), "events.jsonl"), DefaultBundleLimit)
		if err != nil {
			return StoredBundle{}, err
		}
		var manifest Manifest
		if err := decodeStrictJSON(manifestData, &manifest); err != nil {
			return StoredBundle{}, err
		}
		events, err := decodeEventsJSONL(eventsData)
		if err != nil {
			return StoredBundle{}, err
		}
		bundle := StoredBundle{Manifest: manifest, Events: events, PublicKey: identity.PublicKey}
		if err := verifyStoredBundle(policy.RepoID, bundle); err != nil {
			return StoredBundle{}, err
		}
		return bundle, nil
	}
	path := filepath.Join(sharedRoot(manager.repo), "pulled", deviceID, bundleID.String()+".json")
	data, err := readRegularFile(path, MaxMaterializedLimit)
	if err != nil {
		return StoredBundle{}, err
	}
	var bundle StoredBundle
	if err := decodeStrictJSON(data, &bundle); err != nil {
		return StoredBundle{}, err
	}
	if err := verifyStoredBundle(policy.RepoID, bundle); err != nil {
		return StoredBundle{}, err
	}
	return bundle, nil
}

func verifyStoredBundle(repoID primitives.RepoID, bundle StoredBundle) error {
	public, err := publicKeyForDevice(bundle.PublicKey, bundle.Manifest.DeviceID)
	if err != nil {
		return err
	}
	item := BatchBundle{
		BundleID:  bundle.Manifest.BundleID,
		Path:      bundlePath(bundle.Manifest.BundleID),
		RepoID:    bundle.Manifest.RepoID,
		SessionID: bundle.Manifest.SessionID,
		TurnID:    bundle.Manifest.TurnID,
		Sequence:  bundle.Manifest.SourceSequence,
	}
	if err := validateManifest(repoID, bundle.Manifest.DeviceID, item, bundle.Manifest); err != nil {
		return err
	}
	if err := verifyManifest(public, bundle.Manifest); err != nil {
		return err
	}
	eventsJSON, err := marshalEventsJSONL(bundle.Events)
	if err != nil {
		return err
	}
	if bundle.Manifest.ContentHashes["events.jsonl"] != sha256Bytes(eventsJSON) {
		return fmt.Errorf("shared history event content hash mismatch for %s", bundle.Manifest.BundleID)
	}
	return validateProjectedEvents(bundle.Manifest, bundle.Events)
}

func promoteCommitted(state *stateFile, head string) int {
	count := 0
	for bundleID := range state.Committed {
		state.Published[bundleID] = head
		delete(state.Committed, bundleID)
		delete(state.Blocked, bundleID)
		count++
	}
	return count
}

func countPulled(repo *checkpoint.Repo) (int, error) {
	root := filepath.Join(sharedRoot(repo), "pulled")
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return count, err
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
