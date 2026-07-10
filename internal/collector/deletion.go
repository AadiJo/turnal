package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AadiJo/turnal/internal/telemetry"
)

const (
	PostHogUSAppHost       = "https://us.posthog.com"
	PostHogEUAppHost       = "https://eu.posthog.com"
	maxDeletionAPIResponse = 64 * 1024
)

type PostHogDeletionConfig struct {
	AppHost        string
	ProjectID      int
	PersonalAPIKey string
	Client         *http.Client
}

type PostHogDeletionClient struct {
	host      string
	projectID int
	apiKey    string
	client    *http.Client
}

type BulkDeletionResponse struct {
	PersonsFound                int               `json:"persons_found"`
	PersonsDeleted              int               `json:"persons_deleted"`
	EventsQueuedForDeletion     bool              `json:"events_queued_for_deletion"`
	RecordingsQueuedForDeletion bool              `json:"recordings_queued_for_deletion"`
	DeletionErrors              []json.RawMessage `json:"deletion_errors"`
}

func NewPostHogDeletionClient(config PostHogDeletionConfig) (*PostHogDeletionClient, error) {
	host := strings.TrimRight(strings.TrimSpace(config.AppHost), "/")
	if host == "" {
		host = PostHogUSAppHost
	}
	if host != PostHogUSAppHost && host != PostHogEUAppHost {
		return nil, errors.New("PostHog application host is not allowlisted")
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid PostHog application host")
	}
	if config.ProjectID <= 0 {
		return nil, errors.New("PostHog project ID is required")
	}
	if strings.TrimSpace(config.PersonalAPIKey) == "" {
		return nil, errors.New("PostHog personal API key is required")
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
	return &PostHogDeletionClient{
		host:      host,
		projectID: config.ProjectID,
		apiKey:    config.PersonalAPIKey,
		client:    client,
	}, nil
}

func (client *PostHogDeletionClient) Request(ctx context.Context, id telemetry.UUID) (BulkDeletionResponse, error) {
	payload := struct {
		DistinctIDs      []string `json:"distinct_ids"`
		DeleteEvents     bool     `json:"delete_events"`
		DeleteRecordings bool     `json:"delete_recordings"`
		KeepPerson       bool     `json:"keep_person"`
	}{
		DistinctIDs:      []string{id.String()},
		DeleteEvents:     true,
		DeleteRecordings: true,
		KeepPerson:       false,
	}
	var response BulkDeletionResponse
	status, err := client.call(ctx, http.MethodPost, fmt.Sprintf("/api/projects/%d/persons/bulk_delete/", client.projectID), payload, &response)
	if err != nil {
		return BulkDeletionResponse{}, err
	}
	if status != http.StatusAccepted {
		return BulkDeletionResponse{}, fmt.Errorf("PostHog deletion request returned status %d", status)
	}
	if !response.EventsQueuedForDeletion || len(response.DeletionErrors) > 0 {
		return BulkDeletionResponse{}, errors.New("PostHog did not queue event deletion cleanly")
	}
	return response, nil
}

func (client *PostHogDeletionClient) CountEvents(ctx context.Context, id telemetry.UUID) (uint64, error) {
	query := fmt.Sprintf(
		"SELECT count() FROM events WHERE distinct_id = '%s' AND event IN ('turnal_daily_active', 'turnal_metric')",
		id.String(),
	)
	payload := map[string]any{
		"query": map[string]any{
			"kind":  "HogQLQuery",
			"query": query,
		},
	}
	var response struct {
		Results [][]json.Number `json:"results"`
	}
	status, err := client.call(ctx, http.MethodPost, fmt.Sprintf("/api/projects/%d/query/", client.projectID), payload, &response)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK || len(response.Results) != 1 || len(response.Results[0]) != 1 {
		return 0, errors.New("PostHog event count response is invalid")
	}
	count, err := strconv.ParseUint(response.Results[0][0].String(), 10, 64)
	if err != nil {
		return 0, errors.New("PostHog event count is not an integer")
	}
	return count, nil
}

func (client *PostHogDeletionClient) call(ctx context.Context, method, path string, payload any, destination any) (int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.host+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", postHogUserAgent)
	response, err := client.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDeletionAPIResponse+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(body) > maxDeletionAPIResponse {
		return response.StatusCode, errors.New("PostHog deletion response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return response.StatusCode, errors.New("PostHog deletion response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return response.StatusCode, errors.New("PostHog deletion response has trailing data")
	}
	return response.StatusCode, nil
}
