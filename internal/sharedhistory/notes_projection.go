package sharedhistory

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
)

type builtNoteBundle struct {
	Stored   StoredNoteBundle
	NoteJSON []byte
	Manifest []byte
	Path     string
}

// noteOperation is one publishable note action.
//
// Create and delete are separate immutable publications rather than a rewrite
// of one path, because a published bundle can never change: receivers treat a
// changed bundle under a known id as tampering. Folding them back into a
// surviving set is the receiver's job.
type noteOperation struct {
	Operation string
	Note      notes.Note
}

func noteBundlePath(bundleID primitives.BundleID) string {
	value := bundleID.String()
	digest := strings.TrimPrefix(value, "bundle_")
	return filepath.ToSlash(filepath.Join("notes", digest[:2], value))
}

func noteLocator(deviceID string, bundleID primitives.BundleID) string {
	return "v1n:" + deviceID + ":" + bundleID.String()
}

func parseNoteLocator(value string) (string, primitives.BundleID, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 || parts[0] != "v1n" || len(parts[1]) != 32 {
		return "", "", fmt.Errorf("invalid note locator %q", value)
	}
	for _, character := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", "", fmt.Errorf("invalid device id in note locator")
		}
	}
	bundleID, err := primitives.ParseBundleID(parts[2])
	if err != nil {
		return "", "", err
	}
	return parts[1], bundleID, nil
}

// deriveNoteBundleID keys a bundle on the note and the operation, so a later
// delete never collides with the create it hides.
func deriveNoteBundleID(repoID primitives.RepoID, noteID primitives.NoteID, operation string) (primitives.BundleID, error) {
	parsedRepoID, err := primitives.ParseRepoID(repoID.String())
	if err != nil {
		return "", err
	}
	parsedNoteID, err := primitives.ParseNoteID(noteID.String())
	if err != nil {
		return "", err
	}
	if operation != NoteOperationCreate && operation != NoteOperationDelete {
		return "", fmt.Errorf("unknown note operation %q", operation)
	}
	return primitives.DeriveBundleID(parsedRepoID, mustNoteStreamID(parsedNoteID, operation), noteOperationTurnID())
}

// mustNoteStreamID reuses the bundle-id derivation by projecting the note and
// operation into the stream position. It keeps one derivation function rather
// than introducing a second hashing scheme with its own collision surface.
func mustNoteStreamID(noteID primitives.NoteID, operation string) primitives.EventStreamID {
	digest := sha256Bytes([]byte("turnal-note-bundle-v1\x00" + noteID.String() + "\x00" + operation))
	streamID, err := primitives.ParseEventStreamID("stream_" + strings.TrimPrefix(digest, "sha256:")[:32])
	if err != nil {
		panic(err)
	}
	return streamID
}

func noteOperationTurnID() primitives.TurnID {
	turnID, err := primitives.NewTurnID(1)
	if err != nil {
		panic(err)
	}
	return turnID
}

// listPublishableNotes returns note operations eligible for publication.
//
// Only notes authored in this store are publishable: a pulled note belongs to
// its own publisher's ref, and republishing it would forge that author's words
// under this device's signature.
func listPublishableNotes(repo *checkpoint.Repo, repoID primitives.RepoID) ([]noteOperation, error) {
	events, err := notes.ReadEvents(repo)
	if err != nil {
		return nil, err
	}
	surviving := notes.Project(events, notes.Query{})
	survivingByID := make(map[primitives.NoteID]notes.Note, len(surviving))
	for _, note := range surviving {
		survivingByID[note.NoteID] = note
	}

	var operations []noteOperation
	created := make(map[primitives.NoteID]notes.Note)
	var order []primitives.NoteID
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeNoteCreate:
			payload, err := notes.ParseCreatePayload(event.Payload)
			if err != nil {
				continue
			}
			if payload.Target.RepoID != repoID {
				continue
			}
			if _, exists := created[payload.NoteID]; exists {
				continue
			}
			note := notes.Note{
				NoteID: payload.NoteID, Target: payload.Target, Text: payload.Text,
				Anchor: payload.Anchor, Author: payload.Author, Redacted: payload.Redacted,
				CreatedAt: event.Time, StreamID: event.StreamID, Seq: event.Seq,
			}
			created[payload.NoteID] = note
			order = append(order, payload.NoteID)
		}
	}
	for _, noteID := range order {
		note := created[noteID]
		// A note whose body was withheld locally has nothing publishable to say.
		if note.Redacted {
			continue
		}
		operations = append(operations, noteOperation{Operation: NoteOperationCreate, Note: note})
		if _, alive := survivingByID[noteID]; !alive {
			operations = append(operations, noteOperation{Operation: NoteOperationDelete, Note: note})
		}
	}
	sort.SliceStable(operations, func(i, j int) bool {
		if !operations[i].Note.CreatedAt.Time.Equal(operations[j].Note.CreatedAt.Time) {
			return operations[i].Note.CreatedAt.Time.Before(operations[j].Note.CreatedAt.Time)
		}
		return operations[i].Operation < operations[j].Operation
	})
	return operations, nil
}

// buildNoteBundle projects one note operation into its signed publishable form.
func buildNoteBundle(repo *checkpoint.Repo, identity deviceIdentity, policy notesPolicyFile, policyDigest string, operation noteOperation, workspaceRoot string) (builtNoteBundle, error) {
	bundleID, err := deriveNoteBundleID(policy.RepoID, operation.Note.NoteID, operation.Operation)
	if err != nil {
		return builtNoteBundle{}, err
	}
	omissions := map[string]int{}
	redactions := map[string]int{}
	truncations := Truncations{}

	target := NoteTargetProjection{
		RepoID:    operation.Note.Target.RepoID,
		StreamID:  operation.Note.Target.StreamID,
		SessionID: operation.Note.Target.SessionID,
		TurnID:    operation.Note.Target.TurnID,
	}
	if operation.Note.Target.Locator != "" {
		if _, _, err := parseLocator(operation.Note.Target.Locator); err != nil {
			return builtNoteBundle{}, fmt.Errorf("note target locator is not a shared history locator: %w", err)
		}
		target.Locator = operation.Note.Target.Locator
	}

	projection := NoteProjection{
		SchemaVersion: NotesSchemaVersion,
		Operation:     operation.Operation,
		NoteID:        operation.Note.NoteID,
		Target:        target,
		CreatedAt:     operation.Note.CreatedAt.Time,
	}

	if operation.Operation == NoteOperationCreate {
		if policy.PromptMode == PromptModeMetadataOnly {
			omissions["note_text_policy"]++
		} else {
			text := sanitizeText(workspaceRoot, operation.Note.Text, policy.FieldLimit, &truncations)
			recordRedactions(redactions, text)
			projection.Text = &TextProjection{
				Text: text.Text, Redacted: operation.Note.Redacted || text.Redacted,
				Truncated: text.Truncated, Bytes: text.OriginalBytes,
			}
		}
		if operation.Note.Anchor != nil {
			path := sanitizeIdentifier(workspaceRoot, operation.Note.Anchor.Path.String(), &truncations)
			recordRedactions(redactions, path)
			if path.Text != "" {
				projection.Anchor = &NoteAnchorProjection{
					Path:      path.Text,
					LineStart: operation.Note.Anchor.LineStart,
					LineEnd:   operation.Note.Anchor.LineEnd,
				}
			} else {
				omissions["note_anchor_path"]++
			}
			// The anchor digest binds file content, so it is deliberately local.
			// Publishing it would let a receiver confirm guessed line contents.
			omissions["note_anchor_digest"]++
		}
		if operation.Note.Author != "" {
			omissions["note_author"]++
		}
	}

	noteJSON, err := json.Marshal(projection)
	if err != nil {
		return builtNoteBundle{}, fmt.Errorf("encode note projection: %w", err)
	}
	noteJSON = append(noteJSON, '\n')

	manifest := NoteManifest{
		SchemaVersion:    NotesSchemaVersion,
		BundleID:         bundleID,
		RepoID:           policy.RepoID,
		DeviceID:         identity.DeviceID,
		Operation:        operation.Operation,
		NoteID:           operation.Note.NoteID,
		References:       target.Locator,
		Target:           target,
		PolicyHash:       policyDigest,
		PromptMode:       policy.PromptMode,
		EvidenceClass:    EvidencePublisherClaim,
		AllowlistVersion: policy.AllowlistVersion,
		ScannerVersion:   policy.ScannerVersion,
		ProducerVersion:  producerVersion(),
		Omissions:        omissions,
		Redactions:       redactions,
		Truncations:      truncations,
		ContentHashes:    map[string]string{"note.json": sha256Bytes(noteJSON)},
		CreatedAt:        operation.Note.CreatedAt.Time,
	}
	manifest, err = signNoteManifest(identity, manifest)
	if err != nil {
		return builtNoteBundle{}, err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return builtNoteBundle{}, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if len(manifestJSON)+len(noteJSON) > policy.BundleLimit {
		return builtNoteBundle{}, fmt.Errorf("note bundle %s is %d bytes after projection; limit is %d", bundleID, len(manifestJSON)+len(noteJSON), policy.BundleLimit)
	}
	return builtNoteBundle{
		Stored:   StoredNoteBundle{Manifest: manifest, Note: projection, PublicKey: identity.PublicKey},
		NoteJSON: noteJSON,
		Manifest: manifestJSON,
		Path:     noteBundlePath(bundleID),
	}, nil
}

// validateNoteManifest enforces the policy a manifest declares. A signature
// proves who published a bundle, not that the bundle honors its own policy.
func validateNoteManifest(repoID primitives.RepoID, deviceID string, item NoteBatchBundle, manifest NoteManifest) error {
	if manifest.SchemaVersion != NotesSchemaVersion || manifest.EvidenceClass != EvidencePublisherClaim {
		return fmt.Errorf("unsupported note schema or evidence class")
	}
	if err := validatePromptMode(manifest.PromptMode); err != nil {
		return err
	}
	if manifest.BundleID != item.BundleID || manifest.DeviceID != deviceID || item.RepoID != repoID || manifest.RepoID != item.RepoID {
		return fmt.Errorf("note manifest identity does not match its batch and repository")
	}
	if manifest.NoteID != item.NoteID || manifest.Operation != item.Operation {
		return fmt.Errorf("note manifest identity does not match its batch")
	}
	if manifest.Operation != NoteOperationCreate && manifest.Operation != NoteOperationDelete {
		return fmt.Errorf("unknown note operation %q", manifest.Operation)
	}
	wantBundleID, err := deriveNoteBundleID(manifest.RepoID, manifest.NoteID, manifest.Operation)
	if err != nil || wantBundleID != manifest.BundleID {
		return fmt.Errorf("note bundle id is not derived from its repository, note, and operation")
	}
	if manifest.Target.RepoID != manifest.RepoID {
		return fmt.Errorf("note target names a different repository")
	}
	if _, err := primitives.ParseSessionID(manifest.Target.SessionID.String()); err != nil {
		return fmt.Errorf("note target session is invalid: %w", err)
	}
	if _, err := primitives.NewTurnID(manifest.Target.TurnID.Uint64()); err != nil {
		return fmt.Errorf("note target turn is invalid: %w", err)
	}
	if manifest.References != "" {
		if _, _, err := parseLocator(manifest.References); err != nil {
			return fmt.Errorf("note reference is not a shared history locator: %w", err)
		}
		if manifest.References != manifest.Target.Locator {
			return fmt.Errorf("note reference does not match its target locator")
		}
	}
	if len(manifest.ContentHashes) != 1 || manifest.ContentHashes["note.json"] == "" {
		return fmt.Errorf("note manifest must record exactly one content hash")
	}
	return nil
}

// validateNoteProjection rejects a bundle that carries more than the policy it
// declares allows, even when the publisher signed it consistently.
func validateNoteProjection(manifest NoteManifest, note NoteProjection) error {
	if note.SchemaVersion != NotesSchemaVersion {
		return fmt.Errorf("unsupported note projection schema")
	}
	if note.NoteID != manifest.NoteID || note.Operation != manifest.Operation {
		return fmt.Errorf("note projection identity does not match its manifest")
	}
	if note.Target != manifest.Target {
		return fmt.Errorf("note projection target does not match its manifest")
	}
	if note.Operation == NoteOperationDelete {
		if note.Text != nil || note.Anchor != nil {
			return fmt.Errorf("note removal must not carry text or an anchor")
		}
		return nil
	}
	if manifest.PromptMode == PromptModeMetadataOnly && note.Text != nil {
		return fmt.Errorf("metadata_only note bundle carries note text")
	}
	if note.Text != nil {
		if err := validateTextProjection(*note.Text); err != nil {
			return fmt.Errorf("invalid note text: %w", err)
		}
	}
	if note.Anchor != nil {
		if note.Anchor.Path == "" || len(note.Anchor.Path) > DefaultFieldLimit || containsControl(note.Anchor.Path) {
			return fmt.Errorf("invalid note anchor path")
		}
		if note.Anchor.LineStart < 0 || note.Anchor.LineEnd < 0 {
			return fmt.Errorf("invalid note anchor range")
		}
		if note.Anchor.LineEnd != 0 && note.Anchor.LineEnd < note.Anchor.LineStart {
			return fmt.Errorf("inverted note anchor range")
		}
	}
	return nil
}
