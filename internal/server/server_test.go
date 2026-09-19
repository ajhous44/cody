package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajhous44/cody/internal/agent"
	"github.com/ajhous44/cody/internal/provider"
)

type stubEngine struct {
	mu       sync.Mutex
	model    string
	approval string
	history  []provider.Message
	send     func(ctx context.Context, text string, h agent.Handler) error
}

func (s *stubEngine) Send(ctx context.Context, text string, h agent.Handler) error {
	if s.send != nil {
		return s.send(ctx, text, h)
	}
	h(agent.Event{Kind: agent.KindText, Text: "echo: " + text})
	return nil
}

func (s *stubEngine) History() []provider.Message { return s.history }
func (s *stubEngine) Model() string               { return s.model }
func (s *stubEngine) SetModel(m string)           { s.model = m }
func (s *stubEngine) ProviderName() string        { return "stub" }
func (s *stubEngine) Approval() string            { return s.approval }
func (s *stubEngine) SessionID() string           { return "20260101-000000-test" }
func (s *stubEngine) Root() string                { return "/tmp/stub" }

func (s *stubEngine) SetApproval(mode string) error {
	switch mode {
	case "ask", "auto", "yolo":
		s.approval = mode
		return nil
	}
	return errors.New("approval must be ask, auto, or yolo")
}

type stubTool struct{}

func (stubTool) Name() string                                         { return "bash" }
func (stubTool) Description() string                                  { return "stub" }
func (stubTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (stubTool) Mutating() bool                                       { return true }
func (stubTool) Summarize(json.RawMessage) string                     { return "echo hi" }
func (stubTool) Run(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func newTestServer(t *testing.T, eng Engine) (*httptest.Server, *Server) {
	t.Helper()
	srv := New(eng, "127.0.0.1", "test-token")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

func get(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Cody-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func post(t *testing.T, url, token string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url+"?token="+token, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cody-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})

	resp := get(t, ts.URL+"/api/state", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = get(t, ts.URL+"/api/state", "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = get(t, ts.URL+"/api/state", "test-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid token: status = %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var state map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state["provider"] != "stub" || state["session"] != "20260101-000000-test" {
		t.Fatalf("state = %+v", state)
	}
}

func TestHostGuardRejectsForeignHost(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/state?token=test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func readSSE(t *testing.T, ctx context.Context, url, token string, onState func()) []map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events?token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	var events []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sent := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		events = append(events, ev)
		if ev["type"] == "state" && !sent && onState != nil {
			sent = true
			onState()
		}
		switch ev["type"] {
		case "turn_end", "turn_cancelled":
			return events
		}
	}
	return events
}

func TestPromptStreamsEvents(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := readSSE(t, ctx, ts.URL, "test-token", func() {
		resp := post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "hi"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("prompt status = %d", resp.StatusCode)
		}
	})

	var gotText, gotEnd bool
	for _, ev := range events {
		if ev["type"] == "text" && ev["text"] == "echo: hi" {
			gotText = true
		}
		if ev["type"] == "turn_end" {
			gotEnd = true
		}
	}
	if !gotText || !gotEnd {
		t.Fatalf("events = %+v", events)
	}
}

func TestApprovalRoundTrip(t *testing.T) {
	srv := New(&stubEngine{}, "127.0.0.1", "test-token")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events?token=test-token", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	next := func() map[string]any {
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				continue
			}
			return ev
		}
		return nil
	}
	if ev := next(); ev == nil || ev["type"] != "state" {
		t.Fatalf("expected state event, got %+v", ev)
	}

	type result struct {
		allow bool
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		allow, err := srv.Approve(ctx, stubTool{}, `{"command":"echo hi"}`)
		resultCh <- result{allow, err}
	}()

	var approvalID string
	for {
		ev := next()
		if ev == nil {
			t.Fatal("stream ended before approval event")
		}
		if ev["type"] == "approval" {
			approvalID, _ = ev["id"].(string)
			break
		}
	}
	if approvalID == "" {
		t.Fatal("approval event missing id")
	}
	approveResp := post(t, ts.URL+"/api/approve", "test-token", map[string]any{"id": approvalID, "allow": true})
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d", approveResp.StatusCode)
	}
	select {
	case r := <-resultCh:
		if r.err != nil || !r.allow {
			t.Fatalf("approve result = %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("approve timed out")
	}

	missing := post(t, ts.URL+"/api/approve", "test-token", map[string]any{"id": "nope", "allow": true})
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id status = %d", missing.StatusCode)
	}
}

func TestInterrupt(t *testing.T) {
	blocking := &stubEngine{
		send: func(ctx context.Context, text string, h agent.Handler) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ts, _ := newTestServer(t, blocking)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := readSSE(t, ctx, ts.URL, "test-token", func() {
		resp := post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "hi"})
		defer resp.Body.Close()
		go func() {
			time.Sleep(100 * time.Millisecond)
			ir := post(t, ts.URL+"/api/interrupt", "test-token", map[string]any{})
			ir.Body.Close()
		}()
	})
	var cancelled bool
	for _, ev := range events {
		if ev["type"] == "turn_cancelled" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatalf("events = %+v", events)
	}
}

func TestModelAndApprovalEndpoints(t *testing.T) {
	eng := &stubEngine{model: "m1", approval: "ask"}
	ts, _ := newTestServer(t, eng)

	resp := post(t, ts.URL+"/api/model", "test-token", map[string]string{"model": "m2"})
	resp.Body.Close()
	if eng.Model() != "m2" {
		t.Fatalf("model = %q", eng.Model())
	}
	resp = post(t, ts.URL+"/api/approval", "test-token", map[string]string{"mode": "yolo"})
	resp.Body.Close()
	if eng.Approval() != "yolo" {
		t.Fatalf("approval = %q", eng.Approval())
	}
	resp = post(t, ts.URL+"/api/approval", "test-token", map[string]string{"mode": "nope"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d", resp.StatusCode)
	}
}

func TestIndexServed(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})
	resp := get(t, ts.URL+"/", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "cody") {
		t.Fatal("index page missing brand")
	}
}
