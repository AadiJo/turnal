package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReleasedSenderIsNetworkDisabled(t *testing.T) {
	sender := NewSender("0.4.2")
	if sender.Enabled() {
		t.Fatal("released sender unexpectedly enabled")
	}
	result, err := sender.Send(context.Background(), nil)
	if err != nil || result.Disposition != SendDisabled {
		t.Fatalf("Send() = %#v, %v", result, err)
	}
}

func TestRolloutSelectionIsStableAndBounded(t *testing.T) {
	id := mustUUID(t, "167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	first := InRollout(id, 10)
	for range 20 {
		if InRollout(id, 10) != first {
			t.Fatal("rollout selection changed for the same installation ID")
		}
	}
	if InRollout(id, 0) || !InRollout(id, 100) || InRollout(UUID{}, 100) {
		t.Fatal("rollout boundary handling is invalid")
	}
	selected := 0
	for index := 1; index <= 1000; index++ {
		candidate := mustUUID(t, fmt.Sprintf("%08x-0000-4000-a000-%012x", index, index))
		if InRollout(candidate, 10) {
			selected++
		}
	}
	if selected < 70 || selected > 130 {
		t.Fatalf("10%% rollout selected %d of 1000 deterministic IDs", selected)
	}
}

func TestInvalidReleaseRolloutConfigurationFailsClosed(t *testing.T) {
	old := collectorRolloutPercent
	t.Cleanup(func() { collectorRolloutPercent = old })
	for _, value := range []string{"", "-1", "101", "not-a-number"} {
		collectorRolloutPercent = value
		if got := RolloutPercent(); got != 0 {
			t.Fatalf("RolloutPercent(%q) = %d", value, got)
		}
	}
	collectorRolloutPercent = "37"
	if got := RolloutPercent(); got != 37 {
		t.Fatalf("RolloutPercent() = %d", got)
	}
}

func TestSenderUsesFixedContractAndAcceptsOnly202(t *testing.T) {
	aggregate := testDailyAggregate(t)
	transport := &captureTransport{status: http.StatusAccepted, header: make(http.Header)}
	sender := Sender{
		endpoint: CollectorURL,
		version:  "0.4.2",
		client:   &http.Client{Transport: transport, Timeout: NetworkTimeout},
	}
	result, err := sender.Send(context.Background(), []DailyAggregate{aggregate})
	if err != nil || result.Disposition != SendAccepted {
		t.Fatalf("Send() = %#v, %v", result, err)
	}
	if transport.request.Method != http.MethodPost || transport.request.URL.String() != CollectorURL {
		t.Fatalf("request target = %s %s", transport.request.Method, transport.request.URL)
	}
	if got := transport.request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := transport.request.Header.Get("User-Agent"); got != "turnal/0.4.2" {
		t.Fatalf("User-Agent = %q", got)
	}
	for _, forbidden := range []string{"prompt", "workspace", "repository", "path", "hostname"} {
		if strings.Contains(string(transport.body), forbidden) {
			t.Fatalf("request body contains %q: %s", forbidden, transport.body)
		}
	}
}

func TestSenderResponseClassification(t *testing.T) {
	for _, test := range []struct {
		status      int
		disposition SendDisposition
	}{
		{status: http.StatusAccepted, disposition: SendAccepted},
		{status: http.StatusRequestTimeout, disposition: SendRetryable},
		{status: http.StatusTooManyRequests, disposition: SendRetryable},
		{status: http.StatusInternalServerError, disposition: SendRetryable},
		{status: http.StatusBadRequest, disposition: SendRejected},
		{status: http.StatusUnauthorized, disposition: SendRejected},
		{status: http.StatusFound, disposition: SendRejected},
		{status: http.StatusGone, disposition: SendKillSwitch},
	} {
		result := classifyResponse(test.status, make(http.Header))
		if result.Disposition != test.disposition {
			t.Fatalf("classifyResponse(%d) = %#v", test.status, result)
		}
		if test.status == http.StatusGone && result.RetryAfter != KillSwitchBackoff {
			t.Fatalf("kill-switch backoff = %s", result.RetryAfter)
		}
	}
	header := make(http.Header)
	header.Set("Retry-After", "120")
	if got := classifyResponse(http.StatusTooManyRequests, header).RetryAfter; got != 2*time.Minute {
		t.Fatalf("Retry-After = %s", got)
	}
}

func TestSenderTreatsTransportFailureAsRetryable(t *testing.T) {
	sender := Sender{
		endpoint: CollectorURL,
		version:  "0.4.2",
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
	}
	result, err := sender.Send(context.Background(), []DailyAggregate{testDailyAggregate(t)})
	if err == nil || result.Disposition != SendRetryable {
		t.Fatalf("Send() = %#v, %v", result, err)
	}
}

func TestSenderRejectsNonAllowlistedEndpointAndBatchCount(t *testing.T) {
	sender := Sender{endpoint: "https://example.com/v1/batch", version: "0.4.2"}
	result, err := sender.Send(context.Background(), []DailyAggregate{testDailyAggregate(t)})
	if err == nil || result.Disposition != SendDisabled {
		t.Fatalf("redirectable sender = %#v, %v", result, err)
	}
	sender.endpoint = CollectorURL
	many := make([]DailyAggregate, MaxBatchesPerRequest+1)
	for index := range many {
		many[index] = testDailyAggregate(t)
	}
	result, err = sender.Send(context.Background(), many)
	if err == nil || result.Disposition != SendRejected {
		t.Fatalf("oversized batch list = %#v, %v", result, err)
	}
}

type captureTransport struct {
	status  int
	header  http.Header
	request *http.Request
	body    []byte
}

func (transport *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.body = body
	return &http.Response{
		StatusCode: transport.status,
		Header:     transport.header,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    request,
	}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testDailyAggregate(t *testing.T) DailyAggregate {
	t.Helper()
	aggregate, err := NewDailyAggregate(
		mustUUID(t, "167e8e5d-84fc-46bd-a39c-b67d47658f8e"),
		time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		supportedTestBuild(),
		map[MetricKey]uint64{MetricInstallationActive: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	return aggregate
}
