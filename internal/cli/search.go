package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/discovery"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/semantic"
	"github.com/spf13/cobra"
)

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

			targets, warnings, err := localSearchTargets(allProjects, allWorktrees)
			if err != nil {
				return err
			}
			results, searchWarnings, err := searchLocalHistory(cmd.Context(), targets, queryindex.SearchQuery{
				Query:   strings.Join(args, " "),
				Session: sessionID,
			}, semanticSearch, limit)
			if err != nil {
				return err
			}
			warnings = append(warnings, searchWarnings...)
			for _, warning := range warnings {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning); err != nil {
					return err
				}
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

type localSearchTarget struct {
	storeID     string
	metadataDir string
	worktreeID  primitives.WorktreeID
	project     *discovery.Project
}

func localSearchTargets(allProjects, allWorktrees bool) ([]localSearchTarget, []string, error) {
	if !allProjects {
		repo, err := openCheckpointRepo()
		if err != nil {
			return nil, nil, err
		}
		worktreeID := repo.WorktreeID
		if allWorktrees {
			worktreeID = ""
		}
		return []localSearchTarget{{
			storeID:     repo.StoreID.String(),
			metadataDir: repo.MetadataDir,
			worktreeID:  worktreeID,
		}}, nil, nil
	}

	stores, err := checkpoint.ListRegisteredStores()
	if err != nil {
		return nil, nil, err
	}
	if len(stores) == 0 {
		return nil, nil, fmt.Errorf("no local Turnal projects are registered")
	}
	targets := make([]localSearchTarget, 0, len(stores))
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
		targets = append(targets, localSearchTarget{
			storeID:     store.StoreID.String(),
			metadataDir: store.StorePath,
			project:     project,
		})
	}
	return targets, warnings, nil
}

func searchLocalHistory(
	ctx context.Context,
	targets []localSearchTarget,
	query queryindex.SearchQuery,
	semanticSearch bool,
	limit int,
) ([]discovery.Result, []string, error) {
	type keywordHit struct {
		target localSearchTarget
		result queryindex.SearchResult
	}
	var keywordHits []keywordHit
	var documents []discovery.Candidate
	var warnings []string
	opened := 0
	for _, target := range targets {
		store, err := openHealthySearchIndex(target.metadataDir)
		if err != nil {
			if target.project == nil {
				return nil, nil, err
			}
			detail := strings.TrimSuffix(err.Error(), "; run turnal reindex")
			warnings = append(warnings, fmt.Sprintf(
				"skipped %s (%s): %s; run turnal reindex from that project",
				target.project.Name,
				target.project.Root,
				detail,
			))
			continue
		}
		opened++
		storeQuery := query
		storeQuery.WorktreeID = target.worktreeID
		storeQuery.Limit = 0
		matches, searchErr := store.Search(storeQuery)
		if searchErr != nil {
			_ = store.Close()
			return nil, nil, searchErr
		}
		for _, result := range matches {
			keywordHits = append(keywordHits, keywordHit{target: target, result: result})
		}
		if semanticSearch {
			indexed, documentsErr := store.SearchDocuments(storeQuery)
			if documentsErr != nil {
				_ = store.Close()
				return nil, nil, documentsErr
			}
			for _, document := range indexed {
				documents = append(documents, discovery.Candidate{
					Scope:    target.storeID,
					Project:  target.project,
					Document: document,
				})
			}
		}
		if err := store.Close(); err != nil {
			return nil, nil, err
		}
	}
	if opened == 0 {
		return nil, warnings, fmt.Errorf("no local project has a usable search index")
	}

	sort.SliceStable(keywordHits, func(i, j int) bool {
		if keywordHits[i].result.Rank != keywordHits[j].result.Rank {
			return keywordHits[i].result.Rank < keywordHits[j].result.Rank
		}
		leftProject, rightProject := searchTargetSortKey(keywordHits[i].target), searchTargetSortKey(keywordHits[j].target)
		if leftProject != rightProject {
			return leftProject < rightProject
		}
		if keywordHits[i].result.SessionID != keywordHits[j].result.SessionID {
			return keywordHits[i].result.SessionID < keywordHits[j].result.SessionID
		}
		return keywordHits[i].result.TurnID < keywordHits[j].result.TurnID
	})

	if !semanticSearch {
		candidates := make([]discovery.Candidate, 0, len(keywordHits))
		for rank, hit := range keywordHits {
			candidates = append(candidates, discovery.Candidate{
				Scope:   hit.target.storeID,
				Project: hit.target.project,
				Document: queryindex.SearchDocument{
					Result: hit.result,
				},
				Keyword:     true,
				KeywordRank: rank,
			})
		}
		results, err := discovery.KeywordResults(candidates, limit)
		return results, warnings, err
	}

	byTurn := make(map[string]int, len(documents))
	for index, candidate := range documents {
		byTurn[searchCandidateKey(candidate.Scope, candidate.Document.Result)] = index
	}
	for rank, hit := range keywordHits {
		key := searchCandidateKey(hit.target.storeID, hit.result)
		if index, ok := byTurn[key]; ok {
			documents[index].Document.Result = hit.result
			documents[index].Keyword = true
			documents[index].KeywordRank = rank
			continue
		}
		documents = append(documents, discovery.Candidate{
			Scope:   hit.target.storeID,
			Project: hit.target.project,
			Document: queryindex.SearchDocument{
				Result: hit.result,
			},
			Keyword:     true,
			KeywordRank: rank,
		})
	}
	encoder, err := newSearchEncoder(ctx)
	if err != nil {
		return nil, warnings, err
	}
	results, err := discovery.Rank(query.Query, documents, encoder, limit)
	return results, warnings, err
}

func searchCandidateKey(storeID string, result queryindex.SearchResult) string {
	return storeID + "\x00" + result.StreamID.String() + "\x00" + result.TurnID.String()
}

func searchTargetSortKey(target localSearchTarget) string {
	if target.project == nil {
		return target.storeID
	}
	return target.project.Root + "\x00" + target.storeID
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
