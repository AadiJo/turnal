package usage

import "testing"

func TestDeltaSubtractsCumulativeCounters(t *testing.T) {
	previous := TokenUsage{InputTokens: 100, CacheReadTokens: 50, OutputTokens: 20, APICalls: 2}
	got, ok := Delta(TokenUsage{InputTokens: 160, CacheReadTokens: 90, OutputTokens: 35, APICalls: 3}, &previous)
	want := (TokenUsage{InputTokens: 60, CacheReadTokens: 40, OutputTokens: 15, APICalls: 1})
	if !ok || got != want {
		t.Fatalf("Delta() = %+v, want %+v", got, want)
	}
}

func TestDeltaRejectsMissingBaselineAndCounterReset(t *testing.T) {
	previous := TokenUsage{InputTokens: 100, OutputTokens: 20}
	current := TokenUsage{InputTokens: 10, OutputTokens: 2}
	for _, baseline := range []*TokenUsage{nil, &previous} {
		if got, ok := Delta(current, baseline); ok || got != (TokenUsage{}) {
			t.Fatalf("Delta() = %+v, %v, want zero, false", got, ok)
		}
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

func TestEstimateCostModelMatching(t *testing.T) {
	for _, tt := range []struct {
		model     string
		inputCost int64
	}{
		{"gpt-5.4", 2_500_000},
		{" GPT-5.4-MINI ", 750_000},
		{"gpt-5.4-mini-2026-03-17", 750_000},
		{"gpt-5.6-sol", 4_000_000},
		{"gpt-5.6-sol-2026-09-04", 4_000_000},
		{"claude-opus-4-1-20250805", 15_000_000},
		{"claude-sonnet-4-5-20250929", 3_000_000},
		{"gpt-5.4-pro", 0},
		{"gpt-5.4-nano", 0},
		{"gpt-5.4-unknown", 0},
		{"gpt-5.5-pro", 0},
		{"gpt-5.3-codex-spark", 0},
		{"private-gpt-5.6-sol", 0},
		{"gpt-5.6-sol-extra", 0},
		{"claude-opus-4-99", 0},
		{"claude-sonnet-4-99", 0},
		{"claude-fable-5-2", 0},
		{"claude-opus-4-1-20250230", 0},
		{"gpt-5.4-2026-02-30", 0},
		{"gpt-5.4-2026-3-17", 0},
		{"gpt-5.4-mini-2026-03-17-extra", 0},
	} {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := EstimateCostMicros(tt.model, TokenUsage{InputTokens: 1_000_000})
			if got != tt.inputCost || ok != (tt.inputCost > 0) {
				t.Fatalf("EstimateCostMicros() = %d, %v, want %d, %v", got, ok, tt.inputCost, tt.inputCost > 0)
			}
		})
	}
}

func TestEstimateCostMiniCachedAndOutputTokens(t *testing.T) {
	got, ok := EstimateCostMicros("gpt-5.4-mini", TokenUsage{CacheReadTokens: 1_000_000, OutputTokens: 1_000_000})
	if !ok || got != 4_575_000 {
		t.Fatalf("EstimateCostMicros() = %d, %v, want 4575000, true", got, ok)
	}
}

func TestEstimateCostRejectsUnsupportedCacheWriteAndInvalidTokens(t *testing.T) {
	for _, tokens := range []TokenUsage{
		{InputTokens: 100, CacheWriteTokens: 1},
		{InputTokens: -1},
		{ReasoningTokens: -1},
		{APICalls: -1},
	} {
		if got, ok := EstimateCostMicros("gpt-5.4-mini", tokens); ok || got != 0 {
			t.Fatalf("EstimateCostMicros(%+v) = %d, %v, want 0, false", tokens, got, ok)
		}
	}
}

func TestDeltaRejectsInvalidCounters(t *testing.T) {
	valid := TokenUsage{InputTokens: 100}
	invalid := TokenUsage{InputTokens: -1}
	for _, tt := range []struct {
		current  TokenUsage
		previous TokenUsage
	}{
		{valid, invalid},
		{invalid, invalid},
		{invalid, valid},
	} {
		if got, ok := Delta(tt.current, &tt.previous); ok || got != (TokenUsage{}) {
			t.Fatalf("Delta(%+v, %+v) = %+v, %v, want zero, false", tt.current, tt.previous, got, ok)
		}
	}
}
