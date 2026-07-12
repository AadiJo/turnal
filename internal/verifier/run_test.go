package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/config"
)

func TestRunPassingVerifier(t *testing.T) {
	report := runDefinitions(t, helperVerifier("passing", "pass"))
	if !report.Successful() || report.Summary.Passed != 1 {
		t.Fatalf("report = %#v", report)
	}
	check := report.Checks[0]
	if check.Status != StatusPassed || check.ExitCode == nil || *check.ExitCode != 0 {
		t.Fatalf("check = %#v", check)
	}
}

func TestRunNonzeroExitContinuesInDeclarationOrder(t *testing.T) {
	report := runDefinitions(t,
		helperVerifier("first-fails", "fail"),
		helperVerifier("second-passes", "pass"),
	)
	if report.Successful() || report.Summary.Failed != 1 || report.Summary.Passed != 1 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	if report.Checks[0].Name != "first-fails" || report.Checks[0].Status != StatusFailed || report.Checks[1].Name != "second-passes" || report.Checks[1].Status != StatusPassed {
		t.Fatalf("checks = %#v", report.Checks)
	}
	if report.Checks[0].ExitCode == nil || *report.Checks[0].ExitCode != 17 {
		t.Fatalf("failed exit = %#v", report.Checks[0].ExitCode)
	}
}

func TestRunTimeoutTerminatesChild(t *testing.T) {
	definition := helperVerifier("slow", "sleep")
	definition.Timeout = 50 * time.Millisecond
	started := time.Now()
	report := runDefinitions(t, definition)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timed out verifier took %s", elapsed)
	}
	check := report.Checks[0]
	if check.Status != StatusTimedOut || !check.TimedOut || report.Summary.TimedOut != 1 {
		t.Fatalf("timeout check = %#v summary=%#v", check, report.Summary)
	}
}

func TestRunCancellationStopsVerification(t *testing.T) {
	definition := helperVerifier("cancelled", "sleep")
	later := filepath.Join(t.TempDir(), "later")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	started := time.Now()
	_, err := Run(ctx, Request{
		Root:        t.TempDir(),
		Target:      Target{Kind: TargetLiveWorkspace},
		Verifiers:   []config.Verifier{definition, helperVerifier("must-not-run", "touch", later)},
		OutputLimit: DefaultOutputLimit,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled verification took %s", elapsed)
	}
	if _, statErr := os.Stat(later); !os.IsNotExist(statErr) {
		t.Fatalf("later verifier ran after cancellation: %v", statErr)
	}
}

func TestRunMissingExecutableIsLaunchError(t *testing.T) {
	definition := config.Verifier{Name: "missing", Command: filepath.Join(t.TempDir(), "definitely missing"), Timeout: time.Second}
	report := runDefinitions(t, definition)
	check := report.Checks[0]
	if check.Status != StatusLaunchError || check.LaunchError == "" || check.ExitCode != nil {
		t.Fatalf("launch check = %#v", check)
	}
}

func TestRunRejectsInvalidDefinitionBeforeLaunching(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "launched")
	definition := helperVerifier("", "touch", sentinel)
	_, err := Run(context.Background(), Request{Root: t.TempDir(), Target: Target{Kind: TargetLiveWorkspace}, Verifiers: []config.Verifier{definition}})
	if err == nil || !strings.Contains(err.Error(), "name must not be empty") {
		t.Fatalf("Run error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("invalid definition launched a command: %v", statErr)
	}
}

func TestRunCapturesAndTruncatesStreamsIndependently(t *testing.T) {
	t.Run("separate streams", func(t *testing.T) {
		report := runDefinitions(t, helperVerifier("streams", "streams"))
		check := report.Checks[0]
		if check.Stdout != "stdout text\n" || check.Stderr != "stderr text\n" {
			t.Fatalf("stdout=%q stderr=%q", check.Stdout, check.Stderr)
		}
		if check.StdoutTruncated || check.StderrTruncated {
			t.Fatalf("unexpected truncation: %#v", check)
		}
	})

	t.Run("large stdout", func(t *testing.T) {
		report := runRequest(t, Request{OutputLimit: 32, Verifiers: []config.Verifier{helperVerifier("large-stdout", "large-stdout")}})
		check := report.Checks[0]
		if len(check.Stdout) != 32 || !check.StdoutTruncated || check.StderrTruncated {
			t.Fatalf("stdout len=%d stdout truncated=%t stderr truncated=%t", len(check.Stdout), check.StdoutTruncated, check.StderrTruncated)
		}
	})

	t.Run("large stderr", func(t *testing.T) {
		report := runRequest(t, Request{OutputLimit: 32, Verifiers: []config.Verifier{helperVerifier("large-stderr", "large-stderr")}})
		check := report.Checks[0]
		if len(check.Stderr) != 32 || !check.StderrTruncated || check.StdoutTruncated {
			t.Fatalf("stderr len=%d stderr truncated=%t stdout truncated=%t", len(check.Stderr), check.StderrTruncated, check.StdoutTruncated)
		}
	})
}

func TestRunPassesArgumentsLiterallyAndSupportsSpaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	definition := helperVerifier("literal", "args", "$HOME", "argument with spaces", "*.go", "semi;colon")
	report := runRequest(t, Request{Root: root, Verifiers: []config.Verifier{definition}})
	var got struct {
		Args []string `json:"args"`
		CWD  string   `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(report.Checks[0].Stdout), &got); err != nil {
		t.Fatalf("decode helper output: %v; output=%q", err, report.Checks[0].Stdout)
	}
	want := []string{"$HOME", "argument with spaces", "*.go", "semi;colon"}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got.Args, want)
	}
	gotInfo, err := os.Stat(got.CWD)
	if err != nil {
		t.Fatalf("stat helper cwd %q: %v", got.CWD, err)
	}
	wantInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat evaluation root %q: %v", root, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("cwd = %q, want filesystem identity of %q", got.CWD, root)
	}
}

func TestReportJSONVersionAndHumanFailureSummary(t *testing.T) {
	report := runDefinitions(t, helperVerifier("broken-check", "fail"))
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.SchemaVersion != SchemaVersion || decoded.Summary.Outcome != "failed" {
		t.Fatalf("decoded report = %#v", decoded)
	}

	var output bytes.Buffer
	if err := WriteHuman(&output, report); err != nil {
		t.Fatalf("WriteHuman: %v", err)
	}
	for _, want := range []string{"0 passed, 1 failed", "FAIL", "broken-check", "exit 17"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("human output missing %q:\n%s", want, output.String())
		}
	}
}

func TestSuccessfulWaitWinsDeadlineBoundary(t *testing.T) {
	result := Check{Status: StatusLaunchError}
	classifyWaitResult(&result, nil, context.DeadlineExceeded, nil)
	if result.Status != StatusPassed || result.ExitCode == nil || *result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("classification = %#v, want passed", result)
	}
}

func runDefinitions(t *testing.T, definitions ...config.Verifier) Report {
	t.Helper()
	return runRequest(t, Request{Verifiers: definitions})
}

func runRequest(t *testing.T, request Request) Report {
	t.Helper()
	if request.Root == "" {
		request.Root = t.TempDir()
	}
	request.Target = Target{Kind: TargetLiveWorkspace, Display: "live workspace", Mutable: true, Environment: "inherited"}
	report, err := Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func helperVerifier(name, mode string, args ...string) config.Verifier {
	helperArgs := []string{"-test.run=^TestVerifierHelperProcess$", "--", mode}
	helperArgs = append(helperArgs, args...)
	return config.Verifier{Name: name, Command: os.Args[0], Args: helperArgs, Timeout: 5 * time.Second}
}

func TestVerifierHelperProcess(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	args := os.Args[separator+2:]
	switch mode {
	case "pass":
		os.Exit(0)
	case "fail":
		os.Exit(17)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "streams":
		fmt.Fprintln(os.Stdout, "stdout text")
		fmt.Fprintln(os.Stderr, "stderr text")
	case "large-stdout":
		fmt.Fprint(os.Stdout, strings.Repeat("o", 4096))
		fmt.Fprint(os.Stderr, "small")
	case "large-stderr":
		fmt.Fprint(os.Stdout, "small")
		fmt.Fprint(os.Stderr, strings.Repeat("e", 4096))
	case "args":
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(18)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": args, "cwd": cwd})
	case "touch":
		if len(args) != 1 || os.WriteFile(args[0], []byte("launched"), 0o600) != nil {
			os.Exit(20)
		}
	default:
		os.Exit(19)
	}
	os.Exit(0)
}
