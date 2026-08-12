package semantic

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/discovery"
	queryindex "github.com/AadiJo/turnal/internal/index"
)

// requireModel loads the real encoder, skipping when the model is not already
// cached. Tests must not download an 8 MB model on an offline or cold machine.
func requireModel(t *testing.T) *Encoder {
	t.Helper()
	if os.Getenv("TURNAL_TEST_SEMANTIC") == "" {
		t.Skip("set TURNAL_TEST_SEMANTIC=1 to run tests against the downloaded model")
	}
	encoder, err := NewEncoder(context.Background())
	if err != nil {
		t.Skipf("local semantic model unavailable: %v", err)
	}
	return encoder
}

func similarity(t *testing.T, encoder *Encoder, query, document string) float32 {
	t.Helper()
	vectors, err := encoder.EncodeMany([]string{query, document})
	if err != nil {
		t.Fatalf("EncodeMany: %v", err)
	}
	var score float32
	for index := range vectors[0] {
		score += vectors[0][index] * vectors[1][index]
	}
	return score
}

// The model mean-pools token vectors, so text appended around a prompt pulls
// the document vector toward that filler. This is why SearchDocuments embeds
// only the prompt and assistant text: folding in the adapter name, model name,
// tool names, and raw event text measurably buries a real match.
func TestMetadataDilutesMeaningMatch(t *testing.T) {
	encoder := requireModel(t)

	const query = "why does history sync fail open"
	const prompt = "Context upload must not block the source push."

	focused := similarity(t, encoder, query, prompt)
	diluted := similarity(t, encoder, query, strings.Join([]string{
		"codex",
		"gpt-5",
		prompt,
		"apply_patch\nread_file\nbash",
		"internal/index/store.go\ninternal/cli/search.go",
		strings.Repeat("tool_call apply_patch exit 0 diff --git ", 200),
	}, "\n"))

	if diluted >= focused {
		t.Fatalf("diluted similarity %.3f >= focused %.3f; the premise of the narrow document is wrong", diluted, focused)
	}
	t.Logf("focused=%.3f diluted=%.3f", focused, diluted)
}

func TestPromptOnlyExampleIsMeaningMatch(t *testing.T) {
	encoder := requireModel(t)

	const query = "why does history sync fail open"
	const prompt = "Context upload must not block the source push."
	results, err := discovery.Rank(query, []discovery.Candidate{{
		Document: queryindex.SearchDocument{Text: prompt},
	}}, encoder, 20)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(results) != 1 || results[0].Match.Kind != "meaning" {
		t.Fatalf("results = %#v, want the prompt-only meaning match", results)
	}
}

// A genuinely related turn must clear the floor, and unrelated work must not.
func TestSimilarityFloorSeparatesRelatedFromUnrelated(t *testing.T) {
	encoder := requireModel(t)

	const query = "why does history sync fail open"
	related := []string{
		"Context upload must not block the source push.\nI made the upload path non-fatal so a failure is logged and the push proceeds.",
		"Make the checkpoint mirror tolerate a remote outage.\nErrors from the mirror are now warnings; recording continues.",
	}
	unrelated := []string{
		"Rename the CSS variable for the sidebar hover state.\nRenamed and updated call sites.",
		"Chocolate cake recipe with buttermilk and espresso.\nHere is the recipe.",
		"Cap the number of lanes drawn in the graph.\nLanes above the cap collapse into one column.",
	}

	for _, document := range related {
		if score := similarity(t, encoder, query, document); score < discovery.SimilarityFloor() {
			t.Errorf("related document scored %.3f, below the %.3f floor:\n%s", score, discovery.SimilarityFloor(), document)
		}
	}
	for _, document := range unrelated {
		if score := similarity(t, encoder, query, document); score >= discovery.SimilarityFloor() {
			t.Errorf("unrelated document scored %.3f, at or above the %.3f floor:\n%s", score, discovery.SimilarityFloor(), document)
		}
	}
}

func TestEncodeManyReturnsOneVectorPerText(t *testing.T) {
	encoder := requireModel(t)

	texts := []string{"first prompt", "second prompt", ""}
	vectors, err := encoder.EncodeMany(texts)
	if err != nil {
		t.Fatalf("EncodeMany: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("vectors = %d, want %d", len(vectors), len(texts))
	}
	for index, vector := range vectors {
		if len(vector) == 0 {
			t.Fatalf("vector %d is empty", index)
		}
	}
}
