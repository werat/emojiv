package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const jevEndpoint = "https://api.typesafe.ai/v1/systemone"

type JevQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type JevRequest struct {
	State     any                    `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]JevQuestion `json:"questions"`
}

type JevAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

type JevUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type JevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]JevAnswer `json:"answers"`
	Usage   JevUsage             `json:"usage"`
}

type JevClient struct {
	APIKey string
	HTTP   *http.Client
}

// Evaluate calls the Jev endpoint, retrying 429/529 with exponential backoff.
func (c *JevClient) Evaluate(ctx context.Context, req *JevRequest) (*JevResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	backoff := 250 * time.Millisecond
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, jevEndpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.APIKey))
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTP.Do(httpReq)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529
		if retryable && attempt < 3 {
			select {
			case <-time.After(backoff):
				backoff *= 2
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, data)
		}

		var out JevResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("jev: decoding response: %w", err)
		}
		return &out, nil
	}
}
