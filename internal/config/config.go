package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultOllamaURL     = "http://localhost:11434/v1"
	DefaultOpenRouterURL = "https://openrouter.ai/api/v1"
	DefaultEmbedModel    = "nomic-embed-text"
	// DefaultOpenRouterEmbedModel is used when embed_provider is openrouter
	// and embed_model is still the local default.
	DefaultOpenRouterEmbedModel = "openai/text-embedding-3-small"
)

type Provider struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

type Config struct {
	Provider       string              `json:"provider"`
	Model          string              `json:"model"`
	Approval       string              `json:"approval"`
	EmbedModel     string              `json:"embed_model"`
	EmbedProvider  string              `json:"embed_provider"`
	Providers      map[string]Provider `json:"providers"`
	WebEnabled     bool                `json:"web_enabled,omitempty"`
	BrowserEnabled bool                `json:"browser_enabled,omitempty"`
	BraveSearchKey string              `json:"brave_search_key,omitempty"`
	Debug          bool                `json:"debug,omitempty"`
	PlanGate       bool                `json:"plan_gate,omitempty"`
	// Sandbox confines shell commands: off, workspace, or strict.
	Sandbox         string   `json:"sandbox,omitempty"`
	SandboxWritable []string `json:"sandbox_writable,omitempty"`
}

func Dir() string {
	if dir := os.Getenv("CODY_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cody"
	}
	return filepath.Join(home, ".cody")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func Default() *Config {
	return &Config{
		Provider:      "ollama",
		Approval:      "ask",
		EmbedModel:    DefaultEmbedModel,
		EmbedProvider: "ollama",
		Providers: map[string]Provider{
			"ollama":     {BaseURL: DefaultOllamaURL},
			"openrouter": {BaseURL: DefaultOpenRouterURL},
		},
	}
}

// Load returns the saved settings with environment overrides applied.
func Load() (*Config, error) {
	c, err := LoadFile()
	if err != nil {
		return nil, err
	}
	c.applyEnv()
	c.normalize()
	return c, nil
}

// LoadFile returns only the saved settings. Edit and Save this, not Load's
// result, so environment overrides and env-provided keys never reach disk.
func LoadFile() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", Path(), err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	c.normalize()
	return c, nil
}

func (c *Config) applyEnv() {
	if v, err := strconv.ParseBool(os.Getenv("CODY_WEB")); err == nil {
		c.WebEnabled = v
	}
	if v, err := strconv.ParseBool(os.Getenv("CODY_BROWSER")); err == nil {
		c.BrowserEnabled = v
	}
	if v, err := strconv.ParseBool(os.Getenv("CODY_DEBUG")); err == nil {
		c.Debug = v
	}
	if v, err := strconv.ParseBool(os.Getenv("CODY_PLAN")); err == nil {
		c.PlanGate = v
	}
	if v := os.Getenv("CODY_SANDBOX"); v != "" {
		c.Sandbox = v
	}
	if v := os.Getenv("CODY_PROVIDER"); v != "" {
		c.Provider = v
	}
	if v := os.Getenv("CODY_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODY_APPROVAL"); v != "" {
		c.Approval = v
	}
	if v := os.Getenv("CODY_EMBED_PROVIDER"); v != "" {
		c.EmbedProvider = v
	}
	if host := os.Getenv("OLLAMA_HOST"); host != "" && host != "http://localhost:11434" && host != "http://127.0.0.1:11434" {
		p := c.provider("ollama")
		p.BaseURL = strings.TrimRight(host, "/") + "/v1"
		c.Providers["ollama"] = p
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		p := c.provider("openrouter")
		p.APIKey = key
		c.Providers["openrouter"] = p
	}
	if key := os.Getenv("CODY_API_KEY"); key != "" {
		p := c.provider(c.Provider)
		p.APIKey = key
		c.Providers[c.Provider] = p
	}
}

func (c *Config) SearchKey() string {
	if key := os.Getenv("BRAVE_SEARCH_API_KEY"); key != "" {
		return key
	}
	return c.BraveSearchKey
}

func (c *Config) normalize() {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = "ollama"
	}
	c.Sandbox = strings.ToLower(strings.TrimSpace(c.Sandbox))
	switch c.Approval {
	case "ask", "auto", "yolo":
	default:
		c.Approval = "ask"
	}
	if strings.TrimSpace(c.EmbedModel) == "" {
		c.EmbedModel = DefaultEmbedModel
	}
	// Anything but an explicit "openrouter" stays local: indexing to a hosted
	// provider uploads every indexed file, so it is never a fallback.
	if c.EmbedProvider = strings.ToLower(strings.TrimSpace(c.EmbedProvider)); c.EmbedProvider != "openrouter" {
		c.EmbedProvider = "ollama"
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	for name, def := range Default().Providers {
		cur := c.Providers[name]
		if cur.BaseURL == "" {
			cur.BaseURL = def.BaseURL
		}
		c.Providers[name] = cur
	}
}

// Embedding returns where index embeddings are computed and with which model.
func (c *Config) Embedding() (provider, model string) {
	if c.EmbedProvider == "openrouter" && c.EmbedModel == DefaultEmbedModel {
		return c.EmbedProvider, DefaultOpenRouterEmbedModel
	}
	return c.EmbedProvider, c.EmbedModel
}

func (c *Config) provider(name string) Provider {
	if c.Providers == nil {
		return Provider{}
	}
	return c.Providers[name]
}

func (c *Config) ProviderConfig(name string) Provider { return c.provider(name) }

func (c *Config) Key(name string) string {
	if env := os.Getenv(strings.ToUpper(name) + "_API_KEY"); env != "" {
		return env
	}
	if key := c.provider(name).APIKey; key != "" {
		return key
	}
	return os.Getenv("CODY_API_KEY")
}

// Save writes the config to disk with owner-only permissions. Keys stay on
// this machine; nothing here is ever sent anywhere by cody itself.
func Save(c *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Clone gives each engine its own configuration snapshot.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	out := *c
	out.Providers = make(map[string]Provider, len(c.Providers))
	for name, p := range c.Providers {
		out.Providers[name] = p
	}
	return &out
}
