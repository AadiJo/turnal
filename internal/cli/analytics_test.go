package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

func TestAnalyticsExplicitOptInStatusShowOffAndReset(t *testing.T) {
	configureTelemetryTestEnv(t)
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceSource)

	stdout, _, code := runTelemetryCLI(t, "analytics", "on")
	if code != 0 || !strings.Contains(stdout, "Analytics enabled globally") || !strings.Contains(stdout, "Network sending is disabled") {
		t.Fatalf("analytics on = code %d\n%s", code, stdout)
	}
	status := readAnalyticsStatusJSON(t)
	if !status.Enabled || status.Preference != telemetry.PreferenceOn || status.InstallationID == "" || status.Collector != "disabled in this build" {
		t.Fatalf("enabled status = %#v", status)
	}
	firstID := status.InstallationID

	recordTelemetryMetrics(telemetry.MetricInstallationActive, telemetry.MetricCommandStatusSuccess)
	stdout, _, code = runTelemetryCLI(t, "analytics", "show", "--json")
	if code != 0 {
		t.Fatalf("analytics show code = %d", code)
	}
	var snapshot telemetry.QueueSnapshot
	if err := json.Unmarshal([]byte(stdout), &snapshot); err != nil {
		t.Fatalf("decode show output: %v\n%s", err, stdout)
	}
	if len(snapshot.Current) != 1 || snapshot.FileCount() != 1 || len(snapshot.Current[0].Metrics) != 2 {
		t.Fatalf("queued payload = %#v", snapshot)
	}

	stdout, _, code = runTelemetryCLI(t, "analytics", "off")
	if code != 0 || !strings.Contains(stdout, "Unsent telemetry was deleted") {
		t.Fatalf("analytics off = code %d\n%s", code, stdout)
	}
	status = readAnalyticsStatusJSON(t)
	if status.Enabled || status.Preference != telemetry.PreferenceOff || status.InstallationID != firstID || status.QueueFiles != 0 {
		t.Fatalf("disabled status = %#v", status)
	}

	stdout, _, code = runTelemetryCLI(t, "analytics", "reset")
	if code != 0 || !strings.Contains(stdout, "installation ID was rotated") {
		t.Fatalf("analytics reset = code %d\n%s", code, stdout)
	}
	status = readAnalyticsStatusJSON(t)
	if status.Preference != telemetry.PreferenceOff || status.InstallationID == "" || status.InstallationID == firstID {
		t.Fatalf("reset status = %#v", status)
	}
}

func TestAnalyticsEnvironmentOverrideWins(t *testing.T) {
	configureTelemetryTestEnv(t)
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceNPM)
	if _, _, code := runTelemetryCLI(t, "analytics", "on"); code != 0 {
		t.Fatalf("analytics on code = %d", code)
	}
	t.Setenv("DO_NOT_TRACK", "1")
	status := readAnalyticsStatusJSON(t)
	if status.Enabled || status.Reason != telemetry.ReasonEnvironmentOptOut || status.Origin != "environment override" {
		t.Fatalf("overridden status = %#v", status)
	}
}

func TestTelemetryNoticeWritesOnlyNoticeState(t *testing.T) {
	configureTelemetryTestEnv(t)
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceSource)
	oldCanDisplay := telemetryNoticeCanDisplay
	oldTimeNow := timeNow
	telemetryNoticeCanDisplay = func() bool { return true }
	timeNow = func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		telemetryNoticeCanDisplay = oldCanDisplay
		timeNow = oldTimeNow
	})

	root := &cobra.Command{Use: "turnal"}
	statusCommand := &cobra.Command{Use: "status"}
	root.AddCommand(statusCommand)
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	if !maybeShowTelemetryNotice(root, statusCommand) {
		t.Fatal("first eligible command did not show disclosure")
	}
	if !strings.Contains(stderr.String(), "Nothing was\nsent during this command") && !strings.Contains(stderr.String(), "Nothing was\r\nsent during this command") {
		t.Fatalf("notice output = %q", stderr.String())
	}
	runtime, err := currentTelemetryRuntime()
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Preference != telemetry.PreferenceUnset || state.AnonymousID != nil || state.NoticeAt != "2026-07-10T12:00:00Z" {
		t.Fatalf("notice state = %#v", state)
	}
	if maybeShowTelemetryNotice(root, statusCommand) {
		t.Fatal("notice was shown twice")
	}
	current, err := filepath.Glob(filepath.Join(runtime.aggregates.CacheDir, "current", "*.json"))
	if err != nil || len(current) != 0 {
		t.Fatalf("notice recorded telemetry: %v, %v", current, err)
	}
}

func TestTelemetryNoticeExcludesJSONHiddenAndAnalyticsCommands(t *testing.T) {
	oldCanDisplay := telemetryNoticeCanDisplay
	telemetryNoticeCanDisplay = func() bool { return true }
	t.Cleanup(func() { telemetryNoticeCanDisplay = oldCanDisplay })

	root := &cobra.Command{Use: "turnal"}
	jsonCommand := &cobra.Command{Use: "status"}
	var jsonOutput bool
	jsonCommand.Flags().BoolVar(&jsonOutput, "json", false, "json")
	if err := jsonCommand.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	hidden := &cobra.Command{Use: "hook", Hidden: true}
	analytics := &cobra.Command{Use: "analytics"}
	analyticsStatus := &cobra.Command{Use: "status"}
	analytics.AddCommand(analyticsStatus)
	root.AddCommand(jsonCommand, hidden, analytics)

	for _, command := range []*cobra.Command{jsonCommand, hidden, analyticsStatus} {
		if shouldShowTelemetryNotice(command) {
			t.Fatalf("notice allowed for %s", command.CommandPath())
		}
	}
}

func TestTelemetryCommandMapperUsesOnlyCanonicalFamilies(t *testing.T) {
	root := NewRootCmd()
	for _, test := range []struct {
		args []string
		key  telemetry.MetricKey
	}{
		{args: []string{"status"}, key: telemetry.MetricCommandStatusSuccess},
		{args: []string{"log"}, key: telemetry.MetricCommandLogSuccess},
		{args: []string{"replay", "start", "target"}, key: telemetry.MetricCommandReplayCheckoutSuccess},
		{args: []string{"replay", "next"}, key: telemetry.MetricCommandReplayMoveSuccess},
		{args: []string{"replay", "remove"}, key: telemetry.MetricCommandReplayRemoveSuccess},
	} {
		root.SetArgs(test.args)
		command, _, err := root.Find(test.args)
		if err != nil {
			t.Fatalf("Find(%v): %v", test.args, err)
		}
		key, ok := telemetryCommandMetric(command, nil)
		if !ok || key != test.key {
			t.Fatalf("metric for %v = %s, %v", test.args, key, ok)
		}
	}
	unknown := &cobra.Command{Use: "custom"}
	root.AddCommand(unknown)
	if _, ok := telemetryCommandMetric(unknown, nil); ok {
		t.Fatal("unknown command produced a metric")
	}
}

func TestCommandTelemetryRecordsInstallationAndOutcomeTogether(t *testing.T) {
	configureTelemetryTestEnv(t)
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceSource)
	if _, _, code := runTelemetryCLI(t, "analytics", "on"); code != 0 {
		t.Fatalf("analytics on code = %d", code)
	}
	root := &cobra.Command{Use: "turnal"}
	statusCommand := &cobra.Command{Use: "status"}
	root.AddCommand(statusCommand)
	recordCommandTelemetry(statusCommand, nil)
	runtime, err := currentTelemetryRuntime()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.aggregates.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Current) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	metrics := snapshot.Current[0].Metrics
	if len(metrics) != 2 || metrics[0] != (telemetry.MetricCount{Key: telemetry.MetricCommandStatusSuccess, Count: 1}) || metrics[1] != (telemetry.MetricCount{Key: telemetry.MetricInstallationActive, Count: 1}) {
		t.Fatalf("command metrics = %#v", metrics)
	}
}

func readAnalyticsStatusJSON(t *testing.T) analyticsStatus {
	t.Helper()
	stdout, stderr, code := runTelemetryCLI(t, "analytics", "status", "--json")
	if code != 0 {
		t.Fatalf("analytics status code = %d, stderr=%s", code, stderr)
	}
	var status analyticsStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode status: %v\n%s", err, stdout)
	}
	return status
}

func runTelemetryCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := NewRootCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	code := executeRoot(root)
	return stdout.String(), stderr.String(), code
}

func configureTelemetryTestEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Setenv("CI", "")
	t.Setenv("CONTINUOUS_INTEGRATION", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("BUILDKITE", "")
	t.Setenv("CIRCLECI", "")
	t.Setenv("JENKINS_URL", "")
	t.Setenv("TF_BUILD", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("TURNAL_NO_ANALYTICS", "")
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
}
