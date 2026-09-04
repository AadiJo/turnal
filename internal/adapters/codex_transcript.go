package adapters

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/usage"
)

type codexTranscriptLine struct {
	Type    string `json:"type"`
	Payload struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Info *struct {
			Total *codexTokenUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexCumulativeUsage reads the newest cumulative token_count from a rollout.
// The session_meta check prevents an unrelated JSONL file from being accepted.
func codexCumulativeUsage(payload hookPayload) *transcriptUsage {
	path := strings.TrimSpace(payload.TranscriptPath)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return &transcriptUsage{}
	}
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}

	sessionMatched := false
	var latest *codexTokenUsage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var line codexTranscriptLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		switch line.Type {
		case "session_meta":
			if strings.EqualFold(line.Payload.ID, payload.SessionID) {
				sessionMatched = true
			}
		case "event_msg":
			event := line.Payload
			if event.Type == "token_count" && event.Info != nil && event.Info.Total != nil {
				latest = event.Info.Total
			}
		}
	}
	if scanner.Err() != nil || !sessionMatched {
		return nil
	}
	if latest == nil {
		return &transcriptUsage{}
	}
	if latest.CachedInputTokens > latest.InputTokens {
		return nil
	}
	result := usage.TokenUsage{
		InputTokens: latest.InputTokens - latest.CachedInputTokens, CacheReadTokens: latest.CachedInputTokens,
		OutputTokens: latest.OutputTokens, ReasoningTokens: latest.ReasoningOutputTokens,
	}
	if !result.Valid() {
		return nil
	}
	// token_count also appears on rate-limit updates, so it cannot count requests.
	return &transcriptUsage{Total: result, HasUsage: true}
}
