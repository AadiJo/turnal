package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRegistryURL = "https://registry.npmjs.org"
const defaultRegistryTimeout = 30 * time.Second

// The abbreviated packument media type is only valid for the package document.
// Per-version endpoints reject it with 406, so they use the standard JSON type.
const (
	abbreviatedPackumentMediaType = "application/vnd.npm.install-v1+json"
	registryVersionMediaType      = "application/json"
)

type HTTPRegistry struct {
	BaseURL string
	Client  *http.Client
}

func (r HTTPRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	var payload struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := r.getJSON(ctx, encodedPackagePath(), abbreviatedPackumentMediaType, &payload); err != nil {
		return nil, fmt.Errorf("query npm dist-tags: %w", err)
	}
	return payload.DistTags, nil
}

func (r HTTPRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	var payload struct {
		Version string `json:"version"`
	}
	path := encodedPackagePath() + "/" + url.PathEscape(npmTag)
	if err := r.getJSON(ctx, path, registryVersionMediaType, &payload); err != nil {
		return "", fmt.Errorf("query npm %s version: %w", npmTag, err)
	}
	return strings.TrimSpace(payload.Version), nil
}

func (r HTTPRegistry) getJSON(ctx context.Context, path string, accept string, target any) error {
	baseURL := strings.TrimRight(r.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultRegistryURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", accept)
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: defaultRegistryTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("registry returned %s", response.Status)
	}
	const maxResponseBytes = 10 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read registry response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("registry response exceeds %d bytes", maxResponseBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode registry response: %w", err)
	}
	return nil
}

func encodedPackagePath() string {
	return "%40aadijo%2Fturnal"
}
