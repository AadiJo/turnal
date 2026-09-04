// Package usage defines provider-neutral token accounting and local cost estimates.
package usage

import (
	"strings"
	"time"
)

// TokenUsage is the billable token shape shared by captured events and views.
// ReasoningTokens is informational because providers include it in OutputTokens.
type TokenUsage struct {
	InputTokens      int64 `json:"input_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	OutputTokens     int64 `json:"output_tokens,omitempty"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
	APICalls         int64 `json:"api_calls,omitempty"`
}

func (u TokenUsage) Tokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}

func (u TokenUsage) Valid() bool {
	return u.InputTokens >= 0 && u.CacheReadTokens >= 0 && u.CacheWriteTokens >= 0 &&
		u.OutputTokens >= 0 && u.ReasoningTokens >= 0 && u.APICalls >= 0
}

func (u *TokenUsage) Add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.APICalls += other.APICalls
}

// Delta converts a cumulative provider reading to the usage since the prior
// reading. Missing baselines and counter resets cannot be attributed safely.
func Delta(current TokenUsage, previous *TokenUsage) (TokenUsage, bool) {
	if previous == nil || !current.Valid() || !previous.Valid() || current.InputTokens < previous.InputTokens ||
		current.CacheReadTokens < previous.CacheReadTokens ||
		current.CacheWriteTokens < previous.CacheWriteTokens ||
		current.OutputTokens < previous.OutputTokens ||
		current.ReasoningTokens < previous.ReasoningTokens ||
		current.APICalls < previous.APICalls {
		return TokenUsage{}, false
	}
	return TokenUsage{
		InputTokens:      current.InputTokens - previous.InputTokens,
		CacheReadTokens:  current.CacheReadTokens - previous.CacheReadTokens,
		CacheWriteTokens: current.CacheWriteTokens - previous.CacheWriteTokens,
		OutputTokens:     current.OutputTokens - previous.OutputTokens,
		ReasoningTokens:  current.ReasoningTokens - previous.ReasoningTokens,
		APICalls:         current.APICalls - previous.APICalls,
	}, true
}

const PricingVersion = "2026-09-04"

type rates struct {
	input      float64
	cacheRead  float64
	cacheWrite float64
	output     float64
}

// EstimateCostMicros returns an API-equivalent estimate in millionths of a US
// dollar. It deliberately returns false for unknown models instead of guessing.
func EstimateCostMicros(model string, tokens TokenUsage) (int64, bool) {
	rate, ok := ratesForModel(strings.ToLower(strings.TrimSpace(model)))
	if !ok || !tokens.Valid() || tokens.CacheWriteTokens > 0 && rate.cacheWrite == 0 {
		return 0, false
	}
	cost := float64(tokens.InputTokens)*rate.input +
		float64(tokens.CacheReadTokens)*rate.cacheRead +
		float64(tokens.CacheWriteTokens)*rate.cacheWrite +
		float64(tokens.OutputTokens)*rate.output
	return int64(cost + 0.5), true
}

// Rates are microdollars per token, numerically equal to USD per million tokens.
func ratesForModel(model string) (rates, bool) {
	// Provider snapshots append a date to an otherwise supported model name.
	// Strip only a valid date, so new variants do not inherit a base model's rate.
	layout := "2006-01-02"
	if strings.HasPrefix(model, "claude-") {
		layout = "20060102"
	}
	if suffix := len(model) - len(layout); suffix > 0 && model[suffix-1] == '-' {
		if date, err := time.Parse(layout, model[suffix:]); err == nil && date.Format(layout) == model[suffix:] {
			model = model[:suffix-1]
		}
	}
	switch model {
	case "gpt-5.6", "gpt-5.6-sol":
		return rates{input: 4, cacheRead: 0.4, cacheWrite: 5, output: 20}, true
	case "gpt-5.6-terra":
		return rates{input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 12}, true
	case "gpt-5.6-luna":
		return rates{input: 0.2, cacheRead: 0.02, cacheWrite: 0.25, output: 1.2}, true
	case "gpt-5.5":
		return rates{input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 30}, true
	case "gpt-5.4":
		return rates{input: 2.5, cacheRead: 0.25, cacheWrite: 3.125, output: 15}, true
	case "gpt-5.4-mini":
		return rates{input: 0.75, cacheRead: 0.075, output: 4.5}, true
	case "gpt-5.3-codex", "gpt-5.2":
		return rates{input: 1.75, cacheRead: 0.175, cacheWrite: 2.1875, output: 14}, true
	case "claude-fable-5", "claude-mythos-5":
		return rates{input: 10, cacheRead: 1, cacheWrite: 12.5, output: 50}, true
	case "claude-fable-5-1", "claude-fable-5.1", "claude-mythos-5-1", "claude-mythos-5.1":
		return rates{input: 10, cacheRead: 0.25, cacheWrite: 12.5, output: 50}, true
	case "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5":
		return rates{input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 25}, true
	case "claude-opus-4-1", "claude-opus-4":
		return rates{input: 15, cacheRead: 1.5, cacheWrite: 18.75, output: 75}, true
	case "claude-sonnet-5":
		return rates{input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 10}, true
	case "claude-sonnet-4", "claude-sonnet-4-5", "claude-sonnet-4-6":
		return rates{input: 3, cacheRead: 0.3, cacheWrite: 3.75, output: 15}, true
	case "claude-haiku-4-5":
		return rates{input: 1, cacheRead: 0.1, cacheWrite: 1.25, output: 5}, true
	default:
		return rates{}, false
	}
}

// Summary combines usage with the completeness and pricing context required by
// aggregate views. Cost is only added for turns whose model is recognized.
type Summary struct {
	TokenUsage
	EstimatedCostMicros int64 `json:"estimated_cost_micros,omitempty"`
	PricedTokens        int64 `json:"priced_tokens,omitempty"`
	CoveredTurns        int   `json:"covered_turns,omitempty"`
	TotalTurns          int   `json:"total_turns,omitempty"`
}

func (s *Summary) Add(other Summary) {
	s.TokenUsage.Add(other.TokenUsage)
	s.EstimatedCostMicros += other.EstimatedCostMicros
	s.PricedTokens += other.PricedTokens
	s.CoveredTurns += other.CoveredTurns
	s.TotalTurns += other.TotalTurns
}

func SummarizeTurn(model string, tokens *TokenUsage) Summary {
	summary := Summary{TotalTurns: 1}
	if tokens == nil {
		return summary
	}
	summary.TokenUsage = *tokens
	summary.CoveredTurns = 1
	if cost, ok := EstimateCostMicros(model, *tokens); ok {
		summary.EstimatedCostMicros = cost
		summary.PricedTokens = tokens.Tokens()
	}
	return summary
}
