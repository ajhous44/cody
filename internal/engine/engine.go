// Package engine is the headless core shared by every client: the CLI, the
// web GUI, benchmarks, and future frontends. It owns the provider, the agent
// loop, tools, approval policy, and the session store. Clients only render
// events and answer approval requests.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/clip"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/index"
	"github.com/ajhous44/cody/v3/internal/prompt"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/session"
	"github.com/ajhous44/cody/v3/internal/tool"
)

type Options struct {
	Root       string
	Cfg        *config.Config
	Version    string
	Store      *session.Store
	Approver   agent.Approver // asked only when the approval mode requires it
	Resumed    bool           // session already has a meta record
	History    []provider.Message
	DeferModel bool // Serve setup before a provider/model is available.
}

type Engine struct {
	root        string
	cfg         *config.Config
	version     string
	approver    agent.Approver
	p           provider.Provider
	model       string
	ag          *agent.Agent
	store       *session.Store
	meta        bool
	mu          sync.Mutex // guards configuration, provider, agent, and store
	runMu       sync.Mutex // serializes turns, configuration, and session switches
	deferModel  bool
	budgetReady bool
	indexed     bool           // search is registered only when an index exists
	skills      []prompt.Skill // skill index in the prompt; bodies load on demand
	browser     *tool.Browser
}

func New(opts Options) (*Engine, error) {
	if opts.Cfg == nil {
		return nil, errors.New("engine: config is required")
	}
	if opts.Store == nil {
		return nil, errors.New("engine: session store is required")
	}
	e := &Engine{
		root:       opts.Root,
		cfg:        opts.Cfg.Clone(),
		deferModel: opts.DeferModel,
		version:    opts.Version,
		approver:   opts.Approver,
		store:      opts.Store,
		meta:       opts.Resumed,
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
// durably recording each message before the next action.
func (e *Engine) Send(ctx context.Context, text string, h agent.Handler) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := e.Config()
	if cfg.Provider == "ollama" {
		e.announceColdModel(ctx, h)
		if _, err := provider.EnsureOllama(ctx, LocalOllamaURL(cfg)); err != nil {
			return err
		}
	}
	if e.model == "" {
		p, model, err := buildProvider(ctx, cfg, true)
		if err != nil {
			return err
		}
		e.installProvider(cfg, p, model)
	}
	// `cody index` ran (or the index was deleted), or a skill was added or edited.
	if indexExists(e.root) != e.indexed || skillsKey(discoverSkills(e.root)) != skillsKey(e.skills) {
		e.installProvider(cfg, e.p, e.model)
	}
	e.ag.SetCacheKey(fmt.Sprintf("%x", sha256.Sum256([]byte(e.root+"\x00"+e.SessionID()))))
	e.refreshBudget(ctx)
	var recordErr error
	handler := func(ev agent.Event) {
		if ev.Kind == agent.KindUsage && ev.Usage != nil && recordErr == nil {
			recordErr = e.recordBatch([]session.Record{{Type: "usage", Usage: ev.Usage}})
			if recordErr != nil {
				cancel()
			}
		}
		if h != nil {
			h(ev)
		}
	}
	_, err := e.ag.Prompt(ctx, text, handler)
	return errors.Join(err, recordErr)
}

// Show loading before daemon startup; the first streamed response clears it.
func (e *Engine) announceColdModel(ctx context.Context, h agent.Handler) {
	if h == nil || e.p == nil || e.p.Name() != "ollama" {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	loaded, known := provider.OllamaModelLoaded(pctx, LocalOllamaURL(e.Config()), e.Model())
	if !known || !loaded {
		h(agent.Event{Kind: agent.KindThinking, Text: strings.TrimSpace("Loading model… " + e.Model())})
	}
}

func (e *Engine) recordBatch(recs []session.Record) error {
	if len(recs) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.meta {
		if err := e.writeMetaLocked(); err != nil {
			return err
		}
	}
	return e.store.AppendBatch(recs)
}

func (e *Engine) Model() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.model
}
func (e *Engine) ProviderName() string { e.mu.Lock(); defer e.mu.Unlock(); return e.cfg.Provider }
func (e *Engine) Root() string         { return e.root }
func (e *Engine) SessionID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.ID()
}
func (e *Engine) SessionPath() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.Path()
}
func (e *Engine) Approval() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Approval
}
func (e *Engine) Config() *config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Clone()
}

func (e *Engine) SetModel(model string) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.model = model
	e.cfg.Model = model
	e.ag.SetModel(model)
	e.ag.SetLimits(defaultBudget(e.p.Name()), 0)
	e.budgetReady = false
}

func (e *Engine) SetApproval(mode string) error {
	switch mode {
	case "ask", "auto", "yolo":
		e.mu.Lock()
		e.cfg.Approval = mode
		e.mu.Unlock()
		return nil
	}
	return errors.New("approval must be ask, auto, or yolo")
}

func (e *Engine) SetApprover(a agent.Approver) { e.mu.Lock(); defer e.mu.Unlock(); e.approver = a }

func (e *Engine) SetProviderName(name string) error {
	return e.useProvider(context.Background(), name)
}

func (e *Engine) History() []provider.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ag.History()
}

func (e *Engine) SetHistory(msgs []provider.Message) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ag.SetHistory(msgs)
}

func (e *Engine) NewSession() error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.Close(); err != nil {
		return err
	}
	e.store = session.New(e.root, "")
	e.meta = false
	e.ag.SetHistory(nil)
	// Write the session file now so the thread is visible (and resumable)
	// before the first message is sent.
	return e.writeMetaLocked()
}

// Title asks the active model for a short thread title based on the first ask,
// stores it, and returns it. The model is asked for JSON; the reply is parsed
// without another model call, and anything unusable falls back to a trimmed
// version of the ask and finally to the current date and time. A hung model
// cannot hold the turn for more than the timeout.
func (e *Engine) Title(ctx context.Context, text string) string {
	// Remember which thread is being named: the turn runs alongside this
	// call, and a session switch in between must not rename the new thread.
	e.mu.Lock()
	sid, p, model := e.store.ID(), e.p, e.model
	e.mu.Unlock()
	title := ""
	if p != nil && model != "" {
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		stream, err := p.Stream(tctx, provider.Request{
			Model: model,
			Messages: []provider.Message{
				{Role: "system", Content: `You name chat threads. Reply with one JSON object and nothing else: {"title":"3 to 5 lowercase words"}. No markdown, no code fences, no extra text.`},
				{Role: "user", Content: text},
			},
		})
		if err == nil {
			var b strings.Builder
			for {
				d, err := stream.Recv()
				if err != nil {
					break
				}
				b.WriteString(d.Content)
			}
			stream.Close()
			title = parseTitle(b.String())
		}
	}
	if title == "" {
		title = fallbackTitle(text)
	}
	if title == "" {
		title = time.Now().Format("Jan 2 15:04")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store.ID() != sid {
		// The thread was switched (new or resumed session) while naming
		// ran; leave the new thread alone.
		return ""
	}
	e.ensureSessionFileLocked()
	_ = session.Rename(e.root, sid, title)
	return title
}

// parseTitle extracts a title without a model call: it looks for the first JSON
// object in the reply and accepts a few likely keys, then any string value,
// then falls back to the raw text with markdown noise stripped.
func parseTitle(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		var obj map[string]any
		if json.Unmarshal([]byte(s[i:j+1]), &obj) == nil {
			for _, key := range []string{"title", "reason", "name", "summary"} {
				if v, ok := obj[key].(string); ok {
					if t := sanitizeTitle(v); t != "" {
						return t
					}
				}
			}
			for _, v := range obj {
				if str, ok := v.(string); ok {
					if t := sanitizeTitle(str); t != "" {
						return t
					}
				}
			}
			// Valid JSON but nothing usable in it: not a title.
			return ""
		}
	}
	cleaned := strings.ReplaceAll(s, "```json", "")
	cleaned = strings.ReplaceAll(cleaned, "```", "")
	return sanitizeTitle(cleaned)
}

// ensureSessionFileLocked makes sure the transcript exists on disk so a
// title can be attached before the first message is recorded. Callers must
// hold e.mu.
func (e *Engine) ensureSessionFileLocked() {
	if !e.meta {
		_ = e.writeMetaLocked()
	}
}

func sanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`“”‘’")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ".!?,;:")
	words := strings.Fields(s)
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.TrimSpace(clip.Bytes(strings.Join(words, " "), 60))
}

func fallbackTitle(text string) string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return ""
	}
	if len(words) > 5 {
		words = words[:5]
	}
	return strings.TrimSpace(clip.Bytes(strings.Join(words, " "), 60))
}

// Resume switches to an existing session in this workspace ("last" allowed),
// replacing the in-memory history and appending later turns to that file.
func (e *Engine) Resume(id string) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	resolved, recs, err := session.Load(e.root, id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.Close(); err != nil {
		return err
	}
	e.store = session.New(e.root, resolved)
	e.meta = true
	e.ag.SetHistory(MessagesFromRecords(recs))
	return nil
}

// ContextLimit is the current context window, for the CLI's live status.
func (e *Engine) ContextLimit() int {
	e.mu.Lock()
	ag := e.ag
	e.mu.Unlock()
	return ag.ContextTokens()
}

// Fork switches to a new session copied from id (the current session when
// empty) before user turn `turn`; see session.Fork. It returns that turn's text.
func (e *Engine) Fork(id string, turn int) (string, error) {
	if id == "" {
		id = e.SessionID()
	}
	forked, prefill, err := session.Fork(e.root, id, turn)
	if err != nil {
		return "", err
	}
	return prefill, e.Resume(forked)
}

func (e *Engine) Close() error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		e.browser.Close()
		e.browser = nil
	}
	return e.store.Close()
}

func (e *Engine) useProvider(ctx context.Context, name string) error {
	cfg := e.Config()
	cfg.Provider = name
	return e.Configure(cfg)
}

// Configure replaces the provider even when only its credentials changed.
func (e *Engine) Configure(cfg *config.Config) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cfg = cfg.Clone()
	e.mu.Lock()
	unchanged := e.ag != nil && reflect.DeepEqual(e.cfg, cfg)
	e.mu.Unlock()
	if unchanged {
		return nil
	}
	p, model, err := buildProvider(context.Background(), cfg, !e.deferModel)
	if err != nil {
		return err
	}
	e.installProvider(cfg, p, model)
	return nil
}

func (e *Engine) installProvider(cfg *config.Config, p provider.Provider, model string) {
	e.mu.Lock()
	var history []provider.Message
	if e.ag != nil {
		history = e.ag.History()
	}
	e.cfg, e.p, e.model = cfg, p, model
	e.indexed = indexExists(e.root)
	e.skills = discoverSkills(e.root)
	system := prompt.Base(e.root) + prompt.Optional(prompt.Opts{Web: cfg.WebEnabled, Browser: cfg.BrowserEnabled, Plan: cfg.PlanGate, Sandbox: cfg.Sandbox}) + prompt.SkillsIndex(e.skills)
	e.ag = agent.New(agent.Options{
		Provider: p, Model: model, System: system, Root: e.root, Tools: e.tools(),
		Approver: e.approverFor(), ContextTokens: defaultBudget(p.Name()), Debug: cfg.Debug, RequirePlan: cfg.PlanGate,
		Record: func(m provider.Message) error { return e.recordBatch([]session.Record{recordFromMessage(m)}) },
	})
	e.ag.SetHistory(history)
	e.budgetReady = false
	e.mu.Unlock()
}

func (e *Engine) approverFor() agent.Approver {
	return agent.ApproverFunc(func(ctx context.Context, t tool.Tool, args string) (bool, error) {
		e.mu.Lock()
		mode, approver := e.cfg.Approval, e.approver
		e.mu.Unlock()
		// A plan is the user's checkpoint, so it is asked in every mode;
		// headless callers with no approver have no one to ask.
		if t.Name() == "plan" {
			if approver == nil {
				return true, nil
			}
			return approver.Approve(ctx, t, args)
		}
		switch mode {
		case "yolo":
			return true, nil
		case "auto":
			// auto covers file edits; anything that reaches outside the workspace still asks.
			switch t.Name() {
			case "bash", "browser", "web_fetch", "web_search":
			default:
				return true, nil
			}
		}
		if approver == nil {
			return false, nil
		}
		return approver.Approve(ctx, t, args)
	})
}

func (e *Engine) tools() *tool.Registry {
	tools := []tool.Tool{
		tool.NewRead(e.root),
		tool.NewWrite(e.root),
		tool.NewEdit(e.root),
		tool.NewBash(e.root).WithSandbox(tool.Sandbox{Mode: e.cfg.Sandbox, Writable: e.cfg.SandboxWritable}),
		tool.NewGrep(e.root),
		tool.NewGlob(e.root),
	}
	if e.cfg.PlanGate {
		tools = append(tools, tool.NewPlan())
	}
	if len(e.skills) > 0 {
		tools = append(tools, tool.NewSkill(e.skills))
	}
	if e.indexed {
		tools = append(tools, tool.NewSearch(e.root, index.Path(e.root), EmbedID(e.cfg), Embedder(e.cfg)))
	}
	if e.cfg.WebEnabled {
		tools = append(tools, tool.NewWebFetch())
		if key := e.cfg.SearchKey(); key != "" {
			tools = append(tools, tool.NewWebSearch(key))
		}
	}
	if e.cfg.BrowserEnabled {
		if e.browser == nil {
			e.browser = tool.NewBrowser()
		}
		tools = append(tools, e.browser)
	} else if e.browser != nil {
		e.browser.Close()
		e.browser = nil
	}
	return tool.NewRegistry(tools...)
}

// writeMetaLocked appends the session header. Callers must hold e.mu.
func (e *Engine) writeMetaLocked() error {
	err := e.store.Append(session.Record{Type: "meta", Meta: map[string]string{
		"version":   e.version,
		"provider":  e.p.Name(),
		"model":     e.model,
		"workspace": e.root,
	}})
	if err == nil {
		e.meta = true
	}
	return err
}

// MessagesFromRecords rebuilds provider messages from a session transcript.
func MessagesFromRecords(recs []session.Record) []provider.Message {
	var out []provider.Message
	var pending []provider.ToolCall
	flush := func() {
		for _, c := range pending {
			out = append(out, provider.Message{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
				Content: "execution interrupted; outcome unknown. Inspect the workspace before retrying."})
		}
		pending = nil
	}
	for _, r := range recs {
		if r.Type != "message" {
			continue
		}
		if r.Role != "tool" {
			flush()
		}
		m := provider.Message{Role: r.Role, Content: r.Text, ToolCallID: r.ToolCallID, Name: r.Name}
		for _, c := range r.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: c.ID, Type: "function", Function: provider.Func{Name: c.Name, Arguments: c.Args}})
		}
		if r.Role == "tool" {
			for i, c := range pending {
				if c.ID == r.ToolCallID {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		}
		out = append(out, m)
		pending = append(pending, m.ToolCalls...)
	}
	flush()
	return out
}

func recordFromMessage(m provider.Message) session.Record {
	r := session.Record{Type: "message", Role: m.Role, Text: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
	for _, c := range m.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
	}
	return r
}

// Embedder builds the index embedder. Local Ollama is the default so indexing
// stays private even when chat is hosted; OpenRouter is used only when
// embed_provider opts in.
func Embedder(cfg *config.Config) index.Embedder {
	name, model := cfg.Embedding()
	if name == "openrouter" {
		base, key := cfg.ProviderConfig("openrouter").BaseURL, cfg.Key("openrouter")
		return index.EmbedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
			return provider.OpenRouterEmbed(ctx, base, key, model, texts)
		})
	}
	base := LocalOllamaURL(cfg)
	return index.EmbedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		if _, err := provider.EnsureOllama(ctx, base); err != nil {
			return nil, err
		}
		return provider.OllamaEmbed(ctx, base, model, texts)
	})
}

// EmbedID names the vector space an index was built in. Local ids stay bare
// model names so existing indexes keep working.
func EmbedID(cfg *config.Config) string {
	name, model := cfg.Embedding()
	if name == "openrouter" {
		return "openrouter:" + model
	}
	return model
}

func discoverSkills(root string) []prompt.Skill {
	return prompt.DiscoverSkills(prompt.SkillDirs(root, config.Dir())...)
}

// skillsKey changes when a skill is added, removed, renamed, or edited.
func skillsKey(skills []prompt.Skill) string {
	var b strings.Builder
	for _, s := range skills {
		fmt.Fprintf(&b, "%s\x00%s\x00%d\n", s.Name, s.Dir, s.Mod)
	}
	return b.String()
}

func indexExists(root string) bool {
	_, err := os.Stat(index.Path(root))
	return err == nil
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

func buildProvider(ctx context.Context, cfg *config.Config, resolveModel bool) (provider.Provider, string, error) {
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
	if model == "" && resolveModel {
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

// defaultBudget is the trim budget used before the real context window is
// known. refreshBudget resolves model metadata before the first request, without stalling
// engine construction on a probe.
func defaultBudget(providerName string) int {
	if providerName != "ollama" {
		return 64000
	}
	return 8000
}

// Resolve limits once per hosted model; local runner limits may change after loading.
func (e *Engine) refreshBudget(ctx context.Context) {
	if e.budgetReady {
		return
	}
	cfg := e.Config()
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	window, output := modelLimits(probe, cfg, cfg.Provider, e.model)
	if window > 0 {
		e.ag.SetLimits(window, output)
		e.budgetReady = cfg.Provider != "ollama"
	}
}

func modelLimits(ctx context.Context, cfg *config.Config, name, model string) (int, int) {
	if model == "" {
		return 0, 0
	}
	switch name {
	case "ollama":
		return provider.OllamaLoadedContext(ctx, cfg.ProviderConfig(name).BaseURL, model), 0
	case "openrouter":
		models, err := provider.ListOpenRouterModels(ctx, cfg.ProviderConfig(name).BaseURL)
		if err == nil {
			for _, m := range models {
				if m.ID == model {
					return m.ContextLength, m.TopProvider.MaxCompletionTokens
				}
			}
		}
	}
	return 0, 0
}
