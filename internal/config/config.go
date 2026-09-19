package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultOllamaURL     = "http://localhost:11434/v1"
	DefaultOpenRouterURL = "https://openrouter.ai/api/v1"
	DefaultEmbedModel    = "nomic-embed-text"
)

type Provider struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

type Config struct {
	Provider   string              `json:"provider"`
	Model      string              `json:"model"`
	Approval   string              `json:"approval"`
	EmbedModel string              `json:"embed_model"`
	Providers  map[string]Provider `json:"providers"`
}

func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cody"
	}
	return filepath.Join(home, ".cody")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func Default() *Config {
	return &Config{
		Provider:   "ollama",
		Approval:   "ask",
		EmbedModel: DefaultEmbedModel,
		Providers: map[string]Provider{
			"ollama":     {BaseURL: DefaultOllamaURL},
			"openrouter": {BaseURL: DefaultOpenRouterURL},
		},
	}
}

func Load() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", Path(), err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	c.applyEnv()
	c.normalize()
	return c, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("CODY_PROVIDER"); v != "" {
		c.Provider = v
	}
	if v := os.Getenv("CODY_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODY_APPROVAL"); v != "" {
		c.Approval = v
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

func (c *Config) normalize() {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = "ollama"
	}
	switch c.Approval {
	case "ask", "auto", "yolo":
	default:
		c.Approval = "ask"
	}
	if strings.TrimSpace(c.EmbedModel) == "" {
		c.EmbedModel = DefaultEmbedModel
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
