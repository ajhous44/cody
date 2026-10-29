package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"

	"github.com/ajhous44/cody/v3/internal/config"
)

func TestWebSettingsAreIndependentAndKeysStayPrivate(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	ts, _ := newTestServer(t, &stubEngine{root: t.TempDir()})
	resp := post(t, ts.URL+"/api/settings", "test-token", map[string]any{"webEnabled": true, "braveSearchKey": "test-search-secret"})
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(data), "test-search-secret") {
		t.Fatalf("settings leaked or failed: %d %s", resp.StatusCode, data)
	}
	var settings map[string]any
	getJSON(t, ts.URL+"/api/settings", "test-token", &settings)
	if settings["webEnabled"] != true || settings["browserEnabled"] != false || settings["braveSearchKeySet"] != true {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	cfg, err := config.Load()
	if err != nil || !cfg.WebEnabled || cfg.BrowserEnabled || cfg.SearchKey() != "test-search-secret" {
		t.Fatal("settings not persisted independently")
	}
	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"browserEnabled": true, "braveSearchKey": ""})
	resp.Body.Close()
	getJSON(t, ts.URL+"/api/settings", "test-token", &settings)
	if settings["webEnabled"] != true || settings["browserEnabled"] != true || settings["braveSearchKeySet"] != false {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"braveSearchKey": "bad\r\nheader"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("invalid key accepted")
	}
}

func TestDisablingWebToolsCancelsActiveTurn(t *testing.T) {
	for _, feature := range []string{"webEnabled", "browserEnabled"} {
		t.Run(feature, func(t *testing.T) {
			entered, stopped := make(chan struct{}), make(chan struct{})
			eng := &stubEngine{root: t.TempDir(), send: func(ctx context.Context, _ string, _ agent.Handler) error {
				close(entered)
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			}}
			ts, srv := newTestServer(t, eng)
			defer srv.Close()
			resp := post(t, ts.URL+"/api/settings", "test-token", map[string]any{feature: true})
			resp.Body.Close()
			resp = post(t, ts.URL+"/api/prompt", "test-token", map[string]any{"text": "wait"})
			resp.Body.Close()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("turn did not start")
			}
			resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{feature: false})
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatal(resp.Status)
			}
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("disabled tools left a turn running")
			}
		})
	}
}

// A GUI save persists only what the user changed: environment overrides and
// env-provided keys stay in memory for this run and never reach config.json.
func TestSettingsSaveDoesNotPersistEnvironment(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(config.Path(), []byte(`{"model":"saved-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"OPENROUTER_API_KEY": "env-secret", "CODY_API_KEY": "", "CODY_PROVIDER": "openrouter",
		"CODY_APPROVAL": "yolo", "CODY_EMBED_PROVIDER": "openrouter", "CODY_WEB": "true", "CODY_MODEL": "",
	} {
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	eng := &stubEngine{root: t.TempDir()}
	srv, err := New(Options{Root: eng.Root(), Host: "127.0.0.1", Token: "test-token", Cfg: cfg,
		NewEngine: func(string) (Engine, error) { return eng, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := post(t, ts.URL+"/api/settings", "test-token", map[string]any{"browserEnabled": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	raw, _ := os.ReadFile(config.Path())
	saved, err := config.LoadFile()
	if err != nil || strings.Contains(string(raw), "env-secret") || saved.Provider != "ollama" || saved.Approval != "ask" ||
		saved.EmbedProvider != "ollama" || saved.WebEnabled || !saved.BrowserEnabled || saved.Model != "saved-model" {
		t.Fatalf("environment leaked into config.json or edit lost:\n%s", raw)
	}
	var live map[string]any
	getJSON(t, ts.URL+"/api/settings", "test-token", &live)
	if live["provider"] != "openrouter" || live["webEnabled"] != true || live["browserEnabled"] != true {
		t.Fatalf("running settings lost environment overrides: %+v", live)
	}
	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"openrouterKey": "gui-key"})
	resp.Body.Close()
	if saved, _ = config.LoadFile(); saved.ProviderConfig("openrouter").APIKey != "gui-key" {
		t.Fatal("key entered in the GUI not saved")
	}
}

// The picker goes through the same Ollama autostart as a turn, so a stopped
// local Ollama is started (here refused by CODY_NO_AUTOSTART) instead of its
// models silently missing.
func TestModelProbeUsesOllamaAutostart(t *testing.T) {
	t.Setenv("CODY_NO_AUTOSTART", "1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg := config.Default()
	cfg.Providers["ollama"] = config.Provider{BaseURL: "http://" + addr + "/v1"}
	ollama, _ := probeModels(context.Background(), cfg)
	if ollama["available"] != false || !strings.Contains(fmt.Sprint(ollama["error"]), "autostart") {
		t.Fatalf("probe skipped autostart: %+v", ollama)
	}
}

func TestSandboxSettingValidatedAndSaved(t *testing.T) {
	ts, _ := newTestServer(t, &stubEngine{root: t.TempDir()})
	resp := post(t, ts.URL+"/api/settings", "test-token", map[string]any{"sandbox": "everywhere"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mode accepted: %d", resp.StatusCode)
	}
	resp = post(t, ts.URL+"/api/settings", "test-token", map[string]any{"sandbox": "strict"})
	resp.Body.Close()
	var live map[string]any
	getJSON(t, ts.URL+"/api/settings", "test-token", &live)
	if saved, _ := config.LoadFile(); live["sandbox"] != "strict" || saved.Sandbox != "strict" {
		t.Fatalf("sandbox not applied and saved: %v / %+v", live["sandbox"], saved)
	}
}
