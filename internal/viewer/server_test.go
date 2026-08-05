package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/projects"
)

// newViewerTestServer builds a viewer over an isolated project index. The state
// directory is redirected so tests never read or write the developer's registry.
func newViewerTestServer(t *testing.T) (*Server, *checkpoint.Repo) {
	t.Helper()
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	repo := newViewerTestRepo(t)
	// The temp workspace is not a Git repo, so it is not auto-registered.
	// Adopt it explicitly, the same way turnal init and the add flow do.
	if err := repo.RegisterStore(); err != nil {
		t.Fatal(err)
	}
	db, err := projects.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server, err := NewServer(db, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.registry.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return server, repo
}

func TestServerRequiresBootstrapAndScopesViewerSession(t *testing.T) {
	server, repo := newViewerTestServer(t)
	server.expectedHost = "127.0.0.1:41731"
	server.expectedBase = "http://" + server.expectedHost
	prefix := "/" + server.launchPath + "/"

	indexRequest := httptest.NewRequest(http.MethodGet, server.expectedBase+prefix, nil)
	indexRequest.Host = server.expectedHost
	indexResponse := httptest.NewRecorder()
	server.ServeHTTP(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d, body=%s", indexResponse.Code, indexResponse.Body.String())
	}
	if strings.Contains(indexResponse.Body.String(), server.launchSecret) {
		t.Fatal("launch secret leaked into the HTML response")
	}
	if !strings.Contains(indexResponse.Body.String(), prefix+"assets/") {
		t.Fatal("runtime asset base was not installed in the HTML response")
	}

	scopedWorkspace := "api/v1/projects/" + repo.StoreID.String() + "/workspace"
	unauthorized := httptest.NewRequest(http.MethodGet, server.expectedBase+prefix+scopedWorkspace, nil)
	unauthorized.Host = server.expectedHost
	unauthorized.Header.Set(viewerHeader, "1")
	unauthorizedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResponse.Code)
	}

	body, _ := json.Marshal(map[string]string{"secret": server.launchSecret})
	bootstrapRequest := httptest.NewRequest(http.MethodPost, server.expectedBase+prefix+"api/v1/auth/bootstrap", bytes.NewReader(body))
	bootstrapRequest.Host = server.expectedHost
	bootstrapRequest.Header.Set("Origin", server.expectedBase)
	bootstrapRequest.Header.Set(viewerHeader, "1")
	bootstrapResponse := httptest.NewRecorder()
	server.ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body=%s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookies := bootstrapResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != viewerCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != prefix {
		t.Fatalf("bootstrap cookie = %#v", cookies)
	}

	workspaceRequest := httptest.NewRequest(http.MethodGet, server.expectedBase+prefix+scopedWorkspace, nil)
	workspaceRequest.Host = server.expectedHost
	workspaceRequest.Header.Set(viewerHeader, "1")
	workspaceRequest.AddCookie(cookies[0])
	workspaceResponse := httptest.NewRecorder()
	server.ServeHTTP(workspaceResponse, workspaceRequest)
	if workspaceResponse.Code != http.StatusOK {
		t.Fatalf("workspace status = %d, body=%s", workspaceResponse.Code, workspaceResponse.Body.String())
	}

	replayRequest := httptest.NewRequest(http.MethodPost, server.expectedBase+prefix+"api/v1/auth/bootstrap", bytes.NewReader(body))
	replayRequest.Host = server.expectedHost
	replayRequest.Header.Set("Origin", server.expectedBase)
	replayRequest.Header.Set(viewerHeader, "1")
	replayResponse := httptest.NewRecorder()
	server.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replayed bootstrap status = %d", replayResponse.Code)
	}

	badHostRequest := httptest.NewRequest(http.MethodGet, "http://attacker.test"+prefix, nil)
	badHostRequest.Host = "attacker.test"
	badHostResponse := httptest.NewRecorder()
	server.ServeHTTP(badHostResponse, badHostRequest)
	if badHostResponse.Code != http.StatusForbidden {
		t.Fatalf("bad host status = %d", badHostResponse.Code)
	}
}

// The write routes are the only way the viewer changes the filesystem, so they
// must reject a caller that holds the session cookie but cannot echo the write
// token. A cross-origin page can cause the cookie to be sent; it cannot read it.
func TestWriteRoutesRequireTheWriteToken(t *testing.T) {
	server, repo := newViewerTestServer(t)
	server.expectedHost = "127.0.0.1:41732"
	server.expectedBase = "http://" + server.expectedHost
	prefix := "/" + server.launchPath + "/"

	body, _ := json.Marshal(map[string]string{"secret": server.launchSecret})
	bootstrapRequest := httptest.NewRequest(http.MethodPost, server.expectedBase+prefix+"api/v1/auth/bootstrap", bytes.NewReader(body))
	bootstrapRequest.Host = server.expectedHost
	bootstrapRequest.Header.Set("Origin", server.expectedBase)
	bootstrapRequest.Header.Set(viewerHeader, "1")
	bootstrapResponse := httptest.NewRecorder()
	server.ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", bootstrapResponse.Code)
	}
	var bootstrapBody struct {
		WriteToken string `json:"write_token"`
	}
	if err := json.Unmarshal(bootstrapResponse.Body.Bytes(), &bootstrapBody); err != nil {
		t.Fatal(err)
	}
	if bootstrapBody.WriteToken == "" {
		t.Fatal("bootstrap did not return a write token")
	}
	cookie := bootstrapResponse.Result().Cookies()[0]

	for _, testCase := range []struct {
		name   string
		method string
		route  string
	}{
		{"add", http.MethodPost, "api/v1/projects"},
		{"remove", http.MethodDelete, "api/v1/projects/" + repo.StoreID.String()},
	} {
		request := httptest.NewRequest(testCase.method, server.expectedBase+prefix+testCase.route, strings.NewReader("{}"))
		request.Host = server.expectedHost
		request.Header.Set(viewerHeader, "1")
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s without write token: status = %d, body=%s", testCase.name, response.Code, response.Body.String())
		}
	}

	// With the token, remove succeeds and leaves the store on disk.
	remove := httptest.NewRequest(http.MethodDelete, server.expectedBase+prefix+"api/v1/projects/"+repo.StoreID.String(), nil)
	remove.Host = server.expectedHost
	remove.Header.Set(viewerHeader, "1")
	remove.Header.Set(viewerWriteHeader, bootstrapBody.WriteToken)
	remove.AddCookie(cookie)
	removeResponse := httptest.NewRecorder()
	server.ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body=%s", removeResponse.Code, removeResponse.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo.MetadataDir, "git")); err != nil {
		t.Fatalf("remove deleted the store from disk: %v", err)
	}
}

// A key minted for one project must not resolve through another project's path,
// and an unknown project must fail before any store is opened.
func TestUnknownProjectIsRejected(t *testing.T) {
	server, _ := newViewerTestServer(t)
	server.expectedHost = "127.0.0.1:41733"
	server.expectedBase = "http://" + server.expectedHost
	prefix := "/" + server.launchPath + "/"

	body, _ := json.Marshal(map[string]string{"secret": server.launchSecret})
	bootstrapRequest := httptest.NewRequest(http.MethodPost, server.expectedBase+prefix+"api/v1/auth/bootstrap", bytes.NewReader(body))
	bootstrapRequest.Host = server.expectedHost
	bootstrapRequest.Header.Set("Origin", server.expectedBase)
	bootstrapRequest.Header.Set(viewerHeader, "1")
	bootstrapResponse := httptest.NewRecorder()
	server.ServeHTTP(bootstrapResponse, bootstrapRequest)
	cookie := bootstrapResponse.Result().Cookies()[0]

	request := httptest.NewRequest(http.MethodGet, server.expectedBase+prefix+"api/v1/projects/store_missing/sessions", nil)
	request.Host = server.expectedHost
	request.Header.Set(viewerHeader, "1")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown project status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "unknown_project") {
		t.Fatalf("unknown project body = %s", response.Body.String())
	}
}

func TestRemovedProjectCannotBeReadThroughCachedService(t *testing.T) {
	server, repo := newViewerTestServer(t)
	if _, err := server.registry.service(context.Background(), repo.StoreID.String()); err != nil {
		t.Fatal(err)
	}
	if err := server.registry.db.Deregister(context.Background(), repo.StoreID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := server.registry.service(context.Background(), repo.StoreID.String()); err == nil {
		t.Fatal("removed project remained accessible through cached service")
	}
}

// A wrapped agent run records two sessions for one piece of work: the wrapper's
// own session and the provider's hook session. They share a checkpoint pair, so
// counting both doubles the change totals and puts a promptless twin in the
// activity feed. Only the prompted session should survive.
func TestRedundantWrapperSessionsAreDropped(t *testing.T) {
	finished := time.Date(2026, 8, 3, 18, 13, 46, 0, time.UTC)
	sessions := []SessionSummaryView{
		// The wrapper: same change shape, finishes a few seconds later, no prompt.
		{Key: "wrapper", Adapter: "codex", Additions: 11, FileCount: 1, FinishedAt: finished.Add(4 * time.Second), runID: "run-linked", captureKind: "wrapper"},
		// The provider session that actually carries the prompt.
		{Key: "hooks", Adapter: "codex", Model: "gpt-5.6-sol", PromptPreview: "Add a Usage section", Additions: 11, FileCount: 1, FinishedAt: finished, runID: "run-linked", captureKind: "provider"},
		// Same shape and time, but no durable run link: this is unrelated work.
		{Key: "coincidental", Adapter: "codex", Additions: 11, FileCount: 1, FinishedAt: finished.Add(3 * time.Second)},
		// An unrelated manual checkpoint: promptless, but nothing matches it.
		{Key: "manual", Adapter: "manual", Additions: 3, FileCount: 1, FinishedAt: finished.Add(-time.Hour)},
	}

	redundant := redundantWrapperSessions(sessions)
	if _, dropped := redundant["wrapper"]; !dropped {
		t.Fatal("the promptless wrapper twin was kept")
	}
	if _, dropped := redundant["hooks"]; dropped {
		t.Fatal("the prompted session was dropped")
	}
	if _, dropped := redundant["manual"]; dropped {
		t.Fatal("an unmatched manual checkpoint was dropped; it is the only record of that work")
	}
	if _, dropped := redundant["coincidental"]; dropped {
		t.Fatal("an unrelated session was dropped based only on matching counts and time")
	}
}

func TestResolveInitialSessionFailsClosedOnFriendlyIDAmbiguity(t *testing.T) {
	sessions := []SessionSummaryView{
		{ID: "shared", Key: "first"},
		{ID: "shared", Key: "second"},
		{ID: "unique", Key: "third"},
	}
	if key, err := resolveInitialSession("unique", sessions); err != nil || key != "third" {
		t.Fatalf("unique resolution = %q, %v", key, err)
	}
	if _, err := resolveInitialSession("shared", sessions); err == nil {
		t.Fatal("ambiguous friendly session id was accepted")
	}
	if _, err := resolveInitialSession("missing", sessions); err == nil {
		t.Fatal("missing friendly session id was accepted")
	}
}

func TestViewerWriteTimeoutCoversFolderPicker(t *testing.T) {
	if viewerWriteTimeout <= pickerTimeout {
		t.Fatalf("viewer write timeout %s must exceed picker timeout %s", viewerWriteTimeout, pickerTimeout)
	}
}

func TestActivityRouteRejectsUnboundedLimits(t *testing.T) {
	server, _ := newViewerTestServer(t)
	server.bootstrapped = true
	server.sessionUntil = time.Now().Add(time.Minute)
	request := httptest.NewRequest(http.MethodGet, "/activity?limit=101", nil)
	request.Header.Set(viewerHeader, "1")
	request.AddCookie(&http.Cookie{Name: viewerCookie, Value: server.sessionToken})
	response := httptest.NewRecorder()
	server.serveAPI(response, request, "activity")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_limit") {
		t.Fatalf("activity limit response = %d, %s", response.Code, response.Body.String())
	}
}

func TestAddProjectPersistsGitSyncChoice(t *testing.T) {
	server, _ := newViewerTestServer(t)
	target := t.TempDir()
	result, err := server.registry.addProject(context.Background(), AddProjectRequest{
		Directory: target,
		Agent:     "none",
		GitSync:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(result.StorePath, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[git_sync]") || !strings.Contains(string(data), "enabled = true") {
		t.Fatalf("git-sync choice was not persisted:\n%s", data)
	}
}

func TestAddProjectReportsPartialSetupInPlainLanguage(t *testing.T) {
	server, _ := newViewerTestServer(t)
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, ".claude", "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(AddProjectRequest{Directory: target, Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(body))
	request.Header.Set(viewerWriteHeader, server.sessionToken)
	response := httptest.NewRecorder()
	server.addProject(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("partial add response = %d, %s", response.Code, response.Body.String())
	}
	var result AddProjectResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Root != target || result.Warning == "" {
		t.Fatalf("partial add result = %#v", result)
	}
	if strings.Contains(result.Warning, ".turnal") || strings.Contains(result.Warning, "sqlite") {
		t.Fatalf("partial add warning exposed implementation details: %q", result.Warning)
	}
	if _, err := os.Stat(filepath.Join(target, ".turnal", "git")); err != nil {
		t.Fatalf("partial add hid its completed folder setup: %v", err)
	}
}
