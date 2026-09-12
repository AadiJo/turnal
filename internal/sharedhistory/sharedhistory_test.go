package sharedhistory

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestPreviewProjectsOnlyAllowlistedRedactedContext(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	remote := filepath.Join(t.TempDir(), "history.git")
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"ghp_0123456789abcdefghijklmnop",
		repo.WorkspaceRoot.String(),
		"/etc/passwd",
		"cat private.txt",
		"raw tool result",
		"provider-private-payload",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("preview contains forbidden source material %q: %s", forbidden, text)
		}
	}
	for _, want := range []string{"[REDACTED]", "$WORKSPACE", EvidencePublisherClaim} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview does not contain %q: %s", want, text)
		}
	}
	if !plan.ApprovalRequired {
		t.Fatal("new policy did not require explicit approval")
	}
	if plan.Manifest.Omissions["tool_input"] != 1 || plan.Manifest.Omissions["tool_output"] != 1 || plan.Manifest.Omissions["event_type:adapter.raw"] != 1 {
		t.Fatalf("omissions = %#v", plan.Manifest.Omissions)
	}
	if plan.Manifest.Redactions["path_full"] == 0 || plan.Manifest.Redactions["workspace_path"] == 0 || plan.Manifest.Redactions["secret"] == 0 {
		t.Fatalf("redactions = %#v", plan.Manifest.Redactions)
	}
	if plan.Manifest.Redactions["secret:provider_token"] == 0 {
		t.Fatalf("redactions do not identify the detector: %#v", plan.Manifest.Redactions)
	}
	if err := verifyStoredBundle(repo.RepoID, StoredBundle{Manifest: plan.Manifest, Events: plan.Events, PublicKey: mustDevice(t, repo).PublicKey}); err != nil {
		t.Fatalf("verify preview bundle: %v", err)
	}
}

func TestProjectionBlocksSecretLikeManifestIdentifiers(t *testing.T) {
	sessionID, err := primitives.ParseSessionID("ghp_0123456789abcdefghijklmnop")
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildBundle(nil, deviceIdentity{}, policyFile{}, "", turnSource{
		Stream:        eventlog.DurableStream{SessionID: sessionID},
		WorkspaceRoot: "/workspace",
	})
	if err == nil || !strings.Contains(err.Error(), "secret-like") {
		t.Fatalf("secret-like session id error = %v", err)
	}
}

func TestSanitizeTextDetectsSupportedSecretFamilies(t *testing.T) {
	tests := map[string]string{
		"github fine grained": "github_pat_11AA22bb33CC44dd55EE66ff77GG88hh",
		"gitlab":              "glpat-11AA22bb33CC44dd55EE66ff77GG88hh",
		"npm":                 "npm_11AA22bb33CC44dd55EE66ff77GG88hh99",
		"pypi":                "pypi-11AA22bb33CC44dd55EE66ff77GG88hh",
		"hugging face":        "hf_11AA22bb33CC44dd55EE66ff77GG88hh",
		"slack":               strings.Join([]string{"xox", "b-123456789012-abcdefghijklmnop"}, ""),
		"slack webhook":       strings.Join([]string{"https://hooks.", "slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"}, ""),
		"stripe":              strings.Join([]string{"sk_", "live_11AA22bb33CC44dd55EE66ff77GG88hh"}, ""),
		"google":              "AIzaSyA1234567890abcdefghijklmnop",
		"aws assignment":      "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"bearer":              "Bearer abcdefghijklmnopqrstuvwxyz.123456789",
		"jwt":                 "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmno",
		"url userinfo":        "https://private-user:private-password@example.invalid/path",
		"private key":         "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-material",
	}
	for name, secret := range tests {
		t.Run(name, func(t *testing.T) {
			truncations := Truncations{}
			result := sanitizeText("/workspace", "before "+secret+" after", DefaultFieldLimit, &truncations)
			if !result.SecretRedacted || strings.Contains(result.Text, secret) || !strings.Contains(result.Text, "[REDACTED]") {
				t.Fatalf("sanitizeText(%q) = %#v", secret, result)
			}
		})
	}
}

func TestSanitizeTextDoesNotRedactBenignIdentifiers(t *testing.T) {
	truncations := Truncations{}
	value := "token bucket, secret sauce, github_pattern, and bearer capacity"
	result := sanitizeText("/workspace", value, DefaultFieldLimit, &truncations)
	if result.Redacted || result.Text != value {
		t.Fatalf("benign text was redacted: %#v", result)
	}
}

func TestManifestMetadataReasonsUseTerminalSafeKeys(t *testing.T) {
	for _, reason := range []string{"tool_input", "event_type:adapter.raw", "invalid-checkpoint+v1"} {
		if !validMetadataReason(reason) {
			t.Errorf("valid metadata reason rejected: %q", reason)
		}
	}
	for _, reason := range []string{"", "escape\x1b[31m", "bidi\u202e", "contains space", "UPPER"} {
		if validMetadataReason(reason) {
			t.Errorf("unsafe metadata reason accepted: %q", reason)
		}
	}
}

func FuzzSanitizeTextNeverLeaksWorkspaceRoot(f *testing.F) {
	for _, seed := range []string{
		"/private/team workspace/file.txt",
		"quoted '/private/team workspace/file.txt'",
		"file:///private/team workspace/file.txt",
		"prefix/private/team workspace-sibling",
		"token=ghp_0123456789abcdefghijklmnop",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		truncations := Truncations{}
		result := sanitizeText("/private/team workspace", value, DefaultFieldLimit, &truncations)
		if strings.Contains(strings.ToLower(result.Text), "/private/team workspace") {
			t.Fatalf("workspace root leaked from %q as %q", value, result.Text)
		}
		if strings.Contains(result.Text, workspaceProjectionMarker) {
			t.Fatalf("internal workspace marker leaked from %q as %q", value, result.Text)
		}
	})
}

func TestLinkedWorktreeProjectionUsesSourceWorkspaceRoot(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	parent := t.TempDir()
	mainPath := filepath.Join(parent, "main")
	linkedPath := filepath.Join(parent, "linked secret project")
	if err := os.MkdirAll(mainPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, mainPath, "init")
	runTestGit(t, mainPath, "config", "user.email", "shared-history@example.invalid")
	runTestGit(t, mainPath, "config", "user.name", "Shared History Test")
	if err := os.WriteFile(filepath.Join(mainPath, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, mainPath, "add", "tracked.txt")
	runTestGit(t, mainPath, "commit", "-m", "initial")
	mainRoot, err := primitives.ParseWorkspaceRoot(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	mainRepo, err := checkpoint.Init(mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, mainPath, "worktree", "add", "-b", "linked-privacy-test", linkedPath)
	linkedRoot, err := primitives.ParseWorkspaceRoot(linkedPath)
	if err != nil {
		t.Fatal(err)
	}
	linkedRepo, err := checkpoint.Open(linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, turnID := recordSharedHistoryTurn(t, linkedRepo)
	if _, err := Configure(mainRepo, ConfigureOptions{Remote: filepath.Join(parent, "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(mainRepo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan.Events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), linkedPath) || strings.Contains(string(encoded), "secret project") {
		t.Fatalf("linked worktree path escaped projection: %s", encoded)
	}
	if !strings.Contains(string(encoded), "$WORKSPACE") {
		t.Fatalf("linked worktree root was not normalized: %s", encoded)
	}
}

func TestSanitizeTextNormalizesMacOSPrivateWorkspaceAliases(t *testing.T) {
	tests := []struct {
		name          string
		workspaceRoot string
		text          string
	}{
		{
			name:          "registered private path",
			workspaceRoot: "/private/var/folders/demo/linked secret project",
			text:          "Inspect /var/folders/demo/linked secret project/private.txt",
		},
		{
			name:          "captured private path",
			workspaceRoot: "/var/folders/demo/linked secret project",
			text:          "Inspect /private/var/folders/demo/linked secret project/private.txt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncations := Truncations{}
			got := sanitizeText(test.workspaceRoot, test.text, DefaultFieldLimit, &truncations)
			if got.Text != "Inspect $WORKSPACE/private.txt" {
				t.Fatalf("sanitized text = %q", got.Text)
			}
			if !got.Redacted {
				t.Fatal("workspace alias was not marked redacted")
			}
		})
	}
}

func TestSanitizeTextDoesNotPartiallyReplaceSiblingPaths(t *testing.T) {
	tests := []struct {
		name          string
		workspaceRoot string
		text          string
	}{
		{
			name:          "unix sibling",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /home/alice/project-secret/private.txt",
		},
		{
			name:          "windows sibling",
			workspaceRoot: `C:\Users\alice\project`,
			text:          `Inspect C:\Users\alice\project-secret\private.txt`,
		},
		{
			name:          "sibling with spaces",
			workspaceRoot: "/home/alice/project",
			text:          `Inspect "/home/alice/project secret/private.txt"`,
		},
		{
			name:          "unquoted sibling with spaces",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /home/alice/project secret/private.txt",
		},
		{
			name:          "punctuated sibling",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /home/alice/project,secret/private.txt",
		},
		{
			name:          "embedded in enclosing path",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /srv/home/alice/project/private.txt",
		},
		{
			name:          "case-insensitive sibling",
			workspaceRoot: "/Users/Alice/Secret Project",
			text:          "Inspect /users/alice/secret project-secret/private.txt",
		},
		{
			name:          "connector sibling without separator",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /home/alice/project and secret",
		},
		{
			name:          "repeated punctuation sibling",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /home/alice/project..private",
		},
		{
			name:          "unquoted enclosing path with spaces",
			workspaceRoot: "/home/alice/project",
			text:          "Inspect /srv /home/alice/project/private.txt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncations := Truncations{}
			got := sanitizeText(test.workspaceRoot, test.text, DefaultFieldLimit, &truncations)
			if got.Text != "[PATH_REDACTED]" || !got.Redacted {
				t.Fatalf("ambiguous path was not redacted as a whole: %#v", got)
			}
		})
	}
}

func TestSanitizeTextRecognizesUnambiguousWorkspacePathBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		workspaceRoot string
		text          string
		want          string
	}{
		{
			name:          "quoted",
			workspaceRoot: "/home/alice/project",
			text:          `See "/home/alice/project", then continue`,
			want:          `See "$WORKSPACE", then continue`,
		},
		{
			name:          "quoted after another path",
			workspaceRoot: "/home/alice/project",
			text:          `Compare /tmp/input with "/home/alice/project/file"`,
			want:          `[PATH_REDACTED]`,
		},
		{
			name:          "quoted before ambiguous path",
			workspaceRoot: "/home/alice/project",
			text:          `Use "/home/alice/project/file" then /opt/Secret Project/private.txt`,
			want:          `[PATH_REDACTED]`,
		},
		{
			name:          "unix trailing separator",
			workspaceRoot: "/home/alice/project/",
			text:          "Inspect /home/alice/project/private.txt",
			want:          "Inspect $WORKSPACE/private.txt",
		},
		{
			name:          "windows trailing separator",
			workspaceRoot: `C:\Users\Alice\Project\`,
			text:          `Inspect C:\Users\Alice\Project\private.txt`,
			want:          `Inspect $WORKSPACE\private.txt`,
		},
		{
			name:          "unix filesystem root",
			workspaceRoot: "/",
			text:          "Inspect /private.txt",
			want:          "Inspect $WORKSPACE/private.txt",
		},
		{
			name:          "unix filesystem root after URL",
			workspaceRoot: "/",
			text:          "URL https://example.com then /private.txt",
			want:          "URL https://example.com then $WORKSPACE/private.txt",
		},
		{
			name:          "unix filesystem root after colon",
			workspaceRoot: "/",
			text:          "path:/private.txt",
			want:          "path:$WORKSPACE/private.txt",
		},
		{
			name:          "unix filesystem root in file URI",
			workspaceRoot: "/",
			text:          "file:///private.txt",
			want:          "file://$WORKSPACE/private.txt",
		},
		{
			name:          "windows drive root",
			workspaceRoot: `C:\`,
			text:          `Inspect C:\private.txt`,
			want:          `Inspect $WORKSPACE\private.txt`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncations := Truncations{}
			got := sanitizeText(test.workspaceRoot, test.text, DefaultFieldLimit, &truncations)
			if got.Text != test.want || !got.Redacted {
				t.Fatalf("sanitized text = %#v, want %q", got, test.want)
			}
		})
	}
}

func TestSanitizeTextFailsClosedOnAmbiguousWorkspaceRootMentions(t *testing.T) {
	for _, text := range []string{
		"Working in /home/alice/project and testing",
		"See /home/alice/project, then continue",
		"See /home/alice/project.",
	} {
		truncations := Truncations{}
		got := sanitizeText("/home/alice/project", text, DefaultFieldLimit, &truncations)
		if got.Text != "[PATH_REDACTED]" || !got.Redacted {
			t.Fatalf("ambiguous root mention was not redacted as a whole: %#v", got)
		}
	}
}

func TestSanitizeTextFailsClosedOnAbsolutePathsWithSpaces(t *testing.T) {
	for _, text := range []string{
		"Inspect /opt/Secret Project/private.txt",
		`Inspect C:\Users\Alice\Secret Project\private.txt`,
	} {
		truncations := Truncations{}
		got := sanitizeText("/workspace", text, DefaultFieldLimit, &truncations)
		if got.Text != "[PATH_REDACTED]" || !got.Redacted {
			t.Fatalf("ambiguous absolute path was not redacted as a whole: %#v", got)
		}
	}
}

func TestSanitizeTextPreservesNestedNormalizedWorkspacePath(t *testing.T) {
	truncations := Truncations{}
	got := sanitizeText("/home/alice/project", "Inspect /home/alice/project/private/nested.txt", DefaultFieldLimit, &truncations)
	if got.Text != "Inspect $WORKSPACE/private/nested.txt" || !got.Redacted {
		t.Fatalf("normalized workspace path was rescrubbed: %#v", got)
	}
}

func TestSanitizeTextRejectsWorkspaceParentTraversal(t *testing.T) {
	for _, test := range []struct {
		workspaceRoot string
		text          string
	}{
		{workspaceRoot: "/home/alice/project", text: "Inspect /home/alice/project/../private-sibling/secret.txt"},
		{workspaceRoot: `C:\Users\Alice\Project`, text: `Inspect C:\Users\Alice\Project\..\private-sibling\secret.txt`},
	} {
		truncations := Truncations{}
		got := sanitizeText(test.workspaceRoot, test.text, DefaultFieldLimit, &truncations)
		if got.Text != "[PATH_REDACTED]" || !got.Redacted {
			t.Fatalf("parent traversal was not redacted as a whole: %#v", got)
		}
	}
}

func TestSanitizeTextFailsClosedOnAnyAbsolutePath(t *testing.T) {
	for _, text := range []string{
		"Inspect /secret.txt",
		"Inspect /opt/秘密/file",
		"Inspect /opt/secret,name",
		"Inspect →/secret.txt",
		`Inspect C:\secret.txt`,
		"Inspect file:///secret.txt",
		"Inspect file://localhost/secret.txt",
		"Inspect file://server/share/private.txt",
		"Inspect →file://server/share/private.txt",
		"Inspect uri:file://server/share/private.txt",
	} {
		truncations := Truncations{}
		got := sanitizeText("/workspace", text, DefaultFieldLimit, &truncations)
		if got.Text != "[PATH_REDACTED]" || !got.Redacted {
			t.Fatalf("absolute path was not redacted as a whole: %#v", got)
		}
	}
}

func TestSanitizeTextDoesNotPreserveSecondPathInsideWorkspaceQuote(t *testing.T) {
	truncations := Truncations{}
	got := sanitizeText("/workspace", `Inspect '/workspace/a and /etc/shadow'`, DefaultFieldLimit, &truncations)
	if got.Text != "[PATH_REDACTED]" || !got.Redacted {
		t.Fatalf("second quoted absolute path was preserved: %#v", got)
	}
}

func TestSanitizeTextNeutralizesInternalWorkspaceMarkerInput(t *testing.T) {
	truncations := Truncations{}
	got := sanitizeText("/workspace", "literal "+workspaceProjectionMarker+"/private.txt", DefaultFieldLimit, &truncations)
	if got.Text != "[PATH_REDACTED]" || strings.Contains(got.Text, workspaceProjectionMarker) || !got.Redacted {
		t.Fatalf("internal marker input was not neutralized: %#v", got)
	}
}

func TestSanitizeTextDoesNotTreatURLAsAbsolutePath(t *testing.T) {
	truncations := Truncations{}
	got := sanitizeText("/workspace", "Visit https://example.com/private/path", DefaultFieldLimit, &truncations)
	if got.Text != "Visit https://example.com/private/path" || got.Redacted {
		t.Fatalf("URL was treated as an absolute path: %#v", got)
	}
}

func TestSanitizeTextMatchesCaseInsensitiveWorkspaceAliases(t *testing.T) {
	tests := []struct {
		name          string
		workspaceRoot string
		text          string
		want          string
	}{
		{
			name:          "macos",
			workspaceRoot: "/Users/Alice/Secret Project",
			text:          "Inspect /users/alice/secret project/private.txt",
			want:          "Inspect $WORKSPACE/private.txt",
		},
		{
			name:          "windows",
			workspaceRoot: `C:\Users\Alice\Secret Project`,
			text:          `Inspect c:\users\alice\secret project\private.txt`,
			want:          `Inspect $WORKSPACE\private.txt`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncations := Truncations{}
			got := sanitizeText(test.workspaceRoot, test.text, DefaultFieldLimit, &truncations)
			if got.Text != test.want || !got.Redacted {
				t.Fatalf("sanitized text = %#v, want %q", got, test.want)
			}
		})
	}
}

func TestAbsoluteWorkspaceRootValidationIsPlatformNeutral(t *testing.T) {
	for _, root := range []string{"/home/alice/project", `C:\Users\Alice\project`, "d:/work/project", `\\server\share\project`} {
		if !isAbsoluteWorkspaceRoot(root) {
			t.Errorf("absolute workspace root rejected: %q", root)
		}
	}
	for _, root := range []string{"", "relative/project", `C:relative`, `\rooted`, "/workspace/..", `C:\workspace\.\project`, "bad\x00root"} {
		if isAbsoluteWorkspaceRoot(root) {
			t.Errorf("non-absolute workspace root accepted: %q", root)
		}
	}
}

func TestPromptOmissionIsTypedAndDoesNotPublishText(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeOmit}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	for _, event := range plan.Events {
		if event.Type != primitives.EventTypePromptUser {
			continue
		}
		if event.Prompt == nil || !event.Prompt.Omitted || event.Prompt.Text != "" {
			t.Fatalf("prompt projection = %#v", event.Prompt)
		}
		return
	}
	t.Fatal("preview did not contain a typed prompt omission")
}

func TestMetadataOnlyOmitsPromptIntentAndAssistantText(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeMetadataOnly}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manifest.Omissions["prompt_policy"] != 1 || plan.Manifest.Omissions["intent_policy"] != 1 || plan.Manifest.Omissions["assistant_policy"] != 1 {
		t.Fatalf("metadata-only omissions = %#v", plan.Manifest.Omissions)
	}
	for _, event := range plan.Events {
		if event.Intent != nil || event.Assistant != nil || (event.Prompt != nil && !event.Prompt.Omitted) {
			t.Fatalf("metadata-only event contains text projection: %#v", event)
		}
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Fix the shared history sync", "Implemented token=", "cat private.txt"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata-only plan contains %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectBranchGatesOnPolicyAndRefShape(t *testing.T) {
	redacted := policyFile{PromptMode: PromptModeRedactedText}
	for _, testCase := range []struct {
		name     string
		policy   policyFile
		branch   string
		detached bool
		want     string
		omission string
	}{
		{name: "short name", policy: redacted, branch: "refs/heads/main", want: "main"},
		{name: "nested name", policy: redacted, branch: "refs/heads/feature/retry-fix", want: "feature/retry-fix"},
		{name: "omit still names the branch", policy: policyFile{PromptMode: PromptModeOmit}, branch: "refs/heads/main", want: "main"},
		{name: "metadata only publishes no branch", policy: policyFile{PromptMode: PromptModeMetadataOnly}, branch: "refs/heads/main", omission: "branch_policy"},
		{name: "detached head", policy: redacted, detached: true, omission: "branch_detached"},
		{name: "control characters", policy: redacted, branch: "refs/heads/ma\x1b[31min", omission: "invalid_branch"},
		{name: "spaces", policy: redacted, branch: "refs/heads/my branch", omission: "invalid_branch"},
		{name: "bare refs prefix", policy: redacted, branch: "refs/heads/", omission: "invalid_branch"},
		{name: "absent and attached", policy: redacted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			branch, omission := projectBranch(testCase.policy, testCase.branch, testCase.detached)
			if branch != testCase.want || omission != testCase.omission {
				t.Fatalf("projectBranch = (%q, %q), want (%q, %q)", branch, omission, testCase.want, testCase.omission)
			}
			if omission != "" && !validMetadataReason(omission) {
				t.Fatalf("omission reason %q is not wire-safe", omission)
			}
		})
	}
}

func TestInvisibleCharactersCannotSmuggleAnAbsolutePath(t *testing.T) {
	// A zero-width character in front of a separator used to hide the path from
	// boundary detection, and sanitizeIdentifier then removed it and published
	// the exact path.
	for _, value := range []string{
		"\u200b/etc/passwd",
		"\ufeff/etc/passwd",
		"\u200d/etc/passwd",
		"\u2060/etc/passwd",
		"\u202e/etc/passwd",
	} {
		truncations := Truncations{}
		if got := sanitizeText("/workspace", value, DefaultFieldLimit, &truncations); got.Text != "[PATH_REDACTED]" {
			t.Fatalf("sanitizeText(%q) = %q, want redaction", value, got.Text)
		}
		got := sanitizeIdentifier("/workspace", value, &truncations)
		if got.Text != "[PATH_REDACTED]" {
			t.Fatalf("sanitizeIdentifier(%q) = %q, want redaction", value, got.Text)
		}
		if !got.Redacted {
			t.Fatalf("sanitizeIdentifier(%q) did not report redaction", value)
		}
	}
	// A workspace path hidden the same way must still normalize rather than leak.
	truncations := Truncations{}
	if got := sanitizeText("/workspace", "\u200b/workspace/private.txt", DefaultFieldLimit, &truncations); got.Text != "$WORKSPACE/private.txt" {
		t.Fatalf("hidden workspace path = %q", got.Text)
	}
	// Ordinary text must survive untouched.
	if got := sanitizeText("/workspace", "plain tool name", DefaultFieldLimit, &truncations); got.Text != "plain tool name" || got.Redacted {
		t.Fatalf("benign text changed: %#v", got)
	}
}

func TestReceiverRejectsBundlesThatViolateTheirDeclaredPromptMode(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, repo)

	// Build a fully self-consistent forgery: drop the prompt event so the
	// prompt-mode check is satisfied, then re-derive the source refs and content
	// hash so nothing else objects. Only an explicit policy check can catch
	// this, and without one the receiver accepts assistant text in a bundle
	// labeled metadata_only.
	forge := func(t *testing.T, extraLink *SourceLink) error {
		t.Helper()
		var events []ContextEvent
		var refs []SourceRef
		for _, event := range plan.Events {
			if event.Prompt != nil {
				continue
			}
			events = append(events, event)
			refs = append(refs, event.Source)
		}
		carriesText := false
		for _, event := range events {
			if event.Intent != nil || event.Assistant != nil {
				carriesText = true
			}
		}
		if !carriesText {
			t.Fatal("forged bundle carries no text, so it would not prove anything")
		}
		eventsJSON, err := marshalEventsJSONL(events)
		if err != nil {
			t.Fatal(err)
		}
		manifest := plan.Manifest
		manifest.PromptMode = PromptModeMetadataOnly
		manifest.SourceRefs = refs
		manifest.SourceSequence = SequenceRange{First: refs[0].Seq, Last: refs[len(refs)-1].Seq}
		manifest.ContentHashes = map[string]string{"events.jsonl": sha256Bytes(eventsJSON)}
		if extraLink != nil {
			manifest.SourceLinks = append(append([]SourceLink{}, manifest.SourceLinks...), *extraLink)
		}
		signed, err := signManifest(identity, manifest)
		if err != nil {
			t.Fatal(err)
		}
		return verifyStoredBundle(repo.RepoID, StoredBundle{Manifest: signed, Events: events, PublicKey: identity.PublicKey})
	}

	if err := forge(t, nil); err == nil {
		t.Fatal("receiver accepted metadata_only bundle carrying text projections")
	}
	if err := forge(t, &SourceLink{CommitSHA: "4444444444444444444444444444444444444444", Branch: "secret-branch"}); err == nil {
		t.Fatal("receiver accepted metadata_only manifest naming a source branch")
	}
}

func TestOversizeGitOutputStopsTheCommandInsteadOfDraining(t *testing.T) {
	buffer := &limitedBuffer{limit: 8}
	if written, err := buffer.Write([]byte("12345")); written != 5 || err != nil {
		t.Fatalf("under-limit write = %d, %v", written, err)
	}
	// Once the limit is passed the writer must report an error so the producing
	// command is torn down rather than allowed to stream an unbounded object.
	written, err := buffer.Write([]byte("6789abcdef"))
	if written != 10 || err == nil {
		t.Fatalf("over-limit write = %d, %v", written, err)
	}
	if !buffer.overflow {
		t.Fatal("overflow was not recorded")
	}
	if buffer.data.Len() > 8 {
		t.Fatalf("buffer retained %d bytes beyond its limit", buffer.data.Len())
	}
}

func TestSummarizeReportsPendingWithoutConfiguringOrLocking(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)

	// An unconfigured workspace is not an error: turnal status renders this
	// unconditionally and must stay silent for people who never share.
	summary, err := Summarize(repo)
	if err != nil || summary.Configured {
		t.Fatalf("unconfigured summary = %#v, %v", summary, err)
	}

	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	summary, err = Summarize(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Configured || !summary.Enabled || summary.Approved || summary.Pending != 1 {
		t.Fatalf("configured summary = %#v", summary)
	}

	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if summary, err = Summarize(repo); err != nil || !summary.Approved || summary.Pending != 1 {
		t.Fatalf("approved summary = %#v, %v", summary, err)
	}

	// Summarize must not take the shared-history lock, or turnal status would
	// block behind a concurrent sync. Calling it from inside the lock proves it
	// does not try to reacquire.
	if _, err := withSharedHistoryLock(repo, "test hold", func() (struct{}, error) {
		held, holdErr := Summarize(repo)
		if holdErr != nil || held.Pending != 1 {
			t.Fatalf("summary under held lock = %#v, %v", held, holdErr)
		}
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Disable(repo); err != nil {
		t.Fatal(err)
	}
	if summary, err = Summarize(repo); err != nil || !summary.Configured || summary.Enabled {
		t.Fatalf("disabled summary = %#v, %v", summary, err)
	}
}

func TestListSurfacesSourceCommitAndFiltersByPrefix(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}
	bundles, err := New(repo).List(context.Background(), ListOptions{})
	if err != nil || len(bundles) != 1 {
		t.Fatalf("listed bundles = %d, %v", len(bundles), err)
	}
	// The test recorder captures no workspace Git context, so there is no source
	// commit to surface. Filtering on any commit must therefore exclude it
	// rather than matching everything.
	if bundles[0].SourceCommit != "" {
		t.Fatalf("unexpected source commit %q", bundles[0].SourceCommit)
	}
	filtered, err := New(repo).List(context.Background(), ListOptions{CommitSHA: "abc123"})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("commit filter matched %d bundles, %v", len(filtered), err)
	}
}

func TestSummarizeBundleTakesFirstCommitAndBranch(t *testing.T) {
	summary := summarizeBundle("v1:device:bundle", StoredBundle{Manifest: Manifest{SourceLinks: []SourceLink{
		{Checkpoint: "ckpt_0123456789abcdef0123456789abcdef"},
		{CommitSHA: "4444444444444444444444444444444444444444", Branch: "release-2"},
	}}}, true)
	if summary.SourceCommit != "4444444444444444444444444444444444444444" || summary.Branch != "release-2" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestManifestNamesTheProjectionThatProducedIt(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manifest.AllowlistVersion != AllowlistVersion || plan.Manifest.ScannerVersion != ScannerVersion {
		t.Fatalf("manifest projection versions = (%q, %q)", plan.Manifest.AllowlistVersion, plan.Manifest.ScannerVersion)
	}
	if plan.Manifest.ProducerVersion == "" {
		t.Fatal("manifest does not name the producing Turnal version")
	}
	// The receiver must accept what the publisher just produced.
	if err := verifyStoredBundle(repo.RepoID, StoredBundle{Manifest: plan.Manifest, Events: plan.Events, PublicKey: mustDevice(t, repo).PublicKey}); err != nil {
		t.Fatalf("verify bundle carrying projection versions: %v", err)
	}
}

func TestReceiverRejectsUnsafeProjectionLabels(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, repo)
	for _, testCase := range []struct {
		name    string
		corrupt func(Manifest) Manifest
	}{
		{name: "scanner version", corrupt: func(manifest Manifest) Manifest {
			manifest.ScannerVersion = "turnal\x1b[31m-secrets"
			return manifest
		}},
		{name: "producer version", corrupt: func(manifest Manifest) Manifest {
			manifest.ProducerVersion = "0.0.5 rm -rf /"
			return manifest
		}},
		{name: "source branch", corrupt: func(manifest Manifest) Manifest {
			manifest.SourceLinks = []SourceLink{{CommitSHA: "4444444444444444444444444444444444444444", Branch: "main\x1b[31m"}}
			return manifest
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Re-sign so the rejection comes from validation, not the signature.
			tampered, err := signManifest(identity, testCase.corrupt(plan.Manifest))
			if err != nil {
				t.Fatal(err)
			}
			err = verifyStoredBundle(repo.RepoID, StoredBundle{Manifest: tampered, Events: plan.Events, PublicKey: identity.PublicKey})
			if err == nil {
				t.Fatal("receiver accepted an unsafe projection label")
			}
		})
	}
}

func TestProjectionLimitsTruncateFieldsAndBlockOversizeBundles(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, err := primitives.ParseSessionID("limit-test")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	turnID := started.TurnID
	if _, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		SourceID:  "limit:prompt",
		Payload:   mustTestJSON(t, map[string]any{"text": strings.Repeat("x", DefaultFieldLimit+200)}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.Manifest.Truncations.Count != 1 || plan.Manifest.Truncations.OriginalBytes != DefaultFieldLimit+200 {
		t.Fatalf("truncations = %#v", plan.Manifest.Truncations)
	}
	for _, event := range plan.Events {
		if event.Prompt != nil && (!event.Prompt.Truncated || len(event.Prompt.Text) != DefaultFieldLimit) {
			t.Fatalf("truncated prompt = %#v", event.Prompt)
		}
	}

	policy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy.BundleLimit = 1024
	digest, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.ApprovedHash = digest
	if err := writeJSONAtomic(policyPath(repo), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Blocked != 1 || result.Published != 0 || result.Remaining != 0 {
		t.Fatalf("oversize sync result = %#v", result)
	}
	status, err := New(repo).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blocked) != 1 {
		t.Fatalf("blocked status = %#v", status)
	}
}

func TestSignaturesAndClosedEventSchemaRejectTampering(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	identity := mustDevice(t, repo)
	public, err := publicKeyForDevice(identity.PublicKey, identity.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := plan.Manifest
	manifest.EvidenceClass = "verified_source"
	if err := verifyManifest(public, manifest); err == nil {
		t.Fatal("tampered manifest signature verified")
	}

	event := plan.Events[0]
	event.Type = primitives.EventTypePromptUser
	data, err := marshalEventsJSONL([]ContextEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEventsJSONL(data); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong typed payload error = %v", err)
	}
	foreignRepoID, err := primitives.NewRepoID()
	if err != nil {
		t.Fatal(err)
	}
	item := BatchBundle{
		BundleID:  plan.Manifest.BundleID,
		Path:      bundlePath(plan.Manifest.BundleID),
		RepoID:    foreignRepoID,
		SessionID: plan.Manifest.SessionID,
		TurnID:    plan.Manifest.TurnID,
		Sequence:  plan.Manifest.SourceSequence,
	}
	if err := validateManifest(repo.RepoID, identity.DeviceID, item, plan.Manifest); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("mismatched batch repository error = %v", err)
	}
}

func TestFrozenV1SignaturePayloadRemainsWireCompatible(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	manifest := plan.Manifest
	manifest.Signature = ""
	legacyManifest, err := json.Marshal(unsignedManifest(manifest))
	if err != nil {
		t.Fatal(err)
	}
	frozenManifest, err := json.Marshal(manifestSigningPayloadV1(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyManifest, frozenManifest) {
		t.Fatalf("frozen manifest signature payload changed\nlegacy=%s\nfrozen=%s", legacyManifest, frozenManifest)
	}
	identity := mustDevice(t, repo)
	batch := Batch{SchemaVersion: SchemaVersion, DeviceID: identity.DeviceID, PublicKey: identity.PublicKey, Bundles: []BatchBundle{{BundleID: manifest.BundleID, Path: bundlePath(manifest.BundleID), RepoID: manifest.RepoID, SessionID: manifest.SessionID, TurnID: manifest.TurnID, Sequence: manifest.SourceSequence}}, CreatedAt: time.Now().UTC()}
	legacyBatch, err := json.Marshal(unsignedBatch(batch))
	if err != nil {
		t.Fatal(err)
	}
	frozenBatch, err := json.Marshal(batchSigningPayloadV1(batch))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyBatch, frozenBatch) {
		t.Fatalf("frozen batch signature payload changed\nlegacy=%s\nfrozen=%s", legacyBatch, frozenBatch)
	}

	nilCollectionsManifest := manifest
	nilCollectionsManifest.SourceRefs = nil
	nilCollectionsManifest.SourceLinks = nil
	nilManifest, err := json.Marshal(unsignedManifest(nilCollectionsManifest))
	if err != nil {
		t.Fatal(err)
	}
	nilFrozenManifest, err := json.Marshal(manifestSigningPayloadV1(nilCollectionsManifest))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nilManifest, nilFrozenManifest) {
		t.Fatalf("frozen nil-collection manifest payload changed\nlegacy=%s\nfrozen=%s", nilManifest, nilFrozenManifest)
	}
	nilBundlesBatch := batch
	nilBundlesBatch.Bundles = nil
	nilBatch, err := json.Marshal(unsignedBatch(nilBundlesBatch))
	if err != nil {
		t.Fatal(err)
	}
	nilFrozenBatch, err := json.Marshal(batchSigningPayloadV1(nilBundlesBatch))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nilBatch, nilFrozenBatch) {
		t.Fatalf("frozen nil-bundle batch payload changed\nlegacy=%s\nfrozen=%s", nilBatch, nilFrozenBatch)
	}
}

func TestFrozenV1SigningGolden(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	identity := deviceIdentity{DeviceID: deviceID(public), PublicKey: base64.RawStdEncoding.EncodeToString(public), public: public, private: private}
	manifest := Manifest{
		SchemaVersion:  SchemaVersion,
		BundleID:       "bundle_0123456789abcdef0123456789abcdef",
		RepoID:         "repo_0123456789abcdef0123456789abcdef",
		DeviceID:       identity.DeviceID,
		ProducerID:     "eprod_0123456789abcdef0123456789abcdef",
		StoreID:        "store_0123456789abcdef0123456789abcdef",
		WorktreeID:     "wt_0123456789abcdef0123456789abcdef",
		StreamID:       "stream_0123456789abcdef0123456789abcdef",
		SessionID:      "frozen-session",
		TurnID:         3,
		SourceSequence: SequenceRange{First: 1, Last: 4},
		SourceRefs:     []SourceRef{{StreamID: "stream_0123456789abcdef0123456789abcdef", Seq: 1, Hash: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}},
		PolicyHash:     "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		PromptMode:     PromptModeRedactedText,
		EvidenceClass:  EvidencePublisherClaim,
		SourceLinks:    []SourceLink{{CommitSHA: "4444444444444444444444444444444444444444", Checkpoint: "ckpt_0123456789abcdef0123456789abcdef"}},
		Omissions:      map[string]int{"tool_input": 2},
		Redactions:     map[string]int{"secret": 1},
		Truncations:    Truncations{Count: 1, OriginalBytes: 99},
		ContentHashes:  map[string]string{"events.jsonl": "sha256:5555555555555555555555555555555555555555555555555555555555555555"},
		CreatedAt:      time.Date(2026, 2, 3, 4, 5, 6, 700000000, time.UTC),
	}
	manifestBytes, err := json.Marshal(manifestSigningPayloadV1(manifest))
	if err != nil {
		t.Fatal(err)
	}
	const manifestHash = "sha256:9d6de23d53e44a26c2e3ee33540089fdb710cbce09a313aa84b817b341bbfca6"
	if got := sha256Bytes(manifestBytes); got != manifestHash {
		t.Fatalf("v1 manifest signing bytes changed: got %s", got)
	}
	signedManifest, err := signManifest(identity, manifest)
	if err != nil {
		t.Fatal(err)
	}
	const manifestSignature = "q73PpII9e38WKqte0ZbthXDD/MWpqfitKXY2+IosRvCGpnHE/HASLdciY3TB1OhyCFl7GH71yDu0Pw4+PqJDDQ"
	if signedManifest.Signature != manifestSignature {
		t.Fatalf("v1 manifest signature changed: got %s", signedManifest.Signature)
	}

	batch := Batch{
		SchemaVersion: SchemaVersion,
		DeviceID:      identity.DeviceID,
		PublicKey:     identity.PublicKey,
		PreviousHead:  "6666666666666666666666666666666666666666",
		Bundles:       []BatchBundle{{BundleID: manifest.BundleID, Path: bundlePath(manifest.BundleID), RepoID: manifest.RepoID, SessionID: manifest.SessionID, TurnID: manifest.TurnID, Sequence: manifest.SourceSequence}},
		CreatedAt:     time.Date(2026, 2, 3, 4, 5, 7, 0, time.UTC),
	}
	batchBytes, err := json.Marshal(batchSigningPayloadV1(batch))
	if err != nil {
		t.Fatal(err)
	}
	const batchHash = "sha256:78977c0226e47f60200291586905a9420655e62b134b34fabd559cfba9709e9c"
	if got := sha256Bytes(batchBytes); got != batchHash {
		t.Fatalf("v1 batch signing bytes changed: got %s", got)
	}
	signedBatch, err := signBatch(identity, batch)
	if err != nil {
		t.Fatal(err)
	}
	const batchSignature = "RB99xb9y3dLhQV3JEdaMNkY0tcOOFDPEL1Z+mSFTA5YBZIGkbe/8GpUfsPQAieRvNGu1I4+/g4QysWCnSMbODQ"
	if signedBatch.Signature != batchSignature {
		t.Fatalf("v1 batch signature changed: got %s", signedBatch.Signature)
	}
}

func TestHistoricalV1SignedFixturesStillVerify(t *testing.T) {
	// These fixtures were signed over the original v1 fields, before the
	// optional redaction counters existed. Keep both the bytes and signatures
	// static so this test exercises verification rather than signing anew.
	const manifestFixture = `{"schema_version":1,"bundle_id":"bundle_0123456789abcdef0123456789abcdef","repo_id":"repo_0123456789abcdef0123456789abcdef","device_id":"65b60673d6ed884bf01c2c222d82ada0","producer_id":"eprod_0123456789abcdef0123456789abcdef","store_id":"store_0123456789abcdef0123456789abcdef","worktree_id":"wt_0123456789abcdef0123456789abcdef","stream_id":"stream_0123456789abcdef0123456789abcdef","session_id":"frozen-session","turn_id":3,"source_sequence_range":{"first":1,"last":4},"source_ref":[{"stream_id":"stream_0123456789abcdef0123456789abcdef","seq":1,"hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}],"policy_hash":"sha256:3333333333333333333333333333333333333333333333333333333333333333","prompt_mode":"redacted_text","evidence_class":"publisher_claim","source_links":[{"commit_sha":"4444444444444444444444444444444444444444","checkpoint_id":"ckpt_0123456789abcdef0123456789abcdef"}],"omissions":{"tool_input":2},"truncations":{"count":1,"original_bytes":99},"content_hashes":{"events.jsonl":"sha256:5555555555555555555555555555555555555555555555555555555555555555"},"created_at":"2026-02-03T04:05:06.7Z","signature":"ZkIdbwSV3pxzcaSACoLohfWd4vxr4Goyk3pLjb80hmBqRQQU8ik8J0UF+1sedt1I9qx9g6sN3r+UsNwIB7hUDg"}`
	const batchFixture = `{"schema_version":1,"device_id":"65b60673d6ed884bf01c2c222d82ada0","public_key":"ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ","previous_head":"6666666666666666666666666666666666666666","bundles":[{"bundle_id":"bundle_0123456789abcdef0123456789abcdef","path":"bundles/01/bundle_0123456789abcdef0123456789abcdef","repo_id":"repo_0123456789abcdef0123456789abcdef","session_id":"frozen-session","turn_id":3,"sequence_range":{"first":1,"last":4}}],"created_at":"2026-02-03T04:05:07Z","signature":"RB99xb9y3dLhQV3JEdaMNkY0tcOOFDPEL1Z+mSFTA5YBZIGkbe/8GpUfsPQAieRvNGu1I4+/g4QysWCnSMbODQ"}`

	var manifest Manifest
	if err := json.Unmarshal([]byte(manifestFixture), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Redactions != nil {
		t.Fatalf("historical fixture unexpectedly has redactions: %#v", manifest.Redactions)
	}
	public, err := publicKeyForDevice("ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ", manifest.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyManifest(public, manifest); err != nil {
		t.Fatalf("verify historical manifest: %v", err)
	}

	var batch Batch
	if err := json.Unmarshal([]byte(batchFixture), &batch); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBatch(batch); err != nil {
		t.Fatalf("verify historical batch: %v", err)
	}
}

func TestGitSyncReplicatesAndRejectsRefRewrite(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)

	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatalf("configure publisher: %v", err)
	}
	plan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatalf("approve preview: %v", err)
	}
	publisherStore, err := openGitStore(context.Background(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(publisherStore.root, "bundles", "private-tool-output.txt")
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePath, []byte("must never be staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, publisher, sessionID, turnID)
	result, err := New(publisher).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Published != 1 || result.Head == "" {
		t.Fatalf("push result = %#v", result)
	}
	if tree := runTestGit(t, publisherStore.root, "ls-tree", "-r", "--name-only", result.Head); strings.Contains(tree, "private-tool-output") {
		t.Fatalf("shared history tree staged a forbidden file: %s", tree)
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	// Both stores represent clones of the same logical project while retaining
	// independent store, worktree, producer, and device identities.
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit, RepoID: publisher.RepoID}); err != nil {
		t.Fatalf("configure receiver: %v", err)
	}
	pull, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pull.Pulled != 1 {
		t.Fatalf("pull result = %#v", pull)
	}
	bundle, err := New(receiver).Read(context.Background(), plan.Locator)
	if err != nil {
		t.Fatalf("read pulled locator: %v", err)
	}
	if bundle.Manifest.BundleID != plan.Manifest.BundleID || bundle.Manifest.DeviceID != plan.Manifest.DeviceID {
		t.Fatalf("pulled bundle identity = %#v", bundle.Manifest)
	}

	attacker := filepath.Join(testRoot, "rewrite")
	if err := os.MkdirAll(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, attacker, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(attacker, "rewrite.txt"), []byte("unrelated history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, attacker, "add", "rewrite.txt")
	runTestGit(t, attacker, "-c", "user.name=Rewrite Test", "-c", "user.email=rewrite@example.invalid", "commit", "-m", "unrelated")
	runTestGit(t, attacker, "push", "--force", remote, "HEAD:"+historyRef(plan.Manifest.DeviceID))

	rewriteResult, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || !strings.Contains(rewriteResult.Quarantined[plan.Manifest.DeviceID], "rewound") {
		t.Fatalf("rewrite quarantine = %#v, %v", rewriteResult.Quarantined, err)
	}
}

func TestPullIgnoresMalformedAndSuffixMatchingRemoteRefs(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}
	store, err := openGitStore(context.Background(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, store.root, "push", remote, "HEAD:refs/turnal/v1/history/junk")
	runTestGit(t, store.root, "push", remote, "HEAD:refs/nested/refs/turnal/v1/history/deeper")

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil || result.Pulled != 1 || len(result.Warnings) != 2 {
		t.Fatalf("pull with junk refs = %#v, %v", result, err)
	}
}

func TestCorruptPublisherDoesNotBlockHealthyPublisher(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	healthy := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "healthy"))
	sessionID, turnID := recordSharedHistoryTurn(t, healthy)
	if _, err := Configure(healthy, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(healthy).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(healthy).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}

	attacker := filepath.Join(testRoot, "attacker")
	runTestGit(t, testRoot, "init", attacker)
	runTestGit(t, attacker, "-c", "user.name=Corrupt Publisher", "-c", "user.email=corrupt@example.invalid", "commit", "--allow-empty", "-m", "not shared history")
	badDevice := strings.Repeat("f", 32)
	runTestGit(t, attacker, "push", remote, "HEAD:"+historyRef(badDevice))

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: healthy.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || result.Pulled != 1 || result.Quarantined[badDevice] == "" {
		t.Fatalf("isolated pull = %#v, %v", result, err)
	}
}

func TestListContinuesPastUnreadablePulledBundle(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}
	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err != nil {
		t.Fatal(err)
	}
	path := pulledBundlePath(receiver, publisher.RepoID, plan.Manifest.DeviceID, plan.Manifest.BundleID)
	if err := os.WriteFile(path, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundles, err := New(receiver).List(context.Background(), ListOptions{})
	if err != nil || len(bundles) != 1 || bundles[0].Error == "" {
		t.Fatalf("list unreadable bundle = %#v, %v", bundles, err)
	}
}

func TestIndependentDevicesCanPublishConcurrently(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	first := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "first"))
	firstSession, firstTurn := recordSharedHistoryTurn(t, first)
	if _, err := Configure(first, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(first).Preview(context.Background(), PreviewOptions{SessionID: firstSession, TurnID: firstTurn, Approve: true}); err != nil {
		t.Fatal(err)
	}
	second := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "second"))
	secondSession, secondTurn := recordSharedHistoryTurn(t, second)
	if _, err := Configure(second, ConfigureOptions{Remote: remote, RepoID: first.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(second).Preview(context.Background(), PreviewOptions{SessionID: secondSession, TurnID: secondTurn, Approve: true}); err != nil {
		t.Fatal(err)
	}

	errors := make(chan error, 2)
	for _, repo := range []*checkpoint.Repo{first, second} {
		go func() {
			_, err := New(repo).Sync(context.Background(), DirectionPush)
			errors <- err
		}()
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: first.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil || result.Pulled != 2 || len(result.Quarantined) != 0 {
		t.Fatalf("concurrent publishers pull = %#v, %v", result, err)
	}
}

func TestRemoteChangePushesApprovedLocalHistory(t *testing.T) {
	testRoot := t.TempDir()
	firstRemote := filepath.Join(testRoot, "first.git")
	secondRemote := filepath.Join(testRoot, "second.git")
	runTestGit(t, testRoot, "init", "--bare", firstRemote)
	runTestGit(t, testRoot, "init", "--bare", secondRemote)

	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: firstRemote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	firstPush, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Configure(repo, ConfigureOptions{Remote: secondRemote, PromptMode: PromptModeRedactedText}); err == nil || !strings.Contains(err.Error(), "--include-existing-history") {
		t.Fatalf("remote migration without consent error = %v", err)
	}
	status, err := Configure(repo, ConfigureOptions{Remote: secondRemote, PromptMode: PromptModeRedactedText, IncludeExistingHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if status.Approved {
		t.Fatal("remote change retained approval")
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	secondPush, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	secondHead := runTestGit(t, testRoot, "ls-remote", "--refs", secondRemote, historyRef(status.DeviceID))
	if secondPush.Published != 0 || secondPush.Head != firstPush.Head || !strings.Contains(secondHead, firstPush.Head) {
		t.Fatalf("migrated push = %#v, remote head = %q, first push = %#v", secondPush, secondHead, firstPush)
	}
}

func TestConfigureRejectsPolicyChangeWithUnpushedOutbox(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "first.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, repo, sessionID, turnID)

	_, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "second.git"), PromptMode: PromptModeOmit})
	if err == nil || !strings.Contains(err.Error(), "unpushed outbox") {
		t.Fatalf("Configure policy change error = %v", err)
	}
}

func TestDisableImmediatelyStopsSharingWithRecoveredUnpushedOutbox(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	remote := filepath.Join(t.TempDir(), "history.git")
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, repo, sessionID, turnID)
	status, err := Disable(repo)
	if err != nil || status.Enabled {
		t.Fatalf("disable status = %#v, %v", status, err)
	}
	if _, err := New(repo).Sync(context.Background(), DirectionPush); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled push error = %v", err)
	}
	status, err = Configure(repo, ConfigureOptions{})
	if err != nil || !status.Enabled || !status.UnpushedLocalTip {
		t.Fatalf("reenabled status = %#v, %v", status, err)
	}
	plan, err := New(repo).PlanPush(context.Background())
	if err != nil || plan.Queued != 1 || plan.Publishable != 1 || plan.BatchSize != 0 || plan.Remaining != 0 || len(plan.Pending) != 1 || !plan.Pending[0].Queued {
		t.Fatalf("queued outbox plan = %#v, %v", plan, err)
	}
}

func TestConfigureMigratesOlderScannerPolicyAndRequiresApproval(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	remote := filepath.Join(t.TempDir(), "history.git")
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy.ScannerVersion = "turnal-secrets-v1"
	oldHash, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.ApprovedHash = oldHash
	if err := writeJSONAtomic(policyPath(repo), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	oldStatus, err := New(repo).Status(context.Background())
	if err != nil || !oldStatus.Configured || !oldStatus.Approved {
		t.Fatalf("old scanner status = %#v, %v", oldStatus, err)
	}
	status, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit})
	if err != nil {
		t.Fatal(err)
	}
	if status.Approved {
		t.Fatalf("migrated policy unexpectedly retained approval: %#v", status)
	}
}

func TestOlderScannerOutboxCanDrainBeforeMigration(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	policy.ScannerVersion = "turnal-secrets-v1"
	oldHash, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.ApprovedHash = oldHash
	if err := writeJSONAtomic(policyPath(repo), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, repo, sessionID, turnID)
	if _, err := Disable(repo); err != nil {
		t.Fatal(err)
	}
	status, err := Configure(repo, ConfigureOptions{})
	if err != nil || !status.Enabled || !status.Approved || !status.UnpushedLocalTip {
		t.Fatalf("legacy outbox resume = %#v, %v", status, err)
	}
	plan, err := New(repo).PlanPush(context.Background())
	if err != nil || !plan.MigrationRequired || plan.ApprovalRequired || plan.Queued != 1 || plan.Publishable != 1 || plan.BatchSize != 0 || plan.Remaining != 0 || len(plan.Pending) != 1 || !plan.Pending[0].Queued {
		t.Fatalf("legacy outbox plan = %#v, %v", plan, err)
	}
	result, err := New(repo).Sync(context.Background(), DirectionPush)
	if err == nil || result.Published != 1 || !strings.Contains(err.Error(), "run turnal share enable") {
		t.Fatalf("legacy outbox drain = %#v, %v", result, err)
	}
	status, err = Configure(repo, ConfigureOptions{})
	if err != nil || status.Approved || status.UnpushedLocalTip {
		t.Fatalf("legacy scanner migration = %#v, %v", status, err)
	}
}

func TestProjectionRedactsIdentifierSecretsAndUNCPaths(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, err := primitives.ParseSessionID("identifier-redaction")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	turnID := started.TurnID
	secret := "ghp_0123456789abcdefghijklmnop"
	for _, input := range []eventlog.AppendInput{
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAgentIntent, Adapter: primitives.AdapterCodex, SourceID: "identifier:intent", Payload: mustTestJSON(t, map[string]any{"problem": "Inspect identifiers", "agent_type": "codex token=" + secret})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeToolCall, Adapter: primitives.AdapterCodex, SourceID: "identifier:tool", Payload: mustTestJSON(t, map[string]any{"tool_name": `shell \\server\share\private ` + secret})},
	} {
		if _, err := repo.EventLog().Append(input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), `server\\share`) {
		t.Fatalf("identifier projection leaked private text: %s", encoded)
	}
	if !strings.Contains(string(encoded), "[REDACTED]") || !strings.Contains(string(encoded), "[PATH_REDACTED]") {
		t.Fatalf("identifier projection did not expose redaction markers: %s", encoded)
	}
}

func TestStrictWireSchemaAndFieldLimits(t *testing.T) {
	var event ContextEvent
	data := []byte(`{"schema_version":1,"unexpected_stdout":"private"}`)
	if err := decodeStrictJSON(data, &event); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decode error = %v", err)
	}
	if err := decodeStrictJSON([]byte(`{"schema_version":2,"schema_version":1}`), &event); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
		t.Fatalf("duplicate field error = %v", err)
	}
	if err := decodeStrictJSON([]byte(`{"schema_version":1,"SCHEMA_VERSION":1}`), &event); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
		t.Fatalf("case-variant field error = %v", err)
	}

	oversized := ContextEvent{
		SchemaVersion: SchemaVersion,
		Type:          primitives.EventTypeAssistantMessage,
		Seq:           1,
		Source:        SourceRef{StreamID: primitives.EventStreamID("stream_0123456789abcdef0123456789abcdef"), Seq: 1, Hash: primitives.EventHash("sha256:" + strings.Repeat("a", 64))},
		Assistant:     &TextProjection{Text: strings.Repeat("x", DefaultFieldLimit+1), Bytes: DefaultFieldLimit + 1},
	}
	if err := validateContextEvent(oversized); err == nil || !strings.Contains(err.Error(), "exceeds limits") {
		t.Fatalf("oversized field validation error = %v", err)
	}
}

func TestContextEventWireFieldsRemainClosed(t *testing.T) {
	want := []string{"schema_version", "type", "seq", "time", "adapter", "source_ref", "lifecycle", "prompt", "intent", "assistant", "tool", "checkpoint", "capture_error"}
	typeOfEvent := reflect.TypeOf(ContextEvent{})
	got := make([]string, 0, typeOfEvent.NumField())
	for index := 0; index < typeOfEvent.NumField(); index++ {
		name, _, _ := strings.Cut(typeOfEvent.Field(index).Tag.Get("json"), ",")
		got = append(got, name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ContextEvent wire fields = %#v, want %#v; schema changes require an explicit version review", got, want)
	}
}

func TestSharedHistoryOperationsUseDedicatedLock(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	repo.LockTimeout = 20 * time.Millisecond
	lock, err := filelock.Acquire(sharedHistoryLockPath(repo), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	_, err = Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeOmit})
	if err == nil || !strings.Contains(err.Error(), "lock busy") {
		t.Fatalf("Configure lock error = %v", err)
	}
}

func TestStatusIsLocalUnlessRemoteCheckIsRequested(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	if _, err := Configure(repo, ConfigureOptions{Remote: missingRemote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, repo, sessionID, turnID)
	status, err := New(repo).Status(context.Background())
	if err != nil || status.RemoteChecked || status.RemoteError != "" || !status.UnpushedLocalTip {
		t.Fatalf("local status = %#v, %v", status, err)
	}
	status, err = New(repo).StatusWithRemote(context.Background())
	if err != nil || !status.RemoteChecked || status.RemoteError == "" {
		t.Fatalf("remote status = %#v, %v", status, err)
	}

	fresh := newSharedHistoryTestRepo(t)
	if _, err := Configure(fresh, ConfigureOptions{Remote: missingRemote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	status, err = New(fresh).StatusWithRemote(context.Background())
	if err != nil || !status.RemoteChecked || status.RemoteError == "" {
		t.Fatalf("fresh remote status = %#v, %v", status, err)
	}
	if _, err := Disable(fresh); err != nil {
		t.Fatal(err)
	}
	status, err = New(fresh).StatusWithRemote(context.Background())
	if err != nil || status.RemoteChecked || !strings.Contains(status.RemoteError, "disabled") {
		t.Fatalf("disabled remote status = %#v, %v", status, err)
	}
}

func TestNetworkOperationsAreBoundedAndReleaseTheLockOnCancellation(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	store.networkTimeout = time.Nanosecond
	if _, err := store.remoteHead(context.Background(), remote, historyRef("0123456789abcdef0123456789abcdef")); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("bounded remote operation error = %v", err)
	}
	_, _, fetchErr := store.fetchAndIngest(context.Background(), remote, remoteRef{
		DeviceID: "0123456789abcdef0123456789abcdef",
		Ref:      historyRef("0123456789abcdef0123456789abcdef"),
		Head:     "1111111111111111111111111111111111111111",
	}, "", repo.RepoID)
	if !isRetryablePullError(fetchErr) {
		t.Fatalf("network fetch error was not retryable: %v", fetchErr)
	}

	store.networkTimeout = DefaultNetworkTimeout
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.remoteHead(canceled, remote, historyRef("0123456789abcdef0123456789abcdef")); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled remote operation error = %v", err)
	}
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Sync(canceled, DirectionPull); err == nil {
		t.Fatal("pull with a cancelled context unexpectedly succeeded")
	}
	if _, err := New(repo).Status(context.Background()); err != nil {
		t.Fatalf("shared history lock was not released after cancellation: %v", err)
	}
}

func TestScrubGitEnvRemovesEveryGitVariable(t *testing.T) {
	cleaned := scrubGitEnv([]string{"PATH=/bin", "GIT_CONFIG_COUNT=1", "GIT_SSH_COMMAND=private", "git_dir=private", "MALFORMED"})
	if len(cleaned) != 1 || cleaned[0] != "PATH=/bin" {
		t.Fatalf("scrubGitEnv = %#v", cleaned)
	}
}

func TestPushRejectsObservedAncestorRewind(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	first, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = recordSharedHistoryTurn(t, repo)
	second, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	if first.Head == second.Head {
		t.Fatalf("two pushes retained one head: %#v %#v", first, second)
	}
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, store.root, "push", "--force", remote, first.Head+":"+historyRef(mustDevice(t, repo).DeviceID))
	if _, err := New(repo).Sync(context.Background(), DirectionPush); err == nil || !strings.Contains(err.Error(), "rewound or was replaced") {
		t.Fatalf("rewound push error = %v", err)
	}
}

func TestPullRecoversObservedHeadFromTrackingRef(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}
	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(receiver)
	if err != nil {
		t.Fatal(err)
	}
	state.LastSeen = map[string]string{}
	if err := saveState(receiver, state); err != nil {
		t.Fatal(err)
	}
	publisherStore, err := openGitStore(context.Background(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, publisherStore.root, "push", remote, ":"+historyRef(plan.Manifest.DeviceID))
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || !strings.Contains(result.Quarantined[plan.Manifest.DeviceID], "disappeared") {
		t.Fatalf("tracking recovery quarantine = %#v, %v", result.Quarantined, err)
	}
	status, err := ForgetDevice(receiver, plan.Manifest.DeviceID)
	if err != nil || status.Quarantined[plan.Manifest.DeviceID] != "" || status.Retired[plan.Manifest.DeviceID] == "" {
		t.Fatalf("acknowledged device removal = %#v, %v", status, err)
	}
	result, err = New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil || len(result.Quarantined) != 0 {
		t.Fatalf("pull after acknowledged removal = %#v, %v", result, err)
	}
}

func TestCredentialRemoteIsRedactedFromStatusAndErrors(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	remote := "https://user:private-token@example.invalid/history.git?access_token=query-secret"
	status, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status.Remote, "private-token") || strings.Contains(status.Remote, "query-secret") || !strings.Contains(status.Remote, "[REDACTED]") {
		t.Fatalf("status remote = %q", status.Remote)
	}
	message := gitCommandError{args: []string{"ls-remote", remote}, output: redactGitOutput("fatal: "+remote, []string{remote}), err: errors.New("failed")}.Error()
	if strings.Contains(message, "private-token") || strings.Contains(message, "query-secret") || !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("Git error leaked remote credentials: %s", message)
	}
	scpRemote := "private-user@example.invalid:history.git"
	scpStatus, err := Configure(repo, ConfigureOptions{Remote: scpRemote, PromptMode: PromptModeOmit})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(scpStatus.Remote, "private-user") || !strings.Contains(scpStatus.Remote, "[REDACTED]@") {
		t.Fatalf("SCP-style status remote = %q", scpStatus.Remote)
	}
	scpMessage := gitCommandError{args: []string{"ls-remote", scpRemote}, output: redactGitOutput("fatal: "+scpRemote, []string{scpRemote}), err: errors.New("failed")}.Error()
	if strings.Contains(scpMessage, "private-user") || !strings.Contains(scpMessage, "[REDACTED]@") {
		t.Fatalf("Git error leaked SCP-style userinfo: %s", scpMessage)
	}
}

func TestCredentialRotationDoesNotChangePublicPolicyHash(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	firstRemote := "https://first-user:first-password@example.invalid/history.git?access_token=first-query"
	if _, err := Configure(repo, ConfigureOptions{Remote: firstRemote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	firstPolicy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, err := policyHash(firstPolicy)
	if err != nil {
		t.Fatal(err)
	}
	secondRemote := "https://second-user:second-password@example.invalid/history.git?access_token=second-query"
	status, err := Configure(repo, ConfigureOptions{Remote: secondRemote, PromptMode: PromptModeOmit})
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := policyHash(secondPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || status.PolicyHash != firstHash {
		t.Fatalf("credential rotation changed policy hash: %s != %s", firstHash, secondHash)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"first-user", "first-password", "first-query", "second-user", "second-password", "second-query"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public status contains transport credential %q: %s", secret, encoded)
		}
	}
}

func TestConfigureRejectsRemoteHelpers(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	if _, err := Configure(repo, ConfigureOptions{Remote: "ext::sh -c malicious", PromptMode: PromptModeOmit}); err == nil || !strings.Contains(err.Error(), "remote helpers") {
		t.Fatalf("remote helper error = %v", err)
	}
}

func TestPullAdvancesOnePublicationBatchPerRun(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	firstSession, firstTurn := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	firstPlan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: firstSession, TurnID: firstTurn, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	firstPush, err := New(publisher).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	secondSession, secondTurn := recordSharedHistoryTurn(t, publisher)
	secondPlan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: secondSession, TurnID: secondTurn})
	if err != nil {
		t.Fatal(err)
	}
	secondPush, err := New(publisher).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	if firstPush.Head == secondPush.Head {
		t.Fatal("two publication batches retained one head")
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	firstPull, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil {
		t.Fatal(err)
	}
	if firstPull.Pulled != 1 {
		t.Fatalf("first pull = %#v", firstPull)
	}
	state, err := loadState(receiver)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSeen[firstPlan.Manifest.DeviceID] != firstPush.Head {
		t.Fatalf("first pull observed %q, want %q", state.LastSeen[firstPlan.Manifest.DeviceID], firstPush.Head)
	}
	if _, err := New(receiver).Read(context.Background(), secondPlan.Locator); !os.IsNotExist(err) {
		t.Fatalf("second batch visible before second pull: %v", err)
	}
	secondPull, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil {
		t.Fatal(err)
	}
	if secondPull.Pulled != 1 {
		t.Fatalf("second pull = %#v", secondPull)
	}
	thirdPull, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err != nil {
		t.Fatal(err)
	}
	if thirdPull.Pulled != 0 {
		t.Fatalf("settled pull = %#v", thirdPull)
	}
}

func TestPushPlanBatchLimitAndList(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	var firstSession primitives.SessionID
	var firstTurn primitives.TurnID
	for index := 0; index < MaxBundlesPerBatch+1; index++ {
		sessionID, turnID := recordSharedHistoryTurn(t, repo)
		if index == 0 {
			firstSession, firstTurn = sessionID, turnID
		}
	}
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: firstSession, TurnID: firstTurn, Approve: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).PlanPush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Publishable != MaxBundlesPerBatch+1 || plan.BatchSize != MaxBundlesPerBatch || plan.Remaining != 1 {
		t.Fatalf("push plan = %#v", plan)
	}
	result, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != MaxBundlesPerBatch || result.Remaining != 1 {
		t.Fatalf("first push = %#v", result)
	}
	bundles, err := New(repo).List(context.Background(), ListOptions{})
	if err != nil || len(bundles) != MaxBundlesPerBatch {
		t.Fatalf("listed bundles = %d, %v", len(bundles), err)
	}
	result, err = New(repo).Sync(context.Background(), DirectionPush)
	if err != nil || result.Published != 1 || result.Remaining != 0 {
		t.Fatalf("second push = %#v, %v", result, err)
	}
}

func TestPushRecoversWhenRemoteAcceptedOutboxBeforeStateSave(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	stageBundleWithoutState(t, repo, sessionID, turnID)
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, repo)
	if _, err := store.push(context.Background(), remote, identity.DeviceID, ""); err != nil {
		t.Fatal(err)
	}
	result, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != 1 {
		t.Fatalf("recovered push = %#v", result)
	}
}

func TestMaterializedBundleDoesNotHTMLEscapeAllowedText(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: filepath.Join(t.TempDir(), "history.git"), PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	events := append([]ContextEvent(nil), plan.Events...)
	for index := range events {
		if events[index].Assistant != nil {
			events[index].Assistant = &TextProjection{Text: strings.Repeat("<", 1<<20)}
			break
		}
	}
	bundle := StoredBundle{
		Manifest:  plan.Manifest,
		Events:    events,
		PublicKey: mustDevice(t, repo).PublicKey,
	}
	if err := materializePulled(repo, bundle); err != nil {
		t.Fatal(err)
	}
	path := pulledBundlePath(repo, plan.Manifest.RepoID, plan.Manifest.DeviceID, plan.Manifest.BundleID)
	data, err := readRegularFile(path, MaxMaterializedLimit)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`\u003c`)) || !bytes.Contains(data, []byte("<<<")) {
		t.Fatal("materialized bundle HTML-escaped allowed text")
	}
}

func TestOpenGitStoreRepairsInterruptedConfiguration(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, store.root, "config", "--unset", "user.name")
	if _, err := openGitStore(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if name := runTestGit(t, store.root, "config", "--local", "--get", "user.name"); name != "Turnal Shared History" {
		t.Fatalf("repaired user.name = %q", name)
	}
	if err := os.Remove(filepath.Join(store.root, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if _, err := openGitStore(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.root, ".git", "HEAD")); err != nil {
		t.Fatalf("reinitialized HEAD: %v", err)
	}
}

func TestPreCommitCrashDoesNotPoisonProjectionAfterPolicyChange(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	repo := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeRedactedText}); err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	source, err := findCompletedTurn(repo, sessionID, turnID, "")
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := buildBundle(repo, mustDevice(t, repo), policy, digest, source)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(store.root, filepath.FromSlash(orphan.Path))
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "manifest.json"), orphan.Manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "events.jsonl"), orphan.EventsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Configure(repo, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repo).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	result, err := New(repo).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != 1 {
		t.Fatalf("push after orphaned projection = %#v", result)
	}
}

func TestRepoIDChangeCannotReuseOldObservationCursorAfterCrash(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(publisher).Sync(context.Background(), DirectionPush); err != nil {
		t.Fatal(err)
	}
	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err != nil {
		t.Fatal(err)
	}
	newRepoID, err := primitives.NewRepoID()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(receiver)
	if err != nil {
		t.Fatal(err)
	}
	policy.RepoID = newRepoID
	policy.ApprovedHash = ""
	// Simulate a crash after the new policy is durable but before state.json is
	// reset. The old tracking refs and LastSeen entries must not cross RepoIDs.
	if err := writeJSONAtomic(policyPath(receiver), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := New(receiver).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Pulled != 0 {
		t.Fatalf("new RepoID counted %d materializations from the old scope", status.Pulled)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || !strings.Contains(result.Quarantined[mustDevice(t, publisher).DeviceID], "identity") {
		t.Fatalf("RepoID transition quarantine = %#v, %v", result.Quarantined, err)
	}
}

func TestPullDoesNotExposeEarlierBundleWhenLaterBundleIsInvalid(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	_, _ = recordSharedHistoryTurn(t, publisher)
	_, _ = recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy(publisher)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, publisher)
	sources, err := listCompletedTurns(publisher)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("completed turns = %d", len(sources))
	}
	bundles := make([]builtBundle, 0, len(sources))
	batch := Batch{SchemaVersion: SchemaVersion, DeviceID: identity.DeviceID, PublicKey: identity.PublicKey, CreatedAt: time.Now().UTC()}
	for _, source := range sources {
		bundle, err := buildBundle(publisher, identity, policy, digest, source)
		if err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, bundle)
		manifest := bundle.Stored.Manifest
		batch.Bundles = append(batch.Bundles, BatchBundle{BundleID: manifest.BundleID, Path: bundle.Path, RepoID: manifest.RepoID, SessionID: manifest.SessionID, TurnID: manifest.TurnID, Sequence: manifest.SourceSequence})
	}
	batch, err = signBatch(identity, batch)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openGitStore(context.Background(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.commitBatch(context.Background(), batch, bundles); err != nil {
		t.Fatal(err)
	}
	secondEvents := filepath.Join(store.root, filepath.FromSlash(bundles[1].Path), "events.jsonl")
	file, err := os.OpenFile(secondEvents, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("tampered\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, store.root, "add", "--", filepath.ToSlash(filepath.Join(bundles[1].Path, "events.jsonl")))
	runTestGit(t, store.root, "commit", "--amend", "--no-edit", "--no-verify")
	runTestGit(t, store.root, "push", remote, "HEAD:"+historyRef(identity.DeviceID))

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || !strings.Contains(result.Quarantined[identity.DeviceID], "content hash mismatch") {
		t.Fatalf("invalid batch quarantine = %#v, %v", result.Quarantined, err)
	}
	firstPath := pulledBundlePath(receiver, bundles[0].Stored.Manifest.RepoID, identity.DeviceID, bundles[0].Stored.Manifest.BundleID)
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("earlier bundle became visible before full batch validation: %v", err)
	}
}

func TestPullRejectsSignedRewriteOfExistingBundlePath(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)
	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, ConfigureOptions{Remote: remote, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	firstPush, err := New(publisher).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatal(err)
	}

	policy, err := loadPolicy(publisher)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, publisher)
	source, err := findCompletedTurn(publisher, sessionID, turnID, plan.Manifest.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildBundle(publisher, identity, policy, digest, source)
	if err != nil {
		t.Fatal(err)
	}
	modifiedEvents := bytes.Replace(bundle.EventsJSON, []byte("{"), []byte("{ "), 1)
	bundle.Stored.Manifest.ContentHashes["events.jsonl"] = sha256Bytes(modifiedEvents)
	bundle.Stored.Manifest, err = signManifest(identity, bundle.Stored.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	modifiedManifest, err := json.MarshalIndent(bundle.Stored.Manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	modifiedManifest = append(modifiedManifest, '\n')
	batch, err := signBatch(identity, Batch{
		SchemaVersion: SchemaVersion,
		DeviceID:      identity.DeviceID,
		PublicKey:     identity.PublicKey,
		PreviousHead:  firstPush.Head,
		Bundles: []BatchBundle{{
			BundleID:  bundle.Stored.Manifest.BundleID,
			Path:      bundle.Path,
			RepoID:    bundle.Stored.Manifest.RepoID,
			SessionID: bundle.Stored.Manifest.SessionID,
			TurnID:    bundle.Stored.Manifest.TurnID,
			Sequence:  bundle.Stored.Manifest.SourceSequence,
		}},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openGitStore(context.Background(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(store.root, filepath.FromSlash(bundle.Path), "manifest.json")
	eventsPath := filepath.Join(store.root, filepath.FromSlash(bundle.Path), "events.jsonl")
	if err := os.WriteFile(manifestPath, modifiedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsPath, modifiedEvents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(store.root, "batch.json"), batch, 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, store.root, "add", "--", "batch.json", bundle.Path+"/manifest.json", bundle.Path+"/events.jsonl")
	runTestGit(t, store.root, "commit", "--no-verify", "-m", "malicious signed rewrite")
	runTestGit(t, store.root, "push", remote, "HEAD:"+historyRef(identity.DeviceID))

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	if _, err := Configure(receiver, ConfigureOptions{Remote: remote, RepoID: publisher.RepoID, PromptMode: PromptModeOmit}); err != nil {
		t.Fatal(err)
	}
	if firstPull, err := New(receiver).Sync(context.Background(), DirectionPull); err != nil || firstPull.Pulled != 1 {
		t.Fatalf("first pull = %#v, %v", firstPull, err)
	}
	result, err := New(receiver).Sync(context.Background(), DirectionPull)
	if err == nil || !strings.Contains(result.Quarantined[identity.DeviceID], "rewrites an existing immutable path") {
		t.Fatalf("signed rewrite quarantine = %#v, %v", result.Quarantined, err)
	}
	state, err := loadState(receiver)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSeen[identity.DeviceID] != firstPush.Head {
		t.Fatalf("rewrite advanced observed head to %q", state.LastSeen[identity.DeviceID])
	}
}

func stageBundleWithoutState(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID) {
	t.Helper()
	policy, err := loadPolicyForUpdate(repo)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, repo)
	source, err := findCompletedTurn(repo, sessionID, turnID, "")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildBundle(repo, identity, policy, digest, source)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := signBatch(identity, Batch{
		SchemaVersion: SchemaVersion,
		DeviceID:      identity.DeviceID,
		PublicKey:     identity.PublicKey,
		Bundles: []BatchBundle{{
			BundleID:  bundle.Stored.Manifest.BundleID,
			Path:      bundle.Path,
			RepoID:    bundle.Stored.Manifest.RepoID,
			SessionID: bundle.Stored.Manifest.SessionID,
			TurnID:    bundle.Stored.Manifest.TurnID,
			Sequence:  bundle.Stored.Manifest.SourceSequence,
		}},
		CreatedAt: New(repo).now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openGitStore(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.commitBatch(context.Background(), batch, []builtBundle{bundle}); err != nil {
		t.Fatal(err)
	}
	// Deliberately skip state.json. Sync must recover this committed outbox.
}

func newSharedHistoryTestRepo(t *testing.T) *checkpoint.Repo {
	t.Helper()
	return newSharedHistoryTestRepoAt(t, t.TempDir())
}

func newSharedHistoryTestRepoAt(t *testing.T, rootPath string) *checkpoint.Repo {
	t.Helper()
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	return repo
}

func recordSharedHistoryTurn(t *testing.T, repo *checkpoint.Repo) (primitives.SessionID, primitives.TurnID) {
	t.Helper()
	sessionID, err := primitives.ParseSessionID("shared-history-test")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	turnID := started.TurnID
	inputs := []eventlog.AppendInput{
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, SourceID: "test:prompt", Payload: mustTestJSON(t, map[string]any{"text": "Inspect " + repo.WorkspaceRoot.String() + "/private.txt token=ghp_0123456789abcdefghijklmnop and /etc/passwd"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAgentIntent, Adapter: primitives.AdapterCodex, SourceID: "test:intent", Payload: mustTestJSON(t, map[string]any{"problem": "Fix the shared history sync", "scope": []string{"internal/sharedhistory"}, "evidence": []string{repo.WorkspaceRoot.String() + "/private.txt"}, "agent_type": "codex"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeToolCall, Adapter: primitives.AdapterCodex, SourceID: "test:tool-call", Payload: mustTestJSON(t, map[string]any{"tool_name": "shell", "input": map[string]any{"command": "cat private.txt"}, "mutation_candidate": true})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeToolResult, Adapter: primitives.AdapterCodex, SourceID: "test:tool-result", Payload: mustTestJSON(t, map[string]any{"tool_name": "shell", "output": "raw tool result"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAssistantMessage, Adapter: primitives.AdapterCodex, SourceID: "test:assistant", Payload: mustTestJSON(t, map[string]any{"text": "Implemented token=ghp_0123456789abcdefghijklmnop"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAdapterRaw, Adapter: primitives.AdapterCodex, SourceID: "test:raw", Payload: mustTestJSON(t, map[string]any{"raw": "provider-private-payload"})},
	}
	for _, input := range inputs {
		if _, err := repo.EventLog().Append(input); err != nil {
			t.Fatalf("append %s: %v", input.Type, err)
		}
	}
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("finish turn: %v", err)
	}
	return sessionID, turnID
}

func mustTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustDevice(t *testing.T, repo *checkpoint.Repo) deviceIdentity {
	t.Helper()
	identity, err := loadOrCreateDevice(repo)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = scrubGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
