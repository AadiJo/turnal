package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestTranslatePostHogProducesPersonlessCountAwareEvents(t *testing.T) {
	aggregate := testAggregate(t, "2026-07-10", telemetry.MetricTurnRecordedCodex, 7)
	payload, err := TranslatePostHog("phc_project", aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if payload.APIKey != "phc_project" || len(payload.Batch) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	active := payload.Batch[0]
	if active.Event != "turnal_daily_active" || active.Timestamp != "2026-07-10T12:00:00Z" || active.Properties.Metric != nil || active.Properties.Count != nil {
		t.Fatalf("active event = %#v", active)
	}
	metric := payload.Batch[1]
	if metric.Event != "turnal_metric" || metric.Properties.Metric == nil || *metric.Properties.Metric != telemetry.MetricTurnRecordedCodex || metric.Properties.Count == nil || *metric.Properties.Count != 7 {
		t.Fatalf("metric event = %#v", metric)
	}
	for _, event := range payload.Batch {
		if event.Properties.ProcessPersonProfile || !event.Properties.GeoIPDisabled || event.Properties.DistinctID != aggregate.AnonymousID.String() || event.Properties.InsertID == "" {
			t.Fatalf("privacy properties = %#v", event.Properties)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "transcript", "workspace", "repository", "hostname", "path", "raw_error"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("PostHog payload contains %q: %s", forbidden, encoded)
		}
	}
}

func TestPostHogClientValidatesAcknowledgement(t *testing.T) {
	for name, test := range map[string]struct {
		status      int
		body        string
		disposition DeliveryDisposition
		wantErr     bool
	}{
		"accepted":       {status: http.StatusOK, body: `{"status":1}`, disposition: DeliveryDelivered},
		"invalid ack":    {status: http.StatusOK, body: `not-json`, disposition: DeliveryRetryable, wantErr: true},
		"rate limited":   {status: http.StatusTooManyRequests, body: `{}`, disposition: DeliveryRetryable},
		"server failure": {status: http.StatusBadGateway, body: `{}`, disposition: DeliveryRetryable},
		"schema failure": {status: http.StatusBadRequest, body: `{}`, disposition: DeliveryRejected},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &postHogTransport{status: test.status, body: test.body, header: make(http.Header)}
			client, err := NewPostHogClient(PostHogConfig{
				Host: PostHogUSHost, Token: "phc_project", Client: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Deliver(context.Background(), testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1))
			if (err != nil) != test.wantErr || result.Disposition != test.disposition {
				t.Fatalf("Deliver() = %#v, %v", result, err)
			}
			if transport.request.URL.String() != PostHogUSHost+"/batch/" || transport.request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("request = %#v", transport.request)
			}
		})
	}
}

func TestPostHogClientRejectsHostAndRetriesNetworkFailure(t *testing.T) {
	if _, err := NewPostHogClient(PostHogConfig{Host: "https://example.com", Token: "phc_project"}); err == nil {
		t.Fatal("unapproved PostHog host accepted")
	}
	client, err := NewPostHogClient(PostHogConfig{
		Host:  PostHogEUHost,
		Token: "phc_project",
		Client: &http.Client{Transport: postHogRoundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Deliver(context.Background(), testAggregate(t, "2026-07-10", telemetry.MetricInstallationActive, 1))
	if err == nil || result.Disposition != DeliveryRetryable || result.Code != "network_error" {
		t.Fatalf("network result = %#v, %v", result, err)
	}
}

type postHogTransport struct {
	status  int
	body    string
	header  http.Header
	request *http.Request
	payload []byte
}

func (transport *postHogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	transport.payload, _ = io.ReadAll(request.Body)
	return &http.Response{
		StatusCode: transport.status,
		Header:     transport.header,
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}

type postHogRoundTrip func(*http.Request) (*http.Response, error)

func (function postHogRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
