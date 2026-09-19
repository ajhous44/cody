// Package engine is the headless core shared by every client: the CLI, the
// web GUI, benchmarks, and future frontends. It owns the provider, the agent
// loop, tools, approval policy, and the session store. Clients only render
// events and answer approval requests.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ajhous44/cody/internal/agent"
	"github.com/ajhous44/cody/internal/config"
	"github.com/ajhous44/cody/internal/index"
	"github.com/ajhous44/cody/internal/prompt"
	"github.com/ajhous44/cody/internal/provider"
	"github.com/ajhous44/cody/internal/session"
	"github.com/ajhous44/cody/internal/tool"
)

type Options struct {
	Root     string
	Cfg      *config.Config
	Version  string
	Store    *session.Store
	Approver agent.Approver // asked only when the approval mode requires it
	Resumed  bool           // session already has a meta record
	History  []provider.Message
}

type Engine struct {
	root     string
	cfg      *config.Config
	version  string
	approver agent.Approver
	p        provider.Provider
	model    string
	ag       *agent.Agent
	store    *session.Store
	meta     bool
}

func New(opts Options) (*Engine, error) {
	if opts.Cfg == nil {
		return nil, errors.New("engine: config is required")
	}
	if opts.Store == nil {
		return nil, errors.New("engine: session store is required")
	}
	e := &Engine{
		root:     opts.Root,
		cfg:      opts.Cfg,
		version:  opts.Version,
		approver: opts.Approver,
		store:    opts.Store,
		meta:     opts.Resumed,
	}
	if err := e.useProvider(context.Background(), opts.Cfg.Provider); err != nil {
		return nil, err
	}
	if len(opts.History) > 0 {
		e.ag.SetHistory(opts.History)
	}
	return e, nil
}

// Send runs one user turn to completion, emitting agent events to h and
// appending everything to the session file.
func (e *Engine) Send(ctx context.Context, text string, h agent.Handler) error {
	var recordErr error
	handler := func(ev agent.Event) {
		if ev.Kind == agent.KindUsage && ev.Usage != nil {
			if err := e.record(session.Record{Type: "usage", Usage: ev.Usage}); err != nil && recordErr == nil {
				recordErr = err
			}
		}
		if h != nil {
			h(ev)
		}
	}
	added, err := e.ag.Prompt(ctx, text, handler)
	for _, m := range added {
		if rerr := e.record(recordFromMessage(m)); rerr != nil && recordErr == nil {
			recordErr = rerr
		}
	}
	return errors.Join(err, recordErr)
}

func (e *Engine) Model() string        { return e.model }
func (e *Engine) ProviderName() string { return e.p.Name() }
func (e *Engine) Root() string         { return e.root }
func (e *Engine) SessionID() string    { return e.store.ID() }
func (e *Engine) SessionPath() string  { return e.store.Path() }
func (e *Engine) Approval() string     { return e.cfg.Approval }
func (e *Engine) Config() *config.Config {
	return e.cfg
}

func (e *Engine) SetModel(model string) {
	e.model = model
	e.cfg.Model = model
	e.ag.SetModel(model)
}

func (e *Engine) SetApproval(mode string) error {
	switch mode {
	case "ask", "auto", "yolo":
		e.cfg.Approval = mode
		return nil
	}
	return errors.New("approval must be ask, auto, or yolo")
}

func (e *Engine) SetApprover(a agent.Approver) { e.approver = a }

func (e *Engine) SetProviderName(name string) error {
	return e.useProvider(context.Background(), name)
}

func (e *Engine) History() []provider.Message { return e.ag.History() }

func (e *Engine) SetHistory(msgs []provider.Message) { e.ag.SetHistory(msgs) }

func (e *Engine) NewSession() error {
	if err := e.store.Close(); err != nil {
		return err
	}
	e.store = session.New(e.root, "")
	e.meta = false
	e.ag.SetHistory(nil)
	return nil
}

func (e *Engine) Close() error { return e.store.Close() }

func (e *Engine) useProvider(ctx context.Context, name string) error {
	e.cfg.Provider = name
	p, model, err := buildProvider(ctx, e.cfg)
	if err != nil {
		return err
	}
	var history []provider.Message
	if e.ag != nil {
		history = e.ag.History()
	}
	e.p = p
	e.model = model
	e.ag = agent.New(agent.Options{
		Provider:      p,
		Model:         model,
		System:        prompt.Build(e.root),
		Tools:         e.tools(),
		Approver:      e.approverFor(),
		ContextTokens: contextBudget(ctx, e.cfg, p.Name(), model),
	})
	e.ag.SetHistory(history)
	return nil
}

func (e *Engine) approverFor() agent.Approver {
	return agent.ApproverFunc(func(ctx context.Context, t tool.Tool, args string) (bool, error) {
		switch e.cfg.Approval {
		case "yolo":
			return true, nil
		case "auto":
			if t.Name() != "bash" {
				return true, nil
			}
		}
		if e.approver == nil {
			return false, nil
		}
		return e.approver.Approve(ctx, t, args)
	})
}

func (e *Engine) tools() *tool.Registry {
	return tool.NewRegistry(
		tool.NewRead(e.root),
		tool.NewWrite(e.root),
		tool.NewEdit(e.root),
		tool.NewBash(e.root),
		tool.NewGrep(e.root),
		tool.NewGlob(e.root),
		tool.NewSearch(e.root, index.Path(e.root), Embedder(e.cfg)),
	)
}

func (e *Engine) record(rec session.Record) error {
	if !e.meta {
		e.meta = true
		if err := e.store.Append(session.Record{Type: "meta", Meta: map[string]string{
			"version":   e.version,
			"provider":  e.p.Name(),
			"model":     e.model,
			"workspace": e.root,
		}}); err != nil {
			return err
		}
	}
	return e.store.Append(rec)
}

// MessagesFromRecords rebuilds provider messages from a session transcript.
func MessagesFromRecords(recs []session.Record) []provider.Message {
	var out []provider.Message
	for _, r := range recs {
		if r.Type != "message" {
			continue
		}
		m := provider.Message{Role: r.Role, Content: r.Text, ToolCallID: r.ToolCallID, Name: r.Name}
		for _, c := range r.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{
				ID: c.ID, Type: "function", Function: provider.Func{Name: c.Name, Arguments: c.Args},
			})
		}
		out = append(out, m)
	}
	return out
}

func recordFromMessage(m provider.Message) session.Record {
	r := session.Record{Type: "message", Role: m.Role, Text: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
	for _, c := range m.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
	}
	return r
}

// Embedder builds an index embedder that always targets the local Ollama
// server so indexing stays private, even when chat runs against a hosted
// provider.
func Embedder(cfg *config.Config) index.Embedder {
	base := LocalOllamaURL(cfg)
	return index.EmbedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		if _, err := provider.EnsureOllama(ctx, base); err != nil {
			return nil, err
		}
		return provider.OllamaEmbed(ctx, base, cfg.EmbedModel, texts)
	})
}

func LocalOllamaURL(cfg *config.Config) string {
	if base := cfg.ProviderConfig("ollama").BaseURL; base != "" {
		return base
	}
	return config.DefaultOllamaURL
}

// EnsureOllama starts a local Ollama server if it is installed but not
// running. The bool reports whether this call started it.
func EnsureOllama(cfg *config.Config) (bool, error) {
	return provider.EnsureOllama(context.Background(), LocalOllamaURL(cfg))
}

func buildProvider(ctx context.Context, cfg *config.Config) (provider.Provider, string, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	base := cfg.ProviderConfig(name).BaseURL
	if base == "" {
		switch name {
		case "ollama":
			base = config.DefaultOllamaURL
		case "openrouter":
			base = config.DefaultOpenRouterURL
		default:
			return nil, "", fmt.Errorf("unknown provider %q; add providers.%s.base_url to %s", name, name, config.Path())
		}
	}
	p := provider.New(name, base, cfg.Key(name))
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		switch name {
		case "ollama":
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			models, err := provider.ListOllamaModels(cctx, base)
			if err != nil {
				return nil, "", fmt.Errorf("no model set and %w", err)
			}
			model = provider.PickOllamaModel(models)
			if model == "" {
				return nil, "", errors.New("no model set and no Ollama models installed (try: ollama pull qwen2.5-coder:7b)")
			}
		default:
			return nil, "", fmt.Errorf("no model set; pass --model or set \"model\" in %s", config.Path())
		}
	}
	return p, model, nil
}

// contextBudget trims input to fit the model's real window. Ollama's
// OpenAI-compatible endpoint cannot set num_ctx, so the runner's reported
// context length is the only reliable number; scripts or hosts that do not
// expose one fall back to a conservative default.
func contextBudget(ctx context.Context, cfg *config.Config, providerName, model string) int {
	if providerName != "ollama" {
		return 64000
	}
	if n := provider.OllamaLoadedContext(ctx, cfg.ProviderConfig("ollama").BaseURL, model); n > 0 {
		budget := n - n/4
		if budget < 2000 {
			budget = 2000
		}
		return budget
	}
	return 8000
}
