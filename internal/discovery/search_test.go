package discovery

import (
	"strings"
	"testing"
	"unicode/utf8"

	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

type encoderFunc func([]string) ([][]float32, error)

func (fn encoderFunc) EncodeMany(texts []string) ([][]float32, error) { return fn(texts) }

// unitEncoder maps each text to a unit vector chosen by keyword, so tests can
// assert on similarity without depending on a downloaded model.
func unitEncoder(vectors map[string][2]float32) Encoder {
	return encoderFunc(func(texts []string) ([][]float32, error) {
		encoded := make([][]float32, 0, len(texts))
		for _, text := range texts {
			vector := [2]float32{0, 1}
			for marker, candidate := range vectors {
				if strings.Contains(strings.ToLower(text), marker) {
					vector = candidate
					break
				}
			}
			encoded = append(encoded, []float32{vector[0], vector[1]})
		}
		return encoded, nil
	})
}

func turn(t *testing.T, session string, number uint64, text string) queryindex.SearchDocument {
	t.Helper()
	turnID, err := primitives.NewTurnID(number)
	if err != nil {
		t.Fatal(err)
	}
	return queryindex.SearchDocument{
		Result: queryindex.SearchResult{
			SessionID: primitives.SessionID(session),
			TurnID:    turnID,
			Prompt:    text,
		},
		Text: text,
	}
}

func TestRankReturnsMeaningMatchesWithoutSharedTerms(t *testing.T) {
	encoder := unitEncoder(map[string][2]float32{
		"fail open": {1, 0},
		"not block": {0.9, 0.1},
		"sidebar":   {0, 1},
	})
	candidates := []Candidate{
		{
			Project:  &Project{Name: "cli", Root: "/src/cli", StoreID: "store-cli"},
			Document: turn(t, "push", 1, "Context upload must not block the source push."),
		},
		{
			Project:  &Project{Name: "web", Root: "/src/web", StoreID: "store-web"},
			Document: turn(t, "style", 2, "Rename the sidebar hover variable."),
		},
	}

	results, err := Rank("why does history sync fail open", candidates, encoder, 20)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want only the related turn", results)
	}
	if results[0].Match.Kind != "meaning" {
		t.Fatalf("match kind = %q, want meaning", results[0].Match.Kind)
	}
	if results[0].Match.Similarity < similarityFloor {
		t.Fatalf("similarity = %v, want at least the floor %v", results[0].Match.Similarity, similarityFloor)
	}
}

// Keyword order is the store's full-text ranking. Meaning similarity annotates
// those hits but must never reorder them, however similar a lower hit looks.
func TestRankKeepsKeywordOrderRegardlessOfSimilarity(t *testing.T) {
	encoder := unitEncoder(map[string][2]float32{
		"weak":   {0, 1},
		"strong": {1, 0},
	})
	var candidates []Candidate
	for rank := 0; rank < 12; rank++ {
		text := "weak keyword hit"
		if rank%2 == 1 {
			text = "strong keyword hit"
		}
		candidates = append(candidates, Candidate{
			Document:    turn(t, "session", uint64(rank+1), text),
			Keyword:     true,
			KeywordRank: rank,
		})
	}

	results, err := Rank("strong", candidates, encoder, 0)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != len(candidates) {
		t.Fatalf("results = %d, want %d", len(results), len(candidates))
	}
	for index, result := range results {
		want, err := primitives.NewTurnID(uint64(index + 1))
		if err != nil {
			t.Fatal(err)
		}
		if result.TurnID != want {
			t.Fatalf("result %d = turn %s, want %s: keyword order was not preserved", index, result.TurnID, want)
		}
	}
}

// Keyword evidence is stronger than an inferred match, so every keyword hit
// outranks every meaning-only hit even when the latter scores near 1.0.
func TestRankTiersKeywordAboveMeaningOnly(t *testing.T) {
	// The meaning-only turn is a near-perfect vector match for the query; the
	// keyword turn is orthogonal to it. Tiering must still put the keyword
	// turn first.
	encoder := unitEncoder(map[string][2]float32{
		"fail open": {1, 0},
		"not block": {1, 0},
		"sidebar":   {0, 1},
	})
	candidates := []Candidate{
		{Document: turn(t, "a", 1, "Context upload must not block the push.")},
		{Document: turn(t, "b", 2, "Rename the sidebar variable."), Keyword: true, KeywordRank: 0},
	}

	results, err := Rank("why does history sync fail open", candidates, encoder, 20)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want 2", results)
	}
	if results[0].Match.Kind != "keyword" {
		t.Fatalf("first result = %#v, want the keyword hit first", results[0])
	}
	if results[1].Match.Kind != "meaning" {
		t.Fatalf("second result = %#v, want the meaning-only hit second", results[1])
	}
}

func TestRankWithoutEncoderReturnsKeywordMatchesOnly(t *testing.T) {
	candidates := []Candidate{
		{Document: turn(t, "a", 1, "unmatched text")},
		{Document: turn(t, "b", 2, "matched text"), Keyword: true, KeywordRank: 0},
	}

	results, err := Rank("matched", candidates, nil, 20)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 1 || results[0].Match.Kind != "keyword" {
		t.Fatalf("results = %#v, want only the keyword match", results)
	}
}

func TestRankAppliesLimitAfterOrdering(t *testing.T) {
	candidates := []Candidate{
		{Document: turn(t, "a", 1, "first"), Keyword: true, KeywordRank: 0},
		{Document: turn(t, "b", 2, "second"), Keyword: true, KeywordRank: 1},
		{Document: turn(t, "c", 3, "third"), Keyword: true, KeywordRank: 2},
	}

	results, err := Rank("query", candidates, nil, 2)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].SessionID != "a" || results[1].SessionID != "b" {
		t.Fatalf("results = %#v, want the two highest-ranked keyword hits", results)
	}
}

func TestRankRejectsMismatchedVectorDimensions(t *testing.T) {
	encoder := encoderFunc(func([]string) ([][]float32, error) {
		return [][]float32{{1, 0}, {1}}, nil
	})
	_, err := Rank("query", []Candidate{{Document: turn(t, "demo", 1, "document")}}, encoder, 20)
	if err == nil || !strings.Contains(err.Error(), "dimensions differ") {
		t.Fatalf("Rank error = %v, want a dimension mismatch", err)
	}
}

func TestRankRejectsShortEncoderOutput(t *testing.T) {
	encoder := encoderFunc(func([]string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	})
	_, err := Rank("query", []Candidate{{Document: turn(t, "demo", 1, "document")}}, encoder, 20)
	if err == nil || !strings.Contains(err.Error(), "vectors for") {
		t.Fatalf("Rank error = %v, want a vector count mismatch", err)
	}
}

// Equal keyword rank across projects is common, so the documented tie-break
// chain decides what a limit keeps. Ties resolve on project root, then session,
// then turn.
func TestRankBreaksTiesOnProjectThenSessionThenTurn(t *testing.T) {
	beta := &Project{Name: "beta", Root: "/src/beta", StoreID: "store-beta"}
	alpha := &Project{Name: "alpha", Root: "/src/alpha", StoreID: "store-alpha"}
	candidates := []Candidate{
		{Project: beta, Document: turn(t, "b", 1, "hit"), Keyword: true},
		{Project: alpha, Document: turn(t, "b", 2, "hit"), Keyword: true},
		{Project: alpha, Document: turn(t, "b", 1, "hit"), Keyword: true},
		{Project: alpha, Document: turn(t, "a", 9, "hit"), Keyword: true},
	}

	results, err := Rank("hit", candidates, nil, 0)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	var order []string
	for _, result := range results {
		order = append(order, result.Project.Name+"/"+string(result.SessionID)+"/"+result.TurnID.String())
	}
	want := []string{"alpha/a/9", "alpha/b/1", "alpha/b/2", "beta/b/1"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("tie-break order = %v, want %v", order, want)
	}
}

// Truncation qualifies a similarity score. A keyword hit is exact evidence, so
// a turn matched only by keyword must not carry the truncation caveat even when
// its indexed text was too long to embed whole.
func TestRankOmitsTruncationCaveatForKeywordOnlyMatches(t *testing.T) {
	encoder := unitEncoder(map[string][2]float32{"query": {1, 0}})
	long := strings.Repeat("unrelated filler ", maxSemanticTextLen)
	candidates := []Candidate{
		{Document: turn(t, "keyword", 1, long), Keyword: true, KeywordRank: 0},
		{Document: turn(t, "meaning", 2, "query "+long)},
	}

	results, err := Rank("query", candidates, encoder, 0)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want both the keyword and meaning hits", results)
	}
	if results[0].Match.Kind != "keyword" {
		t.Fatalf("first result kind = %q, want keyword", results[0].Match.Kind)
	}
	if results[0].Match.SemanticLimited {
		t.Fatal("keyword-only result reported a truncated-text caveat it cannot have used")
	}
	if !results[1].Match.SemanticLimited {
		t.Fatal("meaning result lost the truncated-text caveat that qualifies its score")
	}
}

func TestBoundSemanticTextReportsTruncation(t *testing.T) {
	short, limited := boundSemanticText("  concise prompt  ")
	if short != "concise prompt" || limited {
		t.Fatalf("boundSemanticText(short) = %q, %v", short, limited)
	}

	// A three-byte rune does not divide the byte budget, so the cut lands
	// mid-rune and the partial trailing bytes must be dropped rather than
	// handed to the tokenizer.
	if maxSemanticTextLen%3 == 0 {
		t.Fatal("pick a rune width that does not divide maxSemanticTextLen, or the cut is always aligned")
	}
	long, limited := boundSemanticText(strings.Repeat("☃", maxSemanticTextLen))
	if !limited {
		t.Fatal("boundSemanticText(long) did not report truncation")
	}
	if len(long) > maxSemanticTextLen {
		t.Fatalf("truncated length = %d, want at most %d", len(long), maxSemanticTextLen)
	}
	if !utf8.ValidString(long) {
		t.Fatalf("truncation left invalid UTF-8: %q", long[len(long)-8:])
	}
	if strings.Count(long, "☃") != maxSemanticTextLen/3 {
		t.Fatalf("truncated text kept %d runes, want %d whole runes", strings.Count(long, "☃"), maxSemanticTextLen/3)
	}
}
