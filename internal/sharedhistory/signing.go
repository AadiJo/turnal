package sharedhistory

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

type deviceIdentity struct {
	Version    int    `json:"version"`
	DeviceID   string `json:"device_id"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	public     ed25519.PublicKey
	private    ed25519.PrivateKey
}

func loadOrCreateDevice(repo *checkpoint.Repo) (deviceIdentity, error) {
	path := filepath.Join(sharedRoot(repo), "device.json")
	identity, err := loadDevice(repo)
	if err == nil {
		return identity, nil
	}
	if !os.IsNotExist(err) {
		return deviceIdentity{}, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return deviceIdentity{}, fmt.Errorf("generate shared history device key: %w", err)
	}
	identity = deviceIdentity{
		Version:    1,
		DeviceID:   deviceID(public),
		PublicKey:  base64.RawStdEncoding.EncodeToString(public),
		PrivateKey: base64.RawStdEncoding.EncodeToString(private),
		public:     public,
		private:    private,
	}
	if err := writeJSONAtomic(path, identity, 0o600); err != nil {
		return deviceIdentity{}, err
	}
	return identity, nil
}

func loadDevice(repo *checkpoint.Repo) (deviceIdentity, error) {
	data, err := readRegularFile(filepath.Join(sharedRoot(repo), "device.json"), 1<<20)
	if err != nil {
		return deviceIdentity{}, err
	}
	return parseDevice(data)
}

func parseDevice(data []byte) (deviceIdentity, error) {
	var identity deviceIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return deviceIdentity{}, fmt.Errorf("parse shared history device identity: %w", err)
	}
	if identity.Version != 1 {
		return deviceIdentity{}, fmt.Errorf("unsupported shared history device identity version %d", identity.Version)
	}
	public, err := base64.RawStdEncoding.DecodeString(identity.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return deviceIdentity{}, fmt.Errorf("shared history device public key is invalid")
	}
	private, err := base64.RawStdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return deviceIdentity{}, fmt.Errorf("shared history device private key is invalid")
	}
	wantID := deviceID(public)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(identity.DeviceID)), []byte(wantID)) != 1 {
		return deviceIdentity{}, fmt.Errorf("shared history device id does not match its public key")
	}
	if subtle.ConstantTimeCompare(private[32:], public) != 1 {
		return deviceIdentity{}, fmt.Errorf("shared history device keypair does not match")
	}
	identity.DeviceID = wantID
	identity.public = ed25519.PublicKey(append([]byte(nil), public...))
	identity.private = ed25519.PrivateKey(append([]byte(nil), private...))
	return identity, nil
}

func deviceID(public []byte) string {
	digest := sha256.Sum256(public)
	return hex.EncodeToString(digest[:16])
}

func signManifest(identity deviceIdentity, manifest Manifest) (Manifest, error) {
	manifest.Signature = ""
	data, err := json.Marshal(manifestSigningPayloadV1(manifest))
	if err != nil {
		return Manifest{}, fmt.Errorf("encode shared history manifest signature: %w", err)
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(identity.private, data))
	return manifest, nil
}

func verifyManifest(public ed25519.PublicKey, manifest Manifest) error {
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("shared history manifest signature is invalid")
	}
	manifest.Signature = ""
	data, err := json.Marshal(manifestSigningPayloadV1(manifest))
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, data, signature) {
		return fmt.Errorf("shared history manifest signature verification failed")
	}
	return nil
}

func signBatch(identity deviceIdentity, batch Batch) (Batch, error) {
	batch.Signature = ""
	data, err := json.Marshal(batchSigningPayloadV1(batch))
	if err != nil {
		return Batch{}, fmt.Errorf("encode shared history batch signature: %w", err)
	}
	batch.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(identity.private, data))
	return batch, nil
}

func verifyBatch(batch Batch) (ed25519.PublicKey, error) {
	public, err := publicKeyForDevice(batch.PublicKey, batch.DeviceID)
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawStdEncoding.DecodeString(batch.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("shared history batch signature is invalid")
	}
	batch.Signature = ""
	data, err := json.Marshal(batchSigningPayloadV1(batch))
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(public, data, signature) {
		return nil, fmt.Errorf("shared history batch signature verification failed")
	}
	return public, nil
}

// These payloads freeze the version 1 signature contract independently from
// the in-memory structs. Adding fields for a future schema must not silently
// change the bytes used to verify already-published version 1 history.
type manifestSignatureV1 struct {
	SchemaVersion    int                        `json:"schema_version"`
	BundleID         primitives.BundleID        `json:"bundle_id"`
	RepoID           primitives.RepoID          `json:"repo_id"`
	DeviceID         string                     `json:"device_id"`
	ProducerID       primitives.EventProducerID `json:"producer_id"`
	StoreID          primitives.StoreID         `json:"store_id"`
	WorktreeID       primitives.WorktreeID      `json:"worktree_id"`
	StreamID         primitives.EventStreamID   `json:"stream_id"`
	SessionID        primitives.SessionID       `json:"session_id"`
	TurnID           primitives.TurnID          `json:"turn_id"`
	SourceSequence   sequenceRangeSignatureV1   `json:"source_sequence_range"`
	SourceRefs       []sourceRefSignatureV1     `json:"source_ref"`
	PolicyHash       string                     `json:"policy_hash"`
	PromptMode       PromptMode                 `json:"prompt_mode"`
	EvidenceClass    string                     `json:"evidence_class"`
	AllowlistVersion string                     `json:"allowlist_version,omitempty"`
	ScannerVersion   string                     `json:"scanner_version,omitempty"`
	ProducerVersion  string                     `json:"producer_version,omitempty"`
	SourceLinks      []sourceLinkSignatureV1    `json:"source_links,omitempty"`
	Omissions        map[string]int             `json:"omissions"`
	Redactions       map[string]int             `json:"redactions,omitempty"`
	Truncations      truncationsSignatureV1     `json:"truncations"`
	ContentHashes    map[string]string          `json:"content_hashes"`
	CreatedAt        time.Time                  `json:"created_at"`
	// Signature is always empty in the signing bytes: a signature cannot cover
	// itself. The field is part of the frozen v1 payload, so it must keep
	// serializing as "" rather than being removed.
	Signature string `json:"signature"`
}

type sequenceRangeSignatureV1 struct {
	First primitives.EventSeq `json:"first"`
	Last  primitives.EventSeq `json:"last"`
}

type sourceRefSignatureV1 struct {
	StreamID primitives.EventStreamID `json:"stream_id"`
	Seq      primitives.EventSeq      `json:"seq"`
	Hash     primitives.EventHash     `json:"hash"`
}

type sourceLinkSignatureV1 struct {
	CommitSHA  string `json:"commit_sha,omitempty"`
	Checkpoint string `json:"checkpoint_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

type truncationsSignatureV1 struct {
	Count         int `json:"count"`
	OriginalBytes int `json:"original_bytes"`
}

func manifestSigningPayloadV1(manifest Manifest) manifestSignatureV1 {
	var sourceRefs []sourceRefSignatureV1
	if manifest.SourceRefs != nil {
		sourceRefs = make([]sourceRefSignatureV1, 0, len(manifest.SourceRefs))
		for _, ref := range manifest.SourceRefs {
			sourceRefs = append(sourceRefs, sourceRefSignatureV1{StreamID: ref.StreamID, Seq: ref.Seq, Hash: ref.Hash})
		}
	}
	var sourceLinks []sourceLinkSignatureV1
	if manifest.SourceLinks != nil {
		sourceLinks = make([]sourceLinkSignatureV1, 0, len(manifest.SourceLinks))
		for _, link := range manifest.SourceLinks {
			sourceLinks = append(sourceLinks, sourceLinkSignatureV1{CommitSHA: link.CommitSHA, Checkpoint: link.Checkpoint, Branch: link.Branch})
		}
	}
	return manifestSignatureV1{
		SchemaVersion: manifest.SchemaVersion, BundleID: manifest.BundleID, RepoID: manifest.RepoID,
		DeviceID: manifest.DeviceID, ProducerID: manifest.ProducerID, StoreID: manifest.StoreID,
		WorktreeID: manifest.WorktreeID, StreamID: manifest.StreamID, SessionID: manifest.SessionID,
		TurnID:         manifest.TurnID,
		SourceSequence: sequenceRangeSignatureV1{First: manifest.SourceSequence.First, Last: manifest.SourceSequence.Last},
		SourceRefs:     sourceRefs,
		PolicyHash:     manifest.PolicyHash, PromptMode: manifest.PromptMode, EvidenceClass: manifest.EvidenceClass,
		AllowlistVersion: manifest.AllowlistVersion, ScannerVersion: manifest.ScannerVersion,
		ProducerVersion: manifest.ProducerVersion,
		SourceLinks:     sourceLinks, Omissions: manifest.Omissions, Redactions: manifest.Redactions,
		Truncations:   truncationsSignatureV1{Count: manifest.Truncations.Count, OriginalBytes: manifest.Truncations.OriginalBytes},
		ContentHashes: manifest.ContentHashes, CreatedAt: manifest.CreatedAt,
	}
}

type batchSignatureV1 struct {
	SchemaVersion int                      `json:"schema_version"`
	DeviceID      string                   `json:"device_id"`
	PublicKey     string                   `json:"public_key"`
	PreviousHead  string                   `json:"previous_head,omitempty"`
	Bundles       []batchBundleSignatureV1 `json:"bundles"`
	CreatedAt     time.Time                `json:"created_at"`
	Signature     string                   `json:"signature"`
}

type batchBundleSignatureV1 struct {
	BundleID  primitives.BundleID      `json:"bundle_id"`
	Path      string                   `json:"path"`
	RepoID    primitives.RepoID        `json:"repo_id"`
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    primitives.TurnID        `json:"turn_id"`
	Sequence  sequenceRangeSignatureV1 `json:"sequence_range"`
}

func batchSigningPayloadV1(batch Batch) batchSignatureV1 {
	var bundles []batchBundleSignatureV1
	if batch.Bundles != nil {
		bundles = make([]batchBundleSignatureV1, 0, len(batch.Bundles))
		for _, item := range batch.Bundles {
			bundles = append(bundles, batchBundleSignatureV1{
				BundleID: item.BundleID, Path: item.Path, RepoID: item.RepoID,
				SessionID: item.SessionID, TurnID: item.TurnID,
				Sequence: sequenceRangeSignatureV1{First: item.Sequence.First, Last: item.Sequence.Last},
			})
		}
	}
	return batchSignatureV1{
		SchemaVersion: batch.SchemaVersion, DeviceID: batch.DeviceID, PublicKey: batch.PublicKey,
		PreviousHead: batch.PreviousHead, Bundles: bundles, CreatedAt: batch.CreatedAt,
	}
}

func publicKeyForDevice(encoded, expectedDeviceID string) (ed25519.PublicKey, error) {
	public, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("shared history public key is invalid")
	}
	if expectedDeviceID != deviceID(public) {
		return nil, fmt.Errorf("shared history device id does not match public key")
	}
	return ed25519.PublicKey(public), nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
