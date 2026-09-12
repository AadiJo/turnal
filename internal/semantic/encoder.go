// Package semantic provides the optional local embedding model used by
// meaning-aware history search. Model files are downloaded from Hugging Face
// on first use and cached by go-potion; recorded Turnal text is never sent.
package semantic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	potion "github.com/trengrj/go-potion"
)

// ModelName is the Hugging Face repository backing local meaning matching. It
// is the smallest POTION model: 8 MB on disk, 64-dimensional output.
const ModelName = "minishlab/potion-base-2M"

// Encoder turns text into L2-normalized vectors. It is loaded only when a
// caller asks for semantic search, so ordinary Turnal commands stay offline
// and do not pay the model's load cost.
type Encoder struct {
	model *potion.Potion
	cache *embeddingCache
}

// NewEncoder loads the model, downloading it into the go-potion user cache on
// first use. The context bounds that download.
func NewEncoder(ctx context.Context) (*Encoder, error) {
	model, err := potion.New(ctx, potion.BASE2M)
	if err != nil {
		return nil, fmt.Errorf("load local semantic model %s: %w", ModelName, err)
	}
	encoder := &Encoder{model: model}
	if key, err := modelCacheKey(); err == nil {
		if root, err := os.UserCacheDir(); err == nil {
			encoder.cache = &embeddingCache{dir: filepath.Join(root, "turnal", "embeddings", key), dimensions: model.Dimensions()}
		}
	}
	return encoder, nil
}

// EncodeMany embeds texts in order, one vector per input.
func (e *Encoder) EncodeMany(texts []string) ([][]float32, error) {
	encode := e.model.EncodeMany
	if e.cache != nil {
		encode = func(texts []string) ([][]float32, error) { return e.cache.encode(texts, e.model.EncodeMany) }
	}
	vectors, err := encode(texts)
	if err != nil {
		return nil, fmt.Errorf("encode with %s: %w", ModelName, err)
	}
	return vectors, nil
}
