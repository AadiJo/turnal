// Package discovery ranks keyword and meaning matches from local Turnal
// indexes. It has no storage or network responsibilities; callers decide
// which private stores and embedding implementation are in scope.
package discovery

import (
	"fmt"
	"sort"
	"strings"

	queryindex "github.com/AadiJo/turnal/internal/index"
)

const (
	semanticThreshold  = float32(0.30)
	maxSemanticTextLen = 32 * 1024
)

// Encoder converts text into normalized vectors in one shared vector space.
type Encoder interface {
	EncodeMany([]string) ([][]float32, error)
}

// Project identifies the local store that owns a cross-project result.
type Project struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	StoreID string `json:"store_id"`
}

// Candidate is one indexed turn plus its optional lexical rank. KeywordRank
// is zero-based and meaningful only when Keyword is true.
type Candidate struct {
	Scope       string
	Project     *Project
	Document    queryindex.SearchDocument
	Keyword     bool
	KeywordRank int
}

// Match explains which retrieval path selected a result.
type Match struct {
	Kind            string  `json:"kind"`
	Reason          string  `json:"reason"`
	SemanticScore   float32 `json:"semantic_score,omitempty"`
	SemanticLimited bool    `json:"semantic_text_truncated,omitempty"`
}

// Result stays compatible with the existing flattened search result while
// adding local project identity and an auditable match explanation.
type Result struct {
	queryindex.SearchResult
	Project *Project `json:"project,omitempty"`
	Match   Match    `json:"match"`

	score float64
}

// Rank performs hybrid retrieval. Literal keyword matches always remain
// eligible and rank ahead of meaning-only matches; semantic similarity then
// supplies results that share no query terms.
func Rank(query string, candidates []Candidate, encoder Encoder, limit int) ([]Result, error) {
	if encoder == nil {
		return nil, fmt.Errorf("semantic encoder is required")
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}

	texts := []string{query}
	semanticIndexes := make([]int, 0, len(candidates))
	truncated := make(map[int]bool)
	for index, candidate := range candidates {
		text, limited := boundedSemanticText(candidate.Document.Text)
		if text == "" {
			continue
		}
		semanticIndexes = append(semanticIndexes, index)
		texts = append(texts, text)
		truncated[index] = limited
	}

	vectors, err := encoder.EncodeMany(texts)
	if err != nil {
		return nil, fmt.Errorf("encode local search text: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("semantic encoder returned %d vectors for %d texts", len(vectors), len(texts))
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("semantic encoder returned an empty query vector")
	}

	semanticScores := make(map[int]float32, len(semanticIndexes))
	for vectorIndex, candidateIndex := range semanticIndexes {
		score, err := cosineForNormalized(vectors[0], vectors[vectorIndex+1])
		if err != nil {
			return nil, err
		}
		semanticScores[candidateIndex] = score
	}

	results := make([]Result, 0, len(candidates))
	for index, candidate := range candidates {
		semanticScore := semanticScores[index]
		meaningMatch := semanticScore >= semanticThreshold
		if !candidate.Keyword && !meaningMatch {
			continue
		}

		match := Match{SemanticLimited: truncated[index]}
		rankingScore := float64(semanticScore)
		switch {
		case candidate.Keyword && meaningMatch:
			match.Kind = "keyword+meaning"
			match.Reason = fmt.Sprintf("keyword match; meaning similarity %.2f", semanticScore)
			match.SemanticScore = semanticScore
		case candidate.Keyword:
			match.Kind = "keyword"
			match.Reason = "keyword match"
		case meaningMatch:
			match.Kind = "meaning"
			match.Reason = fmt.Sprintf("meaning similarity %.2f", semanticScore)
			match.SemanticScore = semanticScore
		}
		if candidate.Keyword {
			// Literal identifiers and errors are the strongest evidence. The
			// reciprocal term preserves global FTS order across those matches.
			rankingScore = 2 + 1/float64(candidate.KeywordRank+1) + float64(semanticScore)/100
		}
		results = append(results, Result{
			SearchResult: candidate.Document.Result,
			Project:      candidate.Project,
			Match:        match,
			score:        rankingScore,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		leftProject, rightProject := projectSortKey(results[i].Project), projectSortKey(results[j].Project)
		if leftProject != rightProject {
			return leftProject < rightProject
		}
		if results[i].SessionID != results[j].SessionID {
			return results[i].SessionID < results[j].SessionID
		}
		return results[i].TurnID < results[j].TurnID
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// KeywordResults wraps lexical-only results with the same result contract used
// by hybrid search.
func KeywordResults(candidates []Candidate, limit int) ([]Result, error) {
	if limit < 0 {
		return nil, fmt.Errorf("limit must be zero or greater")
	}
	results := make([]Result, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.Keyword {
			continue
		}
		results = append(results, Result{
			SearchResult: candidate.Document.Result,
			Project:      candidate.Project,
			Match:        Match{Kind: "keyword", Reason: "keyword match"},
		})
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func boundedSemanticText(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) <= maxSemanticTextLen {
		return value, false
	}
	return strings.ToValidUTF8(value[:maxSemanticTextLen], ""), true
}

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
