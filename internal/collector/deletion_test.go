package collector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestPostHogDeletionClientQueuesByDistinctIDAndCountsEvents(t *testing.T) {
	id, err := telemetry.ParseUUID("167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	if err != nil {
		t.Fatal(err)
	}
	transport := &deletionTransport{t: t, id: id.String()}
	client, err := NewPostHogDeletionClient(PostHogDeletionConfig{
		AppHost:        PostHogUSAppHost,
		ProjectID:      42,
		PersonalAPIKey: "phx_personal",
		Client:         &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Request(context.Background(), id)
	if err != nil || !response.EventsQueuedForDeletion || response.PersonsFound != 0 {
		t.Fatalf("Request() = %#v, %v", response, err)
	}
	count, err := client.CountEvents(context.Background(), id)
	if err != nil || count != 0 {
		t.Fatalf("CountEvents() = %d, %v", count, err)
	}
	if transport.calls != 2 {
		t.Fatalf("API calls = %d, want 2", transport.calls)
	}
}

func TestPostHogDeletionClientRejectsUnsafeConfigurationAndErrors(t *testing.T) {
	if _, err := NewPostHogDeletionClient(PostHogDeletionConfig{AppHost: "https://example.com", ProjectID: 1, PersonalAPIKey: "key"}); err == nil {
		t.Fatal("unapproved app host accepted")
	}
	if _, err := NewPostHogDeletionClient(PostHogDeletionConfig{ProjectID: 0, PersonalAPIKey: "key"}); err == nil {
		t.Fatal("missing project ID accepted")
	}
	id, _ := telemetry.ParseUUID("167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	client, err := NewPostHogDeletionClient(PostHogDeletionConfig{
		ProjectID:      1,
		PersonalAPIKey: "key",
		Client: &http.Client{Transport: deletionRoundTrip(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"events_queued_for_deletion":false}`)), Header: make(http.Header), Request: request}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Request(context.Background(), id); err == nil {
		t.Fatal("unconfirmed event deletion accepted")
	}
}

func TestPostHogDeletionClientCountsMarkedCanary(t *testing.T) {
	client, err := NewPostHogDeletionClient(PostHogDeletionConfig{
		ProjectID:      7,
		PersonalAPIKey: "key",
		Client: &http.Client{Transport: deletionRoundTrip(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			if request.URL.Path != "/api/projects/7/query/" || !strings.Contains(string(body), "collector-canary:167e8e5d-84fc-46bd-a39c-b67d47658f8e") || !strings.Contains(string(body), "collector_canary = true") {
				t.Fatalf("canary query = %s %s", request.URL.Path, body)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[[1]]}`)), Header: make(http.Header), Request: request}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := client.CountCanary(context.Background(), "collector-canary:167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	if err != nil || count != 1 {
		t.Fatalf("CountCanary() = %d, %v", count, err)
	}
	if _, err := client.CountCanary(context.Background(), "not-a-canary"); err == nil {
		t.Fatal("invalid canary ID accepted")
	}
}

type deletionTransport struct {
	t     *testing.T
	id    string
	calls int
}

func (transport *deletionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.t.Helper()
	transport.calls++
	if request.Header.Get("Authorization") != "Bearer phx_personal" {
		transport.t.Fatalf("authorization header missing")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		transport.t.Fatal(err)
	}
	var status int
	var response string
	switch request.URL.Path {
	case "/api/projects/42/persons/bulk_delete/":
		var payload struct {
			DistinctIDs  []string `json:"distinct_ids"`
			DeleteEvents bool     `json:"delete_events"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			transport.t.Fatal(err)
		}
		if len(payload.DistinctIDs) != 1 || payload.DistinctIDs[0] != transport.id || !payload.DeleteEvents {
			transport.t.Fatalf("bulk-delete payload = %s", body)
		}
		status = http.StatusAccepted
		response = `{"persons_found":0,"persons_deleted":0,"events_queued_for_deletion":true,"recordings_queued_for_deletion":true,"deletion_errors":[]}`
	case "/api/projects/42/query/":
		if !strings.Contains(string(body), transport.id) || !strings.Contains(string(body), "turnal_metric") {
			transport.t.Fatalf("count query = %s", body)
		}
		status = http.StatusOK
		response = `{"results":[[0]]}`
	default:
		transport.t.Fatalf("unexpected path %s", request.URL.Path)
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(response)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

type deletionRoundTrip func(*http.Request) (*http.Response, error)

func (function deletionRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
