package viewer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/projects"
)

// registry owns the machine-wide project index plus a lazily populated cache of
// per-store read services. Opening a store is not free, so a service is created
// on first use for that project and reused afterwards.
type registry struct {
	db       *projects.DB
	mu       sync.Mutex
	services map[string]*Service
}

func newRegistry(db *projects.DB) *registry {
	return &registry{db: db, services: make(map[string]*Service)}
}

// service resolves a store id to its read service. The error is deliberately
// specific: an unknown project must fail before any path is opened.
func (r *registry) service(ctx context.Context, storeID string) (*Service, error) {
	r.mu.Lock()
	if service, ok := r.services[storeID]; ok {
		r.mu.Unlock()
		return service, nil
	}
	r.mu.Unlock()

	project, err := r.db.Project(ctx, storeID)
	if err != nil {
		return nil, err
	}
	if !project.Present {
		return nil, fmt.Errorf("project store is no longer on disk: %s", project.StorePath)
	}
	root, err := primitives.ParseWorkspaceRoot(project.Root)
	if err != nil {
		return nil, err
	}
	repo, err := checkpoint.OpenAt(root, project.StorePath)
	if err != nil {
		return nil, err
	}
	service, err := NewService(repo)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Another request may have opened the same store concurrently; keep whichever
	// landed first so both requests share one handle.
	if existing, ok := r.services[storeID]; ok {
		return existing, nil
	}
	r.services[storeID] = service
	return service, nil
}

// forget drops a cached service, used after a project leaves the registry.
func (r *registry) forget(storeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, storeID)
}

// refresh re-reads the registry and re-summarizes each store.
func (r *registry) refresh(ctx context.Context) error {
	return r.db.Refresh(ctx, r.summarize)
}

// summarize opens one store read-only and aggregates what the index needs. It
// runs per store during a refresh, so it reuses the cached service when one
// already exists.
func (r *registry) summarize(ctx context.Context, store checkpoint.RegisteredStore) (projects.Summary, error) {
	storeID := store.StoreID.String()
	root := store.StorePath
	for _, worktree := range store.Worktrees {
		if worktree.Root != "" {
			root = worktree.Root
			break
		}
	}
	if root == store.StorePath {
		root = filepath.Dir(store.StorePath)
	}
	workspaceRoot, err := primitives.ParseWorkspaceRoot(root)
	if err != nil {
		return projects.Summary{}, err
	}
	repo, err := checkpoint.OpenAt(workspaceRoot, store.StorePath)
	if err != nil {
		return projects.Summary{}, err
	}
	service, err := NewService(repo)
	if err != nil {
		return projects.Summary{}, err
	}
	r.mu.Lock()
	r.services[storeID] = service
	r.mu.Unlock()

	workspace, err := service.Workspace(ctx)
	if err != nil {
		return projects.Summary{}, err
	}
	sessions, err := service.Sessions(ctx)
	if err != nil {
		return projects.Summary{}, err
	}
	summary := projects.Summary{
		IndexState:   workspace.IndexState,
		HistoryState: workspace.HistoryState,
		TurnCount:    workspace.TurnCount,
		LastActivity: workspace.LastActivity,
	}
	// A wrapped run records two sessions for the same work: the wrapper's own
	// session and the provider's hook session. They share a checkpoint pair, so
	// listing both double-counts the change and shows a promptless twin in the
	// feed. Keep the one carrying prompt text.
	redundant := redundantWrapperSessions(sessions)

	for _, session := range sessions {
		if _, skip := redundant[session.Key]; skip {
			continue
		}
		summary.SessionCount++
		summary.Additions += session.Additions
		summary.Deletions += session.Deletions
		summary.Sessions = append(summary.Sessions, projects.Activity{
			StoreID:    storeID,
			SessionKey: session.Key,
			SessionID:  session.ID,
			Title:      session.PromptPreview,
			Adapter:    session.Adapter,
			Model:      session.Model,
			Branch:     session.Branch,
			Status:     session.Status,
			TurnCount:  session.TurnCount,
			FileCount:  session.FileCount,
			Additions:  session.Additions,
			Deletions:  session.Deletions,
			StartedAt:  session.StartedAt,
			FinishedAt: session.FinishedAt,
		})
	}
	// The newest session supplies the branch and headline on the project row.
	// Prefer the newest session that actually carries a prompt: a wrapped run
	// records both a wrapper session and the provider's hook session, and only
	// the latter has prompt text. Falling back to the plain newest session keeps
	// manual checkpoints, which never carry a prompt, from blanking the row.
	sorted := append([]SessionSummaryView(nil), sessions...)
	sort.Slice(sorted, func(i, j int) bool {
		return sessionRecency(sorted[i]).After(sessionRecency(sorted[j]))
	})
	if len(sorted) > 0 {
		headline := sorted[0]
		for _, session := range sorted {
			if strings.TrimSpace(session.PromptPreview) != "" {
				headline = session
				break
			}
		}
		summary.Branch = headline.Branch
		summary.LastPrompt = headline.PromptPreview
		summary.LastAdapter = headline.Adapter
		if headline.Model != "" {
			summary.LastAdapter = headline.Adapter + " · " + headline.Model
		}
	}
	return summary, nil
}

// redundantWrapperSessions finds sessions that describe the same work as
// another session but carry no prompt. `turnal run` opens its own session for
// wrapper checkpoints while the provider's hooks open a second session for the
// same turn; both end up with identical change counts and finish times. Only the
// hook session has the prompt, so the promptless twin is the redundant one.
//
// Sessions are matched on their change shape and finish time rather than on any
// id, because the two sessions are deliberately independent event streams.
func redundantWrapperSessions(sessions []SessionSummaryView) map[string]struct{} {
	var prompted []SessionSummaryView
	for _, session := range sessions {
		if strings.TrimSpace(session.PromptPreview) != "" {
			prompted = append(prompted, session)
		}
	}
	redundant := make(map[string]struct{})
	for _, session := range sessions {
		if strings.TrimSpace(session.PromptPreview) != "" {
			continue
		}
		for _, twin := range prompted {
			// The wrapper opens slightly before and closes slightly after the
			// provider session it wraps, so finish times differ by seconds
			// rather than matching exactly.
			if session.Additions == twin.Additions &&
				session.Deletions == twin.Deletions &&
				session.FileCount == twin.FileCount &&
				withinWrapperSkew(session.FinishedAt, twin.FinishedAt) {
				redundant[session.Key] = struct{}{}
				break
			}
		}
	}
	return redundant
}

// wrapperSkew bounds how far apart a wrapper session and the provider session
// it wraps may finish while still describing the same work.
const wrapperSkew = 30 * time.Second

func withinWrapperSkew(left, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return false
	}
	delta := left.Sub(right)
	if delta < 0 {
		delta = -delta
	}
	return delta <= wrapperSkew
}

func sessionRecency(session SessionSummaryView) time.Time {
	if !session.FinishedAt.IsZero() {
		return session.FinishedAt
	}
	return session.StartedAt
}

// projectViews converts indexed rows into the API shape.
func projectViews(list []projects.Project) []ProjectView {
	views := make([]ProjectView, 0, len(list))
	for _, project := range list {
		view := ProjectView{
			StoreID:      project.StoreID,
			RepoID:       project.RepoID,
			Name:         project.Name,
			Root:         project.Root,
			StorePath:    project.StorePath,
			Branch:       project.Branch,
			Present:      project.Present,
			IndexState:   project.IndexState,
			HistoryState: project.HistoryState,
			SessionCount: project.SessionCount,
			TurnCount:    project.TurnCount,
			Additions:    project.Additions,
			Deletions:    project.Deletions,
			LastActivity: project.LastActivity,
			LastPrompt:   project.LastPrompt,
			LastAdapter:  project.LastAdapter,
			AddedAt:      project.AddedAt,
		}
		for _, worktree := range project.Worktrees {
			view.Worktrees = append(view.Worktrees, ProjectWorktreeView{
				Root: worktree.Root, GitDir: worktree.GitDir, LastSeenAt: worktree.LastSeenAt,
			})
		}
		views = append(views, view)
	}
	return views
}

func activityViews(list []projects.Activity) []ActivityView {
	views := make([]ActivityView, 0, len(list))
	for _, item := range list {
		views = append(views, ActivityView{
			StoreID:     item.StoreID,
			ProjectName: item.ProjectName,
			SessionKey:  item.SessionKey,
			SessionID:   item.SessionID,
			Title:       item.Title,
			Adapter:     item.Adapter,
			Model:       item.Model,
			Branch:      item.Branch,
			Status:      item.Status,
			TurnCount:   item.TurnCount,
			FileCount:   item.FileCount,
			Additions:   item.Additions,
			Deletions:   item.Deletions,
			StartedAt:   item.StartedAt,
			FinishedAt:  item.FinishedAt,
		})
	}
	return views
}

// addProject runs the same initialization path as turnal init: bootstrap the
// store, optionally update .gitignore, then install agent hooks. This is the
// only write path in the viewer, so it validates the directory first and
// reports every filesystem effect it produced.
func (r *registry) addProject(ctx context.Context, request AddProjectRequest) (AddProjectResult, error) {
	target := strings.TrimSpace(request.Directory)
	if target == "" {
		return AddProjectResult{}, fmt.Errorf("directory is required")
	}
	if strings.HasPrefix(target, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return AddProjectResult{}, fmt.Errorf("resolve home directory: %w", err)
		}
		target = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(target, "~"), string(os.PathSeparator)))
	}
	if !filepath.IsAbs(target) {
		return AddProjectResult{}, fmt.Errorf("directory must be an absolute path")
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil {
		return AddProjectResult{}, fmt.Errorf("read directory %s: %w", target, err)
	}
	if !info.IsDir() {
		return AddProjectResult{}, fmt.Errorf("%s is not a directory", target)
	}
	root, err := primitives.ParseWorkspaceRoot(target)
	if err != nil {
		return AddProjectResult{}, err
	}

	agent := strings.TrimSpace(request.Agent)
	if agent == "" {
		agent = string(adapters.TargetAuto)
	}
	overrides := agentconfig.Overrides{InitAgent: &agent}
	installHooks := agent != string(adapters.TargetNone)
	overrides.InitInstallHooks = &installHooks
	if request.GitSync {
		gitSync := true
		overrides.GitSyncEnabled = &gitSync
	}
	effective, _, err := agentconfig.Resolve(root.String(), overrides)
	if err != nil {
		return AddProjectResult{}, err
	}

	bootstrapped, err := checkpoint.BootstrapWithOptions(root, checkpoint.BootstrapOptions{
		UpdateGitignore: request.UpdateGitignore,
	})
	if err != nil {
		return AddProjectResult{}, err
	}
	// Bootstrap only auto-registers Git workspaces. Adding a project is an
	// explicit adoption, so register it either way or a non-Git project would
	// never appear in the index.
	if err := bootstrapped.Repo.RegisterStore(); err != nil {
		return AddProjectResult{}, err
	}
	result := AddProjectResult{
		StoreID:          bootstrapped.Repo.StoreID.String(),
		Root:             bootstrapped.Repo.WorkspaceRoot.String(),
		StorePath:        bootstrapped.Repo.MetadataDir,
		Attached:         bootstrapped.Attached,
		GitignoreUpdated: bootstrapped.GitignoreUpdated,
	}

	if installHooks {
		targets, resolveErr := adapters.ResolveTargets(root.String(), adapters.Target(effective.Init.Agent))
		if resolveErr != nil {
			return result, resolveErr
		}
		installed, installErr := adapters.InstallWithOptions(root.String(), targets, adapters.InstallOptions{
			HookCommand: effective.Hooks.Command,
		})
		if installErr != nil {
			return result, installErr
		}
		for _, adapter := range installed {
			result.Hooks = append(result.Hooks, string(adapter.Target))
		}
	}

	if err := r.refresh(ctx); err != nil {
		return result, err
	}
	return result, nil
}
