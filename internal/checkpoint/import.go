package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/primitives"
)

type ImportedRef struct {
	OriginalRef string
	StagingRef  string
	FinalRef    string
	Commit      primitives.CommitSHA
}

func ReadStoreIdentity(metadataDir string) (StoreIdentity, error) {
	repo := pathsAt(primitives.WorkspaceRoot(filepath.Dir(metadataDir)), metadataDir)
	return readOrCreateStoreIdentityReadOnly(repo)
}

func readOrCreateStoreIdentityReadOnly(repo *Repo) (StoreIdentity, error) {
	path := filepath.Join(repo.MetadataDir, identityFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return StoreIdentity{}, fmt.Errorf("read store identity %s: %w", path, err)
	}
	var identity StoreIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return StoreIdentity{}, fmt.Errorf("parse store identity %s: %w", path, err)
	}
	if identity.Version != identityVersion {
		return StoreIdentity{}, fmt.Errorf("store identity invariant failed: unsupported version %d", identity.Version)
	}
	if identity.RepoID, err = primitives.ParseRepoID(identity.RepoID.String()); err != nil {
		return StoreIdentity{}, err
	}
	if identity.StoreID, err = primitives.ParseStoreID(identity.StoreID.String()); err != nil {
		return StoreIdentity{}, err
	}
	if identity.GitObjectFormat == "" {
		return StoreIdentity{}, fmt.Errorf("store identity invariant failed: git_object_format is required")
	}
	return identity, nil
}

func ReadWorktreeIdentities(metadataDir string) ([]WorktreeIdentity, error) {
	return listWorktreeIdentities(metadataDir)
}

func (repo *Repo) StageImportRefs(sourceGitDir string, sourceStoreID primitives.StoreID, importID primitives.ImportID) ([]ImportedRef, error) {
	if repo == nil {
		return nil, fmt.Errorf("stage import refs requires repo")
	}
	if _, err := primitives.ParseStoreID(sourceStoreID.String()); err != nil {
		return nil, err
	}
	if _, err := primitives.ParseImportID(importID.String()); err != nil {
		return nil, err
	}
	sourceGitDir, err := filepath.Abs(sourceGitDir)
	if err != nil {
		return nil, fmt.Errorf("resolve source hidden git dir: %w", err)
	}
	stagingPrefix := fmt.Sprintf("refs/agent-vcs/import-staging/%s", importID)
	refspec := fmt.Sprintf("+refs/agent-vcs/*:%s/*", stagingPrefix)
	if _, err := runHiddenGit(repo, "", "fetch", "--no-tags", "--no-write-fetch-head", sourceGitDir, refspec); err != nil {
		return nil, fmt.Errorf("stage hidden git refs: %w", err)
	}
	output, err := runHiddenGit(repo, "", "for-each-ref", "--format=%(refname)%09%(objectname)", stagingPrefix)
	if err != nil {
		return nil, err
	}
	var refs []ImportedRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("import ref listing invariant failed for %q", line)
		}
		stagingRef := fields[0]
		suffix, ok := strings.CutPrefix(stagingRef, stagingPrefix+"/")
		if !ok || suffix == "" {
			return nil, fmt.Errorf("import staging ref invariant failed: %s", stagingRef)
		}
		commit, err := primitives.ParseCommitSHA(fields[1])
		if err != nil {
			return nil, err
		}
		refs = append(refs, ImportedRef{
			OriginalRef: "refs/agent-vcs/" + suffix,
			StagingRef:  stagingRef,
			FinalRef:    fmt.Sprintf("refs/agent-vcs/imports/%s/%s", sourceStoreID, suffix),
			Commit:      commit,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].OriginalRef < refs[j].OriginalRef })
	return refs, nil
}

func ListSourcePrivateRefs(sourceGitDir string, sourceStoreID primitives.StoreID) ([]ImportedRef, error) {
	if _, err := primitives.ParseStoreID(sourceStoreID.String()); err != nil {
		return nil, err
	}
	sourceGitDir, err := filepath.Abs(sourceGitDir)
	if err != nil {
		return nil, err
	}
	output, err := runGitNoRepo(filepath.Dir(sourceGitDir), "--git-dir", sourceGitDir, "for-each-ref", "--format=%(refname)%09%(objectname)", "refs/agent-vcs")
	if err != nil {
		return nil, fmt.Errorf("inspect source hidden git refs: %w", err)
	}
	var refs []ImportedRef
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("source ref listing invariant failed for %q", line)
		}
		suffix, ok := strings.CutPrefix(fields[0], "refs/agent-vcs/")
		if !ok || suffix == "" || strings.HasPrefix(suffix, "import-staging/") {
			continue
		}
		commit, err := primitives.ParseCommitSHA(fields[1])
		if err != nil {
			return nil, err
		}
		refs = append(refs, ImportedRef{
			OriginalRef: fields[0],
			FinalRef:    fmt.Sprintf("refs/agent-vcs/imports/%s/%s", sourceStoreID, suffix),
			Commit:      commit,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].OriginalRef < refs[j].OriginalRef })
	return refs, nil
}

func (repo *Repo) PromoteImportedRefs(refs []ImportedRef) error {
	for _, imported := range refs {
		if _, err := repo.validatePrivateRef(imported.FinalRef); err != nil {
			return err
		}
		if existing, err := repo.RefCommit(imported.FinalRef); err == nil {
			if existing != imported.Commit {
				return fmt.Errorf("import ref collision: %s points to %s, source has %s", imported.FinalRef, existing, imported.Commit)
			}
			continue
		}
		if _, err := runHiddenGit(repo, "", "update-ref", imported.FinalRef, imported.Commit.String(), ""); err != nil {
			return fmt.Errorf("promote imported ref %s: %w", imported.FinalRef, err)
		}
	}
	return nil
}

func (repo *Repo) ClearImportStaging(importID primitives.ImportID) error {
	prefix := fmt.Sprintf("refs/agent-vcs/import-staging/%s", importID)
	refs, err := repo.ListPrivateRefs(prefix)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := runHiddenGit(repo, "", "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

func (repo *Repo) EnsureImportedCheckpointAlias(ref primitives.CheckpointRef, commit primitives.CommitSHA) error {
	parsed, err := primitives.ParseCheckpointRef(ref.String())
	if err != nil {
		return err
	}
	if existing, err := repo.CheckpointCommit(parsed); err == nil {
		if existing != commit {
			return fmt.Errorf("checkpoint alias collision: %s points to %s, source has %s", parsed, existing, commit)
		}
		return nil
	}
	_, err = runHiddenGit(repo, "", "update-ref", parsed.String(), commit.String(), "")
	return err
}

func (repo *Repo) HasCommit(commit primitives.CommitSHA) error {
	parsed, err := primitives.ParseCommitSHA(commit.String())
	if err != nil {
		return err
	}
	_, err = runHiddenGit(repo, "", "cat-file", "-e", parsed.String()+"^{commit}")
	return err
}
