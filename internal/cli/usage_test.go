package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/usage"
)

func TestWriteUsageReportShowsCoverageAndUnpricedSessions(t *testing.T) {
	report := usageReport{
		Project: "demo", PricingVersion: usage.PricingVersion,
		Usage: usage.Summary{
			TokenUsage:          usage.TokenUsage{InputTokens: 1_200, CacheReadTokens: 800, OutputTokens: 100},
			EstimatedCostMicros: 12_500, PricedTokens: 2_000, CoveredTurns: 2, TotalTurns: 3,
		},
		Sessions: []usageSession{
			{ID: "priced", Model: "gpt-5.6-sol", Prompt: "Implement usage", Usage: usage.Summary{TokenUsage: usage.TokenUsage{InputTokens: 2_000}, PricedTokens: 2_000, EstimatedCostMicros: 8_000}},
			{ID: "unknown", Model: "private-model", Usage: usage.Summary{TokenUsage: usage.TokenUsage{InputTokens: 100}}},
		},
	}
	var output bytes.Buffer
	if err := writeUsageReport(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Usage for demo", "Estimated API cost: $0.01", "Coverage: 2/3 turns", "unpriced", "private-model"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}
