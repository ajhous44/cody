package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const userAgent = "cody/0.1"

type OpenAI struct {
	name        string
	baseURL     string
	apiKey      string
	http        *http.Client
	idleTimeout time.Duration
}

func NewOpenAI(name, baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		name:        name,
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		idleTimeout: 2 * time.Minute,
		http: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		}},
	}
}

func (o *OpenAI) Name() string    { return o.name }
func (o *OpenAI) BaseURL() string { return o.baseURL }

type chatRequest struct {
	PromptCacheKey      string            `json:"prompt_cache_key,omitempty"`
	SessionID           string            `json:"session_id,omitempty"`
	CacheControl        map[string]string `json:"cache_control,omitempty"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Model               string            `json:"model"`
	Messages            []Message         `json:"messages"`
	Tools               []ToolSpec        `json:"tools,omitempty"`
	Stream              bool              `json:"stream"`
	StreamOptions       *streamOptions    `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (o *OpenAI) Stream(ctx context.Context, req Request) (Stream, error) {
	payload := chatRequest{Model: req.Model, Messages: req.Messages, Tools: req.Tools, Stream: true}
	// Ask for usage on the final chunk. Providers that do not support stream
	// options ignore the field.
	payload.StreamOptions = &streamOptions{IncludeUsage: true}
	if o.name == "openai" {
		payload.MaxCompletionTokens = req.MaxOutputTokens
	} else {
		payload.MaxTokens = req.MaxOutputTokens
	}
	if req.CacheKey != "" {
		switch o.name {
		case "openai":
			payload.PromptCacheKey = req.CacheKey
		case "openrouter":
			payload.SessionID = req.CacheKey
			payload.PromptCacheKey = req.CacheKey
			if strings.HasPrefix(strings.TrimPrefix(req.Model, "~"), "anthropic/") {
				payload.CacheControl = map[string]string{"type": "ephemeral"}
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			cancel(err)
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
		resp, err = o.http.Do(httpReq)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		retry := false
		delay := time.Duration(250*(1<<attempt)+rand.IntN(100)) * time.Millisecond
		if err == nil {
			retry = resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
			if value := resp.Header.Get("Retry-After"); value != "" {
				if seconds, parseErr := strconv.Atoi(value); parseErr == nil {
					delay = time.Duration(min(max(seconds, 0), 31)) * time.Second
				} else if at, parseErr := http.ParseTime(value); parseErr == nil {
					delay = max(time.Until(at), 0)
				}
			}
			resp.Body = &idleReader{ReadCloser: resp.Body, ctx: streamCtx, cancel: cancel, timeout: o.idleTimeout}
			err = apiError(o.name, req.Model, resp)
			resp.Body.(*idleReader).ReadCloser.Close()
			if cause := context.Cause(streamCtx); cause != nil {
				cancel(cause)
				return nil, cause
			}
		} else {
			var netErr net.Error
			retry = errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
			if retry {
				err = &Error{Msg: fmt.Sprintf("can't reach %s at %s; check that it is running and the network is up", o.name, o.baseURL), Raw: err.Error()}
			}
		}
		if ctx.Err() != nil {
			cancel(ctx.Err())
			return nil, ctx.Err()
		}
		if !retry || attempt >= 2 || delay > 30*time.Second {
			cancel(err)
			return nil, err
		}
		if req.OnRetry != nil {
			req.OnRetry(delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel(ctx.Err())
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	reader := &idleReader{ReadCloser: resp.Body, ctx: streamCtx, cancel: cancel, timeout: o.idleTimeout}
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	return &sseStream{body: reader, sc: sc, provider: o.name, model: req.Model}, nil
}

func apiError(name, model string, resp *http.Response) error {
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
	return statusError(name, model, resp.StatusCode, msg)
}

// Error is a provider failure worded for people. Raw keeps the provider's own
// status and message for debug output.
type Error struct {
	Msg, Raw string
}

func (e *Error) Error() string { return e.Msg }

// Debug appends the raw provider detail to err when there is any.
func Debug(err error) error {
	var pe *Error
	if errors.As(err, &pe) && pe.Raw != "" {
		return fmt.Errorf("%w [debug: %s]", err, pe.Raw)
	}
	return err
}

// statusError turns a provider HTTP failure into one readable sentence. Rate
// limits always get the retry advice. Busy replies get it only when their body
// says nothing useful ("Provider returned error", an HTML proxy page); a real
// reason such as Ollama's out-of-memory 500 is kept, since retrying won't help.
func statusError(name, model string, code int, msg string) error {
	who := model
	if who == "" {
		who = name
	}
	raw := fmt.Sprintf("%s: HTTP %d: %s", name, code, msg)
	generic := msg == "" || msg == http.StatusText(code) || strings.EqualFold(msg, "Provider returned error") || strings.HasPrefix(msg, "<")
	switch {
	case code == http.StatusTooManyRequests:
		return &Error{Msg: fmt.Sprintf("%s is rate-limited right now (%s %d). Try again in a moment or switch models", who, name, code), Raw: raw}
	case (code == http.StatusRequestTimeout || code >= 500) && generic:
		return &Error{Msg: fmt.Sprintf("%s is busy or unavailable right now (%s %d). Try again in a moment or switch models", who, name, code), Raw: raw}
	}
	err := errors.New(raw)
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (check the API key)", err)
	case http.StatusPaymentRequired:
		return fmt.Errorf("%w (check your %s credits)", err, name)
	case http.StatusNotFound:
		return fmt.Errorf("%w (is the server running and the model installed?)", err)
	}
	return err
}

type sseStream struct {
	body     io.ReadCloser
	sc       *bufio.Scanner
	provider string
	model    string
	finished bool
	done     bool
}

func (s *sseStream) Close() error { return s.body.Close() }

func (s *sseStream) Recv() (Delta, error) {
	if s.done {
		return Delta{}, io.EOF
	}
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
			s.done = true
			return Delta{}, io.EOF
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Delta{}, fmt.Errorf("%s: decode stream: %w", s.provider, err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			// OpenRouter reports upstream rate limits mid-stream with a numeric code.
			if code, ok := chunk.Error.Code.(float64); ok && code >= 400 {
				return Delta{}, statusError(s.provider, s.model, int(code), chunk.Error.Message)
			}
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
			if c.FinishReason != "" {
				s.finished = true
			}
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
	if !s.finished {
		return Delta{}, fmt.Errorf("%s: stream interrupted before completion: %w", s.provider, io.ErrUnexpectedEOF)
	}
	s.done = true
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
		Code    any    `json:"code"`
	} `json:"error"`
}

// A stream can run for hours while making progress; only stalled reads time out.
type idleReader struct {
	io.ReadCloser
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timeout time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	timer := time.AfterFunc(r.timeout, func() { r.cancel(errors.New("provider stream stalled; try again")) })
	n, err := r.ReadCloser.Read(p)
	timer.Stop()
	if cause := context.Cause(r.ctx); cause != nil {
		return n, cause
	}
	return n, err
}
func (r *idleReader) Close() error { r.cancel(context.Canceled); return r.ReadCloser.Close() }
