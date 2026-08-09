package discovery

import (
	"strings"
	"testing"

	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

type semanticFixtureEncoder struct{}

func (semanticFixtureEncoder) EncodeMany(texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		text = strings.ToLower(text)
		switch {
		case strings.Contains(text, "history sync fail open"):
			vectors = append(vectors, []float32{1, 0})
		case strings.Contains(text, "context upload must not block"):
			vectors = append(vectors, []float32{0.9, 0.1})
		default:
			vectors = append(vectors, []float32{0, 1})
		}
	}
	return vectors, nil
}

func TestRankCombinesKeywordAndMeaningMatches(t *testing.T) {
	firstTurn, _ := primitives.NewTurnID(1)
	secondTurn, _ := primitives.NewTurnID(2)
	candidates := []Candidate{
		{
			Project: &Project{Name: "turnal-cli", Root: "/src/turnal-cli", StoreID: "store-cli"},
			Document: queryindex.SearchDocument{
				Result: queryindex.SearchResult{SessionID: "push", TurnID: firstTurn, Prompt: "Context upload must not block the source push."},
				Text:   "Context upload must not block the source push.",
			},
		},
		{
			Project: &Project{Name: "turnal-cloud", Root: "/src/turnal-cloud", StoreID: "store-cloud"},
			Document: queryindex.SearchDocument{
				Result: queryindex.SearchResult{SessionID: "failure", TurnID: secondTurn, Prompt: "history sync fail open"},
				Text:   "history sync fail open",
			},
			Keyword:     true,
			KeywordRank: 0,
		},
	}

	results, err := Rank("why does history sync fail open", candidates, semanticFixtureEncoder{}, 20)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want 2", results)
	}
	if results[0].TurnID != secondTurn || results[0].Match.Kind != "keyword+meaning" {
		t.Fatalf("first result = %#v, want literal and meaning match", results[0])
	}
	if results[1].TurnID != firstTurn || results[1].Match.Kind != "meaning" {
		t.Fatalf("second result = %#v, want meaning-only match", results[1])
	}
}

func TestRankRejectsMismatchedVectorDimensions(t *testing.T) {
	turnID, _ := primitives.NewTurnID(1)
	encoder := encoderFunc(func([]string) ([][]float32, error) {
		return [][]float32{{1, 0}, {1}}, nil
	})
	_, err := Rank("query", []Candidate{{Document: queryindex.SearchDocument{
		Result: queryindex.SearchResult{SessionID: "demo", TurnID: turnID},
		Text:   "document",
	}}}, encoder, 20)
	if err == nil || !strings.Contains(err.Error(), "dimensions differ") {
		t.Fatalf("Rank error = %v, want dimension mismatch", err)
	}
}

type encoderFunc func([]string) ([][]float32, error)

func (fn encoderFunc) EncodeMany(texts []string) ([][]float32, error) {
	return fn(texts)
}
