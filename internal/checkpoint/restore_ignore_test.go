package checkpoint

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWalkUnignoredPathsPreservesNestedRulesAndProtectedDirectories(t *testing.T) {
	repo, err := Init(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitignore":         "ignored/\n*.tmp\n",
		"keep.txt":           "keep",
		"ignored/secret.txt": "ignored",
		"private/secret.txt": "denied",
		"nested/.gitignore":  "!keep.tmp\n",
		"nested/keep.tmp":    "keep",
		"nested/drop.tmp":    "ignored",
	}
	for name, content := range files {
		path := filepath.Join(repo.WorkspaceRoot.String(), filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index, cleanup, err := repo.tempIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var visited []string
	if err := repo.walkUnignoredPaths(index, []string{"private"}, func(_, path string, entry fs.DirEntry) error {
		visited = append(visited, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "keep.txt", "nested", "nested/.gitignore", "nested/keep.tmp"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
}
