package verifier

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
)

const (
	evaluationDirName   = "verify"
	evaluationPrefix    = "evaluation-"
	ownershipFileName   = "ownership.json"
	ownershipVersion    = 1
	materializedDirName = "workspace"
)

type PreparedTarget struct {
	Root        string
	Target      Target
	ownedPath   string
	ownedParent string
	ownerToken  string
}

func LiveTarget(repo *checkpoint.Repo) (PreparedTarget, error) {
	if repo == nil {
		return PreparedTarget{}, fmt.Errorf("live verifier target requires checkpoint repo")
	}
	root := repo.WorkspaceRoot.String()
	if strings.TrimSpace(root) == "" {
		return PreparedTarget{}, fmt.Errorf("live verifier target requires workspace root")
	}
	return PreparedTarget{
		Root: root,
		Target: Target{
			Kind:          TargetLiveWorkspace,
			Display:       "live workspace",
			WorkspaceRoot: root,
			WorktreeID:    repo.WorktreeID.String(),
			Mutable:       true,
			Reproducible:  false,
			Environment:   "inherited from the turnal process; values are not recorded",
			Limitations: []string{
				"The live workspace is mutable and was evaluated without creating an automatic checkpoint.",
				"The inherited toolchain, environment, network, and external services are not reproduced by Turnal.",
				"Passing checks are evidence for the declared commands, not proof that the entire change is correct.",
			},
		},
	}, nil
}

func PrepareCheckpoint(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (PreparedTarget, error) {
	if repo == nil {
		return PreparedTarget{}, fmt.Errorf("checkpoint verifier target requires checkpoint repo")
	}
	parsedPhase, err := primitives.ParseCheckpointPhase(phase.String())
	if err != nil {
		return PreparedTarget{}, err
	}
	recalled, err := recall.NewScopedReader(repo.MetadataDir, repo.WorktreeID).RecallTurn(sessionID, turnID, recall.Options{WorktreeID: repo.WorktreeID})
	if err != nil {
		return PreparedTarget{}, fmt.Errorf("resolve verifier checkpoint target: %w", err)
	}
	recorded := recalled.PreCheckpoint
	if parsedPhase == primitives.CheckpointPhasePost {
		recorded = recalled.PostCheckpoint
	}
	if recorded == nil {
		return PreparedTarget{}, fmt.Errorf("checkpoint %s:turn:%s:%s not found in durable turn metadata", sessionID, turnID, parsedPhase)
	}
	if err := verifyRecordedCheckpoint(repo, *recorded); err != nil {
		return PreparedTarget{}, err
	}

	parent := filepath.Join(repo.TmpDir, evaluationDirName)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return PreparedTarget{}, fmt.Errorf("create verifier temporary parent: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return PreparedTarget{}, fmt.Errorf("secure verifier temporary parent: %w", err)
	}
	ownedPath, err := os.MkdirTemp(parent, evaluationPrefix)
	if err != nil {
		return PreparedTarget{}, fmt.Errorf("create verifier temporary directory: %w", err)
	}
	token, err := newOwnershipToken()
	if err != nil {
		_ = os.RemoveAll(ownedPath)
		return PreparedTarget{}, err
	}
	prepared := PreparedTarget{ownedPath: ownedPath, ownedParent: parent, ownerToken: token}
	if err := writeOwnershipProof(ownedPath, token); err != nil {
		_ = os.RemoveAll(ownedPath)
		return PreparedTarget{}, err
	}
	root := filepath.Join(ownedPath, materializedDirName)
	if err := repo.MaterializeCommit(recorded.CommitSHA, root, checkpoint.MaterializeOptions{ApplyCurrentSecretDenyGlobs: true}); err != nil {
		cleanupErr := prepared.Cleanup()
		return PreparedTarget{}, errors.Join(fmt.Errorf("materialize verifier checkpoint: %w", err), cleanupErr)
	}
	if err := requireMetadataAbsent(root); err != nil {
		cleanupErr := prepared.Cleanup()
		return PreparedTarget{}, errors.Join(err, cleanupErr)
	}
	prepared.Root = root
	prepared.Target = Target{
		Kind:          TargetCheckpoint,
		Display:       fmt.Sprintf("%s:turn:%s:%s", recalled.SessionID, recalled.TurnID, parsedPhase),
		WorkspaceRoot: repo.WorkspaceRoot.String(),
		WorktreeID:    recalled.WorktreeID.String(),
		SessionID:     recalled.SessionID.String(),
		Turn:          recalled.TurnID.Uint64(),
		Phase:         parsedPhase.String(),
		CheckpointRef: recorded.Ref.String(),
		Commit:        recorded.CommitSHA.String(),
		Mutable:       false,
		Reproducible:  false,
		Environment:   "inherited from the turnal process; values are not recorded",
		Limitations: []string{
			"Only the captured project surface was materialized; ignored, secrets-denied, and otherwise uncaptured paths are absent.",
			"Turnal and Git metadata directories are absent from the evaluation surface.",
			"Empty directories are not represented by checkpoint trees.",
			"The inherited toolchain, environment, network, and external services are not reproduced by Turnal.",
			"Passing checks are evidence for the declared commands, not proof that the entire change is correct.",
		},
	}
	return prepared, nil
}

func verifyRecordedCheckpoint(repo *checkpoint.Repo, recorded recall.Checkpoint) error {
	commit, err := repo.CheckpointCommit(recorded.Ref)
	if err != nil {
		return fmt.Errorf("resolve recorded checkpoint ref %s: %w", recorded.Ref, err)
	}
	if commit != recorded.CommitSHA {
		return fmt.Errorf("verifier checkpoint integrity failed: ref %s points to %s, durable metadata records %s", recorded.Ref, commit, recorded.CommitSHA)
	}
	if recorded.CanonicalRef != "" {
		canonicalCommit, err := repo.CheckpointCommit(recorded.CanonicalRef)
		if err != nil {
			return fmt.Errorf("resolve recorded canonical checkpoint ref %s: %w", recorded.CanonicalRef, err)
		}
		if canonicalCommit != recorded.CommitSHA {
			return fmt.Errorf("verifier checkpoint integrity failed: canonical ref %s points to %s, durable metadata records %s", recorded.CanonicalRef, canonicalCommit, recorded.CommitSHA)
		}
	}
	return nil
}

func (prepared PreparedTarget) Cleanup() error {
	if prepared.ownedPath == "" {
		return nil
	}
	return cleanupOwnedEvaluation(prepared.ownedParent, prepared.ownedPath, prepared.ownerToken)
}

type ownershipProof struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
	Root    string `json:"root"`
}

func newOwnershipToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate verifier ownership proof: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func writeOwnershipProof(path, token string) error {
	proof := ownershipProof{Version: ownershipVersion, Token: token, Root: filepath.Clean(path)}
	data, err := json.Marshal(proof)
	if err != nil {
		return fmt.Errorf("marshal verifier ownership proof: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(path, ownershipFileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create verifier ownership proof: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write verifier ownership proof: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close verifier ownership proof: %w", err)
	}
	return nil
}

func cleanupOwnedEvaluation(parent, path, token string) error {
	if strings.TrimSpace(parent) == "" || strings.TrimSpace(path) == "" || strings.TrimSpace(token) == "" {
		return fmt.Errorf("refuse verifier cleanup: parent, path, and ownership token are required")
	}
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return fmt.Errorf("refuse verifier cleanup: resolve parent: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("refuse verifier cleanup: resolve path: %w", err)
	}
	if filepath.Dir(pathAbs) != parentAbs || !strings.HasPrefix(filepath.Base(pathAbs), evaluationPrefix) {
		return fmt.Errorf("refuse verifier cleanup: %s is not a direct owned evaluation under %s", pathAbs, parentAbs)
	}
	info, err := os.Lstat(pathAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("refuse verifier cleanup: stat %s: %w", pathAbs, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse verifier cleanup: owned path is not a real directory: %s", pathAbs)
	}
	proofPath := filepath.Join(pathAbs, ownershipFileName)
	proofInfo, err := os.Lstat(proofPath)
	if err != nil {
		return fmt.Errorf("refuse verifier cleanup: ownership proof unavailable at %s: %w", proofPath, err)
	}
	if !proofInfo.Mode().IsRegular() || proofInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse verifier cleanup: ownership proof is not a regular file: %s", proofPath)
	}
	data, err := os.ReadFile(proofPath)
	if err != nil {
		return fmt.Errorf("refuse verifier cleanup: read ownership proof: %w", err)
	}
	var proof ownershipProof
	if err := json.Unmarshal(data, &proof); err != nil {
		return fmt.Errorf("refuse verifier cleanup: parse ownership proof: %w", err)
	}
	if proof.Version != ownershipVersion || proof.Token != token || filepath.Clean(proof.Root) != pathAbs {
		return fmt.Errorf("refuse verifier cleanup: ownership proof does not match %s", pathAbs)
	}
	if err := os.RemoveAll(pathAbs); err != nil {
		return fmt.Errorf("remove owned verifier evaluation %s: %w", pathAbs, err)
	}
	return nil
}

func requireMetadataAbsent(root string) error {
	for _, name := range []string{".git", ".turnal"} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("verifier materialization invariant failed: %s is present", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("verify materialized metadata exclusion at %s: %w", path, err)
		}
	}
	return nil
}
