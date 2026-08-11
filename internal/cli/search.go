package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/discovery"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/semantic"
	"github.com/spf13/cobra"
)

// newSearchEncoder is a variable so tests can rank against a deterministic
// vector space instead of downloading the real model.
var newSearchEncoder = func(ctx context.Context) (discovery.Encoder, error) {
	return semantic.NewEncoder(ctx)
}

func searchCmd() *cobra.Command {
	var session string
	var limit int
	var jsonOutput bool
	var allWorktrees bool
	var allProjects bool
	var semanticSearch bool

	cmd := &cobra.Command{
		Use:          "search [query]",
		Short:        "Search indexed agent turns",
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or greater")
			}
			if allProjects && allWorktrees {
				return fmt.Errorf("--all-projects already includes every worktree; do not combine it with --all-worktrees")
			}

			var sessionID primitives.SessionID
			var err error
			if session != "" {
				sessionID, err = primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
			}

			scopes, warnings, err := searchScopes(allProjects, allWorktrees)
			if err != nil {
				return err
			}

			search := localSearch{
				query: queryindex.SearchQuery{
					Query:   strings.Join(args, " "),
					Session: sessionID,
					Limit:   limit,
				},
				limit: limit,
			}
			if semanticSearch {
				search.newEncoder = newSearchEncoder
			}
			results, searchWarnings, err := search.run(cmd.Context(), scopes)
			// Warnings are reported even when the search failed. When every
			// project is unusable the error says only that, and these lines
			// are the sole record of which projects failed and why.
			if writeErr := writeSearchWarnings(cmd.ErrOrStderr(), append(warnings, searchWarnings...)); writeErr != nil {
				return writeErr
			}
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
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "Search every local project registered on this machine")
	cmd.Flags().BoolVar(&semanticSearch, "semantic", false, "Add local meaning matching (downloads an 8 MB model on first use)")
	return cmd
}

// searchScope is one index to query. project is nil for the current workspace,
// which both labels output and decides failure handling: a broken index in the
// project the user is standing in is an error, while a broken index in one of
// many machine-wide projects is a warning.
type searchScope struct {
	metadataDir string
	worktreeID  primitives.WorktreeID
	project     *discovery.Project
}

// searchScopes resolves the requested breadth into indexes to open.
func searchScopes(allProjects, allWorktrees bool) ([]searchScope, []string, error) {
	if !allProjects {
		repo, err := openCheckpointRepo()
		if err != nil {
			return nil, nil, err
		}
		scope := searchScope{metadataDir: repo.MetadataDir}
		if !allWorktrees {
			scope.worktreeID = repo.WorktreeID
		}
		return []searchScope{scope}, nil, nil
	}

	stores, err := checkpoint.ListRegisteredStores()
	if err != nil {
		return nil, nil, err
	}
	if len(stores) == 0 {
		return nil, nil, fmt.Errorf("no local Turnal projects are registered")
	}

	scopes := make([]searchScope, 0, len(stores))
	var warnings []string
	for _, store := range stores {
		root := store.PreferredRoot()
		project := &discovery.Project{
			Name:    filepath.Base(filepath.Clean(root)),
			Root:    root,
			StoreID: store.StoreID.String(),
		}
		if !checkpoint.StoreExists(store.StorePath) {
			warnings = append(warnings, fmt.Sprintf("skipped %s (%s): recorded store is absent", project.Name, project.Root))
			continue
		}
		scopes = append(scopes, searchScope{metadataDir: store.StorePath, project: project})
	}
	return scopes, warnings, nil
}

// localSearch collects candidates from every scope and ranks them together.
// A nil newEncoder means keyword-only search, which stays fully offline.
type localSearch struct {
	query      queryindex.SearchQuery
	limit      int
	newEncoder func(context.Context) (discovery.Encoder, error)
}

func (s localSearch) run(ctx context.Context, scopes []searchScope) ([]discovery.Result, []string, error) {
	// SQLite can apply the limit itself only when one index is queried by
	// keyword alone. Ranking across several stores, or against meaning
	// matches, needs every hit before it can pick a global top N.
	pushLimit := len(scopes) == 1 && s.newEncoder == nil

	var candidates []discovery.Candidate
	var warnings []string
	searched := 0
	for _, scope := range scopes {
		scopeCandidates, err := s.collect(scope, pushLimit)
		if err != nil {
			// One unreadable project must not hide the healthy ones, but the
			// project the user is standing in has nothing to fall back to.
			if scope.project == nil {
				return nil, warnings, err
			}
			detail := strings.TrimSuffix(err.Error(), "; run turnal reindex")
			warnings = append(warnings, fmt.Sprintf(
				"skipped %s (%s): %s; run turnal reindex from that project",
				scope.project.Name, scope.project.Root, detail,
			))
			continue
		}
		searched++
		candidates = append(candidates, scopeCandidates...)
	}
	if searched == 0 {
		return nil, warnings, fmt.Errorf("no local project has a usable search index")
	}

	var encoder discovery.Encoder
	if s.newEncoder != nil {
		var err error
		if encoder, err = s.newEncoder(ctx); err != nil {
			return nil, warnings, err
		}
	}
	results, err := discovery.Rank(s.query.Query, candidates, encoder, s.limit)
	return results, warnings, err
}

// collect runs both retrieval passes against one index and merges them, so a
// turn matched by keyword and by meaning is a single candidate.
func (s localSearch) collect(scope searchScope, pushLimit bool) ([]discovery.Candidate, error) {
	store, err := openHealthySearchIndex(scope.metadataDir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	query := s.query
	query.WorktreeID = scope.worktreeID
	if !pushLimit {
		query.Limit = 0
	}

	matches, err := store.Search(query)
	if err != nil {
		return nil, err
	}

	candidates := make([]discovery.Candidate, 0, len(matches))
	byTurn := make(map[string]int, len(matches))
	for rank, match := range matches {
		byTurn[searchTurnKey(match)] = len(candidates)
		candidates = append(candidates, discovery.Candidate{
			Project:     scope.project,
			Document:    queryindex.SearchDocument{Result: match},
			Keyword:     true,
			KeywordRank: rank,
		})
	}
	if s.newEncoder == nil {
		return candidates, nil
	}

	documents, err := store.SearchDocuments(query)
	if err != nil {
		return nil, err
	}
	for _, document := range documents {
		if index, ok := byTurn[searchTurnKey(document.Result)]; ok {
			// Keep the keyword result: it carries the FTS snippet and rank.
			candidates[index].Document.Text = document.Text
			continue
		}
		candidates = append(candidates, discovery.Candidate{
			Project:  scope.project,
			Document: document,
		})
	}
	return candidates, nil
}

func searchTurnKey(result queryindex.SearchResult) string {
	return result.StreamID.String() + "\x00" + result.TurnID.String()
}

// writeSearchWarnings reports skipped projects on stderr, keeping stdout to
// results alone so piped output stays parseable.
func writeSearchWarnings(w io.Writer, warnings []string) error {
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
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

func writeSearchResults(w io.Writer, results []discovery.Result) error {
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
		if result.Project != nil {
			if _, err := fmt.Fprintf(w, "  project=%s  root=%s", result.Project.Name, result.Project.Root); err != nil {
				return err
			}
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
		if result.Model != "" {
			separator := "  "
			if result.Adapter != "" {
				separator = " / "
			}
			if _, err := fmt.Fprintf(w, "%s%s", separator, result.Model); err != nil {
				return err
			}
		}
		if at := searchResultTime(result.SearchResult); !at.IsZero() {
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
		if result.Match.Reason != "" {
			if _, err := fmt.Fprintf(w, "  why: %s\n", result.Match.Reason); err != nil {
				return err
			}
		}
		if result.Match.SemanticLimited {
			if _, err := fmt.Fprintln(w, "  warning: meaning match used truncated indexed text"); err != nil {
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
