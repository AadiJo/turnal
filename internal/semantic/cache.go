package semantic

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
)

// Cache files contain only checksummed vectors. Hashing the exact model and
// tokenizer bytes separates model revisions; hashing input text also covers
// any change to the caller's truncation policy. Failures fall back to inference.
type embeddingCache struct {
	dir        string
	dimensions int
}

func modelCacheKey() (string, error) {
	root := os.Getenv("GO_POTION_HOME")
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, "go-potion")
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "turnal-embeddings-v1\x00"+ModelName+"\x00")
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/trengrj/go-potion" {
				if dep.Replace != nil {
					return "", fmt.Errorf("embedding cache disabled for a replaced encoder dependency")
				}
				_, _ = io.WriteString(hash, dep.Version+"\x00"+dep.Sum+"\x00")
			}
		}
	}
	for _, name := range []string{"model.safetensors", "tokenizer.json"} {
		file, err := os.Open(filepath.Join(root, "BASE2M", name))
		if err != nil {
			return "", err
		}
		_, err = io.Copy(hash, file)
		_ = file.Close()
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (cache embeddingCache) path(text string) string {
	digest := sha256.Sum256([]byte(text))
	return filepath.Join(cache.dir, hex.EncodeToString(digest[:])+".bin")
}

func (cache embeddingCache) read(text string) []float32 {
	file, err := os.Open(cache.path(text))
	if err != nil {
		return nil
	}
	defer file.Close()
	size := cache.dimensions*4 + sha256.Size
	data, err := io.ReadAll(io.LimitReader(file, int64(size+1)))
	if err != nil || len(data) != size {
		return nil
	}
	payload := data[:cache.dimensions*4]
	checksum := sha256.Sum256(payload)
	if string(checksum[:]) != string(data[len(payload):]) {
		return nil
	}
	vector := make([]float32, cache.dimensions)
	for i := range vector {
		value := math.Float32frombits(binary.LittleEndian.Uint32(payload[i*4:]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil
		}
		vector[i] = value
	}
	return vector
}

func (cache embeddingCache) write(text string, vector []float32) {
	if len(vector) != cache.dimensions {
		return
	}
	data := make([]byte, len(vector)*4)
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return
		}
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(value))
	}
	digest := sha256.Sum256(data)
	data = append(data, digest[:]...)
	if err := os.MkdirAll(cache.dir, 0700); err != nil {
		return
	}
	file, err := os.CreateTemp(cache.dir, ".vector-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Close(); err != nil {
		return
	}
	// The key is immutable. A concurrent writer can safely win this rename.
	_ = os.Rename(file.Name(), cache.path(text))
}

func (cache embeddingCache) encode(texts []string, encode func([]string) ([][]float32, error)) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	missing := make([]string, 0)
	positions := make(map[string][]int)
	for i, text := range texts {
		if indices, ok := positions[text]; ok {
			positions[text] = append(indices, i)
			continue
		}
		if vector := cache.read(text); vector != nil {
			vectors[i] = vector
			continue
		}
		positions[text] = []int{i}
		missing = append(missing, text)
	}
	if len(missing) == 0 {
		return vectors, nil
	}
	encoded, err := encode(missing)
	if err != nil {
		return nil, err
	}
	if len(encoded) != len(missing) {
		return nil, fmt.Errorf("encoder returned %d vectors for %d texts", len(encoded), len(missing))
	}
	for i, text := range missing {
		if len(encoded[i]) != cache.dimensions {
			return nil, fmt.Errorf("encoder returned %d dimensions, want %d", len(encoded[i]), cache.dimensions)
		}
		for _, position := range positions[text] {
			vectors[position] = encoded[i]
		}
		cache.write(text, encoded[i])
	}
	return vectors, nil
}
