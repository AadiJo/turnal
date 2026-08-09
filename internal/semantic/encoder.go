// Package semantic provides the optional local embedding model used by
// meaning-aware history search. Model files are downloaded from Hugging Face
// on first use and cached by go-potion; recorded Turnal text is never sent.
package semantic

import (
	"context"
	"fmt"

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
}

// NewEncoder loads the model, downloading it into the go-potion user cache on
// first use. The context bounds that download.
func NewEncoder(ctx context.Context) (*Encoder, error) {
	model, err := potion.New(ctx, potion.BASE2M)
	if err != nil {
		return nil, fmt.Errorf("load local semantic model %s: %w", ModelName, err)
	}
	return &Encoder{model: model}, nil
}

// EncodeMany embeds texts in order, one vector per input.
func (e *Encoder) EncodeMany(texts []string) ([][]float32, error) {
	vectors, err := e.model.EncodeMany(texts)
	if err != nil {
		return nil, fmt.Errorf("encode with %s: %w", ModelName, err)
	}
	return vectors, nil
}
