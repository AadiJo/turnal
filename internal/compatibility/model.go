package compatibility

import "github.com/AadiJo/turnal/internal/adapters"

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type Surface string

const (
	SurfaceClaudeCode     Surface = "Claude Code"
	SurfaceClaudeAgentSDK Surface = "Claude Agent SDK"
	SurfaceCodexCLI       Surface = "Codex CLI"
	SurfaceCodexAppServer Surface = "Codex app-server"
)

type VisibilityStatus string

const (
	VisibilityConfirmed      VisibilityStatus = "confirmed"
	VisibilityHostControlled VisibilityStatus = "host-controlled"
	VisibilityNotProbed      VisibilityStatus = "not-probed"
	VisibilityUnavailable    VisibilityStatus = "unavailable"
)

type ExecutionStatus string

const (
	ExecutionExpected       ExecutionStatus = "expected"
	ExecutionConfirmed      ExecutionStatus = "confirmed"
	ExecutionDisabled       ExecutionStatus = "disabled"
	ExecutionUntrusted      ExecutionStatus = "untrusted"
	ExecutionHostControlled ExecutionStatus = "host-controlled"
	ExecutionUnavailable    ExecutionStatus = "unavailable"
)

type CaptureExpectation string

const (
	CaptureAvailable      CaptureExpectation = "available"
	CaptureUnavailable    CaptureExpectation = "unavailable"
	CaptureHostControlled CaptureExpectation = "host-controlled"
)

type Certainty string

const (
	CertaintyConfirmed      Certainty = "confirmed"
	CertaintyLikely         Certainty = "likely"
	CertaintyHostControlled Certainty = "host-controlled"
	CertaintyUnavailable    Certainty = "unavailable"
	CertaintyIncompatible   Certainty = "incompatible"
)

type SurfaceResult struct {
	Provider      Provider
	Surface       Surface
	Configuration adapters.HookConfigurationStatus
	Visibility    VisibilityStatus
	Execution     ExecutionStatus
	Expectation   CaptureExpectation
	Certainty     Certainty
	Discovered    int
	Expected      int
	Enabled       int
	Trusted       int
	Warnings      []string
	Limitations   []string
	Guidance      []string
	ProbeError    string
}

func (result SurfaceResult) NeedsAttention() bool {
	if result.ProbeError != "" {
		return true
	}
	if result.Configuration != adapters.HookConfigurationConfigured {
		return true
	}
	return result.Expectation == CaptureUnavailable
}

type Report struct {
	Surfaces []SurfaceResult
}

func (report Report) NeedsAttention() bool {
	for _, surface := range report.Surfaces {
		if surface.NeedsAttention() {
			return true
		}
	}
	return false
}
