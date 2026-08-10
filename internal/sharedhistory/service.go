package sharedhistory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		return manager.statusLocked(ctx, false)
	})
}

func (manager *Manager) StatusWithRemote(ctx context.Context) (Status, error) {
	if manager == nil || manager.repo == nil {
		return Status{}, fmt.Errorf("shared history status requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "check shared history remote", func() (Status, error) {
		return manager.statusLocked(ctx, true)
	})
}

func (manager *Manager) statusLocked(ctx context.Context, checkRemote bool) (Status, error) {
	ctx = nonNilContext(ctx)
	if _, err := os.Lstat(policyPath(manager.repo)); err != nil {
		if os.IsNotExist(err) {
			return Status{Configured: false}, nil
		}
		return Status{}, err
	}
	policy, err := loadPolicyForUpdate(manager.repo)
	if err != nil {
		return Status{}, err
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Status{}, err
	}
	identity, err := loadDevice(manager.repo)
	if err != nil {
		return Status{}, err
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Status{}, err
	}
	alignStateScope(&state, policy.Remote, policy.RepoID)
	store, storeExists, err := existingGitStore(manager.repo)
	if err != nil {
		return Status{}, err
	}
	if storeExists {
		if err := store.recoverCommittedState(ctx, identity, &state); err != nil {
			return Status{}, err
		}
	}
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
	pulled, err := countPulled(manager.repo, policy.RepoID)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Configured:       true,
		Enabled:          !policy.Disabled,
		Remote:           redactRemote(policy.Remote),
		RepoID:           policy.RepoID,
		PromptMode:       policy.PromptMode,
		PolicyHash:       digest,
		Approved:         policy.ApprovedHash == digest,
		DeviceID:         identity.DeviceID,
		Pending:          pending,
		Blocked:          cloneStringMap(state.Blocked),
		Published:        len(state.Published),
		Pulled:           pulled,
		LastSeen:         cloneStringMap(state.LastSeen),
		Quarantined:      cloneStringMap(state.Quarantined),
		Retired:          cloneStringMap(state.Retired),
		UnpushedLocalTip: len(state.Committed) > 0,
	}
	if !checkRemote {
		return status, nil
	}
	if policy.Disabled {
		status.RemoteError = "shared history is disabled; remote was not checked"
		return status, nil
	}
	status.RemoteChecked = true
	if !storeExists {
		store, err = openGitStore(ctx, manager.repo)
		if err != nil {
			return Status{}, err
		}
	}
	local, err := store.localHead(ctx)
	if err != nil {
		return Status{}, err
	}
	remote, err := store.remoteHead(ctx, policy.Remote, historyRef(identity.DeviceID))
	if err != nil {
		if local != "" {
			status.UnpushedLocalTip = true
		}
		status.RemoteError = err.Error()
		return status, nil
	}
	status.UnpushedLocalTip = local != remote
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
	if policy.Disabled {
		return Plan{}, fmt.Errorf("shared history is disabled; run turnal share enable to resume")
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Plan{}, err
	}
	identity, err := loadDevice(manager.repo)
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

func (manager *Manager) PlanPush(ctx context.Context) (PushPlan, error) {
	if manager == nil || manager.repo == nil {
		return PushPlan{}, fmt.Errorf("shared history push plan requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "plan shared history push", func() (PushPlan, error) {
		policy, err := loadPolicyForUpdate(manager.repo)
		if err != nil {
			return PushPlan{}, err
		}
		if policy.Disabled {
			return PushPlan{}, fmt.Errorf("shared history is disabled; run turnal share enable to resume")
		}
		digest, err := policyHash(policy)
		if err != nil {
			return PushPlan{}, err
		}
		identity, err := loadDevice(manager.repo)
		if err != nil {
			return PushPlan{}, err
		}
		state, err := loadState(manager.repo)
		if err != nil {
			return PushPlan{}, err
		}
		alignStateScope(&state, policy.Remote, policy.RepoID)
		store, exists, err := existingGitStore(manager.repo)
		if err != nil {
			return PushPlan{}, err
		}
		if exists {
			if err := store.recoverCommittedState(ctx, identity, &state); err != nil {
				return PushPlan{}, err
			}
		}
		turns, err := listCompletedTurns(manager.repo)
		if err != nil {
			return PushPlan{}, err
		}
		migrationRequired := policy.AllowlistVersion != AllowlistVersion || policy.ScannerVersion != ScannerVersion
		plan := PushPlan{
			PolicyHash:        digest,
			ApprovalRequired:  policy.ApprovedHash != digest,
			MigrationRequired: migrationRequired,
		}
		batchBytes := 0
		batchCapped := false
		newPublishable := 0
		for _, source := range turns {
			bundleID, err := primitives.DeriveBundleID(policy.RepoID, source.Stream.StreamID, source.TurnID)
			if err != nil {
				return PushPlan{}, err
			}
			if _, ok := state.Published[bundleID.String()]; ok {
				continue
			}
			pending := PendingBundle{Locator: locator(identity.DeviceID, bundleID), SessionID: source.Stream.SessionID, TurnID: source.TurnID, StreamID: source.Stream.StreamID}
			if _, ok := state.Committed[bundleID.String()]; ok {
				pending.Queued = true
				plan.Publishable++
				plan.Queued++
				plan.Pending = append(plan.Pending, pending)
				continue
			}
			// An older approved projection may only drain its already-built
			// outbox. New bundles require migration and fresh approval.
			if migrationRequired {
				continue
			}
			bundle, buildErr := buildBundle(manager.repo, identity, policy, digest, source)
			if buildErr != nil {
				pending.Blocked = buildErr.Error()
				plan.Blocked++
				plan.Pending = append(plan.Pending, pending)
				continue
			}
			pending.Bytes = len(bundle.Manifest) + len(bundle.EventsJSON)
			plan.Publishable++
			newPublishable++
			if !batchCapped && batchAccepts(plan.BatchSize, batchBytes, pending.Bytes) {
				plan.BatchSize++
				batchBytes += pending.Bytes
			} else {
				batchCapped = true
			}
			plan.Pending = append(plan.Pending, pending)
		}
		plan.Remaining = newPublishable - plan.BatchSize
		return plan, nil
	})
}

func (manager *Manager) List(ctx context.Context, options ListOptions) ([]BundleSummary, error) {
	if manager == nil || manager.repo == nil {
		return nil, fmt.Errorf("list shared history requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "list shared history", func() ([]BundleSummary, error) {
		policy, err := loadPolicyForUpdate(manager.repo)
		if err != nil {
			return nil, err
		}
		identity, err := loadDevice(manager.repo)
		if err != nil {
			return nil, err
		}
		state, err := loadState(manager.repo)
		if err != nil {
			return nil, err
		}
		seen := map[string]BundleSummary{}
		localBundleIDs := make(map[string]struct{}, len(state.Published)+len(state.Committed))
		for bundleID := range state.Published {
			localBundleIDs[bundleID] = struct{}{}
		}
		for bundleID := range state.Committed {
			localBundleIDs[bundleID] = struct{}{}
		}
		for bundleID := range localBundleIDs {
			parsed, err := primitives.ParseBundleID(bundleID)
			if err != nil {
				return nil, err
			}
			value := locator(identity.DeviceID, parsed)
			bundle, err := manager.readLocked(ctx, value)
			if err != nil {
				return nil, err
			}
			seen[value] = summarizeBundle(value, bundle, true)
		}
		pulledRoot := filepath.Join(sharedRoot(manager.repo), "pulled", policy.RepoID.String())
		recordUnreadable := func(path string, cause error) {
			relative, err := filepath.Rel(pulledRoot, path)
			if err != nil {
				return
			}
			parts := strings.Split(filepath.ToSlash(relative), "/")
			if len(parts) != 2 || !validDeviceID(parts[0]) {
				return
			}
			bundleID, err := primitives.ParseBundleID(strings.TrimSuffix(parts[1], ".json"))
			if err != nil {
				return
			}
			value := locator(parts[0], bundleID)
			seen[value] = BundleSummary{Locator: value, DeviceID: parts[0], Error: truncateQuarantineReason(cause.Error())}
		}
		err = filepath.WalkDir(pulledRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				return nil
			}
			data, err := readRegularFile(path, MaxMaterializedLimit)
			if err != nil {
				recordUnreadable(path, err)
				return nil
			}
			var bundle StoredBundle
			if err := decodeStrictJSON(data, &bundle); err != nil {
				recordUnreadable(path, err)
				return nil
			}
			if err := verifyStoredBundle(policy.RepoID, bundle); err != nil {
				recordUnreadable(path, err)
				return nil
			}
			value := locator(bundle.Manifest.DeviceID, bundle.Manifest.BundleID)
			if _, exists := seen[value]; !exists {
				seen[value] = summarizeBundle(value, bundle, false)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		result := make([]BundleSummary, 0, len(seen))
		for _, summary := range seen {
			if options.SessionID != "" && summary.SessionID != options.SessionID {
				continue
			}
			if options.DeviceID != "" && summary.DeviceID != options.DeviceID {
				continue
			}
			result = append(result, summary)
		}
		sort.Slice(result, func(i, j int) bool {
			if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
				return result[i].CreatedAt.Before(result[j].CreatedAt)
			}
			return result[i].Locator < result[j].Locator
		})
		return result, nil
	})
}

func summarizeBundle(value string, bundle StoredBundle, local bool) BundleSummary {
	return BundleSummary{
		Locator: value, SessionID: bundle.Manifest.SessionID, TurnID: bundle.Manifest.TurnID,
		StreamID: bundle.Manifest.StreamID, DeviceID: bundle.Manifest.DeviceID,
		CreatedAt: bundle.Manifest.CreatedAt, PromptMode: bundle.Manifest.PromptMode,
		EventCount: len(bundle.Events), Local: local,
	}
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
	policy, err := loadPolicyForUpdate(manager.repo)
	if err != nil {
		return Result{}, err
	}
	if policy.Disabled {
		return Result{}, fmt.Errorf("shared history is disabled; run turnal share enable to resume")
	}
	digest, err := policyHash(policy)
	if err != nil {
		return Result{}, err
	}
	if policy.ApprovedHash != digest {
		return Result{}, fmt.Errorf("shared history policy is not approved; preview a completed turn with --approve")
	}
	identity, err := loadDevice(manager.repo)
	if err != nil {
		return Result{}, err
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignStateScope(&state, policy.Remote, policy.RepoID)
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
	if policy.AllowlistVersion != AllowlistVersion || policy.ScannerVersion != ScannerVersion {
		return Result{Direction: DirectionPush, Published: publishedThisRun, Head: localHead}, fmt.Errorf("published the existing outbox under its approved projection policy; run turnal share enable to migrate to the current projection and approve it")
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
		if !batchAccepts(len(bundles), batchBytes, bundleBytes) {
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
		return Result{Direction: DirectionPush, Published: publishedThisRun, Blocked: blockedThisRun, Remaining: countPendingTurns(policy, state, turns), Head: localHead}, nil
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
	return Result{Direction: DirectionPush, Published: publishedThisRun, Blocked: blockedThisRun, Remaining: countPendingTurns(policy, state, turns), Head: confirmed}, nil
}

func countPendingTurns(policy policyFile, state stateFile, turns []turnSource) int {
	pending := 0
	for _, source := range turns {
		bundleID, err := primitives.DeriveBundleID(policy.RepoID, source.Stream.StreamID, source.TurnID)
		if err != nil {
			continue
		}
		if _, published := state.Published[bundleID.String()]; !published {
			if _, blocked := state.Blocked[bundleID.String()]; blocked {
				continue
			}
			if _, committed := state.Committed[bundleID.String()]; committed {
				continue
			}
			pending++
		}
	}
	return pending
}

func batchAccepts(count, currentBytes, nextBytes int) bool {
	if count >= MaxBundlesPerBatch {
		return false
	}
	return count == 0 || currentBytes+nextBytes <= MaxBatchBytes
}

func (manager *Manager) syncPull(ctx context.Context) (Result, error) {
	policy, err := loadPolicy(manager.repo)
	if err != nil {
		return Result{}, err
	}
	if policy.Disabled {
		return Result{}, fmt.Errorf("shared history is disabled; run turnal share enable to resume")
	}
	state, err := loadState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignStateScope(&state, policy.Remote, policy.RepoID)
	store, err := openGitStore(ctx, manager.repo)
	if err != nil {
		return Result{}, err
	}
	recovered, err := store.recoverTrackingState(ctx, policy.Remote, policy.RepoID, &state)
	if err != nil {
		return Result{}, err
	}
	if recovered {
		if err := saveState(manager.repo, state); err != nil {
			return Result{}, err
		}
	}
	refs, warnings, err := store.listRemoteHistory(ctx, policy.Remote)
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
		if retiredHead, retired := state.Retired[deviceID]; retired && retiredHead == head {
			continue
		}
		if _, exists := present[deviceID]; !exists {
			state.Quarantined[deviceID] = fmt.Sprintf("remote ref disappeared after head %s was observed", head)
		}
	}
	pulled := 0
	for _, ref := range refs {
		delete(state.Retired, ref.DeviceID)
		bundles, observedHead, err := store.fetchAndIngest(ctx, policy.Remote, ref, state.LastSeen[ref.DeviceID], policy.RepoID)
		if err != nil {
			if ctx.Err() != nil {
				if saveErr := saveState(manager.repo, state); saveErr != nil {
					return Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings}, saveErr
				}
				return Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings}, err
			}
			if isRetryablePullError(err) {
				if saveErr := saveState(manager.repo, state); saveErr != nil {
					return Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings}, saveErr
				}
				return Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings}, err
			}
			state.Quarantined[ref.DeviceID] = truncateQuarantineReason(err.Error())
			continue
		}
		pulled += len(bundles)
		state.LastSeen[ref.DeviceID] = observedHead
		delete(state.Quarantined, ref.DeviceID)
	}
	if err := saveState(manager.repo, state); err != nil {
		return Result{Direction: DirectionPull, Pulled: pulled}, err
	}
	result := Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings, Quarantined: cloneStringMap(state.Quarantined)}
	if len(result.Quarantined) > 0 {
		return result, fmt.Errorf("shared history pull quarantined %d publisher(s); inspect turnal share status", len(result.Quarantined))
	}
	return result, nil
}

func truncateQuarantineReason(reason string) string {
	reason = strings.TrimSpace(strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		return character
	}, reason))
	const maxReasonBytes = 2 << 10
	if len(reason) > maxReasonBytes {
		return truncateUTF8(reason, maxReasonBytes)
	}
	return reason
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
	policy, err := loadPolicyForUpdate(manager.repo)
	if err != nil {
		return StoredBundle{}, err
	}
	deviceID, bundleID, err := parseLocator(value)
	if err != nil {
		return StoredBundle{}, err
	}
	identity, err := loadDevice(manager.repo)
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
	path := pulledBundlePath(manager.repo, policy.RepoID, deviceID, bundleID)
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

func countPulled(repo *checkpoint.Repo, repoID primitives.RepoID) (int, error) {
	root := filepath.Join(sharedRoot(repo), "pulled", repoID.String())
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
