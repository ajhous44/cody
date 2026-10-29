package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/bench"
	"github.com/ajhous44/cody/v3/internal/clip"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/engine"
	"github.com/ajhous44/cody/v3/internal/index"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/server"
	"github.com/ajhous44/cody/v3/internal/session"
	"github.com/ajhous44/cody/v3/internal/tool"
	"github.com/ajhous44/cody/v3/internal/ui"
)

// Version is stamped by GoReleaser via ldflags on release builds.
var Version = "3.0.0"

type app struct {
	cfg    *config.Config
	root   string
	u      *ui.UI
	eng    *engine.Engine
	mu     sync.Mutex
	cancel context.CancelFunc
}

func Run(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cody: "+err.Error())
		fmt.Fprintln(os.Stderr, "run `cody help` for usage")
		return 2
	}
	switch opts.command {
	case "help":
		printHelp()
		return 0
	case "version":
		fmt.Println("cody " + Version)
		return 0
	case "sessions":
		return cmdSessions(opts)
	case "replay":
		return cmdReplay(opts)
	case "models":
		return cmdModels(opts)
	case "bench":
		return cmdBench(opts)
	case "index":
		return cmdIndex(opts)
	case "serve":
		return cmdServe(opts)
	}

	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	applyFlags(cfg, opts)
	if strings.EqualFold(cfg.Provider, "ollama") {
		started, err := engine.EnsureOllama(cfg)
		if err != nil {
			return fail(err)
		}
		if started {
			fmt.Fprintln(os.Stderr, "cody: started ollama")
		}
	}
	root, err := workspace(opts)
	if err != nil {
		return fail(err)
	}
	var resumeID string
	var resumeRecs []session.Record
	if opts.resume != "" {
		resumeID, resumeRecs, err = session.Load(root, opts.resume)
		if err != nil {
			return fail(err)
		}
		if meta := findMeta(resumeRecs); meta != nil {
			if opts.provider == "" && meta["provider"] != "" {
				cfg.Provider = meta["provider"]
			}
			if opts.model == "" && meta["model"] != "" {
				cfg.Model = meta["model"]
			}
		}
	}
	u := ui.New(os.Stdin, os.Stdout, os.Stderr)
	u.SetThinkingVisible(opts.showThinking)
	a := &app{cfg: cfg, root: root, u: u}
	a.eng, err = engine.New(engine.Options{
		Root:    root,
		Cfg:     cfg,
		Version: Version,
		Store:   session.New(root, resumeID),
		Resumed: resumeID != "",
		History: engine.MessagesFromRecords(resumeRecs),
		Approver: agent.ApproverFunc(func(ctx context.Context, t tool.Tool, args string) (bool, error) {
			return u.Approve(ctx, t, args)
		}),
	})
	if err != nil {
		return fail(err)
	}
	defer a.eng.Close()
	if resumeID != "" {
		fmt.Fprintf(os.Stderr, "resumed session %s (%s/%s, %d messages)\n",
			resumeID, a.eng.ProviderName(), a.eng.Model(), len(a.eng.History()))
	}

	if len(opts.prompt) > 0 {
		return a.oneShot(strings.Join(opts.prompt, " "))
	}
	return a.repl()
}

func applyFlags(cfg *config.Config, opts options) {
	if opts.provider != "" {
		cfg.Provider = opts.provider
	}
	if opts.model != "" {
		cfg.Model = opts.model
	}
	if opts.approval != "" {
		cfg.Approval = opts.approval
	}
	if opts.debug != nil {
		cfg.Debug = *opts.debug
	}
	if opts.plan {
		cfg.PlanGate = true
	}
}

func (a *app) repl() int {
	a.u.Banner(Version, a.eng.ProviderName(), a.eng.Model(), a.root, a.eng.SessionID())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			if !a.interrupt() {
				fmt.Fprintln(os.Stderr)
				os.Exit(130)
			}
		}
	}()
	for {
		line, err := a.u.Ask("> ")
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stdout)
			return 0
		}
		if err != nil {
			return fail(err)
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if a.command(line) {
				return 0
			}
			continue
		}
		a.turn(line)
	}
}

func (a *app) command(line string) bool {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/exit", "/quit":
		return true
	case "/help":
		a.help()
	case "/model":
		if len(fields) == 1 {
			a.u.Printf("model: %s\n", a.eng.Model())
			return false
		}
		a.eng.SetModel(fields[1])
		a.u.Printf("model: %s\n", a.eng.Model())
	case "/provider":
		if len(fields) == 1 {
			a.u.Printf("provider: %s (%s)\n", a.eng.ProviderName(), a.eng.Model())
			return false
		}
		if err := a.eng.SetProviderName(fields[1]); err != nil {
			a.u.Errorf("cody: %v\n", err)
		} else {
			a.u.Printf("provider: %s (%s)\n", a.eng.ProviderName(), a.eng.Model())
		}
	case "/think":
		if len(fields) == 1 {
			state := "hidden"
			if a.u.ThinkingVisible() {
				state = "shown"
			}
			a.u.Printf("thinking: %s\n", state)
			return false
		}
		switch fields[1] {
		case "on", "show":
			a.u.SetThinkingVisible(true)
			a.u.Printf("thinking: shown\n")
		case "off", "hide":
			a.u.SetThinkingVisible(false)
			a.u.Printf("thinking: hidden\n")
		default:
			a.u.Errorf("cody: thinking must be on or off\n")
		}
	case "/approval":
		if len(fields) == 1 {
			a.u.Printf("approval: %s\n", a.eng.Approval())
			return false
		}
		if err := a.eng.SetApproval(fields[1]); err != nil {
			a.u.Errorf("cody: %v\n", err)
		} else {
			a.u.Printf("approval: %s\n", a.eng.Approval())
		}
	case "/plan":
		if len(fields) > 1 {
			if fields[1] != "on" && fields[1] != "off" {
				a.u.Errorf("cody: /plan takes on or off\n")
				return false
			}
			cfg := a.eng.Config()
			cfg.PlanGate = fields[1] == "on"
			if err := a.eng.Configure(cfg); err != nil {
				a.u.Errorf("cody: %v\n", err)
				return false
			}
		}
		state := "off"
		if a.eng.Config().PlanGate {
			state = "on (changes wait for an approved plan)"
		}
		a.u.Printf("plan gate: %s\n", state)
	case "/index":
		rebuild := len(fields) > 1 && fields[1] == "--rebuild"
		stats, err := runIndex(context.Background(), a.cfg, a.root, rebuild)
		if err != nil {
			a.u.Errorf("cody: %v\n", err)
			return false
		}
		a.u.Printf("indexed %d files, %d chunks (%d embedded, %d unchanged, %d removed)\n",
			stats.Files+stats.Unchanged, stats.Chunks, stats.Embedded, stats.Unchanged, stats.Removed)
	case "/clear":
		a.newSession()
	case "/sessions":
		infos, err := session.List(a.root)
		if err != nil {
			a.u.Errorf("cody: %v\n", err)
			return false
		}
		for _, info := range infos {
			a.u.Printf("%s  %s  %s\n", info.ID, info.Started.Format("2006-01-02 15:04"), info.First)
		}
	default:
		a.u.Errorf("cody: unknown command %s (try /help)\n", fields[0])
	}
	return false
}

func (a *app) help() {
	a.u.Printf("%s\n", strings.Join([]string{
		"/help              show this help",
		"/model [name]      show or set the model",
		"/provider [name]   show or switch provider",
		"/approval [mode]   ask, auto, or yolo",
		"/think [on|off]    show or hide model reasoning",
		"/plan [on|off]     require an approved plan before changes",
		"/index [--rebuild] build or refresh the semantic index",
		"/clear             start a new session",
		"/sessions          list recent sessions",
		"/exit              quit",
	}, "\n"))
}

func (a *app) newSession() {
	if err := a.eng.NewSession(); err != nil {
		a.u.Errorf("cody: %v\n", err)
		return
	}
	a.u.Printf("new session %s\n", a.eng.SessionID())
}

func (a *app) turn(text string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.setCancel(cancel)
	defer func() {
		cancel()
		a.setCancel(nil)
	}()
	err := a.eng.Send(ctx, text, a.u.Event)
	a.u.StopThinking()
	a.u.Flush()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			a.u.Printf("\n%s\n", a.u.Dim("cancelled"))
		} else {
			a.u.Errorf("\ncody: %v\n", err)
		}
		return
	}
	a.u.Printf("\n")
}

func (a *app) oneShot(text string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := a.eng.Send(ctx, text, a.u.Event)
	a.u.StopThinking()
	a.u.Flush()
	a.u.Printf("\n")
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return fail(err)
	}
	a.u.Printf("%s\n", a.u.Dim("session "+a.eng.SessionID()+" (cody resume "+a.eng.SessionID()+")"))
	return 0
}

func (a *app) setCancel(c context.CancelFunc) {
	a.mu.Lock()
	a.cancel = c
	a.mu.Unlock()
}

func (a *app) interrupt() bool {
	a.mu.Lock()
	c := a.cancel
	a.cancel = nil
	a.mu.Unlock()
	if c == nil {
		return false
	}
	c()
	return true
}

func cmdSessions(opts options) int {
	root, err := workspace(opts)
	if err != nil {
		return fail(err)
	}
	infos, err := session.List(root)
	if err != nil {
		return fail(err)
	}
	if opts.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(infos); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(infos) == 0 {
		fmt.Println("no sessions recorded for " + root)
		return 0
	}
	for _, info := range infos {
		fmt.Printf("%s  %s  %s\n", info.ID, info.Started.Format("2006-01-02 15:04"), info.First)
	}
	return 0
}

func cmdReplay(opts options) int {
	id := "last"
	if len(opts.prompt) > 0 {
		id = opts.prompt[0]
	}
	root, err := workspace(opts)
	if err != nil {
		return fail(err)
	}
	_, recs, err := session.Load(root, id)
	if err != nil {
		return fail(err)
	}
	tools := tool.NewRegistry(
		tool.NewRead(root),
		tool.NewWrite(root),
		tool.NewEdit(root),
		tool.NewBash(root),
	)
	for _, r := range recs {
		switch {
		case r.Type == "meta" || r.Type == "usage":
		case r.Role == "user":
			fmt.Printf("\n> %s\n", r.Text)
		case r.Role == "assistant":
			if strings.TrimSpace(r.Text) != "" {
				fmt.Println(r.Text)
			}
			for _, c := range r.ToolCalls {
				summary := c.Args
				if t, ok := tools.Get(c.Name); ok {
					if s := strings.TrimSpace(t.Summarize(json.RawMessage(c.Args))); s != "" {
						summary = s
					}
				}
				fmt.Printf("[tool] %s %s\n", c.Name, truncate(strings.Join(strings.Fields(summary), " "), 120))
			}
		case r.Role == "tool":
			lines := strings.Split(strings.TrimRight(r.Text, "\n"), "\n")
			if len(lines) > 8 {
				lines = append(lines[:8], "...")
			}
			for _, l := range lines {
				fmt.Printf("  %s\n", l)
			}
		}
	}
	return 0
}

func cmdModels(opts options) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	name := cfg.Provider
	if opts.provider != "" {
		name = opts.provider
	}
	base := cfg.ProviderConfig(name).BaseURL
	if base == "" {
		switch name {
		case "ollama":
			base = config.DefaultOllamaURL
		case "openrouter":
			base = config.DefaultOpenRouterURL
		default:
			return fail(fmt.Errorf("unknown provider %q", name))
		}
	}
	filter := strings.ToLower(strings.Join(opts.prompt, " "))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch name {
	case "ollama":
		if _, err := engine.EnsureOllama(cfg); err != nil {
			return fail(err)
		}
		models, err := provider.ListOllamaModels(ctx, base)
		if err != nil {
			return fail(err)
		}
		best := provider.PickOllamaModel(models)
		for _, m := range models {
			if filter != "" && !strings.Contains(strings.ToLower(m.Name), filter) {
				continue
			}
			mark := " "
			if m.Name == best {
				mark = "*"
			}
			tag := "chat"
			if !m.SupportsTools() {
				tag = "no-tools"
			}
			fmt.Printf("%s %-44s %8s  %s\n", mark, m.Name, provider.HumanSize(m.Size), tag)
		}
		fmt.Println("(* default; override with --model)")
	case "openrouter":
		models, err := provider.ListOpenRouterModels(ctx, base)
		if err != nil {
			return fail(err)
		}
		shown := 0
		for _, m := range models {
			if filter != "" && !strings.Contains(strings.ToLower(m.ID), filter) {
				continue
			}
			fmt.Printf("%-58s %8d ctx  $%s/M in\n", m.ID, m.ContextLength, perMillion(m.Pricing.Prompt))
			shown++
			if filter == "" && shown >= 40 {
				fmt.Println("... (`cody models <substring>` to filter)")
				break
			}
		}
	default:
		return fail(fmt.Errorf("unknown provider %q", name))
	}
	return 0
}

func cmdIndex(opts options) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	root, err := workspace(opts)
	if err != nil {
		return fail(err)
	}
	if name, _ := cfg.Embedding(); name == "ollama" {
		if _, err := engine.EnsureOllama(cfg); err != nil {
			return fail(err)
		}
	}
	stats, err := runIndex(context.Background(), cfg, root, opts.rebuild)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("indexed %d files, %d chunks (%d embedded, %d unchanged, %d removed)\n",
		stats.Files+stats.Unchanged, stats.Chunks, stats.Embedded, stats.Unchanged, stats.Removed)
	fmt.Println("index: " + index.Path(root))
	return 0
}

func runIndex(ctx context.Context, cfg *config.Config, root string, rebuild bool) (index.Stats, error) {
	if name, model := cfg.Embedding(); name == "openrouter" {
		fmt.Fprintf(os.Stderr, "embedding with OpenRouter (%s): indexed file contents and search queries are sent to OpenRouter\n", model)
	}
	return index.Index(ctx, root, index.Path(root), engine.Embedder(cfg), index.Options{
		Rebuild:  rebuild,
		Model:    engine.EmbedID(cfg),
		Progress: func(path string) { fmt.Fprintf(os.Stderr, "embedding %s\n", path) },
	})
}

const defaultServeAddr = "127.0.0.1:8765"

func cmdServe(opts options) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	applyFlags(cfg, opts)
	root, err := workspace(opts)
	if err != nil {
		return fail(err)
	}
	var resumeID string
	var resumeRecs []session.Record
	if opts.resume != "" {
		resumeID, resumeRecs, err = session.Load(root, opts.resume)
		if err != nil {
			return fail(err)
		}
		if meta := findMeta(resumeRecs); meta != nil {
			if opts.provider == "" && meta["provider"] != "" {
				cfg.Provider = meta["provider"]
			}
			if opts.model == "" && meta["model"] != "" {
				cfg.Model = meta["model"]
			}
		}
	}
	addr := opts.addr
	if addr == "" {
		addr = defaultServeAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fail(fmt.Errorf("invalid --addr %q: %w", addr, err))
	}

	factory := func(spaceRoot string) (server.Engine, error) {
		return engine.New(engine.Options{
			Root:       spaceRoot,
			DeferModel: true,
			Cfg:        cfg,
			Version:    Version,
			Store:      session.New(spaceRoot, ""),
		})
	}
	srv, err := server.New(server.Options{
		Root:      root,
		Host:      host,
		Token:     opts.token,
		Cfg:       cfg,
		NewEngine: factory,
	})
	if err != nil {
		return fail(err)
	}
	defer srv.Close()
	if resumeID != "" {
		if err := srv.OpenSession(resumeID); err != nil {
			return fail(err)
		}
	}

	urlHost := host
	if urlHost == "" || urlHost == "0.0.0.0" || urlHost == "::" {
		urlHost = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(urlHost, port), srv.Token())
	fmt.Printf("cody serve  %s\n", url)
	fmt.Printf("%s  ·  %s/%s\n", root, srv.ProviderName(), srv.Model())
	if !server.IsLoopbackHost(host) {
		fmt.Println("warning: listening on a non-loopback address; anyone with the token URL can use cody")
	}
	fmt.Println("ctrl-c to stop")

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.watchStdin {
		sigCtx = cancelOnEOF(sigCtx, os.Stdin)
	}
	go func() {
		<-sigCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		httpSrv.Shutdown(ctx)
	}()
	if opts.open {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "cody: could not open browser: %v\n", err)
		}
	}
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(err)
	}
	return 0
}

// cancelOnEOF ends ctx when r closes. The macOS app holds the only write end
// of the server's stdin, so the OS closes it when the app exits for any
// reason, including a crash or force quit, and the server never outlives it.
func cancelOnEOF(ctx context.Context, r io.Reader) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		io.Copy(io.Discard, r)
		cancel()
	}()
	return ctx
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func cmdBench(opts options) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	applyFlags(cfg, opts)
	name := cfg.Provider
	base := cfg.ProviderConfig(name).BaseURL
	if base == "" {
		switch name {
		case "ollama":
			base = config.DefaultOllamaURL
		case "openrouter":
			base = config.DefaultOpenRouterURL
		default:
			return fail(fmt.Errorf("unknown provider %q", name))
		}
	}
	if name == "ollama" {
		if _, err := engine.EnsureOllama(cfg); err != nil {
			return fail(err)
		}
	}
	tasks, err := bench.SelectTasks(opts.task)
	if err != nil {
		return fail(err)
	}
	if opts.list {
		bench.PrintList(os.Stdout, tasks)
		if name == "ollama" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if models, err := provider.ListOllamaModels(ctx, base); err == nil && len(models) > 0 {
				fmt.Println("\nlocal models (smallest first):")
				for _, m := range models {
					fmt.Printf("  %-40s %s\n", m.Name, provider.HumanSize(m.Size))
				}
			}
		}
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = bench.Run(ctx, bench.Options{
		ProviderName: name,
		BaseURL:      base,
		APIKey:       cfg.Key(name),
		Model:        cfg.Model,
		Runs:         opts.runs,
		Keep:         opts.keep,
		JSON:         opts.jsonOut,
		Out:          os.Stdout,
		Progress:     os.Stderr,
	}, tasks)
	if err != nil {
		return fail(err)
	}
	return 0
}

func workspace(opts options) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" && filepath.Clean(root) == filepath.Clean(home) && !opts.allowHome {
		return "", errors.New("refusing to run in your home directory; cd into a project or pass --allow-home")
	}
	return root, nil
}

func recordFromMessage(m provider.Message) session.Record {
	r := session.Record{Type: "message", Role: m.Role, Text: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
	for _, c := range m.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
	}
	return r
}

func findMeta(recs []session.Record) map[string]string {
	for _, r := range recs {
		if r.Type == "meta" {
			return r.Meta
		}
	}
	return nil
}

func perMillion(price string) string {
	f, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return "?"
	}
	return strconv.FormatFloat(f*1e6, 'f', 2, 64)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return clip.Bytes(s, max) + "..."
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "cody: "+err.Error())
	return 1
}

func printHelp() {
	fmt.Print(`cody - a small coding agent

usage:
  cody                     interactive session in the current directory
  cody "prompt"            one-shot prompt
  cody run "prompt"        one-shot prompt
  cody resume [id|last]    resume a session
  cody sessions            list sessions for this workspace
  cody replay [id|last]    print a session transcript
  cody models [filter]     list models for the current provider
  cody index [--rebuild]   build or refresh the local semantic index
  cody serve [flags]       serve the web GUI on http://127.0.0.1:8765
  cody bench [flags]       run the built-in benchmark suite
  cody help                this help
  cody version             print the version

flags:
  -p, --provider NAME      ollama (default) or openrouter
  -m, --model NAME         model name
      --approval MODE      ask (default), auto, yolo
      --yolo               shorthand for --approval yolo
      --show-thinking      print model reasoning traces (hidden by default)
      --plan               require an approved plan before any change
      --resume ID          resume a session by id (or "last")
      --allow-home         allow running with $HOME as the workspace
      --debug[=BOOL]       show raw provider errors and every retry

serve flags:
      --addr HOST:PORT     listen address (default 127.0.0.1:8765)
      --token TOKEN        fixed access token (random by default)
      --open               open the GUI in your browser
      --watch-stdin        exit when stdin closes (the macOS app uses this)

index flags:
      --rebuild            discard the existing index and rebuild from scratch

bench flags:
      --task NAME          run one benchmark task
      --runs N             repeat each task N times
      --list               list benchmark tasks
      --keep               keep temporary task directories
      --json               print machine-readable results

config: ~/.cody/config.json
env:    CODY_PROVIDER, CODY_MODEL, CODY_APPROVAL, CODY_API_KEY,
        CODY_NO_AUTOSTART, CODY_EMBED_PROVIDER, OLLAMA_HOST, OPENROUTER_API_KEY,
        CODY_DEBUG, CODY_PLAN
`)
}
