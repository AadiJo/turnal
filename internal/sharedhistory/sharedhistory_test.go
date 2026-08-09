package sharedhistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
			want:          `[PATH_REDACTED] "$WORKSPACE/file"`,
		},
		{
			name:          "quoted before ambiguous path",
			workspaceRoot: "/home/alice/project",
			text:          `Use "/home/alice/project/file" then /opt/Secret Project/private.txt`,
			want:          `[PATH_REDACTED] "$WORKSPACE/file"`,
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
	} {
		truncations := Truncations{}
		got := sanitizeText("/workspace", text, DefaultFieldLimit, &truncations)
		if got.Text != "[PATH_REDACTED]" || !got.Redacted {
			t.Fatalf("absolute path was not redacted as a whole: %#v", got)
		}
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
	if result.Blocked != 1 || result.Published != 0 {
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

	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err == nil || !strings.Contains(err.Error(), "rewound") {
		t.Fatalf("rewrite detection error = %v", err)
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
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("tracking recovery disappearance error = %v", err)
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
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("RepoID transition reused the old observation cursor: %v", err)
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
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("invalid batch pull error = %v", err)
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
	if _, err := New(receiver).Sync(context.Background(), DirectionPull); err == nil || !strings.Contains(err.Error(), "rewrites an existing immutable path") {
		t.Fatalf("signed rewrite error = %v", err)
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
	policy, err := loadPolicy(repo)
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
