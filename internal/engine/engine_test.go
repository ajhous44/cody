package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/session"
	"github.com/ajhous44/cody/v3/internal/tool"
)

func testConfig(srv *httptest.Server, approval string) *config.Config {
	cfg := config.Default()
	cfg.Provider = "test"
	cfg.Model = "m"
	cfg.Approval = approval
	cfg.Providers["test"] = config.Provider{BaseURL: srv.URL + "/v1"}
	return cfg
}

// sseServer replies to chat completion requests with the given scripted
// response bodies, one per request.
func sseServer(t *testing.T, responses ...[]string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(responses) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range responses[n] {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func newEngine(t *testing.T, cfg *config.Config, root string, approve agent.Approver) *Engine {
	t.Helper()
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	eng, err := New(Options{
		Root:     root,
		Cfg:      cfg,
		Version:  "test",
		Store:    session.New(root, ""),
		Approver: approve,
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func toolCallResponse(name, args string) []string {
	return []string{fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
		name, args)}
}

var finalResponse = []string{`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`}

func TestApprovalPolicy(t *testing.T) {
	t.Run("yolo never asks", func(t *testing.T) {
		srv := sseServer(t, toolCallResponse("write", `{"path":"x.txt","content":"hi"}`), finalResponse)
		defer srv.Close()
		root := t.TempDir()
		var asked atomic.Int32
		eng := newEngine(t, testConfig(srv, "yolo"), root, agent.ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) {
			asked.Add(1)
			return false, nil
		}))
		if err := eng.Send(context.Background(), "go", nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if asked.Load() != 0 {
			t.Fatalf("approver asked %d times in yolo mode", asked.Load())
		}
		if _, err := os.Stat(filepath.Join(root, "x.txt")); err != nil {
			t.Fatalf("write did not happen: %v", err)
		}
	})

	t.Run("auto asks only for bash", func(t *testing.T) {
		srv := sseServer(t, toolCallResponse("write", `{"path":"x.txt","content":"hi"}`), finalResponse)
		defer srv.Close()
		root := t.TempDir()
		var asked atomic.Int32
		eng := newEngine(t, testConfig(srv, "auto"), root, agent.ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) {
			asked.Add(1)
			return false, nil
		}))
		if err := eng.Send(context.Background(), "go", nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if asked.Load() != 0 {
			t.Fatalf("approver asked %d times for a file write in auto mode", asked.Load())
		}
		if _, err := os.Stat(filepath.Join(root, "x.txt")); err != nil {
			t.Fatalf("write did not happen: %v", err)
		}
	})

	t.Run("ask denies when the client says no", func(t *testing.T) {
		srv := sseServer(t, toolCallResponse("bash", `{"command":"echo hi"}`), finalResponse)
		defer srv.Close()
		root := t.TempDir()
		var asked atomic.Int32
		eng := newEngine(t, testConfig(srv, "ask"), root, agent.ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) {
			asked.Add(1)
			return false, nil
		}))
		if err := eng.Send(context.Background(), "go", nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if asked.Load() != 1 {
			t.Fatalf("approver asked %d times, want 1", asked.Load())
		}
		denied := false
		for _, m := range eng.History() {
			if m.Role == "tool" && m.Content == "user denied this action" {
				denied = true
			}
		}
		if !denied {
			t.Fatalf("denial not recorded in history: %+v", eng.History())
		}
	})
}

func TestSendRecordsSession(t *testing.T) {
	srv := sseServer(t, finalResponse)
	defer srv.Close()
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)

	if err := eng.Send(context.Background(), "hello", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	eng.Close()
	id, recs, err := session.Load(root, eng.SessionID())
	if err != nil {
		t.Fatalf("session.Load: %v", err)
	}
	if id != eng.SessionID() {
		t.Fatalf("id = %s", id)
	}
	var meta map[string]string
	var roles []string
	for _, r := range recs {
		if r.Type == "meta" {
			meta = r.Meta
		}
		if r.Type == "message" {
			roles = append(roles, r.Role)
		}
	}
	if meta["model"] != "m" || meta["provider"] != "test" || meta["workspace"] != root {
		t.Fatalf("meta = %+v", meta)
	}
	if len(roles) != 2 || roles[0] != "user" || roles[1] != "assistant" {
		t.Fatalf("roles = %v", roles)
	}
}

func TestMessagesFromRecords(t *testing.T) {
	recs := []session.Record{
		{Type: "meta", Meta: map[string]string{"a": "b"}},
		{Type: "message", Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "bash", Args: "{}"}}},
		{Type: "message", Role: "tool", ToolCallID: "c1", Text: "ok"},
	}
	msgs := MessagesFromRecords(recs)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("tool call not restored: %+v", msgs[0])
	}
	if msgs[0].ToolCalls[0].Type != "function" {
		t.Fatalf("tool call type = %q", msgs[0].ToolCalls[0].Type)
	}
}

func TestNewSessionWritesMeta(t *testing.T) {
	srv := sseServer(t, finalResponse)
	defer srv.Close()
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)

	if err := eng.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	infos, err := session.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != eng.SessionID() {
		t.Fatalf("new thread should be visible immediately: %+v", infos)
	}
}

func TestTitleUsesModelAndStores(t *testing.T) {
	srv := sseServer(t, []string{
		`{"choices":[{"delta":{"content":"\"Fix flaky login test.\""},"finish_reason":"stop"}]}`,
	})
	defer srv.Close()
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)

	title := eng.Title(context.Background(), "the login test keeps failing, please fix it")
	if title != "Fix flaky login test" {
		t.Fatalf("title = %q", title)
	}
	infos, err := session.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].First != "Fix flaky login test" {
		t.Fatalf("title not stored: %+v", infos)
	}
}

func TestParseTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"title":"fix flaky login test"}`, "fix flaky login test"},
		{"Sure!\n```json\n{\"title\": \"refactor retry logic\"}\n```", "refactor retry logic"},
		{`{"reason":"update readme badges"}`, "update readme badges"},
		{`{"summary":"tighten context trimming"}`, "tighten context trimming"},
		{"  Plain summary of the ask  ", "Plain summary of the ask"},
		{`{"title":""}`, ""},
	}
	for _, c := range cases {
		if got := parseTitle(c.in); got != c.want {
			t.Fatalf("parseTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTitleFallsBackToDateTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	eng := newEngine(t, testConfig(srv, "yolo"), t.TempDir(), nil)
	if title := eng.Title(context.Background(), ""); title == "" {
		t.Fatal("expected a date/time fallback title")
	}
}

func TestTitleFallsBackWhenModelFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)

	title := eng.Title(context.Background(), "refactor the payment retry logic please")
	if title != "refactor the payment retry logic" {
		t.Fatalf("fallback title = %q", title)
	}
}

func TestContextBudgetUsesLoadedOllamaContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"m:latest","context_length":16384}]}`)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Providers["ollama"] = config.Provider{BaseURL: srv.URL + "/v1"}
	if got, _ := modelLimits(context.Background(), cfg, "ollama", "m:latest"); got != 16384 {
		t.Fatalf("budget = %d, want 16384", got)
	}
	cfg.Providers["openrouter"] = config.Provider{BaseURL: srv.URL}
	if got, _ := modelLimits(context.Background(), cfg, "openrouter", "m"); got != 0 {
		t.Fatalf("budget = %d, want unknown (0)", got)
	}
}

func TestSendAnnouncesColdOllamaModel(t *testing.T) {
	mux := http.NewServeMux()
	var loaded atomic.Bool
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{ "version": "test" }`)
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		if loaded.Load() {
			fmt.Fprint(w, `{"models":[{"name":"cold:model"}]}`)
		} else {
			fmt.Fprint(w, `{"models":[]}`)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range finalResponse {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.Default()
	cfg.Provider = "ollama"
	cfg.Model = "cold:model"
	cfg.Approval = "yolo"
	cfg.Providers["ollama"] = config.Provider{BaseURL: srv.URL + "/v1"}
	eng := newEngine(t, cfg, t.TempDir(), nil)

	var statuses []string
	err := eng.Send(context.Background(), "hi", func(e agent.Event) {
		if e.Kind == agent.KindThinking {
			statuses = append(statuses, e.Text)
		}
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(statuses) < 2 || statuses[0] != "Loading model… cold:model" {
		t.Fatalf("loading status must precede inference, got %v", statuses)
	}
	loaded.Store(true)
	if err := eng.Send(context.Background(), "hi again", func(e agent.Event) {
		if e.Kind == agent.KindThinking && e.Text != "" {
			t.Errorf("warm model reported loading: %q", e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSendConfiguredOllamaChecksStartup(t *testing.T) {
	t.Setenv("CODY_NO_AUTOSTART", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("inference attempted before Ollama was ready")
		}
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Provider, cfg.Model = "ollama", "saved:model"
	cfg.Providers["ollama"] = config.Provider{BaseURL: srv.URL + "/v1"}
	eng := newEngine(t, cfg, t.TempDir(), nil)
	var loading bool
	err := eng.Send(context.Background(), "hi", func(e agent.Event) {
		loading = loading || e.Kind == agent.KindThinking && strings.Contains(e.Text, "Loading model")
	})
	if err == nil || !strings.Contains(err.Error(), "autostart is disabled") || !loading {
		t.Fatalf("startup check: loading=%v, error=%v", loading, err)
	}
}

func TestSetApprovalRejectsUnknown(t *testing.T) {
	srv := sseServer(t, finalResponse)
	defer srv.Close()
	eng := newEngine(t, testConfig(srv, "ask"), t.TempDir(), nil)
	if err := eng.SetApproval("reckless"); err == nil {
		t.Fatal("expected error for unknown approval mode")
	}
	if err := eng.SetApproval("auto"); err != nil {
		t.Fatalf("SetApproval: %v", err)
	}
	if eng.Approval() != "auto" {
		t.Fatalf("approval = %q", eng.Approval())
	}
}

func TestProviderName(t *testing.T) {
	srv := sseServer(t, finalResponse)
	defer srv.Close()
	eng := newEngine(t, testConfig(srv, "ask"), t.TempDir(), nil)
	if got := eng.ProviderName(); got != "test" {
		t.Fatalf("provider = %q", got)
	}
}

func TestOpenRouterEmbedderIsOptInAndNamesItsIndex(t *testing.T) {
	var model atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		model.Store(req.Model)
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("CODY_API_KEY", "")
	cfg := config.Default()
	if EmbedID(cfg) != config.DefaultEmbedModel {
		t.Fatalf("default index id %q", EmbedID(cfg))
	}
	cfg.EmbedProvider = "openrouter"
	cfg.Providers["openrouter"] = config.Provider{BaseURL: srv.URL, APIKey: "k"}
	if _, err := Embedder(cfg).Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if model.Load() != config.DefaultOpenRouterEmbedModel || EmbedID(cfg) != "openrouter:"+config.DefaultOpenRouterEmbedModel {
		t.Fatalf("model %v id %q", model.Load(), EmbedID(cfg))
	}
}
