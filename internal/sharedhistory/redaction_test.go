package sharedhistory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoldenRedactionCorporaPass(t *testing.T) {
	report, err := reviewGoldenRedactionCorpora()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		for _, result := range report.Cases {
			if result.Outcome == "false_positive" || result.Outcome == "false_negative" {
				t.Logf("%s: %s via %v", result.ID, result.Outcome, result.Detectors)
			}
		}
		t.Fatalf("golden corpus = %#v", report)
	}
	if report.TruePositives == 0 || report.TrueNegatives == 0 {
		t.Fatalf("golden corpus did not exercise both decisions: %#v", report)
	}
	for _, result := range report.Cases {
		if result.ID == "datadog-known-rule" && slices.Contains(result.Detectors, "known_secret") {
			return
		}
	}
	t.Fatal("golden corpus did not exercise the Betterleaks detector")
}

func TestRedactionPipelineMergesOverlappingFindings(t *testing.T) {
	value := "token=github_pat_11AA22bb33CC44dd55EE66ff77GG88hh"
	result := defaultSecretPipeline.Redact(value)
	if result.text != "[REDACTED]" {
		t.Fatalf("redacted text = %q", result.text)
	}
	if result.counts["provider_token"] == 0 || result.counts["credential_value"] == 0 {
		t.Fatalf("detector counts = %#v", result.counts)
	}
}

func TestRedactionReviewSeparatesFalsePositivesAndFalseNegatives(t *testing.T) {
	corpus := strings.Join([]string{
		`{"id":"expected-fp","text":"password=hunter2","expect":"allow"}`,
		`{"id":"expected-fn","text":"ordinary sentence","expect":"redact"}`,
	}, "\n")
	report, err := ReviewRedactionCorpus(strings.NewReader(corpus))
	if err != nil {
		t.Fatal(err)
	}
	if report.FalsePositives != 1 || report.FalseNegatives != 1 || report.Passed() {
		t.Fatalf("review report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceText := range []string{"password=hunter2", "ordinary sentence"} {
		if strings.Contains(string(encoded), sourceText) {
			t.Fatalf("review diagnostics echoed source text %q: %s", sourceText, encoded)
		}
	}
}

func TestDiagnoseRedactionReportsPolicyAndGoldenCoverage(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := DiagnoseRedaction(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostics.Configured || diagnostics.ConfiguredScanner != ScannerVersion || diagnostics.MigrationRequired || diagnostics.Approved {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if len(diagnostics.Detectors) < 6 || !diagnostics.GoldenCorpus.Passed() {
		t.Fatalf("diagnostic coverage = %#v", diagnostics)
	}
	if _, err := New(repo).Status(context.Background()); err != nil {
		t.Fatalf("diagnostics altered policy readability: %v", err)
	}
}

func TestValidRedactionReasonBoundsDetectorMetadata(t *testing.T) {
	for _, reason := range []string{"path_full", "workspace_path", "secret", "secret:high_entropy", "secret:known-secret"} {
		if !validRedactionReason(reason) {
			t.Errorf("valid redaction reason rejected: %q", reason)
		}
	}
	for _, reason := range []string{"", "high_entropy", "secret:", "secret:bad:value", "secret:escape\x1b"} {
		if validRedactionReason(reason) {
			t.Errorf("invalid redaction reason accepted: %q", reason)
		}
	}
}
