package destroy

import (
	"fmt"
	"os"
	"path/filepath"

	"agent-vcs-again/internal/adapters"
	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

const metadataDirName = ".agent-vcs"

type Options struct {
	DryRun         bool
	RemoveHooks    bool
	HookTargets    []adapters.Target
	HookTargetsSet bool
}

type Result struct {
	DryRun        bool
	WorkspaceRoot primitives.WorkspaceRoot
	MetadataDir   string
	HookResults   []adapters.UninstallResult
}

func Run(start string, opts Options) (Result, error) {
	root, metadataDir, err := FindRoot(start)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		DryRun:        opts.DryRun,
		WorkspaceRoot: root,
		MetadataDir:   metadataDir,
	}

	if opts.DryRun {
		if opts.RemoveHooks {
			hookResults, err := uninstallHooks(root.String(), opts)
			if err != nil {
				return Result{}, err
			}
			result.HookResults = hookResults
		}
		return result, nil
	}

	repo := &checkpoint.Repo{
		WorkspaceRoot: root,
		MetadataDir:   metadataDir,
		GitDir:        filepath.Join(metadataDir, "git"),
		TmpDir:        filepath.Join(metadataDir, "tmp"),
	}
	if err := repo.WithWorkspaceLock("destroy metadata", func() error {
		if opts.RemoveHooks {
			hookResults, err := uninstallHooks(root.String(), opts)
			if err != nil {
				return err
			}
			result.HookResults = hookResults
		}
		if err := os.RemoveAll(metadataDir); err != nil {
			return fmt.Errorf("remove metadata dir %s: %w", metadataDir, err)
		}
		return nil
	}); err != nil {
		return Result{}, err
	}

	return result, nil
}

func FindRoot(start string) (primitives.WorkspaceRoot, string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", "", fmt.Errorf("resolve start path: %w", err)
	}

	for {
		candidate := filepath.Join(abs, metadataDirName)
		info, err := os.Lstat(candidate)
		if err == nil {
			if !info.IsDir() {
				return "", "", fmt.Errorf("agent-vcs metadata path is not a directory: %s", candidate)
			}
			root, err := primitives.ParseWorkspaceRoot(abs)
			if err != nil {
				return "", "", err
			}
			return root, candidate, nil
		}
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("stat agent-vcs metadata path %s: %w", candidate, err)
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}

	return "", "", fmt.Errorf("not an agent-vcs workspace: no .agent-vcs directory found")
}

func uninstallHooks(projectRoot string, opts Options) ([]adapters.UninstallResult, error) {
	targets := opts.HookTargets
	if len(targets) == 0 && !opts.HookTargetsSet {
		var err error
		targets, err = adapters.ResolveTargets(projectRoot, adapters.TargetAuto)
		if err != nil {
			return nil, err
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	return adapters.UninstallWithOptions(projectRoot, targets, adapters.UninstallOptions{DryRun: opts.DryRun})
}
