package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureOllamaCancelledDoesNotStart(t *testing.T) {
	t.Setenv("CODY_NO_AUTOSTART", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started, err := EnsureOllama(ctx, "http://127.0.0.1:11434")
	if started || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup: started=%v, err=%v", started, err)
	}
}

func TestPromptCacheWireAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, model        string
		affinity, explicit bool
	}{
		{"openrouter", "anthropic/claude-sonnet-4.5", true, true},
		{"openrouter", "openai/gpt-4.1", true, false},
		{"openai", "gpt-4.1", true, false},
		{"custom", "custom", false, false},
	} {
		t.Run(tc.name+tc.model, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if (payload["prompt_cache_key"] == "opaque-session") != tc.affinity {
					t.Errorf("affinity: %v", payload)
				}
				if tc.name == "openrouter" && payload["session_id"] != "opaque-session" {
					t.Error("missing session affinity")
				}
				_, explicit := payload["cache_control"]
				if explicit != tc.explicit {
					t.Errorf("cache hint: %v", payload)
				}
				key := "max_tokens"
				if tc.name == "openai" {
					key = "max_completion_tokens"
				}
				if payload[key] != float64(123) {
					t.Errorf("output limit: %v", payload)
				}
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3000,\"prompt_tokens_details\":{\"cached_tokens\":2000,\"cache_write_tokens\":500}}}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			p := NewOpenAI(tc.name, srv.URL, "")
			s, err := p.Stream(context.Background(), Request{Model: tc.model, CacheKey: "opaque-session", MaxOutputTokens: 123, NoReasoning: true, Messages: []Message{{Role: "user", Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			d, err := s.Recv()
			if err != nil || d.Usage == nil || d.Usage.PromptDetails == nil || d.Usage.PromptDetails.CachedTokens != 2000 || d.Usage.PromptDetails.CacheWriteTokens != 500 {
				t.Fatalf("cache usage: %+v %v", d, err)
			}
		})
	}
}

// Ollama is driven through its native API so cody, not the server default,
// decides the context window.
func TestOllamaNativeChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Think    *bool
			Options  map[string]float64
			Messages []struct {
				Role, Content string
				ToolName      string `json:"tool_name"`
				ToolCalls     []struct {
					Function struct{ Arguments map[string]string }
				} `json:"tool_calls"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err) // fails when arguments are sent as a string
		}
		m := body.Messages
		if r.URL.Path != "/api/chat" || body.Options["num_ctx"] != 8192 || body.Options["num_predict"] != 99 || body.Think == nil || *body.Think ||
			len(m) != 3 || m[1].ToolCalls[0].Function.Arguments["path"] != "a.go" || m[2].ToolName != "read" {
			t.Errorf("request: %s %+v", r.URL.Path, body)
		}
		fmt.Fprint(w, `{"message":{"thinking":"hm"}}`+"\n"+
			`{"message":{"content":"hi"}}`+"\n"+
			`{"message":{"tool_calls":[{"function":{"name":"read","arguments":{"path":"b.go"}}},{"function":{"name":"glob","arguments":{}}}]}}`+"\n"+
			`{"done":true,"done_reason":"stop","prompt_eval_count":40,"eval_count":2}`+"\n")
	}))
	defer srv.Close()
	s, err := NewOpenAI("ollama", srv.URL+"/v1", "").Stream(context.Background(), Request{
		Model: "m", ContextTokens: 8192, MaxOutputTokens: 99, NoReasoning: true,
		Messages: []Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Function: Func{Name: "read", Arguments: `{"path":"a.go"}`}}}},
			{Role: "tool", Name: "read", Content: "package a"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got []Delta
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	calls := got[2].ToolCalls
	if len(got) != 4 || got[0].Reasoning != "hm" || got[1].Content != "hi" ||
		len(calls) != 2 || calls[0].Index == calls[1].Index || calls[0].Name != "read" || calls[0].Args != `{"path":"b.go"}` ||
		got[3].FinishReason != "stop" || got[3].Usage.PromptTokens != 40 || got[3].Usage.CompletionTokens != 2 {
		t.Fatalf("stream: %+v", got)
	}
}

func TestRetryStatusAndCancellation(t *testing.T) {
	for _, code := range []int{429, 503, 401} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n < 3 {
					w.Header().Set("Retry-After", "0")
					http.Error(w, "try later", code)
					return
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			p := NewOpenAI("test", srv.URL, "")
			s, err := p.Stream(context.Background(), Request{})
			if code == 401 {
				if err == nil || calls.Load() != 1 {
					t.Fatalf("retried fatal error: %v %d", err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			if calls.Load() != 3 {
				t.Fatalf("attempts %d", calls.Load())
			}
		})
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "20")
		http.Error(w, "busy", 429)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := NewOpenAI("test", srv.URL, "").Stream(ctx, Request{OnRetry: func(time.Duration) { cancel() }})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancel: %v calls=%d", err, calls.Load())
	}
}

func TestStreamInterruptionAndIdleTimeout(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(fmt.Sprint(stall), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if stall {
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			p := NewOpenAI("test", srv.URL, "")
			p.idleTimeout = 30 * time.Millisecond
			s, err := p.Stream(context.Background(), Request{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			d, err := s.Recv()
			if err != nil || d.Content != "partial" {
				t.Fatalf("first delta: %+v %v", d, err)
			}
			_, err = s.Recv()
			if stall {
				if err == nil || !strings.Contains(err.Error(), "stalled") {
					t.Fatalf("stall: %v", err)
				}
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("early EOF: %v", err)
			}
		})
	}
}

func TestOpenRouterEmbedMapsIndexesAndRetriesRateLimits(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "m" || len(req.Input) != 2 {
			t.Errorf("payload %+v", req)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
			return
		}
		// Out of order on purpose: vectors must land at their input index.
		fmt.Fprint(w, `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`)
	}))
	defer srv.Close()
	vecs, err := OpenRouterEmbed(context.Background(), srv.URL, "k", "m", []string{"a", "b"})
	if err != nil || calls.Load() != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vecs %v err %v calls %d", vecs, err, calls.Load())
	}
	if _, err := OpenRouterEmbed(context.Background(), srv.URL, "", "m", []string{"a"}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("missing key not reported: %v", err)
	}
}
