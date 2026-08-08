package sharedhistory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestPreviewProjectsOnlyAllowlistedRedactedContext(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	remote := filepath.Join(t.TempDir(), "history.git")
	if _, err := Configure(repo, remote, PromptModeRedactedText); err != nil {
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
	if err := verifyStoredBundle(repo, StoredBundle{Manifest: plan.Manifest, Events: plan.Events, PublicKey: mustDevice(t, repo).PublicKey}); err != nil {
		t.Fatalf("verify preview bundle: %v", err)
	}
}

func TestPromptOmissionIsTypedAndDoesNotPublishText(t *testing.T) {
	repo := newSharedHistoryTestRepo(t)
	sessionID, turnID := recordSharedHistoryTurn(t, repo)
	if _, err := Configure(repo, filepath.Join(t.TempDir(), "history.git"), PromptModeOmit); err != nil {
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
	if _, err := Configure(repo, filepath.Join(t.TempDir(), "history.git"), PromptModeRedactedText); err != nil {
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
	digest, err := policyHash(repo, policy)
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
	if _, err := Configure(repo, filepath.Join(t.TempDir(), "history.git"), PromptModeRedactedText); err != nil {
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
}

func TestGitSyncReplicatesAndRejectsRefRewrite(t *testing.T) {
	testRoot := t.TempDir()
	remote := filepath.Join(testRoot, "history.git")
	runTestGit(t, testRoot, "init", "--bare", remote)

	publisher := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "publisher"))
	sessionID, turnID := recordSharedHistoryTurn(t, publisher)
	if _, err := Configure(publisher, remote, PromptModeRedactedText); err != nil {
		t.Fatalf("configure publisher: %v", err)
	}
	plan, err := New(publisher).Preview(context.Background(), PreviewOptions{SessionID: sessionID, TurnID: turnID, Approve: true})
	if err != nil {
		t.Fatalf("approve preview: %v", err)
	}
	stageBundleWithoutState(t, publisher, sessionID, turnID)
	result, err := New(publisher).Sync(context.Background(), DirectionPush)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Published != 1 || result.Head == "" {
		t.Fatalf("push result = %#v", result)
	}

	receiver := newSharedHistoryTestRepoAt(t, filepath.Join(testRoot, "receiver"))
	// Both stores represent clones of the same logical project while retaining
	// independent store, worktree, producer, and device identities.
	receiver.RepoID = publisher.RepoID
	if _, err := Configure(receiver, remote, PromptModeOmit); err != nil {
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

func stageBundleWithoutState(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID) {
	t.Helper()
	policy, err := loadPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policyHash(repo, policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := mustDevice(t, repo)
	source, err := findCompletedTurn(repo, sessionID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildBundle(repo, identity, policy, digest, source)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := chainAnchor(repo)
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
		ChainAnchor: anchor,
		CreatedAt:   New(repo).now(),
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
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, SourceID: "test:prompt", Payload: mustTestJSON(t, map[string]any{"text": "Inspect " + repo.WorkspaceRoot.String() + "/private.txt and /etc/passwd token=ghp_0123456789abcdefghijklmnop"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAgentIntent, Adapter: primitives.AdapterCodex, SourceID: "test:intent", Payload: mustTestJSON(t, map[string]any{"problem": "Fix the shared history sync", "scope": []string{"internal/sharedhistory"}, "evidence": []string{repo.WorkspaceRoot.String() + "/private.txt"}, "agent_type": "codex"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeToolCall, Adapter: primitives.AdapterCodex, SourceID: "test:tool-call", Payload: mustTestJSON(t, map[string]any{"tool_name": "shell", "input": map[string]any{"command": "cat private.txt"}, "mutation_candidate": true})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeToolResult, Adapter: primitives.AdapterCodex, SourceID: "test:tool-result", Payload: mustTestJSON(t, map[string]any{"tool_name": "shell", "output": "raw tool result"})},
		{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeAssistantMessage, Adapter: primitives.AdapterCodex, SourceID: "test:assistant", Payload: mustTestJSON(t, map[string]any{"text": "Implemented the projection."})},
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
