package usage

import "testing"

func TestDeltaSubtractsCumulativeCounters(t *testing.T) {
	previous := TokenUsage{InputTokens: 100, CacheReadTokens: 50, OutputTokens: 20, APICalls: 2}
	got := Delta(TokenUsage{InputTokens: 160, CacheReadTokens: 90, OutputTokens: 35, APICalls: 3}, &previous)
	want := (TokenUsage{InputTokens: 60, CacheReadTokens: 40, OutputTokens: 15, APICalls: 1})
	if got != want {
		t.Fatalf("Delta() = %+v, want %+v", got, want)
	}
}

func TestDeltaTreatsCounterResetAsNewSegment(t *testing.T) {
	previous := TokenUsage{InputTokens: 100, OutputTokens: 20}
	current := TokenUsage{InputTokens: 10, OutputTokens: 2}
	if got := Delta(current, &previous); got != current {
		t.Fatalf("Delta() = %+v, want %+v", got, current)
	}
}

func TestEstimateCostMicros(t *testing.T) {
	tokens := TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000, OutputTokens: 1_000_000}
	if got, ok := EstimateCostMicros("gpt-5.6-sol", tokens); !ok || got != 29_400_000 {
		t.Fatalf("EstimateCostMicros() = %d, %v, want 29400000, true", got, ok)
	}
	if _, ok := EstimateCostMicros("private-model", tokens); ok {
		t.Fatal("unknown model unexpectedly priced")
	}
}
