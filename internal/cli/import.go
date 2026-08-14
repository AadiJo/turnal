package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sessionhistory"
	"github.com/AadiJo/turnal/internal/workspacegit"
	"github.com/spf13/cobra"
)

func importCmd() *cobra.Command {
	var dryRun bool
	var jsonOutput bool
	var transcriptPath string
	var sessionIDs []string
	cmd := &cobra.Command{
		Use:          "import <adapter>",
		Short:        "Import local agent transcripts as read-only history",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			adapter, err := primitives.ParseAdapterName(args[0])
			if err != nil {
				return err
			}
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			candidates, warnings, err := sessionhistory.Discover(sessionhistory.DiscoverOptions{
				Adapter: adapter, WorkspaceRoot: repo.WorkspaceRoot, Path: transcriptPath, SessionIDs: sessionIDs,
			})
			if err != nil {
				return err
			}
			plan, err := sessionhistory.PlanImport(repo, adapter, candidates, warnings)
			if err != nil {
				return err
			}
			plan.DryRun = dryRun
			if dryRun {
				if jsonOutput {
					return writeIndentedJSON(cmd, plan)
				}
				return writeImportPlan(cmd, plan)
			}
			result, err := sessionhistory.ApplyImport(repo, plan)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeIndentedJSON(cmd, struct {
					Plan   sessionhistory.ImportPlan   `json:"plan"`
					Result sessionhistory.ImportResult `json:"result"`
				}{plan, result})
			}
			return writeImportResult(cmd, plan, result)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report every conversion without writing history")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().StringVar(&transcriptPath, "path", "", "Override the provider transcript directory")
	cmd.Flags().StringSliceVar(&sessionIDs, "session", nil, "Import only this provider session id (repeatable)")
	return cmd
}

func sessionAttachCmd() *cobra.Command {
	var adapterText string
	var commitRevision string
	var dryRun bool
	var jsonOutput bool
	var transcriptPath string
	cmd := &cobra.Command{
		Use:          "attach <session-id>",
		Short:        "Attach an existing session to a source commit without rewriting Git history",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			commit, err := workspacegit.Open(repo.WorkspaceRoot).ResolveCommit(commitRevision)
			if err != nil {
				return err
			}

			sessionID, importedPlan, err := resolveAttachSession(repo, args[0], adapterText, transcriptPath)
			if err != nil {
				return err
			}
			alreadyAttached, err := sessionhistory.AttachmentExists(repo, sessionID, commit)
			if err != nil {
				return err
			}
			preview := struct {
				SessionID        primitives.SessionID       `json:"session_id"`
				CommitSHA        primitives.CommitSHA       `json:"commit_sha"`
				Revision         string                     `json:"revision"`
				HistoryRewritten bool                       `json:"history_rewritten"`
				AlreadyAttached  bool                       `json:"already_attached"`
				ImportPlan       *sessionhistory.ImportPlan `json:"import_plan,omitempty"`
			}{sessionID, commit, commitRevision, false, alreadyAttached, importedPlan}
			if dryRun {
				if importedPlan != nil {
					importedPlan.DryRun = true
				}
				if jsonOutput {
					return writeIndentedJSON(cmd, preview)
				}
				if importedPlan != nil {
					if err := writeImportPlan(cmd, *importedPlan); err != nil {
						return err
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Would attach session %s to %s\n", sessionID, commit)
				fmt.Fprintln(cmd.OutOrStdout(), "Source Git history would not be rewritten.")
				return nil
			}

			var importResult *sessionhistory.ImportResult
			if importedPlan != nil {
				result, err := sessionhistory.ApplyImport(repo, *importedPlan)
				if err != nil {
					return err
				}
				importResult = &result
			}
			event, attached, err := sessionhistory.Attach(repo, sessionID, commit, commitRevision)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeIndentedJSON(cmd, struct {
					SessionID        primitives.SessionID         `json:"session_id"`
					CommitSHA        primitives.CommitSHA         `json:"commit_sha"`
					Revision         string                       `json:"revision"`
					Attached         bool                         `json:"attached"`
					HistoryRewritten bool                         `json:"history_rewritten"`
					ImportResult     *sessionhistory.ImportResult `json:"import_result,omitempty"`
					EventSequence    uint64                       `json:"event_sequence"`
				}{sessionID, commit, commitRevision, attached, false, importResult, event.Seq.Uint64()})
			}
			if importedPlan != nil && importResult != nil {
				if err := writeImportResult(cmd, *importedPlan, *importResult); err != nil {
					return err
				}
			}
			if attached {
				fmt.Fprintf(cmd.OutOrStdout(), "Attached session %s to %s\n", sessionID, commit)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Session %s is already attached to %s\n", sessionID, commit)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Source Git history was not rewritten.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&adapterText, "adapter", "a", "auto", "Transcript adapter for an uncaptured session: auto, claude-code, or codex")
	cmd.Flags().StringVar(&commitRevision, "commit", "HEAD", "Source Git revision to attach")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview transcript import and attachment without writing history")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().StringVar(&transcriptPath, "path", "", "Override the provider transcript directory")
	return cmd
}

func resolveAttachSession(repo *checkpoint.Repo, providerSessionID, adapterText, transcriptPath string) (primitives.SessionID, *sessionhistory.ImportPlan, error) {
	if existingID, err := primitives.ParseSessionID(providerSessionID); err == nil {
		events, readErr := repo.EventLog().Read(existingID)
		if readErr != nil {
			return "", nil, readErr
		}
		if len(events) > 0 {
			return existingID, nil, nil
		}
	}

	var adaptersToTry []primitives.AdapterName
	if adapterText == "" || strings.EqualFold(adapterText, "auto") {
		adaptersToTry = []primitives.AdapterName{primitives.AdapterClaudeCode, primitives.AdapterCodex}
	} else {
		adapter, err := primitives.ParseAdapterName(adapterText)
		if err != nil {
			return "", nil, err
		}
		adaptersToTry = []primitives.AdapterName{adapter}
	}

	var matches []sessionhistory.Candidate
	var warnings []string
	for _, adapter := range adaptersToTry {
		candidates, discoveredWarnings, err := sessionhistory.Discover(sessionhistory.DiscoverOptions{
			Adapter: adapter, WorkspaceRoot: repo.WorkspaceRoot, Path: transcriptPath, SessionIDs: []string{providerSessionID},
		})
		if err != nil {
			return "", nil, err
		}
		matches = append(matches, candidates...)
		if len(candidates) > 0 || len(adaptersToTry) == 1 {
			warnings = append(warnings, discoveredWarnings...)
		}
	}
	if len(matches) == 0 {
		return "", nil, fmt.Errorf("session %s was not found in supported transcript stores for this workspace", providerSessionID)
	}
	if len(matches) > 1 {
		return "", nil, fmt.Errorf("session %s matched more than one transcript; select an adapter with --adapter", providerSessionID)
	}
	plan, err := sessionhistory.PlanImport(repo, matches[0].Adapter, matches, warnings)
	if err != nil {
		return "", nil, err
	}
	plan.DryRun = false
	return matches[0].SessionID, &plan, nil
}

func writeImportPlan(cmd *cobra.Command, plan sessionhistory.ImportPlan) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Import preview for %s\n", plan.Adapter)
	if len(plan.Sessions) == 0 {
		fmt.Fprintln(out, "  no matching transcripts found")
	}
	for _, session := range plan.Sessions {
		fmt.Fprintf(out, "  [%s] %s -> %s (%d turns, %d pending events)\n", session.State, session.ProviderSessionID, session.SessionID, session.TurnCount, session.PendingEvents)
		for _, warning := range session.Warnings {
			fmt.Fprintf(out, "    warning: %s\n", warning)
		}
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(out, "  warning: %s\n", warning)
	}
	if plan.DryRun {
		fmt.Fprintln(out, "No history was written.")
	}
	return nil
}

func writeImportResult(cmd *cobra.Command, plan sessionhistory.ImportPlan, result sessionhistory.ImportResult) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Imported %d %s session(s), %d turn(s), and %d event(s)\n", result.ImportedSessions, plan.Adapter, result.ImportedTurns, result.AppendedEvents)
	if result.SkippedSessions > 0 {
		fmt.Fprintf(out, "Skipped %d session(s) with no new importable history.\n", result.SkippedSessions)
	}
	for _, warning := range uniqueStrings(result.Warnings) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
	}
	return nil
}

func writeIndentedJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
