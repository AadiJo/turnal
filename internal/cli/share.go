package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/AadiJo/turnal/internal/sharedhistory"
	"github.com/spf13/cobra"
)

func shareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Publish privacy-controlled agent context",
	}
	cmd.AddCommand(shareEnableCmd())
	cmd.AddCommand(sharePreviewCmd())
	cmd.AddCommand(shareStatusCmd())
	cmd.AddCommand(shareShowCmd())
	return cmd
}

func shareEnableCmd() *cobra.Command {
	var remote string
	var promptMode string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "enable",
		Short:        "Configure a shared history remote and prompt policy",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote == "" {
				return fmt.Errorf("--remote is required")
			}
			if promptMode == "" {
				return fmt.Errorf("--prompt-mode is required; expected redacted_text or omit")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			status, err := sharedhistory.Configure(repo, remote, sharedhistory.PromptMode(promptMode))
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "shared history configured\n")
			fmt.Fprintf(cmd.OutOrStdout(), "remote:      %s\n", status.Remote)
			fmt.Fprintf(cmd.OutOrStdout(), "device:      %s\n", status.DeviceID)
			fmt.Fprintf(cmd.OutOrStdout(), "prompt mode: %s\n", status.PromptMode)
			fmt.Fprintf(cmd.OutOrStdout(), "policy:      %s\n", status.PolicyHash)
			fmt.Fprintln(cmd.OutOrStdout(), "approval:    required (preview a completed turn with --approve)")
			return nil
		},
	}
	cmd.Flags().StringVar(&remote, "remote", "", "Git URL or path that will receive shared history refs")
	cmd.Flags().StringVar(&promptMode, "prompt-mode", "", "Prompt publication policy: redacted_text or omit")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func sharePreviewCmd() *cobra.Command {
	var approve bool
	var jsonOutput bool
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
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			plan, err := sharedhistory.New(repo).Preview(cmd.Context(), sharedhistory.PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: approve})
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
			writeOmissions(cmd, plan.Manifest.Omissions)
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
	return cmd
}

func shareStatusCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Inspect shared history consent and synchronization state",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			status, err := sharedhistory.New(repo).Status(cmd.Context())
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
			fmt.Fprintf(cmd.OutOrStdout(), "events:      %d\n", len(bundle.Events))
			writeOmissions(cmd, bundle.Manifest.Omissions)
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
	cmd := &cobra.Command{
		Use:          string(direction),
		Short:        "Synchronize shared history " + string(direction),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			result, err := sharedhistory.New(repo).Sync(cmd.Context(), direction)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s complete\n", direction)
			if direction == sharedhistory.DirectionPush {
				fmt.Fprintf(cmd.OutOrStdout(), "published: %d\n", result.Published)
				fmt.Fprintf(cmd.OutOrStdout(), "blocked:   %d\n", result.Blocked)
				if result.Head != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "head:      %s\n", result.Head)
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "pulled: %d\n", result.Pulled)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
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
	fmt.Fprintf(cmd.OutOrStdout(), "remote:      %s\n", status.Remote)
	fmt.Fprintf(cmd.OutOrStdout(), "device:      %s\n", status.DeviceID)
	fmt.Fprintf(cmd.OutOrStdout(), "prompt mode: %s\n", status.PromptMode)
	fmt.Fprintf(cmd.OutOrStdout(), "approved:    %t\n", status.Approved)
	fmt.Fprintf(cmd.OutOrStdout(), "pending:     %d\n", status.Pending)
	fmt.Fprintf(cmd.OutOrStdout(), "published:   %d\n", status.Published)
	fmt.Fprintf(cmd.OutOrStdout(), "pulled:      %d\n", status.Pulled)
	fmt.Fprintf(cmd.OutOrStdout(), "local ahead: %t\n", status.UnpushedLocalTip)
	if status.RemoteError != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "remote error: %s\n", status.RemoteError)
	}
	if len(status.Blocked) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "blocked:")
		keys := make([]string, 0, len(status.Blocked))
		for key := range status.Blocked {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s\n", key, status.Blocked[key])
		}
	}
}

func writeOmissions(cmd *cobra.Command, omissions map[string]int) {
	if len(omissions) == 0 {
		return
	}
	keys := make([]string, 0, len(omissions))
	for key := range omissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintln(cmd.OutOrStdout(), "omissions:")
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %d\n", key, omissions[key])
	}
}

func firstSourceCommit(links []sharedhistory.SourceLink) string {
	for _, link := range links {
		if link.CommitSHA != "" {
			return link.CommitSHA
		}
	}
	return "none"
}
