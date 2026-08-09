package sharedhistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
)

type ConfigureOptions struct {
	Remote                 string
	PromptMode             PromptMode
	RepoID                 primitives.RepoID
	IncludeExistingHistory bool
}

func Configure(repo *checkpoint.Repo, options ConfigureOptions) (Status, error) {
	if repo == nil {
		return Status{}, fmt.Errorf("configure shared history requires checkpoint repo")
	}
	return withSharedHistoryLock(repo, "configure shared history", func() (Status, error) {
		return configureLocked(repo, options)
	})
}

func configureLocked(repo *checkpoint.Repo, options ConfigureOptions) (Status, error) {
	remote := strings.TrimSpace(options.Remote)
	if remote == "" {
		return Status{}, fmt.Errorf("shared history remote is required")
	}
	if strings.ContainsAny(remote, "\r\n\x00") {
		return Status{}, fmt.Errorf("shared history remote contains an invalid control character")
	}
	if strings.HasPrefix(remote, "-") {
		return Status{}, fmt.Errorf("shared history remote must not begin with '-'")
	}
	remote, err := normalizeRemote(remote)
	if err != nil {
		return Status{}, err
	}
	if err := validatePromptMode(options.PromptMode); err != nil {
		return Status{}, err
	}
	sharedRepoID := options.RepoID
	if sharedRepoID == "" {
		sharedRepoID = repo.RepoID
	}
	sharedRepoID, err = primitives.ParseRepoID(sharedRepoID.String())
	if err != nil {
		return Status{}, fmt.Errorf("configure shared history repository identity: %w", err)
	}
	policy := policyFile{
		Version:          1,
		Remote:           remote,
		RepoID:           sharedRepoID,
		PromptMode:       options.PromptMode,
		AllowlistVersion: AllowlistVersion,
		ScannerVersion:   ScannerVersion,
		FieldLimit:       DefaultFieldLimit,
		BundleLimit:      DefaultBundleLimit,
	}
	var previous policyFile
	previousConfigured := false
	if _, err := os.Lstat(policyPath(repo)); err == nil {
		previous, err = loadPolicy(repo)
		if err != nil {
			return Status{}, err
		}
		previousConfigured = true
	} else if !os.IsNotExist(err) {
		return Status{}, err
	}
	identity, err := loadOrCreateDevice(repo)
	if err != nil {
		return Status{}, err
	}
	state, err := loadState(repo)
	if err != nil {
		return Status{}, err
	}
	if previousConfigured {
		ctx := nonNilContext(nil)
		store, err := openGitStore(ctx, repo)
		if err != nil {
			return Status{}, err
		}
		if err := store.recoverCommittedState(ctx, identity, &state); err != nil {
			return Status{}, err
		}
		previousDigest, err := policyHash(previous)
		if err != nil {
			return Status{}, err
		}
		newDigest, err := policyHash(policy)
		if err != nil {
			return Status{}, err
		}
		if previousDigest != newDigest && len(state.Committed) > 0 {
			return Status{}, fmt.Errorf("shared history has an unpushed outbox under the current policy; publish it before changing the remote or privacy policy")
		}
		head, err := store.localHead(ctx)
		if err != nil {
			return Status{}, err
		}
		if publicRemoteIdentity(previous.Remote) != publicRemoteIdentity(policy.Remote) && head != "" && !options.IncludeExistingHistory {
			return Status{}, fmt.Errorf("changing the remote will copy all previously approved shared history; rerun with --include-existing-history to consent")
		}
		if previous.RepoID != policy.RepoID {
			if head != "" {
				return Status{}, fmt.Errorf("shared history repository identity cannot change after this device has published history")
			}
			state.Published = map[string]string{}
			state.Committed = map[string]string{}
			state.Blocked = map[string]string{}
			state.LastSeen = map[string]string{}
		}
		if previousDigest == newDigest {
			policy.ApprovedHash = previous.ApprovedHash
		}
	}
	if err := writeJSONAtomic(policyPath(repo), policy, 0o600); err != nil {
		return Status{}, err
	}
	alignStateScope(&state, remote, policy.RepoID)
	if err := saveState(repo, state); err != nil {
		return Status{}, err
	}
	return New(repo).statusLocked(nil)
}

func normalizeRemote(remote string) (string, error) {
	if strings.Contains(remote, "::") {
		return "", fmt.Errorf("shared history remote helpers are not supported")
	}
	if separator := strings.Index(remote, "://"); separator >= 0 {
		scheme := strings.ToLower(remote[:separator])
		switch scheme {
		case "file", "git", "http", "https", "ssh", "ftp", "ftps":
			return remote, nil
		default:
			return "", fmt.Errorf("unsupported shared history remote scheme %q", scheme)
		}
	}
	if looksLikeSCPRemote(remote) || filepath.IsAbs(remote) {
		return remote, nil
	}
	absolute, err := filepath.Abs(remote)
	if err != nil {
		return "", fmt.Errorf("resolve shared history remote: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func redactRemote(remote string) string {
	scheme := strings.Index(remote, "://")
	if scheme < 0 {
		if colon := strings.IndexByte(remote, ':'); colon > 0 {
			if at := strings.LastIndexByte(remote[:colon], '@'); at >= 0 {
				return "[REDACTED]@" + remote[at+1:]
			}
		}
		return remote
	}
	authorityStart := scheme + 3
	authorityEnd := len(remote)
	if delimiter := strings.IndexAny(remote[authorityStart:], "/?#"); delimiter >= 0 {
		authorityEnd = authorityStart + delimiter
	}
	if at := strings.LastIndexByte(remote[authorityStart:authorityEnd], '@'); at >= 0 {
		remote = remote[:authorityStart] + "[REDACTED]@" + remote[authorityStart+at+1:]
	}
	if secretSuffix := strings.IndexAny(remote, "?#"); secretSuffix >= 0 {
		return remote[:secretSuffix] + remote[secretSuffix:secretSuffix+1] + "[REDACTED]"
	}
	return remote
}

// publicRemoteIdentity removes transport credentials from values that leave
// policy.json or identify durable observation state. Credential rotation must
// not alter consent hashes or make an observed Git endpoint look new.
func publicRemoteIdentity(remote string) string {
	if scheme := strings.Index(remote, "://"); scheme >= 0 {
		if suffix := strings.IndexAny(remote, "?#"); suffix >= 0 {
			remote = remote[:suffix]
		}
		authorityStart := scheme + 3
		authorityEnd := len(remote)
		if slash := strings.IndexByte(remote[authorityStart:], '/'); slash >= 0 {
			authorityEnd = authorityStart + slash
		}
		if at := strings.LastIndexByte(remote[authorityStart:authorityEnd], '@'); at >= 0 {
			remote = remote[:authorityStart] + remote[authorityStart+at+1:]
		}
		return remote
	}
	if colon := strings.IndexByte(remote, ':'); colon > 0 {
		if at := strings.LastIndexByte(remote[:colon], '@'); at >= 0 {
			return remote[at+1:]
		}
	}
	return remote
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
	if strings.HasPrefix(policy.Remote, "-") || strings.ContainsAny(policy.Remote, "\r\n\x00") {
		return policyFile{}, fmt.Errorf("shared history policy remote is invalid")
	}
	if _, err := primitives.ParseRepoID(policy.RepoID.String()); err != nil {
		return policyFile{}, fmt.Errorf("shared history policy repository identity: %w", err)
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

func policyHash(policy policyFile) (string, error) {
	input := struct {
		SchemaVersion int        `json:"schema_version"`
		RepoID        string     `json:"repo_id"`
		Remote        string     `json:"remote"`
		PromptMode    PromptMode `json:"prompt_mode"`
		Allowlist     string     `json:"allowlist_version"`
		Scanner       string     `json:"scanner_version"`
		FieldLimit    int        `json:"field_limit"`
		BundleLimit   int        `json:"bundle_limit"`
	}{SchemaVersion, policy.RepoID.String(), publicRemoteIdentity(policy.Remote), policy.PromptMode, policy.AllowlistVersion, policy.ScannerVersion, policy.FieldLimit, policy.BundleLimit}
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

func alignStateScope(state *stateFile, remote string, repoID primitives.RepoID) {
	remote = publicRemoteIdentity(remote)
	if state.RepoID != repoID {
		state.RepoID = repoID
		state.Committed = map[string]string{}
		state.Published = map[string]string{}
		state.Blocked = map[string]string{}
		state.LastSeen = map[string]string{}
	}
	if state.Remote != remote {
		state.Remote = remote
		state.LastSeen = map[string]string{}
	}
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
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("install shared history file %s: %w", path, err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync shared history directory %s: %w", dir, err)
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

func sharedHistoryLockPath(repo *checkpoint.Repo) string {
	return filepath.Join(sharedRoot(repo), "operation.lock")
}

func withSharedHistoryLock[T any](repo *checkpoint.Repo, operation string, fn func() (T, error)) (T, error) {
	timeout := repo.LockTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	lock, err := filelock.Acquire(sharedHistoryLockPath(repo), timeout)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", operation, err)
	}
	value, operationErr := fn()
	releaseErr := lock.Release()
	if operationErr != nil {
		return value, operationErr
	}
	if releaseErr != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", operation, releaseErr)
	}
	return value, nil
}
