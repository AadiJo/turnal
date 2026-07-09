package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

func searchCmd() *cobra.Command {
	var session string
	var limit int
	var jsonOutput bool
	var allWorktrees bool

	cmd := &cobra.Command{
		Use:          "search [query]",
		Short:        "Search indexed agent turns",
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or greater")
			}

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
			store, err := openHealthySearchIndex(repo.MetadataDir)
			if err != nil {
				return err
			}
			defer store.Close()

			worktreeID := repo.WorktreeID
			if allWorktrees {
				worktreeID = ""
			}
			results, err := store.Search(queryindex.SearchQuery{
				Query:      strings.Join(args, " "),
				Session:    sessionID,
				WorktreeID: worktreeID,
				Limit:      limit,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if jsonOutput {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			return writeSearchResults(out, results)
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to search; defaults to all sessions")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum results; 0 shows all")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	cmd.Flags().BoolVar(&allWorktrees, "all-worktrees", false, "Search every attached and imported worktree")
	return cmd
}

func openHealthySearchIndex(metadataDir string) (*queryindex.Store, error) {
	exists, err := queryindex.Exists(metadataDir)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("search index is missing; run turnal reindex")
	}

	store, err := queryindex.Open(metadataDir)
	if err != nil {
		return nil, err
	}
	healthy, err := store.Healthy()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if !healthy {
		_ = store.Close()
		return nil, fmt.Errorf("search index is stale; run turnal reindex")
	}
	return store, nil
}

func writeSearchResults(w io.Writer, results []queryindex.SearchResult) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(w, "no matches")
		return err
	}

	for index, result := range results {
		if index > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s:%s", result.SessionID, result.TurnID); err != nil {
			return err
		}
		if result.WorktreeID != "" {
			if _, err := fmt.Fprintf(w, "  worktree=%s", result.WorktreeID); err != nil {
				return err
			}
		}
		if result.Adapter != "" {
			if _, err := fmt.Fprintf(w, "  %s", result.Adapter); err != nil {
				return err
			}
		}
		if at := searchResultTime(result); !at.IsZero() {
			if _, err := fmt.Fprintf(w, "  %s", at.UTC().Format("2006-01-02 15:04:05 UTC")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}

		if result.Prompt != "" {
			if _, err := fmt.Fprintf(w, "  prompt: %s\n", truncateText(collapseSearchWhitespace(result.Prompt), 140)); err != nil {
				return err
			}
		}
		if result.Assistant != "" {
			if _, err := fmt.Fprintf(w, "  assistant: %s\n", truncateText(collapseSearchWhitespace(result.Assistant), 140)); err != nil {
				return err
			}
		}
		if len(result.ToolNames) > 0 {
			if _, err := fmt.Fprintf(w, "  tools: %s\n", truncateText(strings.Join(result.ToolNames, ", "), 140)); err != nil {
				return err
			}
		}
		if len(result.Paths) > 0 {
			if _, err := fmt.Fprintf(w, "  files: %s\n", truncateText(strings.Join(result.Paths, ", "), 140)); err != nil {
				return err
			}
		}
		if result.Snippet != "" {
			if _, err := fmt.Fprintf(w, "  match: %s\n", truncateText(collapseSearchWhitespace(result.Snippet), 180)); err != nil {
				return err
			}
		}
	}
	return nil
}

func searchResultTime(result queryindex.SearchResult) time.Time {
	if !result.Last.IsZero() {
		return result.Last
	}
	return result.First
}

func collapseSearchWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
