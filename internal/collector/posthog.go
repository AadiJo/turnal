package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

const (
	PostHogUSHost      = "https://us.i.posthog.com"
	PostHogEUHost      = "https://eu.i.posthog.com"
	PostHogTimeout     = 5 * time.Second
	maxPostHogResponse = 4 * 1024
	maxPostHogRequest  = 20 * 1024 * 1024
	postHogUserAgent   = "turnal-collector/1"
)

type PostHogProperties struct {
	DistinctID           string                   `json:"distinct_id"`
	ProcessPersonProfile bool                     `json:"$process_person_profile"`
	GeoIPDisabled        bool                     `json:"$geoip_disable"`
	InsertID             string                   `json:"$insert_id"`
	SchemaVersion        int                      `json:"schema_version"`
	Metric               *telemetry.MetricKey     `json:"metric,omitempty"`
	Count                *uint64                  `json:"count,omitempty"`
	TurnalVersion        string                   `json:"turnal_version"`
	Channel              telemetry.ReleaseChannel `json:"channel"`
	InstallSource        telemetry.InstallSource  `json:"install_source"`
	OS                   string                   `json:"os"`
	Arch                 string                   `json:"arch"`
	EventDate            string                   `json:"event_date"`
	BatchID              string                   `json:"batch_id"`
}

type PostHogEvent struct {
	Event      string            `json:"event"`
	Properties PostHogProperties `json:"properties"`
	Timestamp  string            `json:"timestamp"`
}

type PostHogBatch struct {
	APIKey string         `json:"api_key"`
	Batch  []PostHogEvent `json:"batch"`
}

func TranslatePostHog(apiKey string, aggregate telemetry.DailyAggregate) (PostHogBatch, error) {
	if strings.TrimSpace(apiKey) == "" {
		return PostHogBatch{}, errors.New("PostHog project token is required")
	}
	if err := aggregate.Validate(); err != nil {
		return PostHogBatch{}, err
	}
	date, _ := time.Parse(time.DateOnly, aggregate.Date)
	timestamp := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	base := PostHogProperties{
		DistinctID:           aggregate.AnonymousID.String(),
		ProcessPersonProfile: false,
		GeoIPDisabled:        true,
		SchemaVersion:        telemetry.SchemaVersion,
		TurnalVersion:        aggregate.Build.Version,
		Channel:              aggregate.Build.Channel,
		InstallSource:        aggregate.Build.InstallSource,
		OS:                   aggregate.Build.OS,
		Arch:                 aggregate.Build.Arch,
		EventDate:            aggregate.Date,
		BatchID:              aggregate.BatchID.String(),
	}
	activeProperties := base
	activeProperties.InsertID = aggregate.BatchID.String() + ":daily_active"
	events := []PostHogEvent{{
		Event:      "turnal_daily_active",
		Properties: activeProperties,
		Timestamp:  timestamp,
	}}
	for _, metric := range aggregate.Metrics {
		key := metric.Key
		count := metric.Count
		properties := base
		properties.InsertID = aggregate.BatchID.String() + ":" + key.String()
		properties.Metric = &key
		properties.Count = &count
		events = append(events, PostHogEvent{
			Event:      "turnal_metric",
			Properties: properties,
			Timestamp:  timestamp,
		})
	}
	return PostHogBatch{APIKey: apiKey, Batch: events}, nil
}

type DeliveryDisposition string

const (
	DeliveryDelivered DeliveryDisposition = "delivered"
	DeliveryRetryable DeliveryDisposition = "retryable"
	DeliveryRejected  DeliveryDisposition = "rejected"
)

type DeliveryResult struct {
	Disposition DeliveryDisposition
	Code        string
	StatusCode  int
	RetryAfter  time.Duration
}

type DeliveryClient interface {
	Deliver(context.Context, telemetry.DailyAggregate) (DeliveryResult, error)
}

type PostHogClient struct {
	host   string
	token  string
	client *http.Client
}

type PostHogConfig struct {
	Host   string
	Token  string
	Client *http.Client
}

func NewPostHogClient(config PostHogConfig) (*PostHogClient, error) {
	host := strings.TrimRight(strings.TrimSpace(config.Host), "/")
	if host == "" {
		host = PostHogUSHost
	}
	if host != PostHogUSHost && host != PostHogEUHost {
		return nil, errors.New("PostHog host is not allowlisted")
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid PostHog host")
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("PostHog project token is required")
	}
	client := config.Client
	if client == nil {
		client = &http.Client{
			Timeout: PostHogTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &PostHogClient{host: host, token: config.Token, client: client}, nil
}

func (client *PostHogClient) Deliver(ctx context.Context, aggregate telemetry.DailyAggregate) (DeliveryResult, error) {
	payload, err := TranslatePostHog(client.token, aggregate)
	if err != nil {
		return DeliveryResult{Disposition: DeliveryRejected, Code: "translation_invalid"}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return DeliveryResult{Disposition: DeliveryRejected, Code: "encoding_error"}, err
	}
	if len(encoded) > maxPostHogRequest {
		return DeliveryResult{Disposition: DeliveryRejected, Code: "request_too_large"}, errors.New("PostHog request exceeds limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.host+"/batch/", bytes.NewReader(encoded))
	if err != nil {
		return DeliveryResult{Disposition: DeliveryRejected, Code: "request_invalid"}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", postHogUserAgent)
	response, err := client.client.Do(request)
	if err != nil {
		return DeliveryResult{Disposition: DeliveryRetryable, Code: "network_error"}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPostHogResponse+1))
	if err != nil {
		return DeliveryResult{Disposition: DeliveryRetryable, Code: "response_read_error", StatusCode: response.StatusCode}, err
	}
	if len(body) > maxPostHogResponse {
		return DeliveryResult{Disposition: DeliveryRetryable, Code: "response_too_large", StatusCode: response.StatusCode}, errors.New("PostHog response exceeds limit")
	}
	result := classifyPostHogResponse(response.StatusCode, response.Header)
	if result.Disposition != DeliveryDelivered {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var acknowledgement map[string]any
	if err := decoder.Decode(&acknowledgement); err != nil || len(acknowledgement) == 0 {
		return DeliveryResult{Disposition: DeliveryRetryable, Code: "invalid_ack", StatusCode: response.StatusCode}, errors.New("PostHog returned an invalid acknowledgement")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DeliveryResult{Disposition: DeliveryRetryable, Code: "invalid_ack", StatusCode: response.StatusCode}, errors.New("PostHog returned a trailing acknowledgement value")
	}
	return result, nil
}

func classifyPostHogResponse(status int, header http.Header) DeliveryResult {
	result := DeliveryResult{StatusCode: status}
	switch {
	case status >= 200 && status < 300:
		result.Disposition = DeliveryDelivered
		result.Code = "accepted"
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		result.Disposition = DeliveryRetryable
		result.Code = "posthog_retryable"
		result.RetryAfter = parseRetryAfter(header.Get("Retry-After"))
	default:
		result.Disposition = DeliveryRejected
		result.Code = "posthog_rejected"
	}
	return result
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 || seconds > 86400 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
