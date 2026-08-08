package sharedhistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
)

func Configure(repo *checkpoint.Repo, remote string, promptMode PromptMode) (Status, error) {
	if repo == nil {
		return Status{}, fmt.Errorf("configure shared history requires checkpoint repo")
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return Status{}, fmt.Errorf("shared history remote is required")
	}
	if strings.ContainsAny(remote, "\r\n\x00") {
		return Status{}, fmt.Errorf("shared history remote contains an invalid control character")
	}
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Status{}, err
	}
	if err := validatePromptMode(promptMode); err != nil {
		return Status{}, err
	}
	policy := policyFile{
		Version:          1,
		Remote:           remote,
		PromptMode:       promptMode,
		AllowlistVersion: AllowlistVersion,
		ScannerVersion:   ScannerVersion,
		FieldLimit:       DefaultFieldLimit,
		BundleLimit:      DefaultBundleLimit,
	}
	if err := writeJSONAtomic(policyPath(repo), policy, 0o600); err != nil {
		return Status{}, err
	}
	if _, err := loadOrCreateDevice(repo); err != nil {
		return Status{}, err
	}
	return New(repo).Status(nilContext())
}

func normalizeRemote(remote string) (string, error) {
	if strings.Contains(remote, "://") || looksLikeSCPRemote(remote) || filepath.IsAbs(remote) {
		return remote, nil
	}
	absolute, err := filepath.Abs(remote)
	if err != nil {
		return "", fmt.Errorf("resolve shared history remote: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func looksLikeSCPRemote(remote string) bool {
	colon := strings.IndexByte(remote, ':')
	if colon <= 0 {
		return false
	}
	prefix := remote[:colon]
	return !strings.ContainsAny(prefix, `/\\`)
}

func validatePromptMode(mode PromptMode) error {
	switch mode {
	case PromptModeRedactedText, PromptModeOmit:
		return nil
	default:
		return fmt.Errorf("invalid prompt mode %q; expected redacted_text or omit", mode)
	}
}

func loadPolicy(repo *checkpoint.Repo) (policyFile, error) {
	data, err := readRegularFile(policyPath(repo), 1<<20)
	if err != nil {
		if os.IsNotExist(err) {
			return policyFile{}, fmt.Errorf("shared history is not configured; run turnal share enable")
		}
		return policyFile{}, err
	}
	var policy policyFile
	if err := json.Unmarshal(data, &policy); err != nil {
		return policyFile{}, fmt.Errorf("parse shared history policy: %w", err)
	}
	if policy.Version != 1 {
		return policyFile{}, fmt.Errorf("unsupported shared history policy version %d", policy.Version)
	}
	if strings.TrimSpace(policy.Remote) == "" {
		return policyFile{}, fmt.Errorf("shared history policy remote is empty")
	}
	if err := validatePromptMode(policy.PromptMode); err != nil {
		return policyFile{}, err
	}
	if policy.AllowlistVersion != AllowlistVersion || policy.ScannerVersion != ScannerVersion {
		return policyFile{}, fmt.Errorf("shared history policy uses unavailable projection versions")
	}
	if policy.FieldLimit <= 0 || policy.FieldLimit > DefaultFieldLimit {
		return policyFile{}, fmt.Errorf("shared history field limit must be between 1 and %d", DefaultFieldLimit)
	}
	if policy.BundleLimit <= 0 || policy.BundleLimit > DefaultBundleLimit {
		return policyFile{}, fmt.Errorf("shared history bundle limit must be between 1 and %d", DefaultBundleLimit)
	}
	return policy, nil
}

func policyHash(repo *checkpoint.Repo, policy policyFile) (string, error) {
	input := struct {
		SchemaVersion int        `json:"schema_version"`
		RepoID        string     `json:"repo_id"`
		Remote        string     `json:"remote"`
		PromptMode    PromptMode `json:"prompt_mode"`
		Allowlist     string     `json:"allowlist_version"`
		Scanner       string     `json:"scanner_version"`
		FieldLimit    int        `json:"field_limit"`
		BundleLimit   int        `json:"bundle_limit"`
	}{SchemaVersion, repo.RepoID.String(), policy.Remote, policy.PromptMode, policy.AllowlistVersion, policy.ScannerVersion, policy.FieldLimit, policy.BundleLimit}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode shared history policy hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func approvePolicy(repo *checkpoint.Repo, policy policyFile, hash string) error {
	policy.ApprovedHash = hash
	return writeJSONAtomic(policyPath(repo), policy, 0o600)
}

func sharedRoot(repo *checkpoint.Repo) string {
	return filepath.Join(repo.MetadataDir, "shared-history")
}

func policyPath(repo *checkpoint.Repo) string {
	return filepath.Join(sharedRoot(repo), "policy.json")
}

func statePath(repo *checkpoint.Repo) string {
	return filepath.Join(sharedRoot(repo), "state.json")
}

func loadState(repo *checkpoint.Repo) (stateFile, error) {
	state := stateFile{Version: 1, Committed: map[string]string{}, Published: map[string]string{}, Blocked: map[string]string{}, LastSeen: map[string]string{}}
	data, err := readRegularFile(statePath(repo), 8<<20)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return stateFile{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return stateFile{}, fmt.Errorf("parse shared history state: %w", err)
	}
	if state.Version != 1 {
		return stateFile{}, fmt.Errorf("unsupported shared history state version %d", state.Version)
	}
	if state.Published == nil {
		state.Published = map[string]string{}
	}
	if state.Committed == nil {
		state.Committed = map[string]string{}
	}
	if state.Blocked == nil {
		state.Blocked = map[string]string{}
	}
	if state.LastSeen == nil {
		state.LastSeen = map[string]string{}
	}
	return state, nil
}

func saveState(repo *checkpoint.Repo, state stateFile) error {
	state.Version = 1
	return writeJSONAtomic(statePath(repo), state, 0o600)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, mode)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create shared history directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".turnal-shared-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary shared history file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install shared history file %s: %w", path, err)
	}
	return nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("shared history file must be regular: %s", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("shared history file exceeds %d bytes: %s", limit, path)
	}
	return os.ReadFile(path)
}

// nilContext keeps Configure independent from caller cancellation while using
// the same public Status path as normal commands.
func nilContext() context.Context { return context.Background() }
