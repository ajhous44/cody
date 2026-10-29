package config

import "testing"

func TestWebConfigOptInAndEnvironment(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	t.Setenv("CODY_WEB", "")
	t.Setenv("CODY_BROWSER", "")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebEnabled || cfg.BrowserEnabled || cfg.SearchKey() != "" {
		t.Fatal("web features enabled by default")
	}
	cfg.WebEnabled = true
	cfg.BraveSearchKey = "stored-test-key"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil || !cfg.WebEnabled || cfg.BrowserEnabled || cfg.SearchKey() != "stored-test-key" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("CODY_WEB", "false")
	t.Setenv("CODY_BROWSER", "true")
	t.Setenv("BRAVE_SEARCH_API_KEY", "env-test-key")
	cfg, err = Load()
	if err != nil || cfg.WebEnabled || !cfg.BrowserEnabled || cfg.SearchKey() != "env-test-key" {
		t.Fatalf("env config err=%v", err)
	}
	if cfg.Clone().BraveSearchKey != "stored-test-key" {
		t.Fatal("clone lost stored key")
	}
}

// Hosted embeddings upload every indexed file, so only an explicit opt-in
// leaves the local default.
func TestEmbeddingDefaultsLocalAndOptsIntoOpenRouter(t *testing.T) {
	t.Setenv("CODY_CONFIG_DIR", t.TempDir())
	for _, tc := range []struct{ env, model, wantProvider, wantModel string }{
		{"", "", "ollama", DefaultEmbedModel},
		{"bogus", "", "ollama", DefaultEmbedModel},
		{"OpenRouter", "", "openrouter", DefaultOpenRouterEmbedModel},
		{"openrouter", "voyageai/voyage-code-3", "openrouter", "voyageai/voyage-code-3"},
	} {
		t.Setenv("CODY_EMBED_PROVIDER", tc.env)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if tc.model != "" {
			cfg.EmbedModel = tc.model
		}
		if p, m := cfg.Embedding(); p != tc.wantProvider || m != tc.wantModel {
			t.Errorf("env %q: got %s/%s, want %s/%s", tc.env, p, m, tc.wantProvider, tc.wantModel)
		}
	}
}
