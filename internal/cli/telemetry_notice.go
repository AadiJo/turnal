package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
	"github.com/spf13/cobra"
)

var telemetryNoticeCanDisplay = func() bool {
	return fileIsTerminal(os.Stderr)
}

func maybeShowTelemetryNotice(root, executed *cobra.Command) bool {
	if !shouldShowTelemetryNotice(executed) {
		return false
	}
	runtime, err := currentTelemetryRuntime()
	if err != nil {
		return false
	}
	preflight := telemetry.EvaluatePolicy(telemetry.PolicyOptions{Preference: telemetry.PreferenceUnset, Build: runtime.build})
	if preflight.Reason == telemetry.ReasonCI || preflight.Reason == telemetry.ReasonEnvironmentOptOut || preflight.Reason == telemetry.ReasonDevelopmentBuild || preflight.Reason == telemetry.ReasonUnsupportedBuild {
		return false
	}
	state, err := runtime.state.Load()
	if err != nil || state.Preference != telemetry.PreferenceUnset || state.NoticeAt != "" {
		return false
	}
	fmt.Fprintln(root.ErrOrStderr())
	fmt.Fprintln(root.ErrOrStderr(), telemetryDisclosure)
	if _, err := runtime.state.MarkNotice(timeNow()); err != nil {
		return false
	}
	return true
}

func shouldShowTelemetryNotice(command *cobra.Command) bool {
	if command == nil || !telemetryNoticeCanDisplay() || commandOrAncestorHidden(command) || commandUsesJSONOutput(command) {
		return false
	}
	switch topLevelCommandName(command) {
	case "", "analytics", "completion", "help", "upgrade", "version":
		return false
	default:
		return true
	}
}

var timeNow = time.Now
