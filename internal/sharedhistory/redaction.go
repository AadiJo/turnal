package sharedhistory

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/betterleaks/betterleaks/detect"
)

const (
	redactionReplacement  = "[REDACTED]"
	maxReviewCaseBytes    = DefaultFieldLimit
	maxReviewCorpusCases  = 10_000
	redactionEntropyFloor = 4.5
)

// RedactionDetectorInfo describes one independently testable detector in the
// shared-history secret pipeline.
type RedactionDetectorInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// RedactionDiagnostics reports the compiled scanner, its detector stages, the
// golden-corpus result, and whether a configured repository needs migration.
type RedactionDiagnostics struct {
	ScannerVersion    string                  `json:"scanner_version"`
	Detectors         []RedactionDetectorInfo `json:"detectors"`
	GoldenCorpus      RedactionReviewReport   `json:"golden_corpus"`
	Configured        bool                    `json:"configured"`
	ConfiguredScanner string                  `json:"configured_scanner,omitempty"`
	PolicyHash        string                  `json:"policy_hash,omitempty"`
	Approved          bool                    `json:"approved"`
	MigrationRequired bool                    `json:"migration_required"`
}

// RedactionReviewCase is one expected scanner decision in a JSONL review
// corpus. Expect must be either "redact" or "allow".
type RedactionReviewCase struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Expect string `json:"expect"`
}

// RedactionReviewResult intentionally omits the source text so diagnostics do
// not echo a candidate secret back into terminals, logs, or CI artifacts.
type RedactionReviewResult struct {
	ID        string   `json:"id"`
	Expect    string   `json:"expect"`
	Actual    string   `json:"actual"`
	Outcome   string   `json:"outcome"`
	Detectors []string `json:"detectors,omitempty"`
}

// RedactionReviewReport separates false positives from false negatives so a
// detector change cannot hide one class of regression behind an aggregate.
type RedactionReviewReport struct {
	ScannerVersion string                  `json:"scanner_version"`
	Total          int                     `json:"total"`
	TruePositives  int                     `json:"true_positives"`
	TrueNegatives  int                     `json:"true_negatives"`
	FalsePositives int                     `json:"false_positives"`
	FalseNegatives int                     `json:"false_negatives"`
	Cases          []RedactionReviewResult `json:"cases"`
}

func (report RedactionReviewReport) Passed() bool {
	return report.FalsePositives == 0 && report.FalseNegatives == 0
}

type secretFinding struct {
	start     int
	end       int
	detector  string
	fullField bool
}

type secretDetectionResult struct {
	text   string
	counts map[string]int
}

type secretDetector interface {
	Info() RedactionDetectorInfo
	Detect(string) []secretFinding
}

type secretPipeline struct {
	detectors []secretDetector
}

var defaultSecretPipeline = newSecretPipeline()

func newSecretPipeline() *secretPipeline {
	return &secretPipeline{detectors: []secretDetector{
		entropyDetector{},
		&betterleaksDetector{},
		knownSecretDetector{},
		credentialedURIDetector{},
		connectionStringDetector{},
		credentialAssignmentDetector{},
	}}
}

func (pipeline *secretPipeline) Info() []RedactionDetectorInfo {
	result := make([]RedactionDetectorInfo, 0, len(pipeline.detectors))
	for _, detector := range pipeline.detectors {
		result = append(result, detector.Info())
	}
	return result
}

func (pipeline *secretPipeline) Detect(value string) []secretFinding {
	var findings []secretFinding
	seen := make(map[secretFinding]struct{})
	for _, detector := range pipeline.detectors {
		for _, finding := range detector.Detect(value) {
			if finding.detector == "" {
				finding.detector = detector.Info().ID
			}
			if finding.fullField {
				finding.start, finding.end = 0, len(value)
			}
			if finding.start < 0 || finding.end <= finding.start || finding.end > len(value) {
				continue
			}
			if _, duplicate := seen[finding]; duplicate {
				continue
			}
			seen[finding] = struct{}{}
			findings = append(findings, finding)
		}
	}
	return findings
}

func (pipeline *secretPipeline) Redact(value string) secretDetectionResult {
	findings := pipeline.Detect(value)
	if len(findings) == 0 {
		return secretDetectionResult{text: value}
	}
	counts := make(map[string]int)
	fullField := false
	for _, finding := range findings {
		counts[finding.detector]++
		if finding.fullField {
			fullField = true
		}
	}
	if fullField {
		return secretDetectionResult{text: redactionReplacement, counts: counts}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].start != findings[j].start {
			return findings[i].start < findings[j].start
		}
		if findings[i].end != findings[j].end {
			return findings[i].end > findings[j].end
		}
		return findings[i].detector < findings[j].detector
	})
	merged := []secretFinding{findings[0]}
	for _, finding := range findings[1:] {
		last := &merged[len(merged)-1]
		if finding.start <= last.end {
			if finding.end > last.end {
				last.end = finding.end
			}
			continue
		}
		merged = append(merged, finding)
	}

	var redacted strings.Builder
	previous := 0
	for _, finding := range merged {
		redacted.WriteString(value[previous:finding.start])
		redacted.WriteString(redactionReplacement)
		previous = finding.end
	}
	redacted.WriteString(value[previous:])
	return secretDetectionResult{text: redacted.String(), counts: counts}
}

type entropyDetector struct{}

var entropyCandidatePattern = regexp.MustCompile(`[A-Za-z0-9+_=-]{10,}`)

func (entropyDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "high_entropy", Description: "Unstructured token-shaped values with Shannon entropy above 4.5"}
}

func (entropyDetector) Detect(value string) []secretFinding {
	var findings []secretFinding
	for _, location := range entropyCandidatePattern.FindAllStringIndex(value, -1) {
		candidate := value[location[0]:location[1]]
		if isPlaceholderSecretValue(candidate) || isPublicDigest(candidate) || isStructuralIdentifier(candidate) {
			continue
		}
		if shannonEntropy(candidate) > redactionEntropyFloor {
			findings = append(findings, secretFinding{start: location[0], end: location[1]})
		}
	}
	return findings
}

var structuralIdentifierPattern = regexp.MustCompile(`(?i)^(?:bundle|case|checkpoint|repo|request|session|store|stream|task|trace|turn|worktree)[_-][a-z0-9_-]+$`)

func isStructuralIdentifier(value string) bool {
	return structuralIdentifierPattern.MatchString(value)
}

func shannonEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := make(map[byte]int)
	for index := range len(value) {
		counts[value[index]]++
	}
	length := float64(len(value))
	var entropy float64
	for _, count := range counts {
		probability := float64(count) / length
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}

func isPublicDigest(value string) bool {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"sha256-", "sha384-", "sha512-", "integrity-"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

type betterleaksDetector struct {
	once     sync.Once
	detector *detect.Detector
	err      error
}

func (*betterleaksDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "known_secret", Description: "Betterleaks rules for known vendor credentials and private keys"}
}

func (detector *betterleaksDetector) Detect(value string) []secretFinding {
	detector.once.Do(func() {
		detector.detector, detector.err = detect.NewDetectorDefaultConfig()
	})
	if detector.err != nil || detector.detector == nil {
		// A scanner initialization failure must reduce disclosure, not silently
		// remove an expected protection layer.
		return []secretFinding{{detector: "known_secret_unavailable", fullField: true}}
	}
	var findings []secretFinding
	for _, finding := range detector.detector.DetectString(value) {
		if finding.Secret == "" || isPlaceholderSecretValue(finding.Secret) {
			continue
		}
		searchFrom := 0
		for searchFrom < len(value) {
			index := strings.Index(value[searchFrom:], finding.Secret)
			if index < 0 {
				break
			}
			start := searchFrom + index
			findings = append(findings, secretFinding{start: start, end: start + len(finding.Secret)})
			searchFrom = start + len(finding.Secret)
		}
	}
	return findings
}

type secretPattern struct {
	pattern   *regexp.Regexp
	fullField bool
}

type knownSecretDetector struct{}

var knownSecretPatterns = []secretPattern{
	{pattern: regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]{20,}|github_pat_[a-z0-9_]{20,}|gl(?:pat|ptt|rt|soat|ft|cbt)-[a-z0-9_-]{20,}|npm_[a-z0-9]{30,}|pypi-[a-z0-9_-]{16,}|hf_[a-z0-9]{20,}|sk-[a-z0-9_-]{20,}|[sr]k_(?:live|test)_[a-z0-9]{16,}|(?:akia|asia|abia|acca)[0-9a-z]{16}|xox[baprs]-[a-z0-9-]{10,}|aiza[0-9a-z_-]{20,})\b`)},
	{pattern: regexp.MustCompile(`(?i)\bhttps://hooks\.slack\.com/services/[a-z0-9/_-]{10,}`)},
	{pattern: regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]{16,}=*`)},
	{pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)},
	{pattern: regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`), fullField: true},
}

func (knownSecretDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "provider_token", Description: "Deterministic provider tokens, bearer values, JWTs, webhooks, and private keys"}
}

func (knownSecretDetector) Detect(value string) []secretFinding {
	var findings []secretFinding
	for _, candidate := range knownSecretPatterns {
		for _, location := range candidate.pattern.FindAllStringIndex(value, -1) {
			findings = append(findings, secretFinding{start: location[0], end: location[1], fullField: candidate.fullField})
		}
	}
	return findings
}

type credentialedURIDetector struct{}

var credentialedURIPattern = regexp.MustCompile("(?i)\\b[a-z][a-z0-9+.-]{1,31}://[^\\s/?#@\\\"'<>:]*:[^\\s/?#@\\\"'<>]+@[^\\s\\\"'<>]+")

func (credentialedURIDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "credentialed_uri", Description: "URLs containing a non-placeholder userinfo password"}
}

func (credentialedURIDetector) Detect(value string) []secretFinding {
	var findings []secretFinding
	for _, location := range credentialedURIPattern.FindAllStringIndex(value, -1) {
		candidate := strings.TrimRight(value[location[0]:location[1]], ".,;:!?)]}")
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.User == nil {
			continue
		}
		password, present := parsed.User.Password()
		if !present || isPlaceholderSecretValue(password) {
			continue
		}
		findings = append(findings, secretFinding{start: location[0], end: location[0] + len(candidate)})
	}
	return findings
}

type connectionStringDetector struct{}

var (
	jdbcConnectionPattern      = regexp.MustCompile("(?i)\\bjdbc:[^\\s\\\"'<>`]+")
	databaseURLPattern         = regexp.MustCompile("(?i)\\b(?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\\+srv)?|redis)://[^\\s\\\"'<>`]+")
	keywordDSNPattern          = regexp.MustCompile(`(?i)\b[a-z_][a-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']+)(?:\s+[a-z_][a-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']+)){2,}`)
	semicolonConnectionPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9 _-]*=(?:\{[^}]*\}|"[^"]*"|'[^']*'|[^=;"'\s]+)(?:;[a-z][a-z0-9 _-]*=(?:\{[^}]*\}|"[^"]*"|'[^']*'|[^=;"'\s]+)){2,}`)
	passwordValuePattern       = regexp.MustCompile(`(?i)(?:^|[?&;\s])(?:password|passwd|pwd)\s*=\s*("[^"]*"|'[^']*'|[^&;\s"']+)`)
	keywordHostPattern         = regexp.MustCompile(`(?i)(?:^|\s)host\s*=`)
	keywordUserPattern         = regexp.MustCompile(`(?i)(?:^|\s)user\s*=`)
	semicolonServerPattern     = regexp.MustCompile(`(?i)(?:^|;)\s*(?:server|data source|datasource|addr|address|network address)\s*=`)
	semicolonUserPattern       = regexp.MustCompile(`(?i)(?:^|;)\s*(?:user id|userid|user|uid)\s*=`)
)

func (connectionStringDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "connection_string", Description: "JDBC, database URL, keyword DSN, and SQL Server connection strings"}
}

func (connectionStringDetector) Detect(value string) []secretFinding {
	var findings []secretFinding
	appendMatches := func(pattern *regexp.Regexp, hasSecret func(string) bool) {
		for _, location := range pattern.FindAllStringIndex(value, -1) {
			end := trimConnectionStringEnd(value, location[0], location[1])
			if end > location[0] && hasSecret(value[location[0]:end]) {
				findings = append(findings, secretFinding{start: location[0], end: end})
			}
		}
	}
	appendMatches(jdbcConnectionPattern, hasPasswordAssignment)
	appendMatches(databaseURLPattern, hasDatabaseURLPassword)
	appendMatches(keywordDSNPattern, func(candidate string) bool {
		return keywordHostPattern.MatchString(candidate) && keywordUserPattern.MatchString(candidate) && hasPasswordAssignment(candidate)
	})
	appendMatches(semicolonConnectionPattern, func(candidate string) bool {
		return semicolonServerPattern.MatchString(candidate) && semicolonUserPattern.MatchString(candidate) && hasPasswordAssignment(candidate)
	})
	return findings
}

func trimConnectionStringEnd(value string, start, end int) int {
	for end > start && strings.ContainsRune(".,;:!?)]", rune(value[end-1])) {
		end--
	}
	return end
}

func hasDatabaseURLPassword(candidate string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	if parsed.User != nil {
		if password, present := parsed.User.Password(); present && !isPlaceholderSecretValue(password) {
			return true
		}
	}
	for key, values := range parsed.Query() {
		if !strings.EqualFold(key, "password") && !strings.EqualFold(key, "passwd") && !strings.EqualFold(key, "pwd") {
			continue
		}
		for _, value := range values {
			if value != "" && !isPlaceholderSecretValue(value) {
				return true
			}
		}
	}
	return false
}

func hasPasswordAssignment(candidate string) bool {
	for _, location := range passwordValuePattern.FindAllStringSubmatchIndex(candidate, -1) {
		if len(location) < 4 || location[2] < 0 || location[3] < 0 {
			continue
		}
		start, end := unquoteRange(candidate, location[2], location[3])
		if end > start && !isPlaceholderSecretValue(candidate[start:end]) {
			return true
		}
	}
	return false
}

type credentialAssignmentDetector struct{}

var credentialAssignmentPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:[a-z0-9_]*(?:password|passwd|pwd|secret|token|api[_-]?key)[a-z0-9_]*)["']?\s*[:=]\s*("[^"]*"|'[^']*'|\$\{[^}]+\}|[^\s,;}\]]+)`)

func (credentialAssignmentDetector) Info() RedactionDetectorInfo {
	return RedactionDetectorInfo{ID: "credential_value", Description: "Non-placeholder values assigned to credential-shaped keys"}
}

func (credentialAssignmentDetector) Detect(value string) []secretFinding {
	var findings []secretFinding
	for _, location := range credentialAssignmentPattern.FindAllStringSubmatchIndex(value, -1) {
		if len(location) < 4 {
			continue
		}
		start, end := location[len(location)-2], location[len(location)-1]
		if start < 0 || end <= start {
			continue
		}
		start, end = unquoteRange(value, start, end)
		if start == end || isPlaceholderSecretValue(value[start:end]) {
			continue
		}
		findings = append(findings, secretFinding{start: start, end: end})
	}
	return findings
}

func unquoteRange(value string, start, end int) (int, int) {
	if end-start < 2 {
		return start, end
	}
	if (value[start] == '"' && value[end-1] == '"') || (value[start] == '\'' && value[end-1] == '\'') {
		return start + 1, end - 1
	}
	return start, end
}

var placeholderSecretValues = map[string]struct{}{
	"redacted": {}, "[redacted]": {}, "<redacted>": {}, "changeme": {}, "example": {},
	"placeholder": {}, "your_password": {}, "your_db_password": {}, "your_secret": {}, "secret_here": {},
}

var bracketedPlaceholderPattern = regexp.MustCompile(`^[a-z][a-z_-]*$`)

func isPlaceholderSecretValue(value string) bool {
	trimmed := strings.Trim(strings.TrimSpace(value), `"'`)
	if trimmed == "" {
		return true
	}
	normalized := strings.ToLower(trimmed)
	if _, ok := placeholderSecretValues[normalized]; ok {
		return true
	}
	if strings.HasPrefix(normalized, "${") && strings.HasSuffix(normalized, "}") {
		return true
	}
	if len(normalized) >= 5 && normalized[0] == '<' && normalized[len(normalized)-1] == '>' {
		interior := normalized[1 : len(normalized)-1]
		if bracketedPlaceholderPattern.MatchString(interior) {
			return true
		}
	}
	if len(normalized) >= 3 && strings.Count(normalized, normalized[:1]) == len(normalized) && strings.Contains("*x.-", normalized[:1]) {
		return true
	}
	return false
}

var reviewCaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// ReviewRedactionCorpus evaluates a strict JSONL corpus without returning or
// printing its source text. Malformed and duplicate cases fail the review.
func ReviewRedactionCorpus(reader io.Reader) (RedactionReviewReport, error) {
	report := RedactionReviewReport{ScannerVersion: ScannerVersion}
	if reader == nil {
		return report, fmt.Errorf("redaction review corpus is required")
	}
	scanner := bufio.NewScanner(reader)
	// JSON escaping can expand a valid decoded field well beyond its 64 KiB
	// publication limit, so the line bound leaves room for the encoded form.
	scanner.Buffer(make([]byte, 64<<10), 8*maxReviewCaseBytes)
	seen := make(map[string]struct{})
	for line := 1; scanner.Scan(); line++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if report.Total >= maxReviewCorpusCases {
			return report, fmt.Errorf("redaction review corpus exceeds %d cases", maxReviewCorpusCases)
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var reviewCase RedactionReviewCase
		if err := decoder.Decode(&reviewCase); err != nil {
			return report, fmt.Errorf("parse redaction review case on line %d: %w", line, err)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return report, fmt.Errorf("parse redaction review case on line %d: %w", line, err)
		}
		if !reviewCaseIDPattern.MatchString(reviewCase.ID) {
			return report, fmt.Errorf("redaction review case on line %d has invalid id %q", line, reviewCase.ID)
		}
		if _, duplicate := seen[reviewCase.ID]; duplicate {
			return report, fmt.Errorf("redaction review case id %q is duplicated", reviewCase.ID)
		}
		seen[reviewCase.ID] = struct{}{}
		if reviewCase.Expect != "redact" && reviewCase.Expect != "allow" {
			return report, fmt.Errorf("redaction review case %q expects %q; expected redact or allow", reviewCase.ID, reviewCase.Expect)
		}
		if len(reviewCase.Text) > maxReviewCaseBytes {
			return report, fmt.Errorf("redaction review case %q exceeds %d bytes", reviewCase.ID, maxReviewCaseBytes)
		}
		appendReviewResult(&report, reviewCase)
	}
	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("read redaction review corpus: %w", err)
	}
	if report.Total == 0 {
		return report, fmt.Errorf("redaction review corpus contains no cases")
	}
	return report, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains more than one JSON value")
		}
		return err
	}
	return nil
}

func appendReviewResult(report *RedactionReviewReport, reviewCase RedactionReviewCase) {
	findings := defaultSecretPipeline.Detect(reviewCase.Text)
	actual := "allow"
	if len(findings) > 0 {
		actual = "redact"
	}
	detectorSet := make(map[string]struct{})
	for _, finding := range findings {
		detectorSet[finding.detector] = struct{}{}
	}
	detectors := make([]string, 0, len(detectorSet))
	for detector := range detectorSet {
		detectors = append(detectors, detector)
	}
	sort.Strings(detectors)
	outcome := "true_negative"
	switch {
	case reviewCase.Expect == "redact" && actual == "redact":
		report.TruePositives++
		outcome = "true_positive"
	case reviewCase.Expect == "allow" && actual == "allow":
		report.TrueNegatives++
	case reviewCase.Expect == "allow":
		report.FalsePositives++
		outcome = "false_positive"
	default:
		report.FalseNegatives++
		outcome = "false_negative"
	}
	report.Total++
	report.Cases = append(report.Cases, RedactionReviewResult{ID: reviewCase.ID, Expect: reviewCase.Expect, Actual: actual, Outcome: outcome, Detectors: detectors})
}

func mergeReviewReports(reports ...RedactionReviewReport) RedactionReviewReport {
	merged := RedactionReviewReport{ScannerVersion: ScannerVersion}
	for _, report := range reports {
		merged.Total += report.Total
		merged.TruePositives += report.TruePositives
		merged.TrueNegatives += report.TrueNegatives
		merged.FalsePositives += report.FalsePositives
		merged.FalseNegatives += report.FalseNegatives
		merged.Cases = append(merged.Cases, report.Cases...)
	}
	return merged
}

//go:embed testdata/redaction/*.jsonl
var goldenRedactionCorpora embed.FS

func reviewGoldenRedactionCorpora() (RedactionReviewReport, error) {
	paths := []string{"testdata/redaction/leaks.jsonl", "testdata/redaction/safe.jsonl"}
	reports := make([]RedactionReviewReport, 0, len(paths))
	for _, path := range paths {
		file, err := goldenRedactionCorpora.Open(path)
		if err != nil {
			return RedactionReviewReport{}, fmt.Errorf("open golden redaction corpus %s: %w", path, err)
		}
		report, reviewErr := ReviewRedactionCorpus(file)
		closeErr := file.Close()
		if reviewErr != nil {
			return RedactionReviewReport{}, fmt.Errorf("review golden redaction corpus %s: %w", path, reviewErr)
		}
		if closeErr != nil {
			return RedactionReviewReport{}, fmt.Errorf("close golden redaction corpus %s: %w", path, closeErr)
		}
		reports = append(reports, report)
	}
	return mergeReviewReports(reports...), nil
}

// DiagnoseRedaction is local and read-only. It runs the embedded golden corpus
// and inspects policy state without contacting a shared-history remote.
func DiagnoseRedaction(repo *checkpoint.Repo) (RedactionDiagnostics, error) {
	if repo == nil {
		return RedactionDiagnostics{}, fmt.Errorf("redaction diagnostics require checkpoint repo")
	}
	golden, err := reviewGoldenRedactionCorpora()
	if err != nil {
		return RedactionDiagnostics{}, err
	}
	diagnostics := RedactionDiagnostics{ScannerVersion: ScannerVersion, Detectors: defaultSecretPipeline.Info(), GoldenCorpus: golden}
	return withSharedHistoryLock(repo, "diagnose shared history redaction", func() (RedactionDiagnostics, error) {
		if _, err := os.Lstat(policyPath(repo)); err != nil {
			if os.IsNotExist(err) {
				return diagnostics, nil
			}
			return RedactionDiagnostics{}, err
		}
		policy, err := loadPolicyForUpdate(repo)
		if err != nil {
			return RedactionDiagnostics{}, err
		}
		digest, err := policyHash(policy)
		if err != nil {
			return RedactionDiagnostics{}, err
		}
		diagnostics.Configured = true
		diagnostics.ConfiguredScanner = policy.ScannerVersion
		diagnostics.PolicyHash = digest
		diagnostics.Approved = policy.ApprovedHash == digest
		diagnostics.MigrationRequired = policy.ScannerVersion != ScannerVersion || policy.AllowlistVersion != AllowlistVersion
		return diagnostics, nil
	})
}
