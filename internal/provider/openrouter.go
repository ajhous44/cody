package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ORModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt string `json:"prompt"`
	} `json:"pricing"`
}

func ListOpenRouterModels(ctx context.Context, baseURL string) ([]ORModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []ORModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
