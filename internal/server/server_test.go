package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/session"
)

type stubEngine struct {
	mu          sync.Mutex
	root        string
	model       string
	approval    string
	history     []provider.Message
	send        func(ctx context.Context, text string, h agent.Handler) error
	resumed     string
	newSessions int
	titleCalls  int
	closed      bool
	approver    agent.Approver
}

func (s *stubEngine) Send(ctx context.Context, text string, h agent.Handler) error {
	if s.send != nil {
		return s.send(ctx, text, h)
	}
	h(agent.Event{Kind: agent.KindText, Text: "echo: " + text})
	return nil
}

func (s *stubEngine) History() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.Message(nil), s.history...)
}
func (s *stubEngine) Model() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}
func (s *stubEngine) SetModel(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = m
}
func (s *stubEngine) ProviderName() string { return "stub" }
func (s *stubEngine) Approval() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approval
}
func (s *stubEngine) SessionID() string { return "20260101-000000-test" }
func (s *stubEngine) Root() string {
	if s.root != "" {
		return s.root
	}
	return "/tmp/stub"
}
func (s *stubEngine) SetApprover(a agent.Approver) { s.approver = a }
func (s *stubEngine) NewSession() error            { s.newSessions++; s.history = nil; return nil }
func (s *stubEngine) Resume(id string) error       { s.resumed = id; return nil }
func (s *stubEngine) Configure(cfg *config.Config) error {
	s.SetModel(cfg.Model)
	return s.SetApproval(cfg.Approval)
}
func (s *stubEngine) Title(ctx context.Context, text string) string {
	s.mu.Lock()
	s.titleCalls++
	s.mu.Unlock()
	return "Model Named Thread"
}
func (s *stubEngine) Close() error { s.closed = true; return nil }

func (s *stubEngine) SetApproval(mode string) error {
	switch mode {
	case "ask", "auto", "yolo":
		s.mu.Lock()
		s.approval = mode
		s.mu.Unlock()
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
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("CODY_API_KEY", "")
	t.Setenv("CODY_NO_AUTOSTART", "1") // never launch a developer's real Ollama
	srv, err := New(Options{
		Root:  eng.Root(),
		Host:  "127.0.0.1",
		Token: "test-token",
		Cfg:   config.Default(),
		NewEngine: func(root string) (Engine, error) {
			if root == eng.Root() {
				return eng, nil
			}
			return &stubEngine{root: root, model: eng.Model(), approval: eng.Approval()}, nil
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

func get(t *testing.T, rawURL, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
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

func getJSON(t *testing.T, rawURL, token string, v any) {
	t.Helper()
	resp := get(t, rawURL, token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d: %s", rawURL, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, rawURL, token string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, rawURL+"?token="+token, strings.NewReader(string(b)))
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

func deleteReq(t *testing.T, rawURL, token string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodDelete, rawURL+"?token="+token, strings.NewReader(string(b)))
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

func patchReq(t *testing.T, rawURL, token string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPatch, rawURL+"?token="+token, strings.NewReader(string(b)))
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

func readSSE(t *testing.T, ctx context.Context, rawURL, token string, onState func()) []map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL+"/api/events?token="+token, nil)
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
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	srv, err := New(Options{
		Root:      "/tmp/stub",
		Host:      "127.0.0.1",
		Token:     "test-token",
		NewEngine: func(string) (Engine, error) { return &stubEngine{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
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
	stub := srv.active().eng.(*stubEngine)
	go func() {
		allow, err := stub.approver.Approve(ctx, stubTool{}, `{"command":"echo hi"}`)
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
	if !strings.Contains(string(b), "mdToHTML") {
		t.Fatal("index page missing markdown renderer")
	}
	if !strings.Contains(string(b), "space-tree") {
		t.Fatal("index page missing project tree")
	}
	if !strings.Contains(string(b), `data-tab="changes"`) || !strings.Contains(string(b), `data-tab="files"`) {
		t.Fatal("index page missing right pane tabs")
	}
	if !strings.Contains(string(b), "composer-wrap") || strings.Contains(string(b), "<footer>") {
		t.Fatal("composer should live inside the middle pane, not a full-width footer")
	}
	if !strings.Contains(string(b), `id="toggle-left-mini"`) || !strings.Contains(string(b), `id="toggle-right-mini"`) {
		t.Fatal("index page missing header fallback toggles")
	}
	if !strings.Contains(string(b), `id="lights"`) || !strings.Contains(string(b), `id="split-left"`) || !strings.Contains(string(b), `id="split-right"`) {
		t.Fatal("index page missing native chrome or split handles")
	}
	if !strings.Contains(string(b), `id="picker-input"`) {
		t.Fatal("index page missing manual path entry in the folder picker")
	}
	if !strings.Contains(string(b), `id="settings-view"`) || !strings.Contains(string(b), `id="model-menu"`) || !strings.Contains(string(b), `id="open-settings"`) {
		t.Fatal("index page missing settings view or model picker")
	}
	if !strings.Contains(string(b), `id="model-hint"`) || !strings.Contains(string(b), `item.title = section === "ollama"`) {
		t.Fatal("index page missing model not-found hint or hover details")
	}
	if !strings.Contains(string(b), `id="close-settings"`) {
		t.Fatal("index page missing settings back control")
	}
}

func TestPickFolderRouteRegistered(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})
	resp := get(t, ts.URL+"/api/pick-folder", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 behind auth (route must exist)", resp.StatusCode)
	}
}

func TestFilesAndFilePreview(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi there"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, &stubEngine{root: root})

	var listing struct {
		Path    string      `json:"path"`
		Entries []fileEntry `json:"entries"`
	}
	getJSON(t, ts.URL+"/api/files", "test-token", &listing)
	if len(listing.Entries) != 2 || !listing.Entries[0].Dir || listing.Entries[0].Name != "sub" {
		t.Fatalf("entries = %+v", listing.Entries)
	}

	var file struct {
		Content string `json:"content"`
	}
	getJSON(t, ts.URL+"/api/file?path=hello.txt", "test-token", &file)
	if file.Content != "hi there" {
		t.Fatalf("content = %q", file.Content)
	}

	resp := get(t, ts.URL+"/api/file?path=../escape", "test-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("escape status = %d, want 400", resp.StatusCode)
	}
}

func TestFSBrowser(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, &stubEngine{root: root})

	var out struct {
		Path string `json:"path"`
		Dirs []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"dirs"`
	}
	getJSON(t, ts.URL+"/api/fs?path="+url.QueryEscape(root), "test-token", &out)
	if out.Path != root || len(out.Dirs) != 1 || out.Dirs[0].Name != "child" {
		t.Fatalf("fs = %+v", out)
	}
}

func TestGitStatusAndDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	gitRun("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "a.txt")
	gitRun("commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, &stubEngine{root: root})

	var status struct {
		Repo  bool      `json:"repo"`
		Files []gitFile `json:"files"`
	}
	getJSON(t, ts.URL+"/api/git", "test-token", &status)
	if !status.Repo || len(status.Files) != 2 {
		t.Fatalf("status = %+v", status)
	}
	labels := map[string]string{}
	for _, f := range status.Files {
		labels[f.Path] = f.Label
	}
	if labels["a.txt"] != "modified" || labels["b.txt"] != "untracked" {
		t.Fatalf("labels = %+v", labels)
	}

	var diff struct {
		Diff string `json:"diff"`
	}
	getJSON(t, ts.URL+"/api/git/diff?path=a.txt", "test-token", &diff)
	if !strings.Contains(diff.Diff, "-old") || !strings.Contains(diff.Diff, "+new") {
		t.Fatalf("diff = %q", diff.Diff)
	}

	var fresh struct {
		Diff string `json:"diff"`
	}
	getJSON(t, ts.URL+"/api/git/diff?path=b.txt", "test-token", &fresh)
	if !strings.Contains(fresh.Diff, "+fresh") {
		t.Fatalf("untracked diff = %q", fresh.Diff)
	}
}

func TestSpaces(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	eng := &stubEngine{root: root}
	ts, srv := newTestServer(t, eng)

	var out struct {
		Spaces []Space `json:"spaces"`
		Active string  `json:"active"`
	}
	getJSON(t, ts.URL+"/api/spaces", "test-token", &out)
	if len(out.Spaces) != 1 || out.Active != root {
		t.Fatalf("spaces = %+v", out)
	}

	resp := post(t, ts.URL+"/api/spaces", "test-token", map[string]string{"name": "Other", "path": other})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("add space status = %d: %s", resp.StatusCode, body)
	}
	getJSON(t, ts.URL+"/api/spaces", "test-token", &out)
	if len(out.Spaces) != 2 || out.Active != other {
		t.Fatalf("after add: %+v", out)
	}
	if srv.active().eng.Root() != other {
		t.Fatalf("engine root = %s", srv.active().eng.Root())
	}

	resp = post(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": filepath.Join(root, "nope")})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad path status = %d", resp.StatusCode)
	}

	// Deleting the active project is allowed when another project remains.
	resp = deleteReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete active status = %d, want 200", resp.StatusCode)
	}
	if srv.active().eng.Root() != root {
		t.Fatalf("active after delete = %s, want %s", srv.active().eng.Root(), root)
	}

	resp = post(t, ts.URL+"/api/space", "test-token", map[string]string{"path": root})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch status = %d", resp.StatusCode)
	}
	if srv.active().eng.Root() != root {
		t.Fatalf("engine root = %s, want %s", srv.active().eng.Root(), root)
	}

	resp = deleteReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	getJSON(t, ts.URL+"/api/spaces", "test-token", &out)
	if len(out.Spaces) != 1 {
		t.Fatalf("after delete: %+v", out.Spaces)
	}
}

func TestRenameAndDeleteProject(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	eng := &stubEngine{root: root}
	ts, srv := newTestServer(t, eng)

	resp := patchReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": root, "name": "renamed"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	var out struct {
		Spaces []Space `json:"spaces"`
	}
	getJSON(t, ts.URL+"/api/spaces", "test-token", &out)
	if len(out.Spaces) != 1 || out.Spaces[0].Name != "renamed" {
		t.Fatalf("spaces = %+v", out.Spaces)
	}

	resp = patchReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": root, "name": "  "})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank name status = %d, want 400", resp.StatusCode)
	}

	resp = post(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	if srv.active().eng.Root() != other {
		t.Fatalf("active = %s, want %s", srv.active().eng.Root(), other)
	}

	// Deleting the active project switches to the remaining one first.
	resp = deleteReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete active status = %d", resp.StatusCode)
	}
	if srv.active().eng.Root() != root {
		t.Fatalf("active after delete = %s, want %s", srv.active().eng.Root(), root)
	}

	// The last project cannot be removed.
	resp = deleteReq(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": root})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete last status = %d, want 400", resp.StatusCode)
	}
}

func TestRenameAndDeleteThread(t *testing.T) {
	root := t.TempDir()
	eng := &stubEngine{root: root}
	ts, _ := newTestServer(t, eng)

	// A real transcript for the session being renamed.
	store := session.New(root, "abc")
	if err := store.Append(session.Record{Type: "meta", Meta: map[string]string{"model": "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(session.Record{Type: "message", Role: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	resp := post(t, ts.URL+"/api/session/rename", "test-token", map[string]string{"id": "abc", "title": "My thread"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename thread status = %d", resp.StatusCode)
	}
	var list struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	getJSON(t, ts.URL+"/api/sessions", "test-token", &list)
	if len(list.Sessions) != 1 || list.Sessions[0].First != "My thread" {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
	resp = post(t, ts.URL+"/api/session/rename", "test-token", map[string]string{"id": "abc", "title": ""})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank title status = %d, want 400", resp.StatusCode)
	}

	// Deleting the active thread starts a fresh session first.
	resp = post(t, ts.URL+"/api/session", "test-token", map[string]string{"id": "new"})
	resp.Body.Close()
	if eng.newSessions != 1 {
		t.Fatalf("newSessions = %d", eng.newSessions)
	}
	resp = deleteReq(t, ts.URL+"/api/session", "test-token", map[string]string{"id": "20260101-000000-test"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete thread status = %d", resp.StatusCode)
	}
	if eng.newSessions != 2 {
		t.Fatalf("deleting the active thread should start a new session, newSessions = %d", eng.newSessions)
	}
}

func TestSwitchWhileBusyAndPerProjectBusy(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	blocking := &stubEngine{
		root: root,
		send: func(ctx context.Context, text string, h agent.Handler) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ts, srv := newTestServer(t, blocking)

	resp := post(t, ts.URL+"/api/spaces", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	resp = post(t, ts.URL+"/api/space", "test-token", map[string]string{"path": root})
	resp.Body.Close()

	resp = post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "hi"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt status = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var st struct {
			Busy bool `json:"busy"`
		}
		getJSON(t, ts.URL+"/api/state", "test-token", &st)
		if st.Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never became busy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Switching focus while a turn runs must work.
	resp = post(t, ts.URL+"/api/space", "test-token", map[string]string{"path": other})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch while busy status = %d, want 200", resp.StatusCode)
	}
	if srv.active().eng.Root() != other {
		t.Fatalf("active root = %s, want %s", srv.active().eng.Root(), other)
	}

	// The busy project rejects another prompt with structured 409.
	resp = post(t, ts.URL+"/api/prompt", "test-token", map[string]any{"text": "again", "path": root})
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || body["error"] != "busy" {
		t.Fatalf("busy prompt: status %d body %+v", resp.StatusCode, body)
	}
	if body["root"] != root {
		t.Fatalf("409 root = %v, want %s", body["root"], root)
	}

	// The other project can run its own turn concurrently.
	other2 := &stubEngine{root: other}
	_ = other2
	resp = post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "parallel"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("parallel prompt in another project status = %d", resp.StatusCode)
	}

	resp = post(t, ts.URL+"/api/interrupt", "test-token", map[string]string{"path": root})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("interrupt status = %d", resp.StatusCode)
	}
}

func TestSettingsEndpoints(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{root: t.TempDir()})

	var settings map[string]any
	getJSON(t, ts.URL+"/api/settings", "test-token", &settings)
	if settings["provider"] != "ollama" || settings["openrouterKeySet"] != false {
		t.Fatalf("settings = %+v", settings)
	}

	resp := post(t, ts.URL+"/api/settings", "test-token", map[string]any{
		"model": "qwen3-coder:14b", "approval": "auto", "openrouterKey": "sk-or-test-key",
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings post status = %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "sk-or-test-key") {
		t.Fatal("API key must never be echoed back")
	}
	getJSON(t, ts.URL+"/api/settings", "test-token", &settings)
	if settings["model"] != "qwen3-coder:14b" || settings["approval"] != "auto" || settings["openrouterKeySet"] != true {
		t.Fatalf("settings after post = %+v", settings)
	}
	info, err := os.Stat(config.Path())
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("config perms = %o, want 600", perm)
	}

	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"approval": "reckless"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad approval status = %d", resp.StatusCode)
	}
}

func TestModelsEndpoint(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{root: t.TempDir()})
	resp := get(t, ts.URL+"/api/models", "test-token")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["ollama"]; !ok {
		t.Fatal("models response missing ollama section")
	}
	or, ok := out["openrouter"].(map[string]any)
	if !ok || or["available"] == true {
		t.Fatalf("openrouter should be unavailable without a key: %+v", out["openrouter"])
	}
}

func TestSessions(t *testing.T) {
	eng := &stubEngine{root: t.TempDir()}
	ts, _ := newTestServer(t, eng)

	var list struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	getJSON(t, ts.URL+"/api/sessions", "test-token", &list)
	if list.Sessions == nil {
		t.Fatal("sessions should be an empty list, not null")
	}

	resp := post(t, ts.URL+"/api/session", "test-token", map[string]string{"id": "new"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || eng.newSessions != 1 {
		t.Fatalf("new session: status %d, calls %d", resp.StatusCode, eng.newSessions)
	}

	resp = post(t, ts.URL+"/api/session", "test-token", map[string]string{"id": "20260101-000000-abcd"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || eng.resumed != "20260101-000000-abcd" {
		t.Fatalf("resume: status %d, id %q", resp.StatusCode, eng.resumed)
	}
}

func TestThreadsPerProject(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	eng := &stubEngine{root: root}
	ts, srv := newTestServer(t, eng)

	resp := post(t, ts.URL+"/api/spaces", "test-token", map[string]string{"name": "Other", "path": other})
	resp.Body.Close()
	if srv.active().eng.Root() != other {
		t.Fatalf("active root = %s", srv.active().eng.Root())
	}

	// Threads can be listed for a project that is not active.
	var list struct {
		Sessions []sessionInfo `json:"sessions"`
		Active   string        `json:"active"`
	}
	getJSON(t, ts.URL+"/api/sessions?path="+url.QueryEscape(root), "test-token", &list)
	if list.Active != "" {
		t.Fatalf("active should be empty for a non-active project, got %q", list.Active)
	}

	resp = get(t, ts.URL+"/api/sessions?path="+url.QueryEscape("/nope"), "test-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown project status = %d, want 400", resp.StatusCode)
	}

	// Opening a thread in another project switches to it first.
	resp = post(t, ts.URL+"/api/session", "test-token", map[string]any{"path": root, "id": "new"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open with path status = %d", resp.StatusCode)
	}
	if srv.active().eng.Root() != root {
		t.Fatalf("engine root = %s, want %s", srv.active().eng.Root(), root)
	}
}

func TestIndexGzipAndETag(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{})
	plain := &http.Client{}
	raw := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := raw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("index missing ETag")
	}
	compressed, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	gunzipped, _ := io.ReadAll(zr)
	zr.Close()
	if !strings.Contains(string(gunzipped), "cody") {
		t.Fatal("gunzipped index missing brand")
	}
	if len(compressed)*3 > len(gunzipped) {
		t.Fatalf("gzip barely compressed: %d -> %d", len(gunzipped), len(compressed))
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidate status = %d, want 304", resp.StatusCode)
	}
}

func TestStateEventOmitsHistoryUnlessFull(t *testing.T) {
	eng := &stubEngine{history: []provider.Message{{Role: "user", Content: "hi"}}}
	_, srv := newTestServer(t, eng)

	light := srv.stateEvent()
	if len(light.History) != 0 {
		t.Fatalf("steady-state event carries %d history messages", len(light.History))
	}
	if light.Session == "" || light.Type != "state" {
		t.Fatalf("light event missing session/type: %+v", light)
	}

	full := srv.stateEventFull()
	if len(full.History) != 1 || full.History[0].Text != "hi" {
		t.Fatalf("full event history = %+v", full.History)
	}
}

func TestGitStatusCached(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %s", out)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, srv := newTestServer(t, &stubEngine{root: root})

	var first struct {
		Repo   bool      `json:"repo"`
		Branch string    `json:"branch"`
		Files  []gitFile `json:"files"`
	}
	getJSON(t, ts.URL+"/api/git", "test-token", &first)
	if !first.Repo || len(first.Files) == 0 {
		t.Fatalf("git status = %+v", first)
	}
	srv.gitMu.Lock()
	cached, ok := srv.gitCache[root]
	srv.gitMu.Unlock()
	if !ok {
		t.Fatal("git status was not cached")
	}

	// A new file inside the TTL must not change the response yet.
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	var second struct {
		Repo   bool      `json:"repo"`
		Branch string    `json:"branch"`
		Files  []gitFile `json:"files"`
	}
	getJSON(t, ts.URL+"/api/git", "test-token", &second)
	if len(second.Files) != len(cached.files) {
		t.Fatalf("cache bypassed: %d files vs %d cached", len(second.Files), len(cached.files))
	}
}

func TestModelsCachedAndInvalidated(t *testing.T) {
	ts, srv := newTestServer(t, &stubEngine{root: t.TempDir()})

	resp := get(t, ts.URL+"/api/models", "test-token")
	first, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d", resp.StatusCode)
	}
	srv.modelsMu.Lock()
	cached := srv.modelsCache.at
	srv.modelsMu.Unlock()
	if cached.IsZero() {
		t.Fatal("models response was not cached")
	}

	resp = get(t, ts.URL+"/api/models", "test-token")
	second, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(first, second) {
		t.Fatal("second models response should be served from cache")
	}

	// Saving settings must invalidate the snapshot (e.g. a new key).
	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"approval": "auto"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", resp.StatusCode)
	}
	srv.modelsMu.Lock()
	invalidated := srv.modelsCache.at.IsZero()
	srv.modelsMu.Unlock()
	if !invalidated {
		t.Fatal("settings save did not invalidate the models cache")
	}
}

func TestGitDiffTruncated(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", out)
	}
	var sb strings.Builder
	for i := 0; i < maxDiffLines+500; i++ {
		sb.WriteString("line number in a very large generated file\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, &stubEngine{root: root})

	var out struct {
		Diff      string `json:"diff"`
		Truncated bool   `json:"truncated"`
	}
	getJSON(t, ts.URL+"/api/git/diff?path=big.txt", "test-token", &out)
	if !out.Truncated {
		t.Fatal("huge diff should be flagged truncated")
	}
	if n := strings.Count(out.Diff, "\n") + 1; n > maxDiffLines+1 {
		t.Fatalf("diff has %d lines, cap is %d", n, maxDiffLines)
	}
}

func TestGitLabel(t *testing.T) {
	for code, want := range map[string]string{
		"??": "untracked", "M": "modified", "MM": "modified", "A": "added", "AM": "added",
		"D": "deleted", "R": "renamed", "C": "copied", "UU": "conflict", "": "clean", "RM": "modified",
	} {
		if got := gitLabel(code); got != want {
			t.Errorf("gitLabel(%q) = %q, want %q", code, got, want)
		}
	}
}
