package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
