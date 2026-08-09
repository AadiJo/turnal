// Package discovery ranks keyword and meaning matches drawn from local Turnal
// indexes. It has no storage or network responsibilities: callers decide which
// private stores are in scope and supply the embedding implementation.
package discovery

import (
	"fmt"
	"sort"
	"strings"

	queryindex "github.com/AadiJo/turnal/internal/index"
)

const (
	// similarityFloor is the cosine score a meaning-only match must clear.
	// Calibrated against potion-base-2M, whose 64-dimensional vectors put
	// loosely-worded development text in the 0.2-0.3 band. The floor keeps the
	// calibrated prompt-only match at 0.334 while rejecting diluted noise.
	similarityFloor = float32(0.30)

	// maxSemanticTextLen bounds one document before embedding. Mean pooling
	// dilutes the vector as text grows, so an unusually long prompt is
	// truncated rather than allowed to wash out the turn's meaning.
	maxSemanticTextLen = 4 * 1024
)

// SimilarityFloor reports the cosine score a meaning-only match must clear, so
// calibration tests can assert against the value ranking actually uses.
func SimilarityFloor() float32 { return similarityFloor }

// Encoder converts text into L2-normalized vectors in one shared vector space.
type Encoder interface {
	EncodeMany([]string) ([][]float32, error)
}

// Project identifies the local store that owns a cross-project result.
type Project struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	StoreID string `json:"store_id"`
}

// Candidate is one indexed turn offered to ranking. Keyword reports whether
// the store's full-text query matched it; KeywordRank is that query's
// zero-based ordering and is meaningful only when Keyword is true.
//
// KeywordRank is per store. bm25 weights terms by their frequency in the
// corpus that produced them, so scores from different projects are not
// comparable and are deliberately not pooled: ranking interleaves each
// project's hits by their within-project position instead.
type Candidate struct {
	Project     *Project
	Document    queryindex.SearchDocument
	Keyword     bool
	KeywordRank int
}

// Match explains why a result was selected, so a reader can tell an exact hit
// from an inferred one without re-running the query.
type Match struct {
	Kind            string  `json:"kind"`
	Reason          string  `json:"reason"`
	Similarity      float32 `json:"similarity,omitempty"`
	SemanticLimited bool    `json:"semantic_text_truncated,omitempty"`
}

// Result embeds the existing flattened search result so JSON consumers keep
// every field they had, and adds project identity plus the match explanation.
type Result struct {
	queryindex.SearchResult
	Project *Project `json:"project,omitempty"`
	Match   Match    `json:"match"`
}

// Rank orders candidates for presentation. A nil encoder ranks keyword matches
// alone; otherwise meaning similarity is computed for every candidate.
//
// Keyword matches are strictly tiered above meaning-only matches and retain the
// store's full-text ordering among themselves. Literal identifiers, error
// strings, and paths are the strongest evidence a search has, so similarity
// never reorders them: it annotates them, and supplies the additional turns
// that share no query terms at all. Meaning-only matches follow, most similar
// first. Ties break on project root then session and turn, so output is stable
// across runs and across machines.
func Rank(query string, candidates []Candidate, encoder Encoder, limit int) ([]Result, error) {
	if limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}

	similarities, truncated, err := score(query, candidates, encoder)
	if err != nil {
		return nil, err
	}

	type ranked struct {
		result      Result
		keyword     bool
		keywordRank int
		similarity  float32
	}

	selected := make([]ranked, 0, len(candidates))
	for index, candidate := range candidates {
		similarity := similarities[index]
		meaning := encoder != nil && similarity >= similarityFloor
		if !candidate.Keyword && !meaning {
			continue
		}
		selected = append(selected, ranked{
			result: Result{
				SearchResult: candidate.Document.Result,
				Project:      candidate.Project,
				Match:        describe(candidate.Keyword, meaning, similarity, truncated[index]),
			},
			keyword:     candidate.Keyword,
			keywordRank: candidate.KeywordRank,
			similarity:  similarity,
		})
	}

	sort.SliceStable(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		if left.keyword != right.keyword {
			return left.keyword
		}
		if left.keyword {
			if left.keywordRank != right.keywordRank {
				return left.keywordRank < right.keywordRank
			}
		} else if left.similarity != right.similarity {
			return left.similarity > right.similarity
		}
		leftProject, rightProject := projectSortKey(left.result.Project), projectSortKey(right.result.Project)
		if leftProject != rightProject {
			return leftProject < rightProject
		}
		if left.result.SessionID != right.result.SessionID {
			return left.result.SessionID < right.result.SessionID
		}
		return left.result.TurnID < right.result.TurnID
	})

	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	results := make([]Result, 0, len(selected))
	for _, entry := range selected {
		results = append(results, entry.result)
	}
	return results, nil
}

func describe(keyword, meaning bool, similarity float32, truncated bool) Match {
	match := Match{SemanticLimited: truncated}
	switch {
	case keyword && meaning:
		match.Kind = "keyword+meaning"
		match.Reason = fmt.Sprintf("keyword match; meaning similarity %.2f", similarity)
		match.Similarity = similarity
	case keyword:
		match.Kind = "keyword"
		match.Reason = "keyword match"
	default:
		match.Kind = "meaning"
		match.Reason = fmt.Sprintf("meaning similarity %.2f", similarity)
		match.Similarity = similarity
	}
	return match
}

// score embeds the query and every candidate carrying usable text in a single
// batch, returning per-candidate similarity and whether that text was
// truncated. A nil encoder yields zero similarity for everything.
func score(query string, candidates []Candidate, encoder Encoder) ([]float32, []bool, error) {
	similarities := make([]float32, len(candidates))
	truncated := make([]bool, len(candidates))
	if encoder == nil {
		return similarities, truncated, nil
	}

	texts := []string{query}
	embedded := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		text, limited := boundSemanticText(candidate.Document.Text)
		truncated[index] = limited
		if text == "" {
			continue
		}
		embedded = append(embedded, index)
		texts = append(texts, text)
	}

	vectors, err := encoder.EncodeMany(texts)
	if err != nil {
		return nil, nil, fmt.Errorf("encode local search text: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, nil, fmt.Errorf("semantic encoder returned %d vectors for %d texts", len(vectors), len(texts))
	}
	if len(vectors[0]) == 0 {
		return nil, nil, fmt.Errorf("semantic encoder returned an empty query vector")
	}

	for position, index := range embedded {
		similarity, err := cosineForNormalized(vectors[0], vectors[position+1])
		if err != nil {
			return nil, nil, err
		}
		similarities[index] = similarity
	}
	return similarities, truncated, nil
}

// boundSemanticText trims a document to the embedding budget, reporting whether
// anything was dropped. Truncation cuts on a rune boundary so the tokenizer
// never sees a partial character.
func boundSemanticText(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) <= maxSemanticTextLen {
		return value, false
	}
	return strings.ToValidUTF8(value[:maxSemanticTextLen], ""), true
}

// cosineForNormalized is a plain dot product: the encoder contract guarantees
// unit-length vectors, so no division is needed.
func cosineForNormalized(left, right []float32) (float32, error) {
	if len(left) != len(right) {
		return 0, fmt.Errorf("semantic vector dimensions differ: %d and %d", len(left), len(right))
	}
	var score float32
	for index := range left {
		score += left[index] * right[index]
	}
	return score, nil
}

func projectSortKey(project *Project) string {
	if project == nil {
		return ""
	}
	return project.Root + "\x00" + project.StoreID
}
