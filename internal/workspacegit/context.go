package workspacegit

import (
	"strings"
)

type Context struct {
	Exists       bool   `json:"exists"`
	WorktreeRoot string `json:"worktree_root,omitempty"`
	Head         string `json:"head,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Detached     bool   `json:"detached,omitempty"`
	Dirty        bool   `json:"dirty"`
	Upstream     string `json:"upstream,omitempty"`
	Base         string `json:"base,omitempty"`
}

func (git Git) Context() (Context, error) {
	inside, err := git.runOutput("rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return Context{Exists: false}, nil
	}

	context := Context{Exists: true}
	if root, err := git.runOutput("rev-parse", "--show-toplevel"); err == nil {
		context.WorktreeRoot = strings.TrimSpace(root)
	}
	if head, err := git.runOutput("rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		context.Head = strings.TrimSpace(head)
	}
	if branch, err := git.runOutput("symbolic-ref", "-q", "HEAD"); err == nil {
		context.Branch = strings.TrimSpace(branch)
	} else if context.Head != "" {
		context.Detached = true
	}
	if status, err := git.runOutput("status", "--porcelain", "--untracked-files=all"); err == nil {
		context.Dirty = strings.TrimSpace(status) != ""
	}
	if upstream, err := git.runOutput("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		context.Upstream = strings.TrimSpace(upstream)
	}
	if context.Head != "" && context.Upstream != "" {
		if base, err := git.runOutput("merge-base", "HEAD", context.Upstream); err == nil {
			context.Base = strings.TrimSpace(base)
		}
	}
	return context, nil
}
