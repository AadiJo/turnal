package viewer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/projects"
)

const (
	viewerHeader = "X-Turnal-Viewer"
	// viewerWriteHeader carries the session token on state-changing requests.
	// A cross-origin page cannot read the HttpOnly cookie, so it cannot forge
	// this header even if a browser attaches the cookie.
	viewerWriteHeader  = "X-Turnal-Write"
	viewerCookie       = "turnal_viewer_session"
	viewerSessionTTL   = 8 * time.Hour
	viewerReadTimeout  = 15 * time.Second
	viewerWriteTimeout = 45 * time.Second
)

type Options struct {
	// Repo is optional. When turnal ui runs inside a recorded project that
	// project is preselected; from anywhere else the global index opens.
	Repo           *checkpoint.Repo
	Port           int
	NoOpen         bool
	InitialSession string
	Out            io.Writer
}

type Server struct {
	registry     *registry
	currentStore string
	assets       fs.FS
	launchPath   string
	launchSecret string
	sessionToken string
	authMu       sync.Mutex
	bootstrapped bool
	sessionUntil time.Time
	expectedHost string
	expectedBase string
	startedAt    time.Time
}

// NewServer builds a viewer over every registered project. The optional repo
// only preselects a project; the server is never scoped to it.
func NewServer(db *projects.DB, current *checkpoint.Repo) (*Server, error) {
	if db == nil {
		return nil, fmt.Errorf("viewer requires an open project index")
	}
	assets, err := productionAssets()
	if err != nil {
		return nil, fmt.Errorf("open embedded viewer assets: %w", err)
	}
	launchPath, err := randomToken(18)
	if err != nil {
		return nil, err
	}
	launchSecret, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	sessionToken, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	server := &Server{
		registry: newRegistry(db), assets: assets, launchPath: launchPath,
		launchSecret: launchSecret, sessionToken: sessionToken,
		startedAt: time.Now().UTC(),
	}
	if current != nil {
		server.currentStore = current.StoreID.String()
	}
	return server, nil
}

func Run(ctx context.Context, options Options) error {
	if options.Port < 0 || options.Port > 65535 {
		return fmt.Errorf("viewer port must be between 0 and 65535")
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	db, err := projects.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	server, err := NewServer(db, options.Repo)
	if err != nil {
		return err
	}
	// Populate the index before serving so the first page load is not empty.
	if err := server.registry.refresh(ctx); err != nil {
		return fmt.Errorf("index recorded projects: %w", err)
	}
	indexed, err := db.Projects(ctx)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(options.Port))
	if err != nil {
		return fmt.Errorf("start loopback viewer: %w", err)
	}
	defer listener.Close()
	server.expectedHost = listener.Addr().String()
	server.expectedBase = "http://" + server.expectedHost

	launchURL := server.expectedBase + "/" + server.launchPath + "/"
	query := url.Values{}
	if server.currentStore != "" {
		query.Set("project", server.currentStore)
	}
	if strings.TrimSpace(options.InitialSession) != "" {
		if options.Repo == nil {
			return fmt.Errorf("--session requires running inside a recorded project")
		}
		service, err := server.registry.service(ctx, server.currentStore)
		if err != nil {
			return fmt.Errorf("resolve initial viewer session: %w", err)
		}
		sessions, err := service.Sessions(ctx)
		if err != nil {
			return fmt.Errorf("resolve initial viewer session: %w", err)
		}
		initialKey, err := resolveInitialSession(options.InitialSession, sessions)
		if err != nil {
			return err
		}
		query.Set("session", initialKey)
	}
	if encoded := query.Encode(); encoded != "" {
		launchURL += "?" + encoded
	}
	launchURL += "#token=" + url.QueryEscape(server.launchSecret)

	fmt.Fprintf(options.Out, "Turnal Prism:  %s\n", launchURL)
	fmt.Fprintf(options.Out, "Projects:      %d indexed\n", len(indexed))
	if options.Repo != nil {
		fmt.Fprintf(options.Out, "Workspace:     %s\n", options.Repo.WorkspaceRoot)
	}
	fmt.Fprintf(options.Out, "Index:         %s\n", db.Path())
	fmt.Fprintf(options.Out, "PID:           %d\n", os.Getpid())
	fmt.Fprintln(options.Out, "Stop:          press Ctrl-C")

	if !options.NoOpen {
		if err := openBrowser(launchURL); err != nil {
			fmt.Fprintf(options.Out, "Browser:       could not open automatically: %v\n", err)
		}
	}

	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: viewerReadTimeout,
		ReadTimeout:       viewerReadTimeout,
		WriteTimeout:      viewerWriteTimeout,
		IdleTimeout:       60 * time.Second,
	}
	signalContext, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	errorChannel := make(chan error, 1)
	go func() {
		errorChannel <- httpServer.Serve(listener)
	}()

	select {
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("stop viewer: %w", err)
		}
		serveErr := <-errorChannel
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	case err := <-errorChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func resolveInitialSession(displayID string, sessions []SessionSummaryView) (string, error) {
	displayID = strings.TrimSpace(displayID)
	var matches []SessionSummaryView
	for _, session := range sessions {
		if session.ID == displayID {
			matches = append(matches, session)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("viewer session %q was not found", displayID)
	case 1:
		return matches[0].Key, nil
	default:
		return "", fmt.Errorf("viewer session %q is ambiguous across %d event streams; open Prism and select the canonical stream", displayID, len(matches))
	}
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.securityHeaders(response)
	if request.Host != server.expectedHost {
		http.Error(response, "forbidden host", http.StatusForbidden)
		return
	}
	prefix := "/" + server.launchPath + "/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(response, request)
		return
	}
	relative := strings.TrimPrefix(request.URL.Path, prefix)
	if strings.HasPrefix(relative, "api/v1/") {
		server.serveAPI(response, request, strings.TrimPrefix(relative, "api/v1/"))
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	server.serveAsset(response, request, relative)
}

func (server *Server) serveAPI(response http.ResponseWriter, request *http.Request, route string) {
	if route == "auth/bootstrap" {
		server.bootstrap(response, request)
		return
	}
	if !server.originAllowed(request) || request.Header.Get(viewerHeader) != "1" || !server.authenticated(request) {
		server.writeError(response, http.StatusUnauthorized, "viewer_locked", "Relaunch the viewer from the Turnal CLI.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	ctx := request.Context()
	segments := strings.Split(strings.Trim(route, "/"), "/")

	// Adding and removing projects are the only write routes. They are state
	// changing, so they additionally require the session token echoed in a
	// header, which a cross-origin caller cannot read from the cookie.
	if request.Method == http.MethodPost && route == "projects" {
		server.addProject(response, request)
		return
	}
	// Choosing a directory shows a native dialog, which spawns a process, so it
	// is gated like the other state-changing routes even though it writes nothing.
	if request.Method == http.MethodPost && route == "pick-directory" {
		server.pickDirectory(response, request)
		return
	}
	if request.Method == http.MethodDelete && len(segments) == 2 && segments[0] == "projects" {
		server.removeProject(response, request, segments[1])
		return
	}
	if request.Method != http.MethodGet {
		server.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This viewer endpoint is read-only.")
		return
	}

	// Global routes come first; everything else is scoped to one project so a
	// key minted for one store can never resolve inside another.
	switch route {
	case "index":
		server.writeIndex(response, request)
		return
	case "projects":
		list, err := server.registry.db.Projects(ctx)
		if err != nil {
			server.failRead(response, err)
			return
		}
		server.writeJSON(response, http.StatusOK, projectViews(list))
		return
	case "activity":
		limit := 0
		if value := request.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				server.writeError(response, http.StatusBadRequest, "invalid_limit", "Activity limit must be an integer.")
				return
			}
			limit = parsed
		}
		list, err := server.registry.db.Activity(ctx, limit)
		if err != nil {
			server.failRead(response, err)
			return
		}
		server.writeJSON(response, http.StatusOK, activityViews(list))
		return
	case "refresh":
		if err := server.registry.refresh(ctx); err != nil {
			server.failRead(response, err)
			return
		}
		server.writeIndex(response, request)
		return
	}

	if len(segments) < 3 || segments[0] != "projects" {
		server.writeError(response, http.StatusNotFound, "not_found", "Viewer resource not found.")
		return
	}
	storeID := segments[1]
	service, err := server.registry.service(ctx, storeID)
	if err != nil {
		server.writeError(response, http.StatusNotFound, "unknown_project", err.Error())
		return
	}
	scoped := segments[2:]
	query := request.URL.Query()

	var result any
	switch {
	case len(scoped) == 1 && scoped[0] == "workspace":
		result, err = service.Workspace(ctx)
	case len(scoped) == 1 && scoped[0] == "sessions":
		result, err = service.Sessions(ctx)
	case len(scoped) == 3 && scoped[0] == "sessions" && scoped[2] == "turns":
		result, err = service.SessionTurns(ctx, scoped[1])
	case len(scoped) == 2 && scoped[0] == "turns":
		result, err = service.Turn(ctx, scoped[1])
	case len(scoped) == 2 && scoped[0] == "diffs":
		result, err = service.Diff(ctx, scoped[1])
	case len(scoped) == 3 && scoped[0] == "diffs" && scoped[2] == "file":
		result, err = service.Patch(ctx, scoped[1], query.Get("path"))
	case len(scoped) == 2 && scoped[0] == "blame":
		line := 0
		if value := query.Get("line"); value != "" {
			line, err = strconv.Atoi(value)
		}
		if err == nil {
			result, err = service.Blame(ctx, scoped[1], query.Get("path"), line)
		}
	default:
		server.writeError(response, http.StatusNotFound, "not_found", "Viewer resource not found.")
		return
	}
	if err != nil {
		server.failRead(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) failRead(response http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusRequestTimeout
	}
	server.writeError(response, status, "viewer_read_failed", err.Error())
}

func (server *Server) writeIndex(response http.ResponseWriter, request *http.Request) {
	list, err := server.registry.db.Projects(request.Context())
	if err != nil {
		server.failRead(response, err)
		return
	}
	registryFile, err := checkpoint.RegistryPath()
	if err != nil {
		server.failRead(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, IndexView{
		Projects:        projectViews(list),
		DBPath:          server.registry.db.Path(),
		RegistryPath:    registryFile,
		ReadOnly:        true,
		NetworkSilent:   true,
		ViewerStartedAt: server.startedAt,
		CurrentStoreID:  server.currentStore,
	})
}

// writeAllowed gates the two state-changing routes. The session cookie alone is
// not enough: a state change also requires the token echoed in a header, which
// only same-origin script that completed bootstrap can supply.
func (server *Server) writeAllowed(request *http.Request) bool {
	server.authMu.Lock()
	token := server.sessionToken
	server.authMu.Unlock()
	return subtle.ConstantTimeCompare([]byte(request.Header.Get(viewerWriteHeader)), []byte(token)) == 1
}

func (server *Server) addProject(response http.ResponseWriter, request *http.Request) {
	if !server.writeAllowed(request) {
		server.writeError(response, http.StatusForbidden, "write_rejected", "This request is missing the viewer write token.")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 8192)
	defer request.Body.Close()
	var input AddProjectRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		server.writeError(response, http.StatusBadRequest, "invalid_request", "Add project payload is invalid.")
		return
	}
	result, err := server.registry.addProject(request.Context(), input)
	if err != nil {
		server.writeError(response, http.StatusBadRequest, "add_project_failed", err.Error())
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

// pickDirectory shows the operating system's folder chooser and returns the
// selected path. A browser file input reports only a file name, never a usable
// filesystem path, so the viewer asks the platform instead of making the user
// type one. Cancelling is reported as an ordinary outcome, and a machine with no
// dialog available says so, letting the UI fall back to a text field.
func (server *Server) pickDirectory(response http.ResponseWriter, request *http.Request) {
	if !server.writeAllowed(request) {
		server.writeError(response, http.StatusForbidden, "write_rejected", "This request is missing the viewer write token.")
		return
	}
	selected, err := pickDirectory(request.Context(), defaultPickerStart())
	if err != nil {
		var cancelled ErrPickerCancelled
		if errors.As(err, &cancelled) {
			server.writeJSON(response, http.StatusOK, map[string]any{"cancelled": true})
			return
		}
		var unavailable ErrPickerUnavailable
		if errors.As(err, &unavailable) {
			server.writeError(response, http.StatusNotImplemented, "picker_unavailable", unavailable.Error())
			return
		}
		server.writeError(response, http.StatusBadRequest, "picker_failed", err.Error())
		return
	}
	server.writeJSON(response, http.StatusOK, map[string]any{"cancelled": false, "directory": selected})
}

func (server *Server) removeProject(response http.ResponseWriter, request *http.Request, storeID string) {
	if !server.writeAllowed(request) {
		server.writeError(response, http.StatusForbidden, "write_rejected", "This request is missing the viewer write token.")
		return
	}
	if err := server.registry.db.Deregister(request.Context(), storeID); err != nil {
		server.writeError(response, http.StatusBadRequest, "remove_project_failed", err.Error())
		return
	}
	server.registry.forget(storeID)
	server.writeJSON(response, http.StatusOK, map[string]any{
		"ok": true, "store_id": storeID, "history_kept": true,
	})
}

func (server *Server) bootstrap(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Bootstrap requires POST.")
		return
	}
	if !server.originAllowed(request) || request.Header.Get(viewerHeader) != "1" {
		server.writeError(response, http.StatusForbidden, "origin_rejected", "Viewer origin rejected.")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 4096)
	defer request.Body.Close()
	var input struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		server.writeError(response, http.StatusBadRequest, "invalid_bootstrap", "Bootstrap payload is invalid.")
		return
	}
	server.authMu.Lock()
	defer server.authMu.Unlock()
	if server.bootstrapped || subtle.ConstantTimeCompare([]byte(input.Secret), []byte(server.launchSecret)) != 1 {
		server.writeError(response, http.StatusUnauthorized, "bootstrap_rejected", "This launch token is invalid or has already been used.")
		return
	}
	server.bootstrapped = true
	server.sessionUntil = time.Now().Add(viewerSessionTTL)
	http.SetCookie(response, &http.Cookie{
		Name: viewerCookie, Value: server.sessionToken, Path: "/" + server.launchPath + "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(viewerSessionTTL.Seconds()),
	})
	// The write token is returned once, to same-origin script that proved it
	// holds the launch secret. It never travels in a URL or a readable cookie.
	server.writeJSON(response, http.StatusOK, map[string]any{
		"ok": true, "expires_at": server.sessionUntil, "write_token": server.sessionToken,
	})
}

func (server *Server) authenticated(request *http.Request) bool {
	cookie, err := request.Cookie(viewerCookie)
	if err != nil {
		return false
	}
	server.authMu.Lock()
	defer server.authMu.Unlock()
	return server.bootstrapped && time.Now().Before(server.sessionUntil) &&
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(server.sessionToken)) == 1
}

func (server *Server) originAllowed(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin == "" || origin == server.expectedBase
}

func (server *Server) serveAsset(response http.ResponseWriter, request *http.Request, relative string) {
	relative = path.Clean("/" + relative)
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" || relative == "." || !strings.Contains(path.Base(relative), ".") {
		server.serveIndex(response, request)
		return
	}
	data, err := fs.ReadFile(server.assets, relative)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	info, err := fs.Stat(server.assets, relative)
	if err != nil || info.IsDir() {
		http.NotFound(response, request)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(relative)); contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	if strings.HasPrefix(relative, "assets/") {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		response.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(response, request, relative, info.ModTime(), bytes.NewReader(data))
}

func (server *Server) serveIndex(response http.ResponseWriter, request *http.Request) {
	content, err := fs.ReadFile(server.assets, "index.html")
	if err != nil {
		http.Error(response, "viewer assets unavailable", http.StatusInternalServerError)
		return
	}
	base := "/" + server.launchPath + "/"
	content = []byte(strings.ReplaceAll(string(content), "/__TURNAL_BASE__/", base))
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if request.Method != http.MethodHead {
		_, _ = response.Write(content)
	}
}

func (server *Server) securityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	response.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	response.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
}

func (server *Server) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func (server *Server) writeError(response http.ResponseWriter, status int, code, message string) {
	server.writeJSON(response, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate viewer secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return nil
}
