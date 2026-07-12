package verifier

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func WriteHuman(writer io.Writer, report Report) error {
	if _, err := fmt.Fprintf(writer, "target: %s\n", report.Target.Display); err != nil {
		return err
	}
	if report.Target.Commit != "" {
		if _, err := fmt.Fprintf(writer, "state:  %s\n", report.Target.Commit); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "checks: %d passed, %d failed, %d timed out, %d could not start, %d infrastructure errors\n\n",
		report.Summary.Passed, report.Summary.Failed, report.Summary.TimedOut, report.Summary.LaunchError,
		report.Summary.InfrastructureErrors); err != nil {
		return err
	}
	for _, check := range report.Checks {
		label, detail := humanStatus(check)
		if _, err := fmt.Fprintf(writer, "%-7s %-24s %8s%s\n", label, check.Name, humanDuration(check.DurationMS), detail); err != nil {
			return err
		}
		for _, infrastructureError := range check.InfrastructureErrors {
			if _, err := fmt.Fprintf(writer, "INFRA   %-24s          %s: %s\n", check.Name, infrastructureError.Stage, infrastructureError.Message); err != nil {
				return err
			}
		}
	}
	if len(report.Target.Limitations) > 0 {
		if _, err := fmt.Fprintln(writer, "\nlimitations:"); err != nil {
			return err
		}
		for _, limitation := range report.Target.Limitations {
			if _, err := fmt.Fprintf(writer, "  - %s\n", limitation); err != nil {
				return err
			}
		}
	}
	return nil
}

func humanStatus(check Check) (string, string) {
	switch check.Status {
	case StatusPassed:
		return "PASS", ""
	case StatusFailed:
		if check.ExitCode != nil {
			return "FAIL", fmt.Sprintf("  exit %d", *check.ExitCode)
		}
		return "FAIL", ""
	case StatusTimedOut:
		return "TIMEOUT", "  limit " + check.Timeout
	case StatusLaunchError:
		detail := strings.TrimSpace(check.LaunchError)
		if detail != "" {
			return "ERROR", "  " + detail
		}
		return "ERROR", "  could not start"
	default:
		return "ERROR", "  unknown result"
	}
}

func humanDuration(milliseconds int64) string {
	return (time.Duration(milliseconds) * time.Millisecond).String()
}
