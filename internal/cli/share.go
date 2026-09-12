package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sharedhistory"
	"github.com/spf13/cobra"
)

func shareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Publish privacy-controlled agent context",
	}
	cmd.AddCommand(shareEnableCmd())
	cmd.AddCommand(shareDisableCmd())
	cmd.AddCommand(shareForgetDeviceCmd())
	cmd.AddCommand(sharePreviewCmd())
	cmd.AddCommand(shareStatusCmd())
	cmd.AddCommand(shareListCmd())
	cmd.AddCommand(shareShowCmd())
	cmd.AddCommand(shareRedactionCmd())
	return cmd
}

func shareRedactionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "redaction",
		Short: "Diagnose and review the shared-history redaction policy",
	}
	cmd.AddCommand(shareRedactionDiagnoseCmd())
	cmd.AddCommand(shareRedactionReviewCmd())
	return cmd
}

func shareRedactionDiagnoseCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "diagnose",
		Short:        "Inspect detector coverage and run the golden corpus",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			diagnostics, err := sharedhistory.DiagnoseRedaction(repo)
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := writeJSON(cmd, diagnostics); err != nil {
					return err
				}
			} else {
				writeRedactionDiagnostics(cmd, diagnostics)
			}
			if !diagnostics.GoldenCorpus.Passed() {
				return fmt.Errorf("redaction golden corpus failed with %d false positive(s) and %d false negative(s)", diagnostics.GoldenCorpus.FalsePositives, diagnostics.GoldenCorpus.FalseNegatives)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func shareRedactionReviewCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "review <corpus.jsonl>...",
		Short:        "Classify corpus cases as false positives or false negatives",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := reviewRedactionFiles(args)
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := writeJSON(cmd, report); err != nil {
					return err
				}
			} else {
				writeRedactionReview(cmd, report)
			}
			if !report.Passed() {
				return fmt.Errorf("redaction review failed with %d false positive(s) and %d false negative(s)", report.FalsePositives, report.FalseNegatives)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON without source text")
	return cmd
}

func reviewRedactionFiles(paths []string) (sharedhistory.RedactionReviewReport, error) {
	merged := sharedhistory.RedactionReviewReport{ScannerVersion: sharedhistory.ScannerVersion}
	seen := make(map[string]struct{})
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return merged, fmt.Errorf("open redaction review corpus %q: %w", path, err)
		}
		report, reviewErr := sharedhistory.ReviewRedactionCorpus(file)
		closeErr := file.Close()
		if reviewErr != nil {
			return merged, fmt.Errorf("review redaction corpus %q: %w", path, reviewErr)
		}
		if closeErr != nil {
			return merged, fmt.Errorf("close redaction review corpus %q: %w", path, closeErr)
		}
		for _, result := range report.Cases {
			if _, duplicate := seen[result.ID]; duplicate {
				return merged, fmt.Errorf("redaction review case id %q is duplicated across corpora", result.ID)
			}
			seen[result.ID] = struct{}{}
		}
		merged.Total += report.Total
		merged.TruePositives += report.TruePositives
		merged.TrueNegatives += report.TrueNegatives
		merged.FalsePositives += report.FalsePositives
		merged.FalseNegatives += report.FalseNegatives
		merged.Cases = append(merged.Cases, report.Cases...)
	}
	return merged, nil
}

func writeRedactionDiagnostics(cmd *cobra.Command, diagnostics sharedhistory.RedactionDiagnostics) {
	fmt.Fprintf(cmd.OutOrStdout(), "scanner: %s\n", diagnostics.ScannerVersion)
	if diagnostics.Configured {
		fmt.Fprintf(cmd.OutOrStdout(), "configured scanner: %s\n", diagnostics.ConfiguredScanner)
		fmt.Fprintf(cmd.OutOrStdout(), "policy: %s\n", diagnostics.PolicyHash)
		fmt.Fprintf(cmd.OutOrStdout(), "approved: %t\n", diagnostics.Approved)
		fmt.Fprintf(cmd.OutOrStdout(), "migration required: %t\n", diagnostics.MigrationRequired)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "configured scanner: none")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "detectors:")
	for _, detector := range diagnostics.Detectors {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s\n", detector.ID, detector.Description)
	}
	writeRedactionReviewSummary(cmd, "golden corpus", diagnostics.GoldenCorpus)
}

func writeRedactionReview(cmd *cobra.Command, report sharedhistory.RedactionReviewReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "scanner: %s\n", report.ScannerVersion)
	writeRedactionReviewSummary(cmd, "review", report)
	for _, result := range report.Cases {
		if result.Outcome != "false_positive" && result.Outcome != "false_negative" {
			continue
		}
		detectors := "none"
		if len(result.Detectors) > 0 {
			detectors = strings.Join(result.Detectors, ",")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s (detectors: %s)\n", result.Outcome, indentSharedText(result.ID), detectors)
	}
}

func writeRedactionReviewSummary(cmd *cobra.Command, label string, report sharedhistory.RedactionReviewReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %d cases\n", label, report.Total)
	fmt.Fprintf(cmd.OutOrStdout(), "true positives: %d\n", report.TruePositives)
	fmt.Fprintf(cmd.OutOrStdout(), "true negatives: %d\n", report.TrueNegatives)
	fmt.Fprintf(cmd.OutOrStdout(), "false positives: %d\n", report.FalsePositives)
	fmt.Fprintf(cmd.OutOrStdout(), "false negatives: %d\n", report.FalseNegatives)
}

func shareForgetDeviceCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:          "forget-device <device-id>",
		Short:        "Acknowledge an intentionally removed teammate device ref",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("rerun with --yes to acknowledge the removed device ref; its last verified head will remain pinned")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if _, err := sharedhistory.ForgetDevice(repo, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "acknowledged removed device %s; its last verified head remains pinned\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the device ref was intentionally removed")
	return cmd
}

func shareDisableCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:          "disable",
		Short:        "Stop shared history synchronization without deleting history",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("rerun with --yes to disable future shared history synchronization; published copies cannot be recalled")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if _, err := sharedhistory.Disable(repo); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "shared history disabled; local and published history was preserved")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm synchronization should be disabled")
	return cmd
}

func shareEnableCmd() *cobra.Command {
	var remote string
	var promptMode string
	var repoID string
	var includeExistingHistory bool
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "enable",
		Short:        "Configure a shared history remote and prompt policy",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			var sharedRepoID primitives.RepoID
			if repoID != "" {
				sharedRepoID, err = primitives.ParseRepoID(repoID)
				if err != nil {
					return err
				}
			}
			status, err := sharedhistory.Configure(repo, sharedhistory.ConfigureOptions{
				Remote:                 remote,
				PromptMode:             sharedhistory.PromptMode(promptMode),
				RepoID:                 sharedRepoID,
				IncludeExistingHistory: includeExistingHistory,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "shared history configured\n")
			fmt.Fprintf(cmd.OutOrStdout(), "enabled:     %t\n", status.Enabled)
			fmt.Fprintf(cmd.OutOrStdout(), "remote:      %s\n", status.Remote)
			fmt.Fprintf(cmd.OutOrStdout(), "repo:        %s\n", status.RepoID)
			fmt.Fprintf(cmd.OutOrStdout(), "device:      %s\n", status.DeviceID)
			fmt.Fprintf(cmd.OutOrStdout(), "prompt mode: %s\n", status.PromptMode)
			fmt.Fprintf(cmd.OutOrStdout(), "policy:      %s\n", status.PolicyHash)
			if status.Approved {
				fmt.Fprintln(cmd.OutOrStdout(), "approval:    current")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "approval:    required (preview a completed turn with --approve)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "Git URL or path that will receive shared history refs")
	cmd.Flags().StringVar(&promptMode, "prompt-mode", "", "Text publication policy: redacted_text, omit, or metadata_only")
	cmd.Flags().StringVar(&repoID, "repo-id", "", "Shared repository ID supplied by a publisher when joining existing history")
	cmd.Flags().BoolVar(&includeExistingHistory, "include-existing-history", false, "Copy this device's previously approved history when changing remotes")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func sharePreviewCmd() *cobra.Command {
	var approve bool
	var jsonOutput bool
	var stream string
	cmd := &cobra.Command{
		Use:          "preview <session>:<turn>",
		Short:        "Show the exact context projection before publication",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, turnID, err := parseTurnTarget(args[0])
			if err != nil {
				return err
			}
			var streamID primitives.EventStreamID
			if stream != "" {
				streamID, err = primitives.ParseEventStreamID(stream)
				if err != nil {
					return err
				}
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			plan, err := sharedhistory.New(repo).Preview(cmd.Context(), sharedhistory.PreviewOptions{SessionID: sessionID, TurnID: turnID, StreamID: streamID, Approve: approve})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, plan)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "locator:       %s\n", plan.Locator)
			fmt.Fprintf(cmd.OutOrStdout(), "evidence:      %s\n", plan.Manifest.EvidenceClass)
			fmt.Fprintf(cmd.OutOrStdout(), "policy:        %s\n", plan.PolicyHash)
			fmt.Fprintf(cmd.OutOrStdout(), "bytes:         %d\n", plan.Bytes)
			fmt.Fprintf(cmd.OutOrStdout(), "events:        %d\n", len(plan.Events))
			fmt.Fprintf(cmd.OutOrStdout(), "prompt mode:   %s\n", plan.Manifest.PromptMode)
			fmt.Fprintf(cmd.OutOrStdout(), "source commit: %s\n", firstSourceCommit(plan.Manifest.SourceLinks))
			if branch := firstSourceBranch(plan.Manifest.SourceLinks); branch != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "source branch: %s\n", indentSharedText(branch))
			}
			writeOmissions(cmd, plan.Manifest.Omissions)
			writeCounts(cmd, "redactions", plan.Manifest.Redactions)
			if approve {
				fmt.Fprintln(cmd.OutOrStdout(), "approval:      recorded for this policy hash")
			} else if plan.ApprovalRequired {
				fmt.Fprintln(cmd.OutOrStdout(), "approval:      required; rerun with --approve after reviewing --json output")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "approval:      current")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&approve, "approve", false, "Approve this schema and policy hash for future publications")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the complete publication projection as JSON")
	cmd.Flags().StringVar(&stream, "stream", "", "Select an event stream when session and turn are ambiguous")
	return cmd
}

func shareStatusCmd() *cobra.Command {
	var jsonOutput bool
	var checkRemote bool
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Inspect shared history consent and synchronization state",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			ctx, stop := sharedHistoryCommandContext(cmd.Context(), sharedHistoryStatusTimeout)
			defer stop()
			manager := sharedhistory.New(repo)
			var status sharedhistory.Status
			if checkRemote {
				status, err = manager.StatusWithRemote(ctx)
			} else {
				status, err = manager.Status(ctx)
			}
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, status)
			}
			writeShareStatus(cmd, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&checkRemote, "check-remote", false, "Contact the configured remote with a bounded timeout")
	return cmd
}

func shareListCmd() *cobra.Command {
	var jsonOutput bool
	var session string
	var device string
	var commit string
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List local and pulled shared-history bundles",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionID primitives.SessionID
			var err error
			if session != "" {
				sessionID, err = primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			bundles, err := sharedhistory.New(repo).List(cmd.Context(), sharedhistory.ListOptions{SessionID: sessionID, DeviceID: device, CommitSHA: commit})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, bundles)
			}
			if len(bundles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no shared history bundles")
				return nil
			}
			for _, bundle := range bundles {
				if bundle.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  pulled  unreadable: %s\n", bundle.Locator, indentSharedText(bundle.Error))
					continue
				}
				location := "pulled"
				if bundle.Local {
					location = "local"
				}
				source := ""
				if bundle.SourceCommit != "" {
					source = "  commit=" + shortCommit(bundle.SourceCommit)
				}
				if bundle.Branch != "" {
					source += "  branch=" + indentSharedText(bundle.Branch)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s:%s  %s  %s  events=%d%s\n", bundle.Locator, bundle.SessionID, bundle.TurnID, location, bundle.CreatedAt.Format(time.RFC3339), bundle.EventCount, source)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().StringVar(&session, "session", "", "Filter by session ID")
	cmd.Flags().StringVar(&device, "device", "", "Filter by publishing device ID")
	cmd.Flags().StringVar(&commit, "commit", "", "Filter by source commit SHA or prefix")
	return cmd
}

func shareShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "show <locator>",
		Short:        "Read a local or pulled shared-history bundle",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			bundle, err := sharedhistory.New(repo).Read(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, bundle)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "bundle:      %s\n", bundle.Manifest.BundleID)
			fmt.Fprintf(cmd.OutOrStdout(), "session:     %s\n", bundle.Manifest.SessionID)
			fmt.Fprintf(cmd.OutOrStdout(), "turn:        %s\n", bundle.Manifest.TurnID)
			fmt.Fprintf(cmd.OutOrStdout(), "publisher:   %s\n", bundle.Manifest.DeviceID)
			fmt.Fprintf(cmd.OutOrStdout(), "evidence:    %s\n", bundle.Manifest.EvidenceClass)
			fmt.Fprintf(cmd.OutOrStdout(), "prompt mode: %s\n", bundle.Manifest.PromptMode)
			if branch := firstSourceBranch(bundle.Manifest.SourceLinks); branch != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "branch:      %s\n", indentSharedText(branch))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "projection:  %s\n", sharedProjectionLabel(bundle.Manifest))
			fmt.Fprintf(cmd.OutOrStdout(), "events:      %d\n", len(bundle.Events))
			writeOmissions(cmd, bundle.Manifest.Omissions)
			writeCounts(cmd, "redactions", bundle.Manifest.Redactions)
			fmt.Fprintln(cmd.OutOrStdout(), "context:")
			writeBundleContext(cmd, bundle.Events)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the complete bundle as JSON")
	return cmd
}

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "Synchronize shared history through Git"}
	cmd.AddCommand(syncDirectionCmd(sharedhistory.DirectionPush))
	cmd.AddCommand(syncDirectionCmd(sharedhistory.DirectionPull))
	cmd.AddCommand(shareStatusCmd())
	return cmd
}

func syncDirectionCmd(direction sharedhistory.Direction) *cobra.Command {
	var jsonOutput bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:          string(direction),
		Short:        "Synchronize shared history " + string(direction),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			manager := sharedhistory.New(repo)
			if dryRun {
				if direction != sharedhistory.DirectionPush {
					return fmt.Errorf("--dry-run is only valid for sync push")
				}
				plan, err := manager.PlanPush(cmd.Context())
				if err != nil {
					return err
				}
				if jsonOutput {
					return writeJSON(cmd, plan)
				}
				writePushPlan(cmd, plan)
				return nil
			}
			ctx, stop := sharedHistoryCommandContext(cmd.Context(), sharedHistorySyncTimeout)
			defer stop()
			result, err := manager.Sync(ctx, direction)
			if jsonOutput {
				if result.Direction != "" {
					if writeErr := writeJSON(cmd, result); writeErr != nil {
						return writeErr
					}
				}
				return err
			}
			if result.Direction != "" {
				writeSyncResult(cmd, direction, result, err == nil)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	if direction == sharedhistory.DirectionPush {
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show pending bundles without contacting the remote")
	}
	return cmd
}

func writeSyncResult(cmd *cobra.Command, direction sharedhistory.Direction, result sharedhistory.Result, completed bool) {
	state := "complete"
	if !completed {
		state = "incomplete"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", direction, state)
	if direction == sharedhistory.DirectionPush {
		fmt.Fprintf(cmd.OutOrStdout(), "published: %d\n", result.Published)
		fmt.Fprintf(cmd.OutOrStdout(), "blocked:   %d\n", result.Blocked)
		fmt.Fprintf(cmd.OutOrStdout(), "remaining: %d\n", result.Remaining)
		if result.Head != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "head:      %s\n", result.Head)
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "pulled: %d\n", result.Pulled)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", indentSharedText(warning))
	}
	writeQuarantined(cmd, result.Quarantined)
}

func writeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeShareStatus(cmd *cobra.Command, status sharedhistory.Status) {
	if !status.Configured {
		fmt.Fprintln(cmd.OutOrStdout(), "shared history: not configured")
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "shared history: configured")
	fmt.Fprintf(cmd.OutOrStdout(), "enabled:     %t\n", status.Enabled)
	fmt.Fprintf(cmd.OutOrStdout(), "remote:      %s\n", status.Remote)
	fmt.Fprintf(cmd.OutOrStdout(), "repo:        %s\n", status.RepoID)
	fmt.Fprintf(cmd.OutOrStdout(), "device:      %s\n", status.DeviceID)
	fmt.Fprintf(cmd.OutOrStdout(), "prompt mode: %s\n", status.PromptMode)
	fmt.Fprintf(cmd.OutOrStdout(), "approved:    %t\n", status.Approved)
	fmt.Fprintf(cmd.OutOrStdout(), "pending:     %d\n", status.Pending)
	fmt.Fprintf(cmd.OutOrStdout(), "published:   %d\n", status.Published)
	fmt.Fprintf(cmd.OutOrStdout(), "pulled:      %d\n", status.Pulled)
	fmt.Fprintf(cmd.OutOrStdout(), "local ahead: %t\n", status.UnpushedLocalTip)
	fmt.Fprintf(cmd.OutOrStdout(), "remote checked: %t\n", status.RemoteChecked)
	if status.RemoteError != "" {
		// Remote-supplied Git stderr reaches here, so it must be escaped like
		// every other untrusted string before it touches a terminal.
		fmt.Fprintf(cmd.OutOrStdout(), "remote error: %s\n", indentSharedText(status.RemoteError))
	}
	if len(status.Blocked) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "blocked:")
		keys := make([]string, 0, len(status.Blocked))
		for key := range status.Blocked {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s\n", indentSharedText(key), indentSharedText(status.Blocked[key]))
		}
	}
	writeQuarantined(cmd, status.Quarantined)
	if len(status.Retired) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "acknowledged removed devices:")
		keys := make([]string, 0, len(status.Retired))
		for key := range status.Retired {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s (last verified %s)\n", key, status.Retired[key])
		}
	}
}

func writeOmissions(cmd *cobra.Command, omissions map[string]int) {
	writeCounts(cmd, "omissions", omissions)
}

func writeCounts(cmd *cobra.Command, label string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", label)
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %d\n", indentSharedText(key), counts[key])
	}
}

const (
	sharedHistoryStatusTimeout = 15 * time.Second
	sharedHistorySyncTimeout   = 2 * time.Minute
)

func sharedHistoryCommandContext(parent context.Context, timeout time.Duration) (context.Context, func()) {
	signaled, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(signaled, timeout)
	return ctx, func() {
		cancel()
		stopSignals()
	}
}

func writePushPlan(cmd *cobra.Command, plan sharedhistory.PushPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "policy:      %s\n", plan.PolicyHash)
	fmt.Fprintf(cmd.OutOrStdout(), "approval required: %t\n", plan.ApprovalRequired)
	fmt.Fprintf(cmd.OutOrStdout(), "migration required: %t\n", plan.MigrationRequired)
	fmt.Fprintf(cmd.OutOrStdout(), "pending:     %d\n", len(plan.Pending))
	fmt.Fprintf(cmd.OutOrStdout(), "publishable: %d\n", plan.Publishable)
	fmt.Fprintf(cmd.OutOrStdout(), "queued outbox: %d\n", plan.Queued)
	fmt.Fprintf(cmd.OutOrStdout(), "next new batch: %d\n", plan.BatchSize)
	fmt.Fprintf(cmd.OutOrStdout(), "blocked:     %d\n", plan.Blocked)
	fmt.Fprintf(cmd.OutOrStdout(), "remaining:   %d\n", plan.Remaining)
	for _, pending := range plan.Pending {
		status := fmt.Sprintf("%d bytes", pending.Bytes)
		if pending.Queued {
			status = "queued in outbox"
		} else if pending.Blocked != "" {
			status = "blocked: " + pending.Blocked
		}
		fmt.Fprintf(cmd.OutOrStdout(), "- %s:%s stream=%s locator=%s %s\n", pending.SessionID, pending.TurnID, pending.StreamID, pending.Locator, status)
	}
}

func writeQuarantined(cmd *cobra.Command, quarantined map[string]string) {
	if len(quarantined) == 0 {
		return
	}
	keys := make([]string, 0, len(quarantined))
	for key := range quarantined {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintln(cmd.OutOrStdout(), "quarantined publishers:")
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s\n", key, indentSharedText(quarantined[key]))
	}
}

func writeBundleContext(cmd *cobra.Command, events []sharedhistory.ContextEvent) {
	for _, event := range events {
		switch {
		case event.Prompt != nil:
			if event.Prompt.Omitted {
				fmt.Fprintln(cmd.OutOrStdout(), "  prompt: [omitted by policy]")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "  prompt: %s\n", indentSharedText(event.Prompt.Text))
			}
		case event.Intent != nil:
			fmt.Fprintf(cmd.OutOrStdout(), "  intent: %s\n", indentSharedText(event.Intent.Problem.Text))
		case event.Assistant != nil:
			fmt.Fprintf(cmd.OutOrStdout(), "  assistant: %s\n", indentSharedText(event.Assistant.Text))
		case event.Tool != nil:
			fmt.Fprintf(cmd.OutOrStdout(), "  tool: %s (%s, %s)\n", indentSharedText(event.Tool.Name), event.Tool.Category, event.Tool.Status)
		case event.Checkpoint != nil:
			fmt.Fprintf(cmd.OutOrStdout(), "  checkpoint: %s %s\n", event.Checkpoint.Phase, event.Checkpoint.SourceCommit)
		case event.CaptureError != nil:
			fmt.Fprintf(cmd.OutOrStdout(), "  capture error: %s\n", event.CaptureError.Kind)
		}
	}
}

func indentSharedText(value string) string {
	var safe strings.Builder
	for _, character := range value {
		switch {
		case character == '\n' || character == '\t':
			safe.WriteRune(character)
		case unicode.IsControl(character) || unicode.Is(unicode.Cf, character):
			fmt.Fprintf(&safe, "\\u%04x", character)
		default:
			safe.WriteRune(character)
		}
	}
	return strings.ReplaceAll(safe.String(), "\n", "\n    ")
}

func firstSourceCommit(links []sharedhistory.SourceLink) string {
	for _, link := range links {
		if link.CommitSHA != "" {
			return link.CommitSHA
		}
	}
	return "none"
}

// sharedProjectionLabel names the allowlist, scanner, and Turnal build that
// produced a bundle. Bundles published before these fields existed report
// "unknown" rather than silently looking like the current projection.
func sharedProjectionLabel(manifest sharedhistory.Manifest) string {
	allowlist := manifest.AllowlistVersion
	scanner := manifest.ScannerVersion
	if allowlist == "" || scanner == "" {
		return "unknown (published before projection versions were recorded)"
	}
	label := fmt.Sprintf("%s, %s", allowlist, scanner)
	if manifest.ProducerVersion != "" {
		label += ", turnal " + manifest.ProducerVersion
	}
	return indentSharedText(label)
}

// shortCommit abbreviates to Git's conventional display length. The full SHA
// stays available in --json output.
func shortCommit(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// firstSourceBranch returns "" when no link names a branch, which happens under
// metadata_only, on a detached HEAD, and outside a Git workspace.
func firstSourceBranch(links []sharedhistory.SourceLink) string {
	for _, link := range links {
		if link.Branch != "" {
			return link.Branch
		}
	}
	return ""
}
