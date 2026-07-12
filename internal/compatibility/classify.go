package compatibility

import (
	"context"
	"fmt"

	"github.com/AadiJo/turnal/internal/adapters"
)

type Options struct {
	WorkspaceRoot string
	HookCommand   string
	Targets       []adapters.Target
	ProbeCodex    bool
	CodexProbe    CodexProbe
}

type CodexProbe interface {
	Probe(context.Context, string, string) (CodexHooksResult, error)
}

func Diagnose(ctx context.Context, options Options) Report {
	static := adapters.InspectHooksForTargets(options.WorkspaceRoot, options.HookCommand, options.Targets)
	byTarget := make(map[adapters.Target]adapters.HookHealth, len(static))
	for _, health := range static {
		byTarget[health.Target] = health
	}

	var report Report
	for _, target := range options.Targets {
		switch target {
		case adapters.TargetClaude:
			health := byTarget[target]
			report.Surfaces = append(report.Surfaces, classifyClaudeCode(health), classifyClaudeSDK(health))
		case adapters.TargetCodex:
			health := byTarget[target]
			report.Surfaces = append(report.Surfaces, classifyCodexCLI(health))
			if options.ProbeCodex {
				report.Surfaces = append(report.Surfaces, probeCodexSurface(ctx, options, health))
			}
		}
	}
	return report
}

func classifyClaudeCode(health adapters.HookHealth) SurfaceResult {
	result := baseStaticResult(ProviderClaude, SurfaceClaudeCode, health)
	result.Limitations = []string{"workspace inspection confirms project configuration, not a live Claude Code process"}
	result.Guidance = []string{"run Claude Code from this project so it loads .claude/settings.json"}
	return result
}

func classifyClaudeSDK(health adapters.HookHealth) SurfaceResult {
	result := SurfaceResult{
		Provider:      ProviderClaude,
		Surface:       SurfaceClaudeAgentSDK,
		Configuration: health.Status,
		Visibility:    VisibilityHostControlled,
		Execution:     ExecutionHostControlled,
		Expectation:   CaptureHostControlled,
		Certainty:     CertaintyHostControlled,
		Limitations: []string{
			"Turnal cannot determine an arbitrary SDK host's settingSources from workspace state",
			"Turnal does not currently consume the Claude Agent SDK message stream directly",
		},
		Guidance: []string{"omit settingSources or include \"project\"; settingSources: [] prevents project hooks from loading"},
	}
	if health.Status != adapters.HookConfigurationConfigured {
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyIncompatible
		result.Guidance = append([]string{"configure the required Claude project hooks"}, result.Guidance...)
	}
	return result
}

func classifyCodexCLI(health adapters.HookHealth) SurfaceResult {
	result := baseStaticResult(ProviderCodex, SurfaceCodexCLI, health)
	result.Limitations = []string{"workspace inspection confirms project configuration, not a live Codex CLI process"}
	result.Guidance = []string{"turnal run -- codex provides wrapper checkpoints even when rich hook capture is unavailable"}
	return result
}

func baseStaticResult(provider Provider, surface Surface, health adapters.HookHealth) SurfaceResult {
	result := SurfaceResult{
		Provider:      provider,
		Surface:       surface,
		Configuration: health.Status,
		Visibility:    VisibilityNotProbed,
		Execution:     ExecutionExpected,
		Expectation:   CaptureAvailable,
		Certainty:     CertaintyLikely,
	}
	if health.Status != adapters.HookConfigurationConfigured {
		result.Visibility = VisibilityUnavailable
		result.Execution = ExecutionUnavailable
		result.Expectation = CaptureUnavailable
		result.Certainty = CertaintyIncompatible
		result.Guidance = append(result.Guidance, health.Problems...)
	}
	return result
}

func probeCodexSurface(ctx context.Context, options Options, health adapters.HookHealth) SurfaceResult {
	result := SurfaceResult{
		Provider:      ProviderCodex,
		Surface:       SurfaceCodexAppServer,
		Configuration: health.Status,
		Visibility:    VisibilityUnavailable,
		Execution:     ExecutionUnavailable,
		Expectation:   CaptureUnavailable,
		Certainty:     CertaintyUnavailable,
		Expected:      len(expectedCodexEventNames),
	}
	if options.CodexProbe == nil {
		result.ProbeError = "Codex app-server probe is not configured"
		return result
	}
	probed, err := options.CodexProbe.Probe(ctx, options.WorkspaceRoot, options.HookCommand+" codex-hook")
	if err != nil {
		result.ProbeError = err.Error()
		result.Guidance = []string{"verify that a compatible codex executable is installed and rerun the probe"}
		return result
	}
	classified := ClassifyCodexHooks(options.WorkspaceRoot, options.HookCommand+" codex-hook", health, probed)
	return classified
}

func ConfigurationSummary(status adapters.HookConfigurationStatus) string {
	if status == "" {
		return "unavailable"
	}
	return string(status)
}

func countSummary(value, total int) string {
	return fmt.Sprintf("%d/%d", value, total)
}
