package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamParsesContentAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("auth = %q", got)
		}
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "key")
	s, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	var got strings.Builder
	var usage *Usage
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got.WriteString(d.Content)
		if d.Usage != nil {
			usage = d.Usage
		}
	}
	if got.String() != "hello" {
		t.Fatalf("content = %q", got.String())
	}
	if usage == nil || usage.PromptTokens != 7 || usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestStreamParsesReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"step 1 \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step 2\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "")
	s, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	var reasoning, content strings.Builder
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		reasoning.WriteString(d.Reasoning)
		content.WriteString(d.Content)
	}
	if reasoning.String() != "step 1 step 2" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if content.String() != "done" {
		t.Fatalf("content = %q", content.String())
	}
}

func TestStreamParsesToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"cmd\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"ls\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "")
	s, err := p.Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	var calls []ToolCallDelta
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		calls = append(calls, d.ToolCalls...)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "bash" {
		t.Fatalf("first fragment = %+v", calls[0])
	}
	if calls[1].Args != `"ls"}` {
		t.Fatalf("second fragment args = %q", calls[1].Args)
	}
}

func TestStreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"no key"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "")
	_, err := p.Stream(context.Background(), Request{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no key") || !strings.Contains(err.Error(), "check the API key") {
		t.Fatalf("error = %v", err)
	}
}

func TestPickOllamaModel(t *testing.T) {
	models := []Model{
		{Name: "llama3.2:1b", Size: 1},
		{Name: "qwen2.5-coder:7b", Size: 5},
		{Name: "qwen2.5-coder:3b", Size: 2},
	}
	if got := PickOllamaModel(models); got != "qwen2.5-coder:3b" {
		t.Fatalf("picked %q, want smallest coder", got)
	}
	if got := PickOllamaModel(nil); got != "" {
		t.Fatalf("picked %q from empty list", got)
	}
	withEmbedding := []Model{
		{Name: "nomic-embed-text:latest", Size: 1, Capabilities: []string{"embedding"}},
		{Name: "qwen2.5-coder:7b", Size: 5, Capabilities: []string{"completion", "tools"}},
	}
	if got := PickOllamaModel(withEmbedding); got != "qwen2.5-coder:7b" {
		t.Fatalf("picked %q, want tool-capable model", got)
	}
	onlyEmbedding := []Model{{Name: "nomic-embed-text:latest", Size: 1, Capabilities: []string{"embedding"}}}
	if got := PickOllamaModel(onlyEmbedding); got != "" {
		t.Fatalf("picked %q, want empty", got)
	}
}

func TestOllamaEmbedBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"embeddings":[[1,2],[3,4]]}`)
	}))
	defer srv.Close()

	got, err := OllamaEmbed(context.Background(), srv.URL+"/v1", "m", []string{"a", "b"})
	if err != nil {
		t.Fatalf("OllamaEmbed: %v", err)
	}
	if len(got) != 2 || got[1][0] != 3 || got[1][1] != 4 {
		t.Fatalf("embeddings = %v", got)
	}
}

func TestOllamaEmbedFallback(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/api/embed" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"embedding":[5,6]}`)
	}))
	defer srv.Close()

	got, err := OllamaEmbed(context.Background(), srv.URL, "m", []string{"a", "b"})
	if err != nil {
		t.Fatalf("OllamaEmbed: %v", err)
	}
	if len(got) != 2 || got[0][0] != 5 || got[1][1] != 6 {
		t.Fatalf("embeddings = %v", got)
	}
	if hits != 3 {
		t.Fatalf("hits = %d, want 1 batch attempt + 2 fallbacks", hits)
	}
}

func TestOllamaUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"version":"0.34.2"}`)
	}))
	if !ollamaUp(context.Background(), srv.URL+"/v1") {
		t.Fatal("expected up")
	}
	srv.Close()
	if ollamaUp(context.Background(), srv.URL) {
		t.Fatal("expected down after close")
	}
}

func TestIsLocalHost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost:11434/v1", true},
		{"http://127.0.0.1:11434/v1", true},
		{"http://[::1]:11434/v1", true},
		{"https://ollama.example.com/v1", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if got := isLocalHost(tc.url); got != tc.want {
			t.Fatalf("isLocalHost(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestOllamaLoadedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[{"name":"gemma4:e4b","context_length":16384},{"name":"other:1b","context_length":4096}]}`)
	}))
	defer srv.Close()

	ctx := context.Background()
	if got := OllamaLoadedContext(ctx, srv.URL+"/v1", "gemma4:e4b"); got != 16384 {
		t.Fatalf("context = %d, want 16384", got)
	}
	if got := OllamaLoadedContext(ctx, srv.URL+"/v1", "missing:1b"); got != 0 {
		t.Fatalf("context = %d, want 0", got)
	}
}

func TestOllamaHost(t *testing.T) {
	if got := OllamaHost("http://localhost:11434/v1"); got != "http://localhost:11434" {
		t.Fatalf("host = %q", got)
	}
	if got := OllamaHost("http://localhost:11434/v1/"); got != "http://localhost:11434" {
		t.Fatalf("host = %q", got)
	}
}
