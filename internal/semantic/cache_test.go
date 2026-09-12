package semantic

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmbeddingCacheReusesVectorsAndRecomputesChangedOrCorruptInputs(t *testing.T) {
	cache := embeddingCache{dir: t.TempDir(), dimensions: 2}
	var batches [][]string
	encode := func(texts []string) ([][]float32, error) {
		batches = append(batches, append([]string(nil), texts...))
		vectors := make([][]float32, len(texts))
		for i, text := range texts {
			vectors[i] = []float32{float32(len(text)), 1}
		}
		return vectors, nil
	}
	first, err := cache.encode([]string{"query", "document", "document"}, encode)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batches, [][]string{{"query", "document"}}) {
		t.Fatalf("batches: %v", batches)
	}
	// A fresh cache instance exercises persistence across search invocations.
	second, err := (embeddingCache{dir: cache.dir, dimensions: 2}).encode([]string{"query", "document", "document"}, encode)
	if err != nil || !reflect.DeepEqual(first, second) || len(batches) != 1 {
		t.Fatalf("cached vectors=%v err=%v batches=%v", second, err, batches)
	}
	if _, err := cache.encode([]string{"new query", "document"}, encode); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batches[1], []string{"new query"}) {
		t.Fatal(batches)
	}
	if err := os.WriteFile(cache.path("document"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.encode([]string{"query", "document"}, encode); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batches[2], []string{"document"}) {
		t.Fatal(batches)
	}
	changed := embeddingCache{dir: filepath.Join(cache.dir, "changed-model"), dimensions: 2}
	if _, err := changed.encode([]string{"document"}, encode); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 4 {
		t.Fatal(batches)
	}
}

func TestEmbeddingCacheFailureDoesNotPreventInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cache := embeddingCache{dir: path, dimensions: 2}
	vectors, err := cache.encode([]string{"text"}, func([]string) ([][]float32, error) { return [][]float32{{1, 0}}, nil })
	if err != nil || !reflect.DeepEqual(vectors, [][]float32{{1, 0}}) {
		t.Fatalf("vectors=%v err=%v", vectors, err)
	}
}

func TestModelCacheKeyChangesWithModelAndTokenizer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GO_POTION_HOME", home)
	dir := filepath.Join(home, "BASE2M")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model.safetensors", "tokenizer.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	previous, err := modelCacheKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model.safetensors", "tokenizer.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		current, err := modelCacheKey()
		if err != nil || current == previous {
			t.Fatalf("key failed to change for %s: %v", name, err)
		}
		previous = current
	}
}
