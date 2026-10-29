package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ORModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	TopProvider   struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
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

var embedClient = &http.Client{Timeout: 2 * time.Minute}

// OpenRouterEmbed returns one vector per input from OpenRouter's
// OpenAI-compatible embeddings endpoint. Rate limits and transient server
// errors retry twice, so one 429 does not abort a long index run.
func OpenRouterEmbed(ctx context.Context, baseURL, apiKey, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if apiKey == "" {
		return nil, errors.New("openrouter: embeddings need an API key (OPENROUTER_API_KEY or GUI settings)")
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("HTTP-Referer", "https://github.com/ajhous44/cody")
		req.Header.Set("X-Title", "cody")
		resp, err := embedClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openrouter: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("openrouter: embeddings: %w", err)
			}
			break
		}
		delay := time.Duration(1<<attempt) * time.Second
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			delay = time.Duration(secs) * time.Second
		}
		retry := resp.StatusCode == 429 || resp.StatusCode >= 500
		err = apiError("openrouter", model, resp)
		resp.Body.Close()
		if !retry || attempt >= 2 || delay > 30*time.Second {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	vecs := make([][]float32, len(inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("openrouter: embedding index %d out of range", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	for i, v := range vecs {
		if len(v) == 0 {
			return nil, fmt.Errorf("openrouter: no embedding for input %d of %d (does %q support embeddings?)", i+1, len(inputs), model)
		}
	}
	return vecs, nil
}
