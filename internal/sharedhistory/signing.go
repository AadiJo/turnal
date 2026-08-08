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

	"github.com/AadiJo/turnal/internal/checkpoint"
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
	data, err := readRegularFile(path, 1<<20)
	if err == nil {
		return parseDevice(data)
	}
	if !os.IsNotExist(err) {
		return deviceIdentity{}, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return deviceIdentity{}, fmt.Errorf("generate shared history device key: %w", err)
	}
	identity := deviceIdentity{
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
	data, err := json.Marshal(unsignedManifest(manifest))
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
	data, err := json.Marshal(unsignedManifest(manifest))
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
	data, err := json.Marshal(unsignedBatch(batch))
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
	unsigned := batch
	unsigned.Signature = ""
	data, err := json.Marshal(unsignedBatch(unsigned))
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(public, data, signature) {
		return nil, fmt.Errorf("shared history batch signature verification failed")
	}
	return public, nil
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
