package sharedhistory

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func notesPolicyPath(repo *checkpoint.Repo) string {
	return filepath.Join(sharedRoot(repo), "notes-policy.json")
}

func notesStatePath(repo *checkpoint.Repo) string {
	return filepath.Join(sharedRoot(repo), "notes-state.json")
}

func notesPulledPath(repo *checkpoint.Repo, repoID primitives.RepoID, deviceID string, bundleID primitives.BundleID) string {
	return filepath.Join(sharedRoot(repo), "pulled-notes", repoID.String(), deviceID, bundleID.String()+".json")
}

// NotesConfigureOptions enables the note channel.
//
// The remote and repository are inherited from the turn-context policy when it
// is configured, because publishing notes to a different project than the turns
// they discuss would produce references that can never resolve.
type NotesConfigureOptions struct {
	PromptMode PromptMode
}

// ConfigureNotes enables note publication under its own policy hash.
//
// Note sharing is opt-in separately from turn-context sharing. A publisher who
// never enables it keeps their existing approval, and their receivers never see
// this ref namespace at all.
func ConfigureNotes(repo *checkpoint.Repo, options NotesConfigureOptions) (NotesStatus, error) {
	if repo == nil {
		return NotesStatus{}, fmt.Errorf("configure note sharing requires checkpoint repo")
	}
	return withSharedHistoryLock(repo, "configure note sharing", func() (NotesStatus, error) {
		base, err := loadPolicyForUpdate(repo)
		if err != nil {
			return NotesStatus{}, fmt.Errorf("note sharing requires shared history; run turnal share enable first: %w", err)
		}
		policy, err := loadNotesPolicyForUpdate(repo)
		if err != nil && !os.IsNotExist(err) {
			return NotesStatus{}, err
		}
		promptMode := options.PromptMode
		if promptMode == "" {
			if policy.PromptMode != "" {
				promptMode = policy.PromptMode
			} else {
				promptMode = base.PromptMode
			}
		}
		if err := validatePromptMode(promptMode); err != nil {
			return NotesStatus{}, err
		}
		policy = notesPolicyFile{
			Version:          1,
			Remote:           base.Remote,
			RepoID:           base.RepoID,
			PromptMode:       promptMode,
			AllowlistVersion: NotesAllowlistVersion,
			ScannerVersion:   ScannerVersion,
			FieldLimit:       DefaultFieldLimit,
			BundleLimit:      DefaultBundleLimit,
			ApprovedHash:     policy.ApprovedHash,
		}
		if err := writeJSONAtomic(notesPolicyPath(repo), policy, 0o600); err != nil {
			return NotesStatus{}, err
		}
		if _, err := loadOrCreateDevice(repo); err != nil {
			return NotesStatus{}, err
		}
		return notesStatusLocked(repo)
	})
}

// DisableNotes stops note synchronization without deleting anything. Copies
// already published cannot be recalled.
func DisableNotes(repo *checkpoint.Repo) (NotesStatus, error) {
	if repo == nil {
		return NotesStatus{}, fmt.Errorf("disable note sharing requires checkpoint repo")
	}
	return withSharedHistoryLock(repo, "disable note sharing", func() (NotesStatus, error) {
		policy, err := loadNotesPolicyForUpdate(repo)
		if err != nil {
			return NotesStatus{}, err
		}
		policy.Disabled = true
		if err := writeJSONAtomic(notesPolicyPath(repo), policy, 0o600); err != nil {
			return NotesStatus{}, err
		}
		return notesStatusLocked(repo)
	})
}

func loadNotesPolicy(repo *checkpoint.Repo) (notesPolicyFile, error) {
	policy, err := loadNotesPolicyForUpdate(repo)
	if err != nil {
		return notesPolicyFile{}, err
	}
	if !policy.Disabled && (policy.AllowlistVersion != NotesAllowlistVersion || policy.ScannerVersion != ScannerVersion) {
		return notesPolicyFile{}, fmt.Errorf("note sharing policy uses unavailable projection versions; rerun turnal share notes enable and approve the updated policy")
	}
	return policy, nil
}

func loadNotesPolicyForUpdate(repo *checkpoint.Repo) (notesPolicyFile, error) {
	data, err := readRegularFile(notesPolicyPath(repo), 1<<20)
	if err != nil {
		if os.IsNotExist(err) {
			return notesPolicyFile{}, err
		}
		return notesPolicyFile{}, err
	}
	var policy notesPolicyFile
	if err := json.Unmarshal(data, &policy); err != nil {
		return notesPolicyFile{}, fmt.Errorf("parse note sharing policy: %w", err)
	}
	if policy.Version != 1 {
		return notesPolicyFile{}, fmt.Errorf("unsupported note sharing policy version %d", policy.Version)
	}
	if strings.TrimSpace(policy.Remote) == "" {
		return notesPolicyFile{}, fmt.Errorf("note sharing policy remote is empty")
	}
	if _, err := primitives.ParseRepoID(policy.RepoID.String()); err != nil {
		return notesPolicyFile{}, fmt.Errorf("note sharing policy repository id is invalid: %w", err)
	}
	if err := validatePromptMode(policy.PromptMode); err != nil {
		return notesPolicyFile{}, err
	}
	if policy.FieldLimit <= 0 || policy.FieldLimit > DefaultFieldLimit {
		policy.FieldLimit = DefaultFieldLimit
	}
	if policy.BundleLimit <= 0 || policy.BundleLimit > DefaultBundleLimit {
		policy.BundleLimit = DefaultBundleLimit
	}
	return policy, nil
}

func notesPolicyHash(policy notesPolicyFile) (string, error) {
	input := struct {
		SchemaVersion int        `json:"schema_version"`
		RepoID        string     `json:"repo_id"`
		Remote        string     `json:"remote"`
		PromptMode    PromptMode `json:"prompt_mode"`
		Allowlist     string     `json:"allowlist_version"`
		Scanner       string     `json:"scanner_version"`
		FieldLimit    int        `json:"field_limit"`
		BundleLimit   int        `json:"bundle_limit"`
	}{NotesSchemaVersion, policy.RepoID.String(), publicRemoteIdentity(policy.Remote), policy.PromptMode, policy.AllowlistVersion, policy.ScannerVersion, policy.FieldLimit, policy.BundleLimit}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode note sharing policy hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func approveNotesPolicy(repo *checkpoint.Repo, policy notesPolicyFile, hash string) error {
	policy.ApprovedHash = hash
	return writeJSONAtomic(notesPolicyPath(repo), policy, 0o600)
}

func loadNotesState(repo *checkpoint.Repo) (notesStateFile, error) {
	state := notesStateFile{
		Version: 1, Committed: map[string]string{}, Published: map[string]string{},
		Blocked: map[string]string{}, LastSeen: map[string]string{}, Quarantined: map[string]string{},
	}
	data, err := readRegularFile(notesStatePath(repo), 8<<20)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return notesStateFile{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return notesStateFile{}, fmt.Errorf("parse note sharing state: %w", err)
	}
	if state.Version != 1 {
		return notesStateFile{}, fmt.Errorf("unsupported note sharing state version %d", state.Version)
	}
	for _, field := range []*map[string]string{&state.Committed, &state.Published, &state.Blocked, &state.LastSeen, &state.Quarantined} {
		if *field == nil {
			*field = map[string]string{}
		}
	}
	return state, nil
}

func saveNotesState(repo *checkpoint.Repo, state notesStateFile) error {
	state.Version = 1
	return writeJSONAtomic(notesStatePath(repo), state, 0o600)
}

func alignNotesStateScope(state *notesStateFile, remote string, repoID primitives.RepoID) {
	remote = publicRemoteIdentity(remote)
	if state.RepoID != repoID {
		state.RepoID = repoID
		state.Committed = map[string]string{}
		state.Published = map[string]string{}
		state.Blocked = map[string]string{}
		state.LastSeen = map[string]string{}
		state.Quarantined = map[string]string{}
	}
	if state.Remote != remote {
		state.Remote = remote
		state.LastSeen = map[string]string{}
		state.Quarantined = map[string]string{}
	}
}

// noteManifestSignatureV1 freezes the note manifest signing contract. References
// and target are inside it: an unsigned reference would let anyone with write
// access to the remote retarget a signed note at a different turn.
type noteManifestSignatureV1 struct {
	SchemaVersion    int                    `json:"schema_version"`
	BundleID         primitives.BundleID    `json:"bundle_id"`
	RepoID           primitives.RepoID      `json:"repo_id"`
	DeviceID         string                 `json:"device_id"`
	Operation        string                 `json:"operation"`
	NoteID           primitives.NoteID      `json:"note_id"`
	References       string                 `json:"references,omitempty"`
	Target           NoteTargetProjection   `json:"target"`
	PolicyHash       string                 `json:"policy_hash"`
	PromptMode       PromptMode             `json:"prompt_mode"`
	EvidenceClass    string                 `json:"evidence_class"`
	AllowlistVersion string                 `json:"allowlist_version,omitempty"`
	ScannerVersion   string                 `json:"scanner_version,omitempty"`
	ProducerVersion  string                 `json:"producer_version,omitempty"`
	Omissions        map[string]int         `json:"omissions,omitempty"`
	Redactions       map[string]int         `json:"redactions,omitempty"`
	Truncations      truncationsSignatureV1 `json:"truncations"`
	ContentHashes    map[string]string      `json:"content_hashes"`
	CreatedAt        string                 `json:"created_at"`
	Signature        string                 `json:"signature"`
}

func noteManifestSigningPayloadV1(manifest NoteManifest) noteManifestSignatureV1 {
	return noteManifestSignatureV1{
		SchemaVersion: manifest.SchemaVersion, BundleID: manifest.BundleID, RepoID: manifest.RepoID,
		DeviceID: manifest.DeviceID, Operation: manifest.Operation, NoteID: manifest.NoteID,
		References: manifest.References, Target: manifest.Target,
		PolicyHash: manifest.PolicyHash, PromptMode: manifest.PromptMode, EvidenceClass: manifest.EvidenceClass,
		AllowlistVersion: manifest.AllowlistVersion, ScannerVersion: manifest.ScannerVersion,
		ProducerVersion: manifest.ProducerVersion,
		Omissions:       manifest.Omissions, Redactions: manifest.Redactions,
		Truncations:   truncationsSignatureV1{Count: manifest.Truncations.Count, OriginalBytes: manifest.Truncations.OriginalBytes},
		ContentHashes: manifest.ContentHashes,
		CreatedAt:     manifest.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
}

func signNoteManifest(identity deviceIdentity, manifest NoteManifest) (NoteManifest, error) {
	manifest.Signature = ""
	data, err := json.Marshal(noteManifestSigningPayloadV1(manifest))
	if err != nil {
		return NoteManifest{}, fmt.Errorf("encode note manifest signature: %w", err)
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(identity.private, data))
	return manifest, nil
}

func verifyNoteManifest(public ed25519.PublicKey, manifest NoteManifest) error {
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("note manifest signature is invalid")
	}
	manifest.Signature = ""
	data, err := json.Marshal(noteManifestSigningPayloadV1(manifest))
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, data, signature) {
		return fmt.Errorf("note manifest signature verification failed")
	}
	return nil
}

type noteBatchSignatureV1 struct {
	SchemaVersion int               `json:"schema_version"`
	DeviceID      string            `json:"device_id"`
	PublicKey     string            `json:"public_key"`
	PreviousHead  string            `json:"previous_head,omitempty"`
	Bundles       []NoteBatchBundle `json:"bundles"`
	CreatedAt     string            `json:"created_at"`
	Signature     string            `json:"signature"`
}

func noteBatchSigningPayloadV1(batch NoteBatch) noteBatchSignatureV1 {
	return noteBatchSignatureV1{
		SchemaVersion: batch.SchemaVersion, DeviceID: batch.DeviceID, PublicKey: batch.PublicKey,
		PreviousHead: batch.PreviousHead, Bundles: batch.Bundles,
		CreatedAt: batch.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
}

func signNoteBatch(identity deviceIdentity, batch NoteBatch) (NoteBatch, error) {
	batch.Signature = ""
	data, err := json.Marshal(noteBatchSigningPayloadV1(batch))
	if err != nil {
		return NoteBatch{}, fmt.Errorf("encode note batch signature: %w", err)
	}
	batch.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(identity.private, data))
	return batch, nil
}

func verifyNoteBatch(batch NoteBatch) (ed25519.PublicKey, error) {
	public, err := publicKeyForDevice(batch.PublicKey, batch.DeviceID)
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawStdEncoding.DecodeString(batch.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("note batch signature is invalid")
	}
	batch.Signature = ""
	data, err := json.Marshal(noteBatchSigningPayloadV1(batch))
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(public, data, signature) {
		return nil, fmt.Errorf("note batch signature verification failed")
	}
	return public, nil
}
