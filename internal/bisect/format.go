package bisect

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/verifier"
)

const failureTailLines = 5

// WriteHuman prints the run log, the bracketing states, and either the
// culprit with the agent's recorded prompt and intent or the reason no single
// turn can be blamed.
func WriteHuman(writer io.Writer, report Report) error {
	emit := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format, args...)
		return err
	}
	searched := 0
	for index, state := range report.States {
		if state.Display() == report.Good.Display() {
			searched = -index
		}
		if state.Display() == report.Bad.Display() {
			searched += index + 1
		}
	}
	if err := emit("bisect %d states from %s to %s, checks: %s\n", searched, report.Good.Display(), report.Bad.Display(), terminalSafe(strings.Join(report.Checks, ", "))); err != nil {
		return err
	}
	for _, run := range report.Runs {
		label := "PASS"
		if run.Outcome == OutcomeFailed {
			label = "FAIL"
		}
		if err := emit("  %s  %-28s %s  %s\n", label, run.State.Display(), shortCommit(run.State.Commit.String()), humanDuration(run.DurationMS)); err != nil {
			return err
		}
	}

	result := report.Result
	if result.Kind == KindNotBisectable {
		if err := emit("not bisectable: %s\n", result.Reason); err != nil {
			return err
		}
		return writeFailureTails(writer, report, report.Runs[len(report.Runs)-1].State)
	}

	if err := emit("first bad: %s\nlast good: %s\n", result.FirstBad.Display(), result.LastGood.Display()); err != nil {
		return err
	}
	switch result.Kind {
	case KindTurn:
		culprit := result.Culprit
		if err := emit("culprit: turn %s:%s\n", culprit.SessionID, culprit.TurnID); err != nil {
			return err
		}
		if culprit.Adapter != "" || culprit.Model != "" {
			if err := emit("  agent:   %s\n", terminalSafe(strings.TrimSpace(culprit.Adapter+" "+culprit.Model))); err != nil {
				return err
			}
		}
		if culprit.Prompt != "" {
			if err := emit("  prompt:  %s\n", strconv.Quote(truncate(collapse(culprit.Prompt), 160))); err != nil {
				return err
			}
		}
		for _, intent := range culprit.Intents {
			line := strconv.Quote(truncate(collapse(intent.Problem), 160))
			if intent.Redacted {
				line += " (redacted)"
			} else if len(intent.Scope) > 0 {
				line += " scope " + terminalSafe(strings.Join(intent.Scope, ", "))
			}
			if err := emit("  intent:  %s\n", line); err != nil {
				return err
			}
		}
	case KindOutsideRecordedTurns:
		if err := emit("culprit: changes outside recorded turns between %s and %s\n", result.LastGood.Display(), result.FirstBad.Display()); err != nil {
			return err
		}
	case KindAmbiguous:
		if err := emit("culprit: ambiguous, %d turns were active between %s and %s\n", len(result.Participants), result.LastGood.Display(), result.FirstBad.Display()); err != nil {
			return err
		}
		labels := make([]string, 0, len(result.Participants))
		for _, participant := range result.Participants {
			labels = append(labels, participantLabel(participant))
		}
		if err := emit("  turns:   %s\n", strings.Join(labels, ", ")); err != nil {
			return err
		}
	}
	if len(result.Changed) > 0 {
		names := make([]string, 0, len(result.Changed))
		for _, change := range result.Changed {
			name := change.Status + " " + change.Path
			if change.OldPath != "" {
				name += " (was " + change.OldPath + ")"
			}
			names = append(names, terminalSafe(name))
		}
		if err := emit("  changed: %s\n", strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	if err := writeFailureTails(writer, report, *result.FirstBad); err != nil {
		return err
	}
	return emit("runs: %d in %s\n", len(report.Runs), humanDuration(report.DurationMS))
}

func participantLabel(ref TurnRef) string {
	switch {
	case ref.Incomplete:
		return ref.String() + " (unfinished)"
	case ref.ExcludedBy != "":
		return ref.String() + " (excluded by --" + ref.ExcludedBy + ")"
	default:
		return ref.String()
	}
}

// writeFailureTails shows each failing check for one state with the last few
// lines of its output, so the failure is visible without --json.
func writeFailureTails(writer io.Writer, report Report, state State) error {
	for _, run := range report.Runs {
		if run.State.Display() != state.Display() {
			continue
		}
		for _, check := range run.Report.Checks {
			if check.Status == verifier.StatusPassed {
				continue
			}
			detail := string(check.Status)
			switch {
			case check.Status == verifier.StatusTimedOut:
				detail = "timed out after " + check.Timeout
			case check.ExitCode != nil:
				detail = "exit " + strconv.Itoa(*check.ExitCode)
			}
			if _, err := fmt.Fprintf(writer, "  failed:  %s %s\n", terminalSafe(check.Name), detail); err != nil {
				return err
			}
			output := strings.TrimSpace(check.Stderr)
			if output == "" {
				output = strings.TrimSpace(check.Stdout)
			}
			for _, line := range tail(output, failureTailLines) {
				if _, err := fmt.Fprintf(writer, "    %s\n", terminalSafe(strings.ReplaceAll(line, "\t", "    "))); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return nil
}

func tail(text string, count int) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return lines
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func terminalSafe(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func humanDuration(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).Round(time.Millisecond).String()
}
