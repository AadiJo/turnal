package cli

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
	"github.com/spf13/cobra"
)

const telemetryBestEffortStateLockTimeout = 5 * time.Millisecond

type telemetryRuntime struct {
	state      telemetry.StateStore
	aggregates telemetry.AggregateStore
	build      telemetry.Build
	sender     telemetry.Sender
}

func currentTelemetryRuntime() (telemetryRuntime, error) {
	state, err := telemetry.DefaultStateStore()
	if err != nil {
		return telemetryRuntime{}, err
	}
	aggregates, err := telemetry.DefaultAggregateStore()
	if err != nil {
		return telemetryRuntime{}, err
	}
	build := currentTelemetryBuild()
	return telemetryRuntime{
		state:      state,
		aggregates: aggregates,
		build:      build,
		sender:     telemetry.NewSender(build.Version),
	}, nil
}

func currentTelemetryBuild() telemetry.Build {
	metadata := currentBuildMetadata()
	return telemetry.RuntimeBuild(
		metadata.Version,
		telemetry.ReleaseChannel(metadata.Channel),
		telemetry.InstallSource(metadata.InstallSource),
	)
}

func recordTelemetryMetrics(keys ...telemetry.MetricKey) {
	build := currentTelemetryBuild()
	preflight := telemetry.EvaluatePolicy(telemetry.PolicyOptions{
		Preference: telemetry.PreferenceUnset,
		Build:      build,
	})
	if preflight.Reason == telemetry.ReasonDevelopmentBuild || preflight.Reason == telemetry.ReasonUnsupportedBuild || preflight.Reason == telemetry.ReasonCI || preflight.Reason == telemetry.ReasonEnvironmentOptOut {
		return
	}
	runtime, err := currentTelemetryRuntime()
	if err != nil {
		return
	}
	if _, err := os.Lstat(runtime.state.Path); err != nil {
		return
	}
	runtime.state.LockTimeout = telemetryBestEffortStateLockTimeout
	runtime.state.QuietLock = true
	state, err := runtime.state.Load()
	if err != nil {
		return
	}
	_, _ = runtime.aggregates.RecordMany(telemetry.RecordOptions{State: state, Build: runtime.build}, keys...)
}

func recordCommandTelemetry(executed *cobra.Command, commandErr error) {
	if !telemetryCommandEligible(executed) {
		return
	}
	keys := []telemetry.MetricKey{telemetry.MetricInstallationActive}
	if key, ok := telemetryCommandMetric(executed, commandErr); ok {
		keys = append(keys, key)
	}
	if commandErr != nil {
		if key, ok := telemetryFailureMetric(commandErr); ok {
			keys = append(keys, key)
		}
	}
	recordTelemetryMetrics(keys...)
}

func telemetryCommandEligible(command *cobra.Command) bool {
	if command == nil || commandOrAncestorHidden(command) {
		return false
	}
	switch topLevelCommandName(command) {
	case "", "analytics", "help", "upgrade", "version", "completion":
		return false
	default:
		return true
	}
}

func telemetryCommandMetric(command *cobra.Command, commandErr error) (telemetry.MetricKey, bool) {
	success := commandErr == nil
	switch topLevelCommandName(command) {
	case "status":
		return successMetric(success, telemetry.MetricCommandStatusSuccess, telemetry.MetricCommandStatusFailure)
	case "log":
		return successMetric(success, telemetry.MetricCommandLogSuccess, telemetry.MetricCommandLogFailure)
	case "sessions":
		return successMetric(success, telemetry.MetricCommandSessionsSuccess, telemetry.MetricCommandSessionsFailure)
	case "show":
		return successMetric(success, telemetry.MetricCommandShowSuccess, telemetry.MetricCommandShowFailure)
	case "search":
		return successMetric(success, telemetry.MetricCommandSearchSuccess, telemetry.MetricCommandSearchFailure)
	case "diff":
		return successMetric(success, telemetry.MetricCommandDiffSuccess, telemetry.MetricCommandDiffFailure)
	case "blame":
		return successMetric(success, telemetry.MetricCommandBlameSuccess, telemetry.MetricCommandBlameFailure)
	case "rollback":
		return successMetric(success, telemetry.MetricCommandRollbackSuccess, telemetry.MetricCommandRollbackFailure)
	case "run":
		if success {
			return telemetry.MetricCommandRunSuccess, true
		}
		var childErr childExitError
		if errors.As(commandErr, &childErr) {
			return telemetry.MetricCommandRunChildFailure, true
		}
		return 0, false
	case "replay":
		switch command.Name() {
		case "replay", "checkout":
			return successMetric(success, telemetry.MetricCommandReplayCheckoutSuccess, telemetry.MetricCommandReplayCheckoutFailure)
		case "next", "prev", "goto":
			return successMetric(success, telemetry.MetricCommandReplayMoveSuccess, telemetry.MetricCommandReplayMoveFailure)
		case "remove":
			return successMetric(success, telemetry.MetricCommandReplayRemoveSuccess, telemetry.MetricCommandReplayRemoveFailure)
		}
	}
	return 0, false
}

func successMetric(success bool, successKey, failureKey telemetry.MetricKey) (telemetry.MetricKey, bool) {
	if success {
		return successKey, true
	}
	return failureKey, true
}

func telemetryFailureMetric(err error) (telemetry.MetricKey, bool) {
	if err == nil {
		return 0, false
	}
	var pathErr *os.PathError
	var executableErr *exec.Error
	if errors.As(err, &pathErr) || errors.As(err, &executableErr) || os.IsNotExist(err) || os.IsPermission(err) {
		return telemetry.MetricFailureIO, true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"corrupt", "hash mismatch", "integrity", "journal mismatch", "checkpoint mismatch"} {
		if strings.Contains(message, marker) {
			return telemetry.MetricFailureIntegrity, true
		}
	}
	for _, marker := range []string{"invalid", "required", "unknown command", "accepts ", "cannot be combined", "must be"} {
		if strings.Contains(message, marker) {
			return telemetry.MetricFailureValidation, true
		}
	}
	return 0, false
}

func maybeScheduleTelemetryFlush(executed *cobra.Command) {
	if !shouldScheduleTelemetryFlush(executed) {
		return
	}
	runtime, err := currentTelemetryRuntime()
	if err != nil || !runtime.sender.Enabled() {
		return
	}
	preflight := telemetry.EvaluatePolicy(telemetry.PolicyOptions{Preference: telemetry.PreferenceUnset, Build: runtime.build})
	if preflight.Reason == telemetry.ReasonDevelopmentBuild || preflight.Reason == telemetry.ReasonUnsupportedBuild || preflight.Reason == telemetry.ReasonCI || preflight.Reason == telemetry.ReasonEnvironmentOptOut {
		return
	}
	state, err := runtime.state.Load()
	if err != nil || state.Preference != telemetry.PreferenceOn || state.AnonymousID == nil || state.AutomaticFlushWait(timeNow()) > 0 {
		return
	}
	_, _ = (telemetry.DetachedScheduler{Sender: runtime.sender, InstallationID: state.AnonymousID}).Schedule()
}

func shouldScheduleTelemetryFlush(command *cobra.Command) bool {
	return telemetryCommandEligible(command)
}
