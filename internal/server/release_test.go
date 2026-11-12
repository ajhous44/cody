package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/engine"
	"github.com/ajhous44/cody/v3/internal/session"
)

func TestSettingsRefreshProviderKey(t *testing.T) {
	dummy, s := newTestServer(t, &stubEngine{root: t.TempDir(), model: "m", approval: "ask"})
	defer dummy.Close()
	auth := make(chan string, 2)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		auth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer providerServer.Close()
	cfg := config.Default()
	cfg.Provider = "openrouter"
	cfg.Model = "test"
	cfg.Providers["openrouter"] = config.Provider{BaseURL: providerServer.URL, APIKey: "old-test-key"}
	root := t.TempDir()
	eng, err := engine.New(engine.Options{Root: root, Cfg: cfg, Store: session.New(root, "")})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	s.opts.Cfg = cfg
	s.activeRoot = root
	s.runners = map[string]*runner{root: {root: root, eng: eng}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"openrouterKey":"new-test-key"}`))
	s.handleSettings(rr, req)
	if rr.Code != 200 {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := eng.Send(ctx, "hello", func(agent.Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := <-auth; got != "Bearer new-test-key" {
		t.Errorf("saved new key but next request used %q", got)
	}
}

func TestPreviewsRejectOutsideSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, &stubEngine{root: root})
	for _, endpoint := range []string{"/api/file?path=escape/secret.txt", "/api/files?path=escape"} {
		resp := get(t, ts.URL+endpoint, "test-token")
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s allowed outside access: %d", endpoint, resp.StatusCode)
		}
	}
}

func TestShutdownWaitsForTurnAndTitle(t *testing.T) {
	started := make(chan struct{})
	eng := &stubEngine{root: t.TempDir(), send: func(ctx context.Context, _ string, _ agent.Handler) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	ts, s := newTestServer(t, eng)
	resp := post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "hi"})
	resp.Body.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !eng.closed || s.stateEvent().Busy {
		t.Fatal("shutdown returned before releasing the runner")
	}
	resp = post(t, ts.URL+"/api/prompt", "test-token", map[string]string{"text": "again"})
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatal("shut down server accepted a new turn")
	}
}
