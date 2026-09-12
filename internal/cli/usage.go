package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/usage"
	"github.com/AadiJo/turnal/internal/viewer"
	"github.com/spf13/cobra"
)

type usageReport struct {
	Project        string         `json:"project"`
	PricingVersion string         `json:"pricing_version"`
	Usage          usage.Summary  `json:"usage"`
	Sessions       []usageSession `json:"sessions"`
}

type usageSession struct {
	ID      string        `json:"id"`
	Adapter string        `json:"adapter,omitempty"`
	Model   string        `json:"model,omitempty"`
	Prompt  string        `json:"prompt,omitempty"`
	Usage   usage.Summary `json:"usage"`
}

func usageCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "usage",
		Short:        "Summarize recorded token usage and estimated cost",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			service, err := viewer.NewService(repo)
			if err != nil {
				return err
			}
			sessions, err := service.Sessions(cmd.Context())
			if err != nil {
				return err
			}
			report := usageReport{Project: filepath.Base(repo.WorkspaceRoot.String()), PricingVersion: usage.PricingVersion}
			for _, session := range sessions {
				report.Usage.Add(session.Usage)
				report.Sessions = append(report.Sessions, usageSession{
					ID: session.ID, Adapter: session.Adapter, Model: session.Model,
					Prompt: session.PromptPreview, Usage: session.Usage,
				})
			}
			sort.Slice(report.Sessions, func(i, j int) bool { return report.Sessions[i].Usage.Tokens() > report.Sessions[j].Usage.Tokens() })
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			return writeUsageReport(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func writeUsageReport(out io.Writer, report usageReport) error {
	var text strings.Builder
	fmt.Fprintf(&text, "Usage for %s\n\n", report.Project)
	fmt.Fprintf(&text, "Tokens: %s (%s input, %s cached, %s output)\n",
		formatTokenCount(report.Usage.Tokens()), formatTokenCount(report.Usage.InputTokens),
		formatTokenCount(report.Usage.CacheReadTokens+report.Usage.CacheWriteTokens), formatTokenCount(report.Usage.OutputTokens))
	if report.Usage.PricedTokens > 0 {
		partial := ""
		if report.Usage.PricedTokens < report.Usage.Tokens() {
			partial = "; some tokens are unpriced"
		}
		fmt.Fprintf(&text, "Estimated API cost: $%.2f (pricing %s%s)\n", float64(report.Usage.EstimatedCostMicros)/1_000_000, report.PricingVersion, partial)
	} else if report.Usage.Tokens() > 0 {
		fmt.Fprintln(&text, "Estimated API cost: unavailable for the recorded models")
	}
	fmt.Fprintf(&text, "Coverage: %d/%d turns\n", report.Usage.CoveredTurns, report.Usage.TotalTurns)
	if len(report.Sessions) == 0 {
		fmt.Fprintln(&text, "\nNo recorded sessions.")
	} else {
		fmt.Fprintln(&text, "\nSessions")
	}
	for _, session := range report.Sessions {
		label := strings.TrimSpace(session.Prompt)
		if label == "" {
			label = session.ID
		}
		if len(label) > 52 {
			label = label[:49] + "..."
		}
		cost := "unpriced"
		if session.Usage.PricedTokens > 0 {
			cost = fmt.Sprintf("$%.2f", float64(session.Usage.EstimatedCostMicros)/1_000_000)
			if session.Usage.PricedTokens < session.Usage.Tokens() {
				cost += "+"
			}
		}
		fmt.Fprintf(&text, "%-52s  %8s  %8s  %s\n", label, formatTokenCount(session.Usage.Tokens()), cost, session.Model)
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func formatTokenCount(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}
