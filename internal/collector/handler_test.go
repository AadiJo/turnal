package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestEphemeralRateLimiterHasBoundedCardinality(t *testing.T) {
	limiter := newEphemeralLimiter(1, time.Minute)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for index := 0; index < maxLimiterWindows; index++ {
		if !limiter.Allow(fmt.Sprintf("192.0.2.%d", index), now) {
			t.Fatalf("key %d rejected before cardinality bound", index)
		}
	}
	if limiter.Allow("new-client", now) || len(limiter.windows) != maxLimiterWindows {
		t.Fatalf("limiter cardinality = %d", len(limiter.windows))
	}
	if !limiter.Allow("new-client", now.Add(time.Minute)) || len(limiter.windows) > maxLimiterWindows {
		t.Fatalf("expired limiter windows were not reclaimed: %d", len(limiter.windows))
	}
}

func TestHandlerDurablyAcceptsAndDeduplicates(t *testing.T) {
	store := testStore(t)
	handler := testHandler(t, store, HandlerConfig{Enabled: true, RequireHTTPS: true})
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	payload := telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch}}

	first := performBatchRequest(t, handler, payload)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}
	var accepted acceptanceResponse
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Status != "durably_accepted" || accepted.Accepted != 1 || accepted.Duplicates != 0 {
		t.Fatalf("first acceptance = %#v", accepted)
	}
	second := performBatchRequest(t, handler, payload)
	if second.Code != http.StatusAccepted {
		t.Fatalf("replay response = %d %s", second.Code, second.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Accepted != 0 || accepted.Duplicates != 1 {
		t.Fatalf("replay acceptance = %#v", accepted)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.DurablyAccepted != 1 {
		t.Fatalf("stats = %#v, %v", stats, err)
	}
}

func TestHandlerRejectsUnknownFieldsInvalidDatesAndDuplicateIDs(t *testing.T) {
	store := testStore(t)
	handler := testHandler(t, store, HandlerConfig{Enabled: true})
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	valid, err := json.Marshal(telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch}})
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"workspace":"secret"`), 1)
	for name, data := range map[string][]byte{
		"unknown field": unknown,
		"trailing JSON": append(append([]byte{}, valid...), []byte(`{}`)...),
		"future date":   bytes.Replace(valid, []byte("2026-07-10"), []byte("2026-07-11"), 1),
		"expired date":  bytes.Replace(valid, []byte("2026-07-10"), []byte("2026-06-25"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", bytes.NewReader(data))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"code\":\"invalid_payload\"}\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
	duplicatePayload := telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch, batch}}
	response := performBatchRequest(t, handler, duplicatePayload)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate request = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerEnforcesMethodHTTPSContentTypeBodyAndRateLimits(t *testing.T) {
	store := testStore(t)
	handler := testHandler(t, store, HandlerConfig{Enabled: true, RequireHTTPS: true, RateLimitPerMinute: 1})
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	payload := telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch}}
	data, _ := json.Marshal(payload)

	get := httptest.NewRequest(http.MethodGet, "https://telemetry.turnal.dev/v1/batch", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", getResponse.Code)
	}

	httpRequest := httptest.NewRequest(http.MethodPost, "http://telemetry.turnal.dev/v1/batch", bytes.NewReader(data))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse := httptest.NewRecorder()
	handler.ServeHTTP(httpResponse, httpRequest)
	if httpResponse.Code != http.StatusUpgradeRequired {
		t.Fatalf("HTTP status = %d", httpResponse.Code)
	}

	typeRequest := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", bytes.NewReader(data))
	typeResponse := httptest.NewRecorder()
	handler.ServeHTTP(typeResponse, typeRequest)
	if typeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type status = %d", typeResponse.Code)
	}

	first := performBatchRequest(t, handler, payload)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first limited request = %d", first.Code)
	}
	second := performBatchRequest(t, handler, payload)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "60" {
		t.Fatalf("rate-limited response = %d %s", second.Code, second.Body.String())
	}

	largeBody := `{"schema_version":1,"batches":[],"padding":"` + strings.Repeat("x", telemetry.MaxRequestBytes) + `"}`
	large := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", strings.NewReader(largeBody))
	large.Header.Set("Content-Type", "application/json")
	large.RemoteAddr = "other:1234"
	largeResponse := httptest.NewRecorder()
	handler.ServeHTTP(largeResponse, large)
	if largeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large response = %d %s", largeResponse.Code, largeResponse.Body.String())
	}
}

func TestHandlerKillSwitchAndDeletionReturnStableResponses(t *testing.T) {
	store := testStore(t)
	disabled := testHandler(t, store, HandlerConfig{Enabled: false})
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	payload := telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch}}
	response := performBatchRequest(t, disabled, payload)
	if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "collection_disabled") || response.Header().Get("Retry-After") == "" {
		t.Fatalf("kill switch response = %d %s", response.Code, response.Body.String())
	}

	if _, err := store.BeginDeletion(context.Background(), batch.AnonymousID, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	enabled := testHandler(t, store, HandlerConfig{Enabled: true})
	response = performBatchRequest(t, enabled, payload)
	if response.Code != http.StatusGone || response.Body.String() != "{\"code\":\"installation_deleted\"}\n" {
		t.Fatalf("deleted response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerDoesNotEchoSensitiveInvalidPayload(t *testing.T) {
	store := testStore(t)
	handler := testHandler(t, store, HandlerConfig{Enabled: true})
	secret := `{"prompt":"sk-live-secret","path":"/home/alice/private"}`
	request := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", strings.NewReader(secret))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "private-hostname")
	request.RemoteAddr = "192.0.2.10:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
	for _, forbidden := range []string{"sk-live-secret", "/home/alice/private", "private-hostname", "192.0.2.10"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestHandlerOperationalMonitorRecordsStableCounters(t *testing.T) {
	store := testStore(t)
	monitor := NewMonitor()
	handler := testHandler(t, store, HandlerConfig{Enabled: true, Monitor: monitor})
	batch := testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1)
	response := performBatchRequest(t, handler, telemetry.CollectorRequest{SchemaVersion: telemetry.SchemaVersion, Batches: []telemetry.DailyAggregate{batch}})
	if response.Code != http.StatusAccepted {
		t.Fatalf("accepted status = %d", response.Code)
	}
	invalid := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", strings.NewReader(`{"schema_version":2,"batches":[]}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.RemoteAddr = "192.0.2.2:4321"
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	snapshot := monitor.Snapshot()
	if snapshot.Requests != 2 || snapshot.AcceptedResponses != 1 || snapshot.AcceptedBatches != 1 || snapshot.SchemaRejected != 1 {
		t.Fatalf("monitor snapshot = %#v", snapshot)
	}
}

func testHandler(t *testing.T, store *Store, config HandlerConfig) *Handler {
	t.Helper()
	config.Now = func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }
	handler, err := NewHandler(store, config)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func performBatchRequest(t *testing.T, handler http.Handler, payload telemetry.CollectorRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://telemetry.turnal.dev/v1/batch", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "192.0.2.1:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
