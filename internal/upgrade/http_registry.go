package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultRegistryURL = "https://registry.npmjs.org"

type HTTPRegistry struct {
	BaseURL string
	Client  *http.Client
}

func (r HTTPRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	var payload struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := r.getJSON(ctx, encodedPackagePath(), &payload); err != nil {
		return nil, fmt.Errorf("query npm dist-tags: %w", err)
	}
	return payload.DistTags, nil
}

func (r HTTPRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	var payload struct {
		Version string `json:"version"`
	}
	path := encodedPackagePath() + "/" + url.PathEscape(npmTag)
	if err := r.getJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("query npm %s version: %w", npmTag, err)
	}
	return strings.TrimSpace(payload.Version), nil
}

func (r HTTPRegistry) getJSON(ctx context.Context, path string, target any) error {
	baseURL := strings.TrimRight(r.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultRegistryURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	client := r.Client
	if client == nil {
		client = http.DefaultClient
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
	if err := json.NewDecoder(io.LimitReader(response.Body, 10<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode registry response: %w", err)
	}
	return nil
}

func encodedPackagePath() string {
	return "%40aadijo%2Fturnal"
}
