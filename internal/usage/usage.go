// Package usage defines provider-neutral token accounting and local cost estimates.
package usage

import "strings"

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
// reading. A counter reset starts a new segment instead of producing negatives.
func Delta(current TokenUsage, previous *TokenUsage) TokenUsage {
	if previous == nil || current.InputTokens < previous.InputTokens ||
		current.CacheReadTokens < previous.CacheReadTokens ||
		current.CacheWriteTokens < previous.CacheWriteTokens ||
		current.OutputTokens < previous.OutputTokens ||
		current.ReasoningTokens < previous.ReasoningTokens ||
		current.APICalls < previous.APICalls {
		return current
	}
	return TokenUsage{
		InputTokens:      current.InputTokens - previous.InputTokens,
		CacheReadTokens:  current.CacheReadTokens - previous.CacheReadTokens,
		CacheWriteTokens: current.CacheWriteTokens - previous.CacheWriteTokens,
		OutputTokens:     current.OutputTokens - previous.OutputTokens,
		ReasoningTokens:  current.ReasoningTokens - previous.ReasoningTokens,
		APICalls:         current.APICalls - previous.APICalls,
	}
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
	if !ok {
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
	switch {
	case model == "gpt-5.6" || strings.Contains(model, "gpt-5.6-sol"):
		return rates{input: 4, cacheRead: 0.4, cacheWrite: 5, output: 20}, true
	case strings.Contains(model, "gpt-5.6-terra"):
		return rates{input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 12}, true
	case strings.Contains(model, "gpt-5.6-luna"):
		return rates{input: 0.2, cacheRead: 0.02, cacheWrite: 0.25, output: 1.2}, true
	case model == "gpt-5.5" || strings.HasPrefix(model, "gpt-5.5-"):
		return rates{input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 30}, true
	case model == "gpt-5.4" || strings.HasPrefix(model, "gpt-5.4-"):
		return rates{input: 2.5, cacheRead: 0.25, cacheWrite: 3.125, output: 15}, true
	case strings.Contains(model, "gpt-5.3-codex") || model == "gpt-5.2":
		return rates{input: 1.75, cacheRead: 0.175, cacheWrite: 2.1875, output: 14}, true
	case strings.Contains(model, "claude-fable-5") || strings.Contains(model, "claude-mythos-5"):
		cacheRead := 1.0
		if strings.Contains(model, "5-1") || strings.Contains(model, "5.1") {
			cacheRead = 0.25
		}
		return rates{input: 10, cacheRead: cacheRead, cacheWrite: 12.5, output: 50}, true
	case strings.Contains(model, "claude-opus-5") || strings.Contains(model, "claude-opus-4-8") ||
		strings.Contains(model, "claude-opus-4-7") || strings.Contains(model, "claude-opus-4-6") ||
		strings.Contains(model, "claude-opus-4-5"):
		return rates{input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 25}, true
	case strings.Contains(model, "claude-opus-4-1") || strings.Contains(model, "claude-opus-4"):
		return rates{input: 15, cacheRead: 1.5, cacheWrite: 18.75, output: 75}, true
	case strings.Contains(model, "claude-sonnet-5"):
		return rates{input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 10}, true
	case strings.Contains(model, "claude-sonnet-4"):
		return rates{input: 3, cacheRead: 0.3, cacheWrite: 3.75, output: 15}, true
	case strings.Contains(model, "claude-haiku-4-5"):
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
