package checkpoint

import (
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestValidateCommitsRejectsMissingAndNonCommitObjects(t *testing.T) {
	repo, err := Init(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.createSnapshotCommit("validate evidence")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ValidateCommits([]primitives.CommitSHA{commit, commit}); err != nil {
		t.Fatal(err)
	}
	blob, err := runHiddenGitWithInput(repo, "", strings.NewReader("not a commit"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{strings.Repeat("1", 40), strings.TrimSpace(blob)} {
		id, err := primitives.ParseCommitSHA(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ValidateCommits([]primitives.CommitSHA{commit, id}); err == nil {
			t.Fatalf("accepted invalid evidence %s", id)
		}
	}
}
