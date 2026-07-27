package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRegistry(t *testing.T) {
	// The real registry answers 406 when a per-version request asks for the
	// abbreviated packument media type, so assert the header per endpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/%40aadijo%2Fturnal":
			if accept != "application/vnd.npm.install-v1+json" {
				t.Errorf("packument Accept = %q", accept)
			}
			_, _ = w.Write([]byte(`{"dist-tags":{"latest":"0.4.2","nightly":"0.4.3-nightly.1"}}`))
		case "/%40aadijo%2Fturnal/nightly":
			if accept == "application/vnd.npm.install-v1+json" {
				http.Error(w, "Not Acceptable", http.StatusNotAcceptable)
				return
			}
			if accept != "application/json" {
				t.Errorf("version Accept = %q", accept)
			}
			_, _ = w.Write([]byte(`{"version":"0.4.3-nightly.1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := HTTPRegistry{BaseURL: server.URL, Client: server.Client()}
	tags, err := registry.DistTags(context.Background())
	if err != nil {
		t.Fatalf("DistTags: %v", err)
	}
	if tags["latest"] != "0.4.2" || tags["nightly"] != "0.4.3-nightly.1" {
		t.Fatalf("tags = %#v", tags)
	}
	version, err := registry.Version(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "0.4.3-nightly.1" {
		t.Fatalf("version = %q", version)
	}
}

func TestHTTPRegistryRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", (10<<20)+1)))
	}))
	defer server.Close()

	_, err := (HTTPRegistry{BaseURL: server.URL, Client: server.Client()}).DistTags(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DistTags error = %v, want response size error", err)
	}
}

func TestHTTPRegistryReportsNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := (HTTPRegistry{BaseURL: server.URL, Client: server.Client()}).Version(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Version error = %v, want status error", err)
	}
}
