package server

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/provider"
)

type modelInfo struct {
	Provider        string   `json:"provider"`
	Name            string   `json:"name"`
	Size            int64    `json:"size,omitempty"`
	ParameterSize   string   `json:"parameterSize,omitempty"`
	Quantization    string   `json:"quantization,omitempty"`
	ContextLength   int      `json:"contextLength,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Loaded          bool     `json:"loaded,omitempty"`
	SizeVRAM        int64    `json:"sizeVram,omitempty"`
	ExpiresAt       string   `json:"expiresAt,omitempty"`
	PromptPrice     string   `json:"promptPrice,omitempty"`
	CompletionPrice string   `json:"completionPrice,omitempty"`
}

// modelsCacheTTL bounds how long the slow provider probes behind /api/models
// are reused. The envelope (current provider/model) is always live; only the
// listings are cached. Saving settings invalidates the snapshot.
const modelsCacheTTL = 30 * time.Second

// modelsSnapshot caches the provider listings behind /api/models.
type modelsSnapshot struct {
	at         time.Time
	ollama     map[string]any
	openrouter map[string]any
}

// handleModels reports the models available from each provider. Ollama and its
// runtime stats are always inspected (an installed but stopped local Ollama is
// started, as before a turn); OpenRouter is only listed when a key is
// configured, so a keyless install stays fully offline.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ollama, openrouter := s.cachedModels(r.Context())
	s.writeJSON(w, map[string]any{
		"provider":   s.active().eng.ProviderName(),
		"model":      s.active().eng.Model(),
		"ollama":     ollama,
		"openrouter": openrouter,
	})
}

func (s *Server) cachedModels(ctx context.Context) (map[string]any, map[string]any) {
	// Held across the probe so concurrent opens share one slow lookup
	// instead of each paying it.
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	if time.Since(s.modelsCache.at) < modelsCacheTTL {
		return s.modelsCache.ollama, s.modelsCache.openrouter
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ollama, openrouter := probeModels(ctx, s.configSnapshot())
	s.modelsCache = modelsSnapshot{at: time.Now(), ollama: ollama, openrouter: openrouter}
	return ollama, openrouter
}

func (s *Server) invalidateModels() {
	s.modelsMu.Lock()
	s.modelsCache.at = time.Time{}
	s.modelsMu.Unlock()
}

func probeModels(ctx context.Context, cfg *config.Config) (map[string]any, map[string]any) {
	ollamaBase := config.DefaultOllamaURL
	if cfg != nil {
		if b := cfg.ProviderConfig("ollama").BaseURL; b != "" {
			ollamaBase = b
		}
	}
	ollama := map[string]any{"available": false, "models": []modelInfo{}}
	var models []provider.Model
	_, err := provider.EnsureOllama(ctx, ollamaBase)
	if err == nil {
		models, err = provider.ListOllamaModels(ctx, ollamaBase)
	}
	if err == nil {
		loaded := map[string]provider.RunningModel{}
		if running, err := provider.OllamaRunning(ctx, ollamaBase); err == nil {
			for _, rm := range running {
				loaded[rm.Name] = rm
			}
		}
		list := make([]modelInfo, 0, len(models))
		for _, m := range models {
			mi := modelInfo{
				Provider:      "ollama",
				Name:          m.Name,
				Size:          m.Size,
				ParameterSize: m.Details.ParameterSize,
				Quantization:  m.Details.Quantization,
				ContextLength: m.Details.ContextLength,
				Capabilities:  m.Capabilities,
			}
			if rm, ok := loaded[m.Name]; ok {
				mi.Loaded = true
				mi.SizeVRAM = rm.SizeVRAM
				mi.ExpiresAt = rm.ExpiresAt
				if mi.ContextLength == 0 {
					mi.ContextLength = rm.ContextLength
				}
			}
			list = append(list, mi)
		}
		ollama = map[string]any{"available": true, "models": list}
	} else {
		ollama = map[string]any{"available": false, "error": err.Error(), "models": []modelInfo{}}
	}

	openrouter := map[string]any{"available": false, "models": []modelInfo{}}
	if cfg != nil && cfg.Key("openrouter") != "" {
		base := cfg.ProviderConfig("openrouter").BaseURL
		if base == "" {
			base = config.DefaultOpenRouterURL
		}
		if models, err := provider.ListOpenRouterModels(ctx, base); err == nil {
			list := make([]modelInfo, 0, len(models))
			for _, m := range models {
				list = append(list, modelInfo{
					Provider:        "openrouter",
					Name:            m.ID,
					ContextLength:   m.ContextLength,
					PromptPrice:     m.Pricing.Prompt,
					CompletionPrice: m.Pricing.Completion,
				})
			}
			openrouter = map[string]any{"available": true, "models": list}
		} else {
			openrouter = map[string]any{"available": true, "error": err.Error(), "models": []modelInfo{}}
		}
	}
	return ollama, openrouter
}

func (s *Server) settingsPayload() map[string]any {
	out := map[string]any{
		"configPath":       config.Path(),
		"provider":         "",
		"model":            "",
		"approval":         "ask",
		"embedModel":       "",
		"openrouterKeySet": false,
		"openrouterKeyEnv": false,
	}
	cfg := s.configSnapshot()
	if cfg == nil {
		return out
	}
	out["provider"] = cfg.Provider
	out["model"] = cfg.Model
	out["approval"] = cfg.Approval
	out["embedModel"] = cfg.EmbedModel
	out["openrouterKeySet"] = cfg.Key("openrouter") != ""
	out["openrouterKeyEnv"] = os.Getenv("OPENROUTER_API_KEY") != "" || os.Getenv("CODY_API_KEY") != ""
	out["webEnabled"] = cfg.WebEnabled
	out["browserEnabled"] = cfg.BrowserEnabled
	out["debug"] = cfg.Debug
	out["braveSearchKeySet"] = cfg.SearchKey() != ""
	out["braveSearchKeyEnv"] = os.Getenv("BRAVE_SEARCH_API_KEY") != ""
	return out
}

// handleSettings reads and writes the local config. Keys are persisted to
// ~/.cody/config.json (0600) and are never echoed back to clients.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, s.settingsPayload())
		return
	}
	var body struct {
		Provider       *string `json:"provider"`
		Model          *string `json:"model"`
		Approval       *string `json:"approval"`
		EmbedModel     *string `json:"embedModel"`
		OpenRouterKey  *string `json:"openrouterKey"`
		WebEnabled     *bool   `json:"webEnabled"`
		BrowserEnabled *bool   `json:"browserEnabled"`
		BraveSearchKey *string `json:"braveSearchKey"`
		Debug          *bool   `json:"debug"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg := s.configSnapshot()
	if cfg == nil {
		http.Error(w, "settings are not writable in this mode", http.StatusBadRequest)
		return
	}
	disableTools := (body.WebEnabled != nil && !*body.WebEnabled && cfg.WebEnabled) ||
		(body.BrowserEnabled != nil && !*body.BrowserEnabled && cfg.BrowserEnabled)
	if body.Provider != nil {
		p := strings.ToLower(strings.TrimSpace(*body.Provider))
		if p != "ollama" && p != "openrouter" && cfg.ProviderConfig(p).BaseURL == "" {
			http.Error(w, "unknown provider", http.StatusBadRequest)
			return
		}
		*body.Provider = p
	}
	if body.Approval != nil {
		switch *body.Approval = strings.TrimSpace(*body.Approval); *body.Approval {
		case "ask", "auto", "yolo":
		default:
			http.Error(w, "approval must be ask, auto, or yolo", http.StatusBadRequest)
			return
		}
	}
	if body.BraveSearchKey != nil {
		key := strings.TrimSpace(*body.BraveSearchKey)
		if len(key) > 512 || strings.ContainsAny(key, "\r\n") {
			http.Error(w, "invalid search key", http.StatusBadRequest)
			return
		}
		*body.BraveSearchKey = key
	}
	// The same edits go to the running config (which carries environment and
	// flag overrides) and to the saved file (which must not): only what the
	// user changed here is persisted.
	apply := func(c *config.Config) {
		if c.Providers == nil {
			c.Providers = map[string]config.Provider{}
		}
		if body.Provider != nil {
			c.Provider = *body.Provider
		}
		if body.Model != nil {
			c.Model = strings.TrimSpace(*body.Model)
		}
		if body.Approval != nil {
			c.Approval = *body.Approval
		}
		if body.EmbedModel != nil {
			c.EmbedModel = strings.TrimSpace(*body.EmbedModel)
		}
		if body.OpenRouterKey != nil {
			p := c.ProviderConfig("openrouter")
			p.APIKey = strings.TrimSpace(*body.OpenRouterKey)
			c.Providers["openrouter"] = p
		}
		if body.WebEnabled != nil {
			c.WebEnabled = *body.WebEnabled
		}
		if body.BrowserEnabled != nil {
			c.BrowserEnabled = *body.BrowserEnabled
		}
		if body.BraveSearchKey != nil {
			c.BraveSearchKey = *body.BraveSearchKey
		}
		if body.Debug != nil {
			c.Debug = *body.Debug
		}
	}
	saved, err := config.LoadFile()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apply(saved)
	if err := config.Save(saved); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apply(cfg)
	s.mu.Lock()
	s.opts.Cfg = cfg
	if disableTools {
		for _, rr := range s.runners {
			if rr.cancel != nil {
				rr.cancel()
			}
		}
	}
	s.mu.Unlock()
	s.invalidateModels()
	if err := s.syncIdleRunners(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeJSON(w, s.settingsPayload())
}
