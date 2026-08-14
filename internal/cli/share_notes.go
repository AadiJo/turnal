package cli

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sharedhistory"
	"github.com/spf13/cobra"
)

// shareNotesCmd groups the note publication channel.
//
// Note sharing is a separate opt-in from turn-context sharing. It publishes on
// its own ref namespace under its own approved policy, so enabling it never
// changes the turn-context policy hash and never asks existing publishers to
// re-approve something they did not turn on.
func shareNotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Publish reviewer notes to teammates",
		Long: `Publish reviewer notes to teammates.

Notes publish on their own ref namespace, separate from turn context. Enabling
note sharing does not change the turn-context policy, and a teammate running a
Turnal build that predates notes ignores this channel instead of failing.

A published note cannot be recalled. Hiding a note locally publishes a removal
that asks teammates to hide it too; it does not erase the copy they already
pulled.`,
	}
	cmd.AddCommand(shareNotesEnableCmd())
	cmd.AddCommand(shareNotesDisableCmd())
	cmd.AddCommand(shareNotesPreviewCmd())
	cmd.AddCommand(shareNotesStatusCmd())
	cmd.AddCommand(shareNotesListCmd())
	return cmd
}

func shareNotesEnableCmd() *cobra.Command {
	var promptMode string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "enable",
		Short:        "Configure note publication and its prompt policy",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			status, err := sharedhistory.ConfigureNotes(repo, sharedhistory.NotesConfigureOptions{
				PromptMode: sharedhistory.PromptMode(promptMode),
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, status)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "note sharing configured")
			fmt.Fprintf(out, "enabled:     %t\n", status.Enabled)
			fmt.Fprintf(out, "remote:      %s\n", status.Remote)
			fmt.Fprintf(out, "device:      %s\n", status.DeviceID)
			fmt.Fprintf(out, "prompt mode: %s\n", status.PromptMode)
			fmt.Fprintf(out, "policy:      %s\n", status.PolicyHash)
			if status.Approved {
				fmt.Fprintln(out, "approval:    current")
			} else {
				fmt.Fprintln(out, "approval:    required (preview a note with --approve)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&promptMode, "prompt-mode", "", "Text publication policy: redacted_text, omit, or metadata_only")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func shareNotesDisableCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:          "disable",
		Short:        "Stop note synchronization without deleting notes",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("rerun with --yes to disable future note synchronization; published copies cannot be recalled")
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if _, err := sharedhistory.DisableNotes(repo); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "note sharing disabled; local and published notes were preserved")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm note synchronization should be disabled")
	return cmd
}

func shareNotesPreviewCmd() *cobra.Command {
	var approve bool
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "preview <note-id>",
		Short:        "Show the exact note projection before publication",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			noteID, err := primitives.ParseNoteID(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			plan, err := sharedhistory.New(repo).PreviewNote(cmd.Context(), sharedhistory.NotePreviewOptions{
				NoteID: noteID, Approve: approve,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, plan)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "locator:     %s\n", plan.Locator)
			fmt.Fprintf(out, "note:        %s\n", plan.Manifest.NoteID)
			fmt.Fprintf(out, "target:      %s:%s\n", plan.Manifest.Target.SessionID, plan.Manifest.Target.TurnID)
			if plan.Manifest.References != "" {
				fmt.Fprintf(out, "references:  %s\n", indentSharedText(plan.Manifest.References))
			}
			fmt.Fprintf(out, "policy:      %s\n", plan.PolicyHash)
			fmt.Fprintf(out, "bytes:       %d\n", plan.Bytes)
			fmt.Fprintf(out, "prompt mode: %s\n", plan.Manifest.PromptMode)
			if plan.Note.Text != nil {
				fmt.Fprintf(out, "text:        %s\n", indentSharedText(plan.Note.Text.Text))
			} else {
				fmt.Fprintln(out, "text:        omitted by policy")
			}
			if plan.Note.Anchor != nil {
				fmt.Fprintf(out, "anchor:      %s", indentSharedText(plan.Note.Anchor.Path))
				if plan.Note.Anchor.LineStart > 0 {
					fmt.Fprintf(out, ":%d", plan.Note.Anchor.LineStart)
					if plan.Note.Anchor.LineEnd > plan.Note.Anchor.LineStart {
						fmt.Fprintf(out, "-%d", plan.Note.Anchor.LineEnd)
					}
				}
				fmt.Fprintln(out)
			}
			writeOmissions(cmd, plan.Manifest.Omissions)
			writeCounts(cmd, "redactions", plan.Manifest.Redactions)
			if approve {
				fmt.Fprintln(out, "approval:    recorded for this policy hash")
			} else if plan.ApprovalRequired {
				fmt.Fprintln(out, "approval:    required; rerun with --approve after reviewing --json output")
			} else {
				fmt.Fprintln(out, "approval:    current")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&approve, "approve", false, "Approve this note schema and policy hash for future publications")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the complete note projection as JSON")
	return cmd
}

func shareNotesStatusCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Inspect note sharing consent and synchronization state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			status, err := sharedhistory.New(repo).NotesStatus(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, status)
			}
			out := cmd.OutOrStdout()
			if !status.Configured {
				fmt.Fprintln(out, "note sharing: not configured")
				return nil
			}
			fmt.Fprintf(out, "enabled:     %t\n", status.Enabled)
			fmt.Fprintf(out, "remote:      %s\n", status.Remote)
			fmt.Fprintf(out, "device:      %s\n", status.DeviceID)
			fmt.Fprintf(out, "prompt mode: %s\n", status.PromptMode)
			fmt.Fprintf(out, "policy:      %s\n", status.PolicyHash)
			fmt.Fprintf(out, "approved:    %t\n", status.Approved)
			fmt.Fprintf(out, "pending:     %d\n", status.Pending)
			fmt.Fprintf(out, "published:   %d\n", status.Published)
			fmt.Fprintf(out, "pulled:      %d\n", status.Pulled)
			for device, reason := range status.Quarantined {
				fmt.Fprintf(out, "quarantined: %s (%s)\n", device, indentSharedText(reason))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	return cmd
}

func shareNotesListCmd() *cobra.Command {
	var jsonOutput bool
	var session string
	var references string
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List notes pulled from teammates",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options := sharedhistory.NoteListOptions{References: references}
			if session != "" {
				sessionID, err := primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
				options.SessionID = sessionID
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			summaries, err := sharedhistory.New(repo).ListNotes(cmd.Context(), options)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, summaries)
			}
			out := cmd.OutOrStdout()
			if len(summaries) == 0 {
				fmt.Fprintln(out, "no pulled notes")
				return nil
			}
			for _, summary := range summaries {
				fmt.Fprintf(out, "%s  %s:%s  publisher=%s\n", summary.Locator, summary.SessionID, summary.TurnID, summary.DeviceID)
				if summary.References != "" {
					fmt.Fprintf(out, "  replies to: %s\n", indentSharedText(summary.References))
				}
				if summary.Text != "" {
					fmt.Fprintf(out, "  %s\n", indentSharedText(summary.Text))
				} else {
					fmt.Fprintln(out, "  text omitted by the publisher's policy")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().StringVar(&session, "session", "", "Filter by target session ID")
	cmd.Flags().StringVar(&references, "references", "", "Only notes replying to this turn-context locator")
	return cmd
}

func syncNotesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notes", Short: "Synchronize reviewer notes through Git"}
	cmd.AddCommand(syncNotesDirectionCmd(sharedhistory.DirectionPush))
	cmd.AddCommand(syncNotesDirectionCmd(sharedhistory.DirectionPull))
	return cmd
}

func syncNotesDirectionCmd(direction sharedhistory.Direction) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:          string(direction),
		Short:        "Synchronize reviewer notes " + string(direction),
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			ctx, stop := sharedHistoryCommandContext(cmd.Context(), sharedHistorySyncTimeout)
			defer stop()
			result, err := sharedhistory.New(repo).SyncNotes(ctx, direction)
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
	return cmd
}
