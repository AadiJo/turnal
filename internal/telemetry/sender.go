package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	CollectorURL         = "https://telemetry.turnal.dev/v1/batch"
	MaxBatchesPerRequest = 14
	MaxRequestBytes      = 64 * 1024
	MaxResponseBodyBytes = 4 * 1024
	NetworkTimeout       = 2 * time.Second
	KillSwitchBackoff    = 30 * 24 * time.Hour
)

// collectorEndpoint is intentionally empty in released builds until all network
// enablement gates in docs/telemetry.md are satisfied. It may only be set by a
// release-time linker flag; configuration and workspace files cannot alter it.
var collectorEndpoint = ""

type SendDisposition string

const (
	SendDisabled   SendDisposition = "disabled"
	SendAccepted   SendDisposition = "accepted"
	SendRetryable  SendDisposition = "retryable"
	SendRejected   SendDisposition = "rejected"
	SendKillSwitch SendDisposition = "kill_switch"
)

type SendResult struct {
	Disposition SendDisposition
	StatusCode  int
	RetryAfter  time.Duration
}

type CollectorRequest struct {
	SchemaVersion int              `json:"schema_version"`
	Batches       []DailyAggregate `json:"batches"`
}

type Sender struct {
	endpoint string
	version  string
	client   *http.Client
}

func NewSender(version string) Sender {
	return Sender{
		endpoint: collectorEndpoint,
		version:  version,
		client: &http.Client{
			Timeout: NetworkTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (sender Sender) Enabled() bool {
	return sender.endpoint == CollectorURL
}

func (sender Sender) Send(ctx context.Context, batches []DailyAggregate) (SendResult, error) {
	if strings.TrimSpace(sender.endpoint) == "" {
		return SendResult{Disposition: SendDisabled}, nil
	}
	if sender.endpoint != CollectorURL {
		return SendResult{Disposition: SendDisabled}, errors.New("telemetry collector endpoint is not allowlisted")
	}
	parsed, err := url.Parse(sender.endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "telemetry.turnal.dev" || parsed.Path != "/v1/batch" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return SendResult{Disposition: SendDisabled}, errors.New("invalid telemetry collector endpoint")
	}
	if len(batches) == 0 || len(batches) > MaxBatchesPerRequest {
		return SendResult{Disposition: SendRejected}, fmt.Errorf("telemetry request must contain between 1 and %d batches", MaxBatchesPerRequest)
	}
	for _, batch := range batches {
		if err := batch.Validate(); err != nil {
			return SendResult{Disposition: SendRejected}, err
		}
	}
	payload, err := json.Marshal(CollectorRequest{SchemaVersion: SchemaVersion, Batches: batches})
	if err != nil {
		return SendResult{Disposition: SendRejected}, fmt.Errorf("encode telemetry request: %w", err)
	}
	if len(payload) > MaxRequestBytes {
		return SendResult{Disposition: SendRejected}, fmt.Errorf("telemetry request exceeds %d bytes", MaxRequestBytes)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, sender.endpoint, bytes.NewReader(payload))
	if err != nil {
		return SendResult{Disposition: SendRejected}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if validSemver(sender.version) {
		request.Header.Set("User-Agent", "turnal/"+sender.version)
	} else {
		request.Header.Set("User-Agent", "turnal/unknown")
	}
	client := sender.client
	if client == nil {
		client = NewSender(sender.version).client
	}
	response, err := client.Do(request)
	if err != nil {
		return SendResult{Disposition: SendRetryable}, fmt.Errorf("send telemetry batch: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBodyBytes))
	return classifyResponse(response.StatusCode, response.Header), nil
}

func classifyResponse(statusCode int, header http.Header) SendResult {
	result := SendResult{StatusCode: statusCode}
	switch {
	case statusCode == http.StatusAccepted:
		result.Disposition = SendAccepted
	case statusCode == http.StatusGone:
		result.Disposition = SendKillSwitch
		result.RetryAfter = KillSwitchBackoff
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500:
		result.Disposition = SendRetryable
		result.RetryAfter = retryAfter(header)
	default:
		result.Disposition = SendRejected
	}
	return result
}

func retryAfter(header http.Header) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	seconds, err := time.ParseDuration(value + "s")
	if err != nil || seconds < 0 || seconds > 24*time.Hour {
		return 0
	}
	return seconds
}
