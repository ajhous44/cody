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
	"testing"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/index"
	"github.com/ajhous44/cody/v3/internal/tool"
)

func TestOptionalWebToolRegistrationAndBrowserApproval(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	e := &Engine{cfg: config.Default(), root: t.TempDir()}
	for _, name := range []string{"web_fetch", "web_search", "browser"} {
		if _, ok := e.tools().Get(name); ok {
			t.Fatalf("%s enabled by default", name)
		}
	}
	e.cfg.WebEnabled = true
	if _, ok := e.tools().Get("web_fetch"); !ok {
		t.Fatal("page reading missing")
	}
	if _, ok := e.tools().Get("web_search"); ok {
		t.Fatal("search exposed without key")
	}
	e.cfg.BraveSearchKey = "fixture-secret"
	e.cfg.BrowserEnabled = true
	registry := e.tools()
	for _, name := range []string{"web_fetch", "web_search", "browser"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing %s", name)
		}
	}
	// Every tool that reaches outside the workspace asks in ask and auto modes.
	for _, name := range []string{"web_fetch", "web_search", "browser"} {
		item, _ := registry.Get(name)
		if !item.Mutating() {
			t.Fatalf("%s bypasses approval", name)
		}
		for _, mode := range []string{"ask", "auto", "yolo"} {
			e.cfg.Approval = mode
			asked := false
			e.approver = agent.ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) { asked = true; return false, nil })
			ok, err := e.approverFor().Approve(context.Background(), item, `{"url":"https://example.com"}`)
			if err != nil || asked != (mode != "yolo") || ok != (mode == "yolo") {
				t.Fatalf("%s mode %s: asked=%v allow=%v err=%v", name, mode, asked, ok, err)
			}
		}
	}
	e.cfg.BrowserEnabled = false
	e.tools()
	if e.browser != nil {
		t.Fatal("disabled browser retained")
	}
}

func TestWebFetchFitsColdLocalContext(t *testing.T) {
	if os.Getenv("CODY_WEB_TEST") != "1" {
		t.Skip("set CODY_WEB_TEST=1 for the public-page context regression")
	}
	for _, browserEnabled := range []bool{false, true} {
		srv := sseServer(t, toolCallResponse("web_fetch", `{"url":"https://go.dev/doc/"}`), finalResponse)
		cfg := testConfig(srv, "ask")
		cfg.WebEnabled, cfg.BrowserEnabled = true, browserEnabled
		eng := newEngine(t, cfg, t.TempDir(), nil)
		eng.budgetReady = true
		eng.ag.SetLimits(defaultBudget("ollama"), 0)
		err := eng.Send(context.Background(), "Read the Go documentation page and cite its title.", nil)
		srv.Close()
		if err != nil {
			t.Fatalf("cold local context (browser=%v): %v", browserEnabled, err)
		}
	}
}

// Tools and rules the model cannot use are not sent: search appears only
// once `cody index` has built an index, even mid-session.
func TestSearchToolAndOptionalRulesFollowAvailability(t *testing.T) {
	requests := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"test","context_length":16384}]}`)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests <- req
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	root := t.TempDir()
	cfg := config.Default()
	cfg.Provider, cfg.Model = "openrouter", "test"
	cfg.Providers["openrouter"] = config.Provider{BaseURL: srv.URL}
	eng := newEngine(t, cfg, root, nil)
	send := func() (tools string, system string) {
		t.Helper()
		if err := eng.Send(context.Background(), "hi", nil); err != nil {
			t.Fatal(err)
		}
		req := <-requests
		b, _ := json.Marshal(req["tools"])
		return string(b), req["messages"].([]any)[0].(map[string]any)["content"].(string)
	}
	tools, system := send()
	if strings.Contains(tools, `"search"`) || strings.Contains(system, "web_fetch") || strings.Contains(system, "browser") {
		t.Fatalf("unavailable tool advertised:\ntools %s\nsystem %s", tools, system)
	}
	db := index.Path(root)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if tools, _ = send(); !strings.Contains(tools, `"search"`) {
		t.Fatalf("search missing after indexing: %s", tools)
	}
}

// The plan tool exists only with the gate on, and it always reaches the user,
// even in yolo, while yolo still skips approval for everything else.
func TestPlanGateToolAndApproval(t *testing.T) {
	e := &Engine{cfg: config.Default(), root: t.TempDir()}
	if _, ok := e.tools().Get("plan"); ok {
		t.Fatal("plan offered with the gate off")
	}
	e.cfg.PlanGate = true
	if _, ok := e.tools().Get("plan"); !ok {
		t.Fatal("plan missing with the gate on")
	}
	var asked []string
	e.cfg.Approval = "yolo"
	e.approver = agent.ApproverFunc(func(_ context.Context, tl tool.Tool, _ string) (bool, error) {
		asked = append(asked, tl.Name())
		return false, nil
	})
	approve := e.approverFor()
	if ok, _ := approve.Approve(context.Background(), tool.NewPlan(), `{"steps":["x"]}`); ok {
		t.Fatal("plan approved without asking in yolo")
	}
	if ok, _ := approve.Approve(context.Background(), tool.NewBash(e.root), `{"command":"true"}`); !ok {
		t.Fatal("yolo stopped auto-approving bash")
	}
	if strings.Join(asked, ",") != "plan" {
		t.Fatalf("asked %v, want only plan", asked)
	}
}

// Skills cost nothing until one exists; a skill added mid-session shows up
// (index line plus skill tool) on the next request.
func TestSkillsAppearWhenAdded(t *testing.T) {
	requests := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"test","context_length":16384}]}`)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests <- req
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	root := t.TempDir()
	cfg := config.Default()
	cfg.Provider, cfg.Model = "openrouter", "test"
	cfg.Providers["openrouter"] = config.Provider{BaseURL: srv.URL}
	eng := newEngine(t, cfg, root, nil)
	send := func() (string, string) {
		t.Helper()
		if err := eng.Send(context.Background(), "hi", nil); err != nil {
			t.Fatal(err)
		}
		req := <-requests
		b, _ := json.Marshal(req["tools"])
		return string(b), req["messages"].([]any)[0].(map[string]any)["content"].(string)
	}
	if tools, system := send(); strings.Contains(tools, `"skill"`) || strings.Contains(system, "Skills:") {
		t.Fatal("skills advertised with none installed")
	}
	dir := filepath.Join(root, ".cody", "skills", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: deploy\ndescription: ship to staging\n---\nsteps"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tools, system := send(); !strings.Contains(tools, `"skill"`) || !strings.Contains(system, "deploy: ship to staging") || strings.Contains(system, "steps") {
		t.Fatalf("skill not offered lazily:\ntools %s\nsystem %s", tools, system)
	}
}
