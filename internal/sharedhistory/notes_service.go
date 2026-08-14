package sharedhistory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

// NotesStatus reports note-channel state. It is separate from Status because a
// publisher can share turn context without ever sharing commentary.
func (manager *Manager) NotesStatus(ctx context.Context) (NotesStatus, error) {
	if manager == nil || manager.repo == nil {
		return NotesStatus{}, fmt.Errorf("note sharing status requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "note sharing status", func() (NotesStatus, error) {
		return notesStatusLocked(manager.repo)
	})
}

func notesStatusLocked(repo *checkpoint.Repo) (NotesStatus, error) {
	policy, err := loadNotesPolicyForUpdate(repo)
	if err != nil {
		if os.IsNotExist(err) {
			return NotesStatus{}, nil
		}
		return NotesStatus{}, err
	}
	digest, err := notesPolicyHash(policy)
	if err != nil {
		return NotesStatus{}, err
	}
	identity, err := loadOrCreateDevice(repo)
	if err != nil {
		return NotesStatus{}, err
	}
	state, err := loadNotesState(repo)
	if err != nil {
		return NotesStatus{}, err
	}
	operations, err := listPublishableNotes(repo, policy.RepoID)
	if err != nil {
		return NotesStatus{}, err
	}
	pending := 0
	for _, operation := range operations {
		bundleID, err := deriveNoteBundleID(policy.RepoID, operation.Note.NoteID, operation.Operation)
		if err != nil {
			return NotesStatus{}, err
		}
		if _, published := state.Published[bundleID.String()]; !published {
			pending++
		}
	}
	pulled, err := countPulledNotes(repo, policy.RepoID)
	if err != nil {
		return NotesStatus{}, err
	}
	return NotesStatus{
		Configured:  true,
		Enabled:     !policy.Disabled,
		Remote:      redactRemote(policy.Remote),
		RepoID:      policy.RepoID,
		PromptMode:  policy.PromptMode,
		PolicyHash:  digest,
		Approved:    policy.ApprovedHash == digest,
		DeviceID:    identity.DeviceID,
		Pending:     pending,
		Published:   len(state.Published),
		Pulled:      pulled,
		Quarantined: cloneStringMap(state.Quarantined),
	}, nil
}

// NotePreviewOptions selects one note to preview before publication.
type NotePreviewOptions struct {
	NoteID  primitives.NoteID
	Approve bool
}

// NotePlan is the exact projection that would be published for one note.
type NotePlan struct {
	Locator          string         `json:"locator"`
	PolicyHash       string         `json:"policy_hash"`
	ApprovalRequired bool           `json:"approval_required"`
	Bytes            int            `json:"bytes"`
	Manifest         NoteManifest   `json:"manifest"`
	Note             NoteProjection `json:"note"`
}

// PreviewNote shows the complete projection for one note and optionally records
// approval for the note policy hash. Approval applies to the policy, not to the
// previewed note, exactly as it does for turn context.
func (manager *Manager) PreviewNote(ctx context.Context, options NotePreviewOptions) (NotePlan, error) {
	if manager == nil || manager.repo == nil {
		return NotePlan{}, fmt.Errorf("note preview requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "preview note", func() (NotePlan, error) {
		policy, err := loadNotesPolicy(manager.repo)
		if err != nil {
			return NotePlan{}, err
		}
		if policy.Disabled {
			return NotePlan{}, fmt.Errorf("note sharing is disabled; run turnal share notes enable to resume")
		}
		digest, err := notesPolicyHash(policy)
		if err != nil {
			return NotePlan{}, err
		}
		identity, err := loadOrCreateDevice(manager.repo)
		if err != nil {
			return NotePlan{}, err
		}
		operations, err := listPublishableNotes(manager.repo, policy.RepoID)
		if err != nil {
			return NotePlan{}, err
		}
		var selected *noteOperation
		for index := range operations {
			if operations[index].Note.NoteID == options.NoteID && operations[index].Operation == NoteOperationCreate {
				selected = &operations[index]
				break
			}
		}
		if selected == nil {
			return NotePlan{}, fmt.Errorf("note %s is not publishable from this store", options.NoteID)
		}
		bundle, err := buildNoteBundle(manager.repo, identity, policy, digest, *selected, manager.repo.WorkspaceRoot.String())
		if err != nil {
			return NotePlan{}, err
		}
		if options.Approve {
			if err := approveNotesPolicy(manager.repo, policy, digest); err != nil {
				return NotePlan{}, err
			}
		}
		return NotePlan{
			Locator:          noteLocator(identity.DeviceID, bundle.Stored.Manifest.BundleID),
			PolicyHash:       digest,
			ApprovalRequired: policy.ApprovedHash != digest && !options.Approve,
			Bytes:            len(bundle.Manifest) + len(bundle.NoteJSON),
			Manifest:         bundle.Stored.Manifest,
			Note:             bundle.Stored.Note,
		}, nil
	})
}

// SyncNotes publishes or retrieves note bundles on the note ref namespace.
func (manager *Manager) SyncNotes(ctx context.Context, direction Direction) (Result, error) {
	if manager == nil || manager.repo == nil {
		return Result{}, fmt.Errorf("note sync requires checkpoint repo")
	}
	return withSharedHistoryLock(manager.repo, "sync notes", func() (Result, error) {
		switch direction {
		case DirectionPush:
			return manager.syncNotesPush(ctx)
		case DirectionPull:
			return manager.syncNotesPull(ctx)
		default:
			return Result{}, fmt.Errorf("unknown note sync direction %q", direction)
		}
	})
}

func (manager *Manager) syncNotesPush(ctx context.Context) (Result, error) {
	policy, err := loadNotesPolicy(manager.repo)
	if err != nil {
		return Result{}, err
	}
	if policy.Disabled {
		return Result{}, fmt.Errorf("note sharing is disabled; run turnal share notes enable to resume")
	}
	digest, err := notesPolicyHash(policy)
	if err != nil {
		return Result{}, err
	}
	if policy.ApprovedHash != digest {
		return Result{}, fmt.Errorf("note sharing policy is not approved; preview a note with --approve")
	}
	identity, err := loadOrCreateDevice(manager.repo)
	if err != nil {
		return Result{}, err
	}
	state, err := loadNotesState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignNotesStateScope(&state, policy.Remote, policy.RepoID)
	store, err := openNotesGitStore(ctx, manager.repo)
	if err != nil {
		return Result{}, err
	}

	// Durable local commits are the outbox. Push them before building more so a
	// network failure never causes a bundle to be regenerated.
	localHead, err := store.localHead(ctx)
	if err != nil {
		return Result{}, err
	}
	published := 0
	if localHead != "" {
		head, err := store.pushNotes(ctx, policy.Remote, identity.DeviceID, state.LastSeen[identity.DeviceID])
		if err != nil {
			return Result{Direction: DirectionPush}, err
		}
		published += promoteCommittedNotes(&state, head)
		state.LastSeen[identity.DeviceID] = head
		if err := saveNotesState(manager.repo, state); err != nil {
			return Result{}, err
		}
	}

	operations, err := listPublishableNotes(manager.repo, policy.RepoID)
	if err != nil {
		return Result{}, err
	}
	var bundles []builtNoteBundle
	blocked := 0
	for _, operation := range operations {
		bundleID, err := deriveNoteBundleID(policy.RepoID, operation.Note.NoteID, operation.Operation)
		if err != nil {
			return Result{}, err
		}
		if _, ok := state.Published[bundleID.String()]; ok {
			continue
		}
		if _, ok := state.Committed[bundleID.String()]; ok {
			continue
		}
		// A removal is only publishable once its create has been published;
		// otherwise a receiver would see a tombstone for a note it never saw.
		if operation.Operation == NoteOperationDelete {
			createID, err := deriveNoteBundleID(policy.RepoID, operation.Note.NoteID, NoteOperationCreate)
			if err != nil {
				return Result{}, err
			}
			_, createPublished := state.Published[createID.String()]
			_, createCommitted := state.Committed[createID.String()]
			if !createPublished && !createCommitted {
				continue
			}
		}
		bundle, err := buildNoteBundle(manager.repo, identity, policy, digest, operation, manager.repo.WorkspaceRoot.String())
		if err != nil {
			state.Blocked[bundleID.String()] = err.Error()
			blocked++
			continue
		}
		delete(state.Blocked, bundleID.String())
		bundles = append(bundles, bundle)
		if len(bundles) == MaxBundlesPerBatch {
			break
		}
	}
	if len(bundles) == 0 {
		if err := saveNotesState(manager.repo, state); err != nil {
			return Result{}, err
		}
		return Result{Direction: DirectionPush, Published: published, Blocked: blocked, Head: localHead}, nil
	}

	previousHead, err := store.localHead(ctx)
	if err != nil {
		return Result{}, err
	}
	batch := NoteBatch{
		SchemaVersion: NotesSchemaVersion,
		DeviceID:      identity.DeviceID,
		PublicKey:     identity.PublicKey,
		PreviousHead:  previousHead,
		CreatedAt:     manager.now(),
	}
	for _, bundle := range bundles {
		batch.Bundles = append(batch.Bundles, NoteBatchBundle{
			BundleID: bundle.Stored.Manifest.BundleID, Path: bundle.Path, RepoID: bundle.Stored.Manifest.RepoID,
			NoteID: bundle.Stored.Manifest.NoteID, Operation: bundle.Stored.Manifest.Operation,
		})
	}
	batch, err = signNoteBatch(identity, batch)
	if err != nil {
		return Result{}, err
	}
	head, err := store.commitNoteBatch(ctx, batch, bundles)
	if err != nil {
		return Result{}, err
	}
	for _, bundle := range bundles {
		state.Committed[bundle.Stored.Manifest.BundleID.String()] = head
	}
	if err := saveNotesState(manager.repo, state); err != nil {
		return Result{}, err
	}
	confirmed, err := store.pushNotes(ctx, policy.Remote, identity.DeviceID, state.LastSeen[identity.DeviceID])
	if err != nil {
		return Result{Direction: DirectionPush, Blocked: blocked, Head: head}, err
	}
	published += promoteCommittedNotes(&state, confirmed)
	state.LastSeen[identity.DeviceID] = confirmed
	if err := saveNotesState(manager.repo, state); err != nil {
		return Result{}, err
	}
	return Result{Direction: DirectionPush, Published: published, Blocked: blocked, Head: confirmed}, nil
}

func (manager *Manager) syncNotesPull(ctx context.Context) (Result, error) {
	policy, err := loadNotesPolicy(manager.repo)
	if err != nil {
		return Result{}, err
	}
	if policy.Disabled {
		return Result{}, fmt.Errorf("note sharing is disabled; run turnal share notes enable to resume")
	}
	state, err := loadNotesState(manager.repo)
	if err != nil {
		return Result{}, err
	}
	alignNotesStateScope(&state, policy.Remote, policy.RepoID)
	store, err := openNotesGitStore(ctx, manager.repo)
	if err != nil {
		return Result{}, err
	}
	refs, warnings, err := store.listRemoteNotes(ctx, policy.Remote)
	if err != nil {
		return Result{}, err
	}
	pulled := 0
	for _, ref := range refs {
		bundles, observedHead, err := store.fetchAndIngestNotes(ctx, policy.Remote, ref, state.LastSeen[ref.DeviceID], policy.RepoID)
		if err != nil {
			if ctx.Err() != nil || isRetryablePullError(err) {
				if saveErr := saveNotesState(manager.repo, state); saveErr != nil {
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
	if err := saveNotesState(manager.repo, state); err != nil {
		return Result{Direction: DirectionPull, Pulled: pulled}, err
	}
	result := Result{Direction: DirectionPull, Pulled: pulled, Warnings: warnings, Quarantined: cloneStringMap(state.Quarantined)}
	if len(result.Quarantined) > 0 {
		return result, fmt.Errorf("note pull quarantined %d publisher(s); inspect turnal share notes status", len(result.Quarantined))
	}
	return result, nil
}

func promoteCommittedNotes(state *notesStateFile, head string) int {
	promoted := 0
	for bundleID := range state.Committed {
		state.Published[bundleID] = head
		delete(state.Committed, bundleID)
		delete(state.Blocked, bundleID)
		promoted++
	}
	return promoted
}

// NoteListOptions filters published note bundles.
type NoteListOptions struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	// References selects notes replying to one turn-context locator, which is
	// how a reviewer follows a shared turn to the commentary about it.
	References string
}

// ListNotes returns pulled note bundles, folded so a published removal hides the
// note it names.
//
// Folding happens here rather than at ingest because create and delete are
// separate immutable publications that may arrive in either order, and a delete
// may arrive from a device whose create is still pending.
func (manager *Manager) ListNotes(ctx context.Context, options NoteListOptions) ([]NoteBundleSummary, error) {
	if manager == nil || manager.repo == nil {
		return nil, fmt.Errorf("note listing requires checkpoint repo")
	}
	policy, err := loadNotesPolicyForUpdate(manager.repo)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	root := filepath.Join(sharedRoot(manager.repo), "pulled-notes", policy.RepoID.String())
	var bundles []StoredNoteBundle
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		data, err := readRegularFile(path, MaxMaterializedLimit)
		if err != nil {
			return nil
		}
		var bundle StoredNoteBundle
		if err := decodeStrictJSON(data, &bundle); err != nil {
			return nil
		}
		bundles = append(bundles, bundle)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	tombstoned := make(map[string]struct{})
	for _, bundle := range bundles {
		if bundle.Manifest.Operation == NoteOperationDelete {
			tombstoned[bundle.Manifest.DeviceID+"\x00"+bundle.Manifest.NoteID.String()] = struct{}{}
		}
	}

	var summaries []NoteBundleSummary
	for _, bundle := range bundles {
		if bundle.Manifest.Operation != NoteOperationCreate {
			continue
		}
		// A removal only hides its own publisher's note.
		if _, hidden := tombstoned[bundle.Manifest.DeviceID+"\x00"+bundle.Manifest.NoteID.String()]; hidden {
			continue
		}
		if options.SessionID != "" && bundle.Manifest.Target.SessionID != options.SessionID {
			continue
		}
		if options.TurnID != 0 && bundle.Manifest.Target.TurnID != options.TurnID {
			continue
		}
		if options.References != "" && bundle.Manifest.References != options.References {
			continue
		}
		summary := NoteBundleSummary{
			Locator:    noteLocator(bundle.Manifest.DeviceID, bundle.Manifest.BundleID),
			NoteID:     bundle.Manifest.NoteID,
			Operation:  bundle.Manifest.Operation,
			DeviceID:   bundle.Manifest.DeviceID,
			SessionID:  bundle.Manifest.Target.SessionID,
			TurnID:     bundle.Manifest.Target.TurnID,
			References: bundle.Manifest.References,
			CreatedAt:  bundle.Manifest.CreatedAt,
		}
		if bundle.Note.Text != nil {
			summary.Text = bundle.Note.Text.Text
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].CreatedAt.Before(summaries[j].CreatedAt)
		}
		return summaries[i].Locator < summaries[j].Locator
	})
	return summaries, nil
}

// NotesForLocator returns published notes replying to one turn-context locator.
//
// share show reads a single named bundle, so a reverse lookup has to scan the
// pulled note index. A reference whose target bundle is absent locally is still
// returned: arrival order across devices is not guaranteed, and hiding the note
// would be worse than reporting an unresolved reference.
func (manager *Manager) NotesForLocator(ctx context.Context, locator string) ([]NoteBundleSummary, error) {
	if _, _, err := parseLocator(locator); err != nil {
		return nil, err
	}
	return manager.ListNotes(ctx, NoteListOptions{References: locator})
}

func countPulledNotes(repo *checkpoint.Repo, repoID primitives.RepoID) (int, error) {
	root := filepath.Join(sharedRoot(repo), "pulled-notes", repoID.String())
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return count, nil
}
