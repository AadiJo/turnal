package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AadiJo/turnal/internal/bisect"
	"github.com/AadiJo/turnal/internal/blame"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/verifier"
	"github.com/spf13/cobra"
)

func bisectCmd() *cobra.Command {
	var good string
	var bad string
	var session string
	var paths []string
	var checks []string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "bisect",
		Short: "Find the recorded turn that made repository checks fail",
		Long: `Binary search recorded checkpoints with the repository verifiers to find
where the checks go from passing to failing. Each candidate is materialized
into a Turnal-owned temporary directory; Turnal never modifies the active
workspace.

Candidates are every completed turn in this worktree, ordered by checkpoint
capture time, from the earliest pre-turn checkpoint to the latest post-turn
checkpoint. Both endpoints are verified first. The result names the last good
and first bad states, which are always adjacent and both verified. When
exactly one turn was active between them, that turn is the culprit and its
recorded prompt and intent are shown. When several turns overlapped, a filter
excluded one, or a turn never finished, the result is ambiguous and lists
them. When no turn was active, the change came from outside recorded turns.

Like git bisect, the search assumes checks stay failing once broken. With a
flaky or non-monotonic history it still reports a verified pass-to-fail
transition, but not necessarily the earliest one.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepoReadOnly()
			if err != nil {
				return err
			}
			verifiers, err := repositoryVerifiers(repo)
			if err != nil {
				return err
			}
			verifiers, err = selectVerifiers(verifiers, checks)
			if err != nil {
				return err
			}
			var filters bisect.Filters
			if session != "" {
				filters.Session, err = primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
			}
			for _, value := range paths {
				path, err := primitives.ParseRepoPath(value)
				if err != nil {
					return fmt.Errorf("--path %q: %w", value, err)
				}
				filters.Paths = append(filters.Paths, path)
			}
			history, err := blame.New(repo).History()
			if err != nil {
				return err
			}
			plan, err := bisect.NewPlan(repo, history, filters)
			if err != nil {
				return err
			}
			if len(plan.States) == 0 && len(plan.ExcludedTurns) > 0 {
				return fmt.Errorf("no completed turn matches --session or --path; %d completed turns were excluded", len(plan.ExcludedTurns))
			}
			if len(plan.States) < 2 {
				return fmt.Errorf("bisect needs at least two distinct recorded states; found %d completed turns yielding %d states", len(history.Completed), len(plan.States))
			}
			goodIndex, badIndex := 0, len(plan.States)-1
			if good != "" {
				if goodIndex, err = resolveBisectEndpoint(plan, good); err != nil {
					return err
				}
			}
			if bad != "" {
				if badIndex, err = resolveBisectEndpoint(plan, bad); err != nil {
					return err
				}
			}
			if goodIndex == badIndex {
				return fmt.Errorf("good and bad resolve to the same state %s with identical content", plan.States[goodIndex].Display())
			}
			if goodIndex > badIndex {
				return fmt.Errorf("good state %s must come before bad state %s", plan.States[goodIndex].Display(), plan.States[badIndex].Display())
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			names := make([]string, 0, len(verifiers))
			for _, definition := range verifiers {
				names = append(names, definition.Name)
			}
			report, err := bisect.Search(ctx, bisect.Request{
				Repo:      repo,
				Plan:      plan,
				GoodIndex: goodIndex,
				BadIndex:  badIndex,
				Probe:     checkpointProbe(repo, verifiers),
				Checks:    names,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else if err := bisect.WriteHuman(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Result.Kind == bisect.KindNotBisectable {
				return commandExitError{code: verifyFailureExitCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&good, "good", "", "Known passing state as <session>:<turn>[:pre|post] (defaults to the earliest pre checkpoint)")
	cmd.Flags().StringVar(&bad, "bad", "", "Known failing state as <session>:<turn>[:pre|post] (defaults to the latest post checkpoint)")
	cmd.Flags().StringVar(&session, "session", "", "Only search turns from this session (other sessions are still disclosed when active in the result window)")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "Only search turns that changed this workspace-relative file or directory (repeatable)")
	cmd.Flags().StringArrayVar(&checks, "check", nil, "Run only the named repository verifier (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit a versioned JSON bisect report")
	return cmd
}

// checkpointProbe materializes one state the same way `turnal verify` does and
// runs the selected verifiers there. Cleanup failures abort the search.
func checkpointProbe(repo *checkpoint.Repo, verifiers []config.Verifier) bisect.Probe {
	return func(ctx context.Context, state bisect.State) (report verifier.Report, returnErr error) {
		prepared, err := verifier.PrepareCheckpoint(repo, state.SessionID, state.TurnID, state.Phase)
		if err != nil {
			return verifier.Report{}, err
		}
		defer func() {
			if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}()
		return verifier.Run(ctx, verifier.Request{
			Root:      prepared.Root,
			Target:    prepared.Target,
			Verifiers: verifiers,
		})
	}
}

// selectVerifiers keeps the configured verifiers whose names were requested,
// in configuration order. An empty request keeps every verifier.
func selectVerifiers(verifiers []config.Verifier, names []string) ([]config.Verifier, error) {
	if len(names) == 0 {
		return verifiers, nil
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.TrimSpace(name)] = false
	}
	selected := make([]config.Verifier, 0, len(names))
	for _, definition := range verifiers {
		if _, ok := wanted[definition.Name]; ok {
			wanted[definition.Name] = true
			selected = append(selected, definition)
		}
	}
	for _, name := range names {
		if !wanted[strings.TrimSpace(name)] {
			available := make([]string, 0, len(verifiers))
			for _, definition := range verifiers {
				available = append(available, definition.Name)
			}
			return nil, fmt.Errorf("verifier %q is not configured; available: %s", name, strings.Join(available, ", "))
		}
	}
	return selected, nil
}

// resolveBisectEndpoint accepts <session>:<turn>[:pre|post] or the canonical
// <session>:turn:<turn>[:pre|post] form. The phase defaults to post.
func resolveBisectEndpoint(plan bisect.Plan, value string) (int, error) {
	sessionID, turnID, phase, err := parseBisectEndpoint(value)
	if err != nil {
		return 0, err
	}
	index, ok := plan.Find(sessionID, turnID, phase)
	if !ok {
		return 0, fmt.Errorf("%s:%s:%s is not a bisect state; it may be incomplete, outside this worktree, or excluded by --session or --path", sessionID, turnID, phase)
	}
	return index, nil
}

func parseBisectEndpoint(value string) (primitives.SessionID, primitives.TurnID, primitives.CheckpointPhase, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, "", fmt.Errorf("bisect endpoint is required")
	}
	if strings.Contains(value, ":turn:") {
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return "", 0, "", err
		}
		phase, ok := target.Phase()
		if !ok {
			phase = primitives.CheckpointPhasePost
		}
		return target.SessionID(), target.TurnID(), phase, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return "", 0, "", fmt.Errorf("bisect endpoint must be <session>:<turn>[:pre|post]")
	}
	sessionID, err := primitives.ParseSessionID(parts[0])
	if err != nil {
		return "", 0, "", err
	}
	turnID, err := primitives.ParseTurnID(parts[1])
	if err != nil {
		return "", 0, "", err
	}
	phase := primitives.CheckpointPhasePost
	if len(parts) == 3 {
		phase, err = primitives.ParseCheckpointPhase(parts[2])
		if err != nil {
			return "", 0, "", err
		}
	}
	return sessionID, turnID, phase, nil
}
