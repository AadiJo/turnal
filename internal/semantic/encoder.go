// Package semantic provides the optional local embedding model used by
// meaning-aware history search. Model files are downloaded from Hugging Face
// on first use and cached by go-potion; recorded Turnal text is never sent.
package semantic

import (
	"context"
	"fmt"

	potion "github.com/trengrj/go-potion"
)

const ModelName = "minishlab/potion-base-2M"

// Encoder wraps the smallest POTION model. It is intentionally loaded only
// when a caller requests semantic search so ordinary Turnal commands remain
// offline and do not pay the model's startup cost.
type Encoder struct {
	model *potion.Potion
}

func NewEncoder(ctx context.Context) (*Encoder, error) {
	model, err := potion.New(ctx, potion.BASE2M)
	if err != nil {
		return nil, fmt.Errorf("load local semantic model %s: %w", ModelName, err)
	}
	return &Encoder{model: model}, nil
}

func (encoder *Encoder) EncodeMany(texts []string) ([][]float32, error) {
	vectors, err := encoder.model.EncodeMany(texts)
	if err != nil {
		return nil, fmt.Errorf("encode with %s: %w", ModelName, err)
	}
	return vectors, nil
}
