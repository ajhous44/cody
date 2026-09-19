package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const userAgent = "cody/0.1"

type OpenAI struct {
	name    string
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewOpenAI(name, baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		}},
	}
}

func (o *OpenAI) Name() string    { return o.name }
func (o *OpenAI) BaseURL() string { return o.baseURL }

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []ToolSpec     `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (o *OpenAI) Stream(ctx context.Context, req Request) (Stream, error) {
	payload := chatRequest{Model: req.Model, Messages: req.Messages, Tools: req.Tools, Stream: true}
	// Ask for usage on the final chunk. Providers that do not support stream
	// options ignore the field.
	payload.StreamOptions = &streamOptions{IncludeUsage: true}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	if o.name == "openrouter" {
		httpReq.Header.Set("HTTP-Referer", "https://github.com/ajhous44/cody")
		httpReq.Header.Set("X-Title", "cody")
	}
	resp, err := o.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, apiError(o.name, resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	return &sseStream{body: resp.Body, sc: sc, provider: o.name}, nil
}

func apiError(name string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := ""
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &nested) == nil && nested.Error.Message != "" {
		msg = nested.Error.Message
	} else {
		var flat struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &flat) == nil && flat.Error != "" {
			msg = flat.Error
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(b))
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	err := fmt.Errorf("%s: HTTP %d: %s", name, resp.StatusCode, msg)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (check the API key)", err)
	case http.StatusNotFound:
		return fmt.Errorf("%w (is the server running and the model installed?)", err)
	}
	return err
}

type sseStream struct {
	body     io.ReadCloser
	sc       *bufio.Scanner
	provider string
}

func (s *sseStream) Close() error { return s.body.Close() }

func (s *sseStream) Recv() (Delta, error) {
	for s.sc.Scan() {
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return Delta{}, io.EOF
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Delta{}, fmt.Errorf("%s: decode stream: %w", s.provider, err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Delta{}, errors.New(chunk.Error.Message)
		}
		d := Delta{Usage: chunk.Usage}
		if len(chunk.Choices) > 0 {
			c := chunk.Choices[0]
			d.Content = c.Delta.Content
			d.Reasoning = c.Delta.ReasoningContent
			if d.Reasoning == "" {
				d.Reasoning = c.Delta.Reasoning
			}
			d.FinishReason = c.FinishReason
			for _, tc := range c.Delta.ToolCalls {
				d.ToolCalls = append(d.ToolCalls, ToolCallDelta{
					Index: tc.Index,
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Args:  tc.Function.Arguments,
				})
			}
		}
		if d.Content == "" && d.Reasoning == "" && len(d.ToolCalls) == 0 && d.Usage == nil && d.FinishReason == "" {
			continue
		}
		return d, nil
	}
	if err := s.sc.Err(); err != nil {
		return Delta{}, fmt.Errorf("%s: read stream: %w", s.provider, err)
	}
	return Delta{}, io.EOF
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}
