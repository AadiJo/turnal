package sharedhistory

import (
	"context"
	"encoding/json"
	"os"
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

func TestRedactionIgnoresSourceAllowDirectives(t *testing.T) {
	secret := "key-" + strings.Repeat("0123456789abcdef", 2)
	for _, directive := range []string{"gitleaks:allow", "betterleaks:allow"} {
		t.Run(directive, func(t *testing.T) {
			value := "mailgun = " + secret + " # " + directive
			result := newSecretPipeline().Redact(value)
			if strings.Contains(result.text, secret) || result.counts["known_secret"] == 0 {
				t.Fatalf("source directive bypassed redaction: %#v", result)
			}
		})
	}
}

func TestSanitizeTextRedactsCompleteQuotedCredentials(t *testing.T) {
	for _, quoted := range []string{
		`"abc\"private-suffix"`,
		`'abc\'private-suffix'`,
		`"abc""private-suffix"`,
		`'abc''private-suffix'`,
		`"changeme\"private-suffix"`,
		`'changeme''private-suffix'`,
		`"abc\\\"private-suffix"`,
	} {
		for _, prefix := range []string{
			`password=`,
			`"password":`,
			`host=db user=app password=`,
			`Server=db;User ID=app;Password=`,
		} {
			input := prefix + quoted
			t.Run(input, func(t *testing.T) {
				result := sanitizeText("", input, DefaultFieldLimit, &Truncations{})
				if strings.Contains(result.Text, "private-suffix") || !result.SecretRedacted {
					t.Fatalf("credential suffix survived publication: %#v", result)
				}
				if strings.HasPrefix(prefix, "host=") || strings.HasPrefix(prefix, "Server=") {
					if result.Text != redactionReplacement || result.SecretDetections["connection_string"] == 0 {
						t.Fatalf("connection string was not completely redacted: %#v", result)
					}
				}
			})
		}
	}
}

func TestBetterleaksRepeatedSecretsProduceOneFindingPerOccurrence(t *testing.T) {
	const occurrences = 1000
	secret := "key-" + strings.Repeat("0123456789abcdef", 2)
	input := strings.Repeat("mailgun = "+secret+"\n", occurrences)
	detector := &betterleaksDetector{}
	findings := detector.Detect(input)
	if len(findings) != occurrences {
		t.Fatalf("got %d findings for %d occurrences", len(findings), occurrences)
	}
	for _, finding := range findings {
		if input[finding.start:finding.end] != secret {
			t.Fatalf("finding did not cover the complete secret: %#v", finding)
		}
	}
}

func TestSanitizeTextRedactsPunctuationInCredentials(t *testing.T) {
	for _, input := range []string{
		`DB_PASSWORD=pa}ss!word`,
		`api_key=ab]cd`,
		`mysql://root:S3cr3t{x}@10.0.0.5:3306/app`,
		`postgres://admin:pa%ss@db.internal/app`,
		`https://user:example@real-secret@example.invalid/path`,
	} {
		t.Run(input, func(t *testing.T) {
			result := sanitizeText("", input, DefaultFieldLimit, &Truncations{})
			_, value, assignment := strings.Cut(input, "=")
			want := redactionReplacement
			if assignment {
				want = strings.TrimSuffix(input, value) + redactionReplacement
			}
			if result.Text != want || !result.SecretRedacted {
				t.Fatalf("credential punctuation escaped redaction: %#v, want %q", result, want)
			}
		})
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

func TestDiagnoseRedactionDoesNotCreateSharingState(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	directory := filepath.Dir(policyPath(repo))
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("sharing state exists before diagnosis: %v", err)
	}
	diagnostics, err := DiagnoseRedaction(repo)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Configured {
		t.Fatal("diagnosis reported a policy in an unconfigured store")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("diagnosis created sharing state: %v", err)
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
