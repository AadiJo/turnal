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
)

const (
	viewerHeader       = "X-Turnal-Viewer"
	viewerCookie       = "turnal_viewer_session"
	viewerSessionTTL   = 8 * time.Hour
	viewerReadTimeout  = 15 * time.Second
	viewerWriteTimeout = 45 * time.Second
)

type Options struct {
	Repo           *checkpoint.Repo
	Port           int
	NoOpen         bool
	InitialSession string
	Out            io.Writer
}

type Server struct {
	service      *Service
	assets       fs.FS
	launchPath   string
	launchSecret string
	sessionToken string
	authMu       sync.Mutex
	bootstrapped bool
	sessionUntil time.Time
	expectedHost string
	expectedBase string
}

func NewServer(repo *checkpoint.Repo) (*Server, error) {
	service, err := NewService(repo)
	if err != nil {
		return nil, err
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
	return &Server{
		service: service, assets: assets, launchPath: launchPath,
		launchSecret: launchSecret, sessionToken: sessionToken,
	}, nil
}

func Run(ctx context.Context, options Options) error {
	if options.Repo == nil {
		return fmt.Errorf("turnal ui requires an open checkpoint repository")
	}
	if options.Port < 0 || options.Port > 65535 {
		return fmt.Errorf("viewer port must be between 0 and 65535")
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	server, err := NewServer(options.Repo)
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
	if strings.TrimSpace(options.InitialSession) != "" {
		sessions, err := server.service.Sessions(ctx)
		if err != nil {
			return fmt.Errorf("resolve initial viewer session: %w", err)
		}
		initialKey, err := resolveInitialSession(options.InitialSession, sessions)
		if err != nil {
			return err
		}
		launchURL += "?session=" + url.QueryEscape(initialKey)
	}
	launchURL += "#token=" + url.QueryEscape(server.launchSecret)

	fmt.Fprintf(options.Out, "Turnal Prism:  %s\n", launchURL)
	fmt.Fprintf(options.Out, "Workspace:     %s\n", options.Repo.WorkspaceRoot)
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
	if request.Method != http.MethodGet {
		server.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This viewer endpoint is read-only.")
		return
	}
	if !server.originAllowed(request) || request.Header.Get(viewerHeader) != "1" || !server.authenticated(request) {
		server.writeError(response, http.StatusUnauthorized, "viewer_locked", "Relaunch the viewer from the Turnal CLI.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	ctx := request.Context()
	segments := strings.Split(strings.Trim(route, "/"), "/")

	var result any
	var err error
	switch {
	case route == "workspace":
		result, err = server.service.Workspace(ctx)
	case route == "sessions":
		result, err = server.service.Sessions(ctx)
	case len(segments) == 3 && segments[0] == "sessions" && segments[2] == "turns":
		result, err = server.service.SessionTurns(ctx, segments[1])
	case len(segments) == 2 && segments[0] == "turns":
		result, err = server.service.Turn(ctx, segments[1])
	case len(segments) == 2 && segments[0] == "diffs":
		result, err = server.service.Diff(ctx, segments[1])
	case len(segments) == 3 && segments[0] == "diffs" && segments[2] == "file":
		result, err = server.service.Patch(ctx, segments[1], request.URL.Query().Get("path"))
	case len(segments) == 2 && segments[0] == "blame":
		line := 0
		if value := request.URL.Query().Get("line"); value != "" {
			line, err = strconv.Atoi(value)
		}
		if err == nil {
			result, err = server.service.Blame(ctx, segments[1], request.URL.Query().Get("path"), line)
		}
	default:
		server.writeError(response, http.StatusNotFound, "not_found", "Viewer resource not found.")
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusRequestTimeout
		}
		server.writeError(response, status, "viewer_read_failed", err.Error())
		return
	}
	server.writeJSON(response, http.StatusOK, result)
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
	server.writeJSON(response, http.StatusOK, map[string]any{"ok": true, "expires_at": server.sessionUntil})
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
