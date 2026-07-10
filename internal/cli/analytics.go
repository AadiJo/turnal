package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
	"github.com/spf13/cobra"
)

const telemetryPolicyURL = "https://turnal.dev/telemetry"

const telemetryDisclosure = `Turnal can collect pseudonymous, aggregate usage analytics to improve reliability
and prioritize features. If enabled, it records command categories, success/failure,
Turnal version, install source, OS/architecture, and a random installation ID.

A Turnal collector forwards personless daily events to PostHog as its analytics
processor. Local queued data expires within 14 days and raw server events within
90 days. Reset rotates the ID; the policy explains deletion requests.

It never sends prompts, agent transcripts, tool data, repository or file names,
paths, diffs, Git data, command arguments, output, or raw errors. Nothing was
sent during this command, and analytics remain off until you enable them.

Inspect: turnal analytics show
Enable:  turnal analytics on
Disable: turnal analytics off  or  TURNAL_NO_ANALYTICS=1
Policy:  https://turnal.dev/telemetry`

type analyticsStatus struct {
	Enabled             bool                   `json:"enabled"`
	Reason              telemetry.PolicyReason `json:"reason"`
	Preference          telemetry.Preference   `json:"preference"`
	Origin              string                 `json:"origin"`
	NoticeAt            string                 `json:"notice_at,omitempty"`
	InstallationID      string                 `json:"installation_id,omitempty"`
	QueueFiles          int                    `json:"queue_files"`
	QueueBytes          int64                  `json:"queue_bytes"`
	LastFlushAttemptAt  string                 `json:"last_flush_attempt_at,omitempty"`
	LastFlushSuccessAt  string                 `json:"last_flush_success_at,omitempty"`
	NetworkBackoffUntil string                 `json:"network_backoff_until,omitempty"`
	Collector           string                 `json:"collector"`
	RolloutPercent      int                    `json:"rollout_percent"`
	RolloutEligible     bool                   `json:"rollout_eligible"`
	LocalRetentionDays  int                    `json:"local_retention_days"`
	RawRetentionDays    int                    `json:"raw_retention_days"`
	PolicyURL           string                 `json:"policy_url"`
}

func analyticsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Control and inspect pseudonymous usage analytics",
	}
	cmd.AddCommand(analyticsStatusCmd())
	cmd.AddCommand(analyticsShowCmd())
	cmd.AddCommand(analyticsOnCmd())
	cmd.AddCommand(analyticsOffCmd())
	cmd.AddCommand(analyticsResetCmd())
	cmd.AddCommand(analyticsFlushCmd())
	return cmd
}

func analyticsStatusCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show effective analytics state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := loadAnalyticsStatus()
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(status)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "enabled:            %t\n", status.Enabled)
			fmt.Fprintf(out, "reason:             %s\n", status.Reason)
			fmt.Fprintf(out, "preference:         %s (%s)\n", status.Preference, status.Origin)
			fmt.Fprintf(out, "installation_id:    %s\n", emptyAsDash(status.InstallationID))
			fmt.Fprintf(out, "notice_at:          %s\n", emptyAsDash(status.NoticeAt))
			fmt.Fprintf(out, "queue:              %d files, %d bytes\n", status.QueueFiles, status.QueueBytes)
			fmt.Fprintf(out, "last_flush_attempt: %s\n", emptyAsDash(status.LastFlushAttemptAt))
			fmt.Fprintf(out, "last_flush_success: %s\n", emptyAsDash(status.LastFlushSuccessAt))
			fmt.Fprintf(out, "network_backoff:    %s\n", emptyAsDash(status.NetworkBackoffUntil))
			fmt.Fprintf(out, "collector:          %s\n", status.Collector)
			fmt.Fprintf(out, "rollout:            %d%% (eligible=%t)\n", status.RolloutPercent, status.RolloutEligible)
			fmt.Fprintf(out, "retention:          %d days local, %d days raw server maximum\n", status.LocalRetentionDays, status.RawRetentionDays)
			fmt.Fprintf(out, "policy:             %s\n", status.PolicyURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func analyticsShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "show",
		Short:        "Show the exact locally queued analytics payload",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return err
			}
			snapshot, err := runtime.aggregates.Inspect()
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			if !jsonOutput {
				fmt.Fprintln(cmd.OutOrStdout(), "Exact queued telemetry payload (nothing is sent by this command):")
			}
			encoder.SetIndent("", "  ")
			return encoder.Encode(snapshot)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON only")
	return cmd
}

func analyticsOnCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "on",
		Short:        "Explicitly enable analytics globally",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), telemetryDisclosure)
			state, err := runtime.state.Enable(time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Analytics enabled globally for later eligible commands.")
			if state.AnonymousID != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Installation ID: %s\n", state.AnonymousID)
			}
			if !runtime.sender.Enabled() {
				fmt.Fprintln(cmd.OutOrStdout(), "Network sending is disabled in this Turnal build; aggregates remain local and inspectable.")
			}
			return nil
		},
	}
}

func analyticsOffCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "off",
		Short:        "Disable analytics and delete unsent data",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return err
			}
			if _, err := runtime.state.SetPreference(telemetry.PreferenceOff); err != nil {
				return err
			}
			if err := runtime.aggregates.DeleteAll(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Analytics disabled. Unsent telemetry was deleted.")
			return nil
		},
	}
}

func analyticsResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "reset",
		Short:        "Disable analytics, delete the queue, and rotate the installation ID",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return err
			}
			state, err := runtime.state.RotateAndDisable()
			if err != nil {
				return err
			}
			if err := runtime.aggregates.DeleteAll(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Analytics disabled. Unsent telemetry was deleted and the installation ID was rotated.")
			if state.AnonymousID != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "New inactive installation ID: %s\n", state.AnonymousID)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Previously accepted server data, if any, follows the published deletion and retention policy.")
			return nil
		},
	}
}

func analyticsFlushCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "flush",
		Short:        "Attempt a foreground analytics flush for diagnostics",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return err
			}
			result, err := (telemetry.Flusher{
				Aggregates: runtime.aggregates,
				State:      runtime.state,
				Sender:     runtime.sender,
				Build:      runtime.build,
			}).Flush(cmd.Context(), true)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Analytics flush: %s", result.Status)
			if result.BatchCount > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " (%d batches)", result.BatchCount)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
}

func internalTelemetryFlushCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "__telemetry-flush",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := currentTelemetryRuntime()
			if err != nil {
				return nil
			}
			_, _ = (telemetry.Flusher{
				Aggregates: runtime.aggregates,
				State:      runtime.state,
				Sender:     runtime.sender,
				Build:      runtime.build,
			}).Flush(cmd.Context(), false)
			return nil
		},
	}
}

func loadAnalyticsStatus() (analyticsStatus, error) {
	runtime, err := currentTelemetryRuntime()
	if err != nil {
		return analyticsStatus{}, err
	}
	state, err := runtime.state.Load()
	if err != nil {
		return analyticsStatus{}, err
	}
	snapshot, err := runtime.aggregates.Inspect()
	if err != nil {
		return analyticsStatus{}, err
	}
	policy := telemetry.EvaluatePolicy(telemetry.PolicyOptions{Preference: state.Preference, Build: runtime.build})
	collector := "disabled in this build"
	if runtime.sender.Enabled() {
		collector = telemetry.CollectorURL
	}
	origin := "global"
	if policy.Reason == telemetry.ReasonCI || policy.Reason == telemetry.ReasonEnvironmentOptOut {
		origin = "environment override"
	} else if policy.Reason == telemetry.ReasonDevelopmentBuild || policy.Reason == telemetry.ReasonUnsupportedBuild {
		origin = "build override"
	}
	status := analyticsStatus{
		Enabled:             policy.Enabled,
		Reason:              policy.Reason,
		Preference:          state.Preference,
		Origin:              origin,
		NoticeAt:            state.NoticeAt,
		QueueFiles:          snapshot.FileCount(),
		QueueBytes:          snapshot.Bytes,
		LastFlushAttemptAt:  state.LastFlushAttemptAt,
		LastFlushSuccessAt:  state.LastFlushSuccessAt,
		NetworkBackoffUntil: state.NetworkBackoffUntil,
		Collector:           collector,
		RolloutPercent:      telemetry.RolloutPercent(),
		LocalRetentionDays:  int(telemetry.DefaultMaxAge / (24 * time.Hour)),
		RawRetentionDays:    90,
		PolicyURL:           telemetryPolicyURL,
	}
	if state.AnonymousID != nil {
		status.InstallationID = state.AnonymousID.String()
		status.RolloutEligible = runtime.sender.EnabledFor(*state.AnonymousID)
	}
	return status, nil
}

func emptyAsDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
