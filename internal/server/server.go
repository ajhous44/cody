// Package server exposes the engine over local HTTP: a JSON/SSE API plus an
// embedded web client. It is the same engine the CLI uses, so the GUI is a
// thin subscriber, not a second agent stack.
//
// Each project (workspace root) owns a runner: its own engine, session store,
// history, and turn state. Switching the focused project or thread never
// interrupts a running turn in another project, and one turn per project runs
// at a time. Events and approval requests carry their project and session so
// clients can route them and highlight threads that need attention.
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/clip"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/session"
	"github.com/ajhous44/cody/v3/internal/tool"
)

//go:embed index.html
var indexHTML []byte

// The shell is static, so its ETag and gzipped form are precomputed once:
// repeat loads revalidate cheaply and travel at roughly a quarter the size.
var (
	indexETag    = etagFor(indexHTML)
	indexGzip    = gzipBytes(indexHTML)
	gitStatusTTL = 3 * time.Second
)

func etagFor(b []byte) string {
	sum := sha1.Sum(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// gitStatusCache is a short-lived snapshot of `git status` per workspace so
// tab switches and turn ends share one probe instead of spawning git each.
type gitStatusCache struct {
	at     time.Time
	branch string
	files  []gitFile
}

const (
	maxHistoryMessages = 80
	maxHistoryText     = 4000
	subBuffer          = 256
)

type Engine interface {
	Send(ctx context.Context, text string, h agent.Handler) error
	History() []provider.Message
	Model() string
	SetModel(string)
	ProviderName() string
	Approval() string
	SetApproval(string) error
	SessionID() string
	Root() string
	SetApprover(agent.Approver)
	NewSession() error
	Resume(id string) error
	Configure(*config.Config) error
	Title(ctx context.Context, text string) string
	Close() error
}

type Space struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Options struct {
	Root      string
	Host      string
	Token     string
	Cfg       *config.Config
	NewEngine func(root string) (Engine, error)
}

type pendingApproval struct {
	ch      chan bool
	tool    string
	args    string
	summary string
}

type runner struct {
	root    string
	eng     Engine
	busy    bool
	cancel  context.CancelFunc
	pending map[string]*pendingApproval
}

type Server struct {
	opts  Options
	host  string
	token string

	spacesFile string

	desiredModel    string
	desiredApproval string

	mu         sync.Mutex
	settingsMu sync.Mutex // serializes settings writes
	wg         sync.WaitGroup
	closed     bool
	runners    map[string]*runner
	spaces     []Space
	activeRoot string
	subs       map[chan []byte]bool

	gitMu    sync.Mutex
	gitCache map[string]gitStatusCache

	modelsMu    sync.Mutex
	modelsCache modelsSnapshot
}

// New creates a server with a runner for the initial root. host is the address
// host the HTTP listener will bind to; when it is loopback, requests with
// non-loopback Host headers are rejected to block DNS rebinding. An empty
// token generates a random one.
func New(opts Options) (*Server, error) {
	if opts.NewEngine == nil {
		return nil, errors.New("server: NewEngine is required")
	}
	if opts.Token == "" {
		opts.Token = randHex(16)
	}
	opts.Cfg = opts.Cfg.Clone()
	s := &Server{
		opts:       opts,
		host:       opts.Host,
		token:      opts.Token,
		spacesFile: filepath.Join(config.Dir(), "spaces.json"),
		runners:    map[string]*runner{},
		subs:       map[chan []byte]bool{},
		activeRoot: opts.Root,
		gitCache:   map[string]gitStatusCache{},
		spaces:     loadSpaces(filepath.Join(config.Dir(), "spaces.json")),
	}
	if !s.hasSpace(opts.Root) {
		s.spaces = append(s.spaces, Space{Name: filepath.Base(opts.Root), Path: opts.Root})
	}
	if err := s.saveSpaces(); err != nil {
		return nil, err
	}
	if _, err := s.runnerFor(opts.Root); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Token() string { return s.token }

// runnerFor returns the runner for a project, creating its engine on first use.
func (s *Server) runnerFor(root string) (*runner, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("server is shutting down")
	}
	if r, ok := s.runners[root]; ok {
		s.mu.Unlock()
		return r, nil
	}
	desiredModel, desiredApproval := s.desiredModel, s.desiredApproval
	cfg := s.opts.Cfg.Clone()
	s.mu.Unlock()

	eng, err := s.opts.NewEngine(root)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		if err := eng.Configure(cfg); err != nil {
			eng.Close()
			return nil, err
		}
	}
	r := &runner{root: root, eng: eng, pending: map[string]*pendingApproval{}}
	eng.SetApprover(agent.ApproverFunc(func(ctx context.Context, t tool.Tool, args string) (bool, error) {
		return s.requestApproval(ctx, r, t, args)
	}))
	if desiredModel != "" {
		eng.SetModel(desiredModel)
	}
	if desiredApproval != "" {
		_ = eng.SetApproval(desiredApproval)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		eng.Close()
		return nil, errors.New("server is shutting down")
	}
	if existing, ok := s.runners[root]; ok {
		s.mu.Unlock()
		eng.Close()
		return existing, nil
	}
	s.runners[root] = r
	s.mu.Unlock()
	return r, nil
}

func (s *Server) active() *runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runners[s.activeRoot]
}

func (s *Server) activeRootPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRoot
}

func (s *Server) ProviderName() string { return s.active().eng.ProviderName() }

func (s *Server) Model() string { return s.active().eng.Model() }

// OpenSession switches the focused project's engine to a session ("last"
// allowed) or to a fresh one when id is empty. Used by the CLI at startup.
func (s *Server) OpenSession(id string) error {
	r := s.active()
	if id == "" || id == "new" {
		return r.eng.NewSession()
	}
	return r.eng.Resume(id)
}

// Close releases every runner's session file.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	runners := make([]*runner, 0, len(s.runners))
	for _, r := range s.runners {
		runners = append(runners, r)
		if r.cancel != nil {
			r.cancel()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	var err error
	for _, r := range runners {
		err = errors.Join(err, r.eng.Close())
	}
	return err
}

// requestApproval publishes an approval request and waits for a browser
// response. It carries the project and session so clients can highlight a
// thread that is waiting when it is not the focused one.
func (s *Server) requestApproval(ctx context.Context, r *runner, t tool.Tool, args string) (bool, error) {
	id := randHex(8)
	ch := make(chan bool, 1)
	summary := t.Summarize(json.RawMessage(args))
	session := r.eng.SessionID()

	s.mu.Lock()
	r.pending[id] = &pendingApproval{ch: ch, tool: t.Name(), args: args, summary: summary}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(r.pending, id)
		s.mu.Unlock()
	}()

	s.broadcast(wireEvent{Type: "approval", ID: id, Tool: t.Name(), Args: args, Summary: summary, Root: r.root, Session: session})
	select {
	case allow := <-ch:
		return allow, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type wireEvent struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    string          `json:"args,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Result  string          `json:"result,omitempty"`
	Ms      int64           `json:"ms,omitempty"`
	Usage   *provider.Usage `json:"usage,omitempty"`
	ID      string          `json:"id,omitempty"`
	Error   string          `json:"error,omitempty"`

	Provider  string  `json:"provider,omitempty"`
	Model     string  `json:"model,omitempty"`
	Approval  string  `json:"approval,omitempty"`
	Session   string  `json:"session,omitempty"`
	Title     string  `json:"title,omitempty"`
	Workspace string  `json:"workspace,omitempty"`
	Busy      bool    `json:"busy,omitempty"`
	Spaces    []Space `json:"spaces,omitempty"`
	Active    string  `json:"active,omitempty"`
	Root      string  `json:"root,omitempty"`

	Running []runningInfo    `json:"running,omitempty"`
	Pending []pendingInfo    `json:"pending,omitempty"`
	History []historyMessage `json:"history,omitempty"`
}

type runningInfo struct {
	Root     string `json:"root"`
	Session  string `json:"session"`
	Approval bool   `json:"approval"`
}

type pendingInfo struct {
	ID      string `json:"id"`
	Root    string `json:"root"`
	Session string `json:"session"`
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	Summary string `json:"summary"`
}

type historyMessage struct {
	Role       string        `json:"role"`
	Text       string        `json:"text,omitempty"`
	Tools      []historyTool `json:"tools,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Turn       int           `json:"turn,omitempty"` // user turn number, for forking
}

type historyTool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.auth(s.handleState))
	mux.HandleFunc("/api/events", s.auth(s.handleEvents))
	mux.HandleFunc("/api/prompt", s.auth(s.handlePrompt))
	mux.HandleFunc("/api/approve", s.auth(s.handleApprove))
	mux.HandleFunc("/api/interrupt", s.auth(s.handleInterrupt))
	mux.HandleFunc("/api/model", s.auth(s.handleModel))
	mux.HandleFunc("/api/approval", s.auth(s.handleApproval))
	mux.HandleFunc("/api/models", s.auth(s.handleModels))
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("/api/spaces", s.auth(s.handleSpaces))
	mux.HandleFunc("/api/space", s.auth(s.handleSpaceSwitch))
	mux.HandleFunc("/api/sessions", s.auth(s.handleSessions))
	mux.HandleFunc("/api/session", s.auth(s.handleSession))
	mux.HandleFunc("/api/session/rename", s.auth(s.handleSessionRename))
	mux.HandleFunc("/api/session/fork", s.auth(s.handleSessionFork))
	mux.HandleFunc("/api/files", s.auth(s.handleFiles))
	mux.HandleFunc("/api/file", s.auth(s.handleFile))
	mux.HandleFunc("/api/fs", s.auth(s.handleFS))
	mux.HandleFunc("/api/pick-folder", s.auth(s.handlePickFolder))
	mux.HandleFunc("/api/git", s.auth(s.handleGit))
	mux.HandleFunc("/api/git/diff", s.auth(s.handleGitDiff))
	return s.hostGuard(mux)
}

func (s *Server) hostGuard(next http.Handler) http.Handler {
	if !IsLoopbackHost(s.host) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if !IsLoopbackHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func IsLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Cody-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", indexETag)
	w.Header().Set("Vary", "Accept-Encoding")
	// The shell carries no secrets, so browsers may cache it and revalidate.
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == indexETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(indexGzip)
		return
	}
	w.Write(indexHTML)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, s.stateEventFull())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, subBuffer)
	s.mu.Lock()
	s.subs[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	writeEvent := func(b []byte) bool {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if b, err := json.Marshal(s.stateEventFull()); err == nil {
		if !writeEvent(b) {
			return
		}
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(b) {
				return
			}
		case <-ping.C:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	root := strings.TrimSpace(body.Path)
	if root == "" {
		root = s.activeRootPath()
	} else {
		root = filepath.Clean(root)
		if !s.hasSpace(root) {
			http.Error(w, "unknown project", http.StatusBadRequest)
			return
		}
	}
	rr, err := s.runnerFor(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if rr.busy {
		session := rr.eng.SessionID()
		s.mu.Unlock()
		s.writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":   "busy",
			"root":    root,
			"session": session,
		})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	rr.busy = true
	rr.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	// The runner is reserved, so switching threads (a full transcript load)
	// runs without s.mu, which other projects' turns need to stream.
	if id := strings.TrimSpace(body.ID); id != "" && id != rr.eng.SessionID() {
		if err := rr.eng.Resume(id); err != nil {
			cancel()
			syncErr := s.release(rr)
			s.wg.Done()
			http.Error(w, errors.Join(err, syncErr).Error(), http.StatusBadRequest)
			return
		}
	}
	sessionID := rr.eng.SessionID()
	newThread := len(rr.eng.History()) == 0

	s.broadcast(wireEvent{Type: "turn_start", Root: root, Session: sessionID})
	go func() {
		defer s.wg.Done()
		defer cancel()
		err := rr.eng.Send(ctx, text, func(e agent.Event) {
			ev := wireEventFromAgent(e)
			ev.Root = root
			ev.Session = sessionID
			s.broadcast(ev)
		})
		ev := wireEvent{Root: root, Session: sessionID}
		switch {
		case err == nil:
			ev.Type = "turn_end"
		case errors.Is(err, context.Canceled):
			ev.Type = "turn_cancelled"
		default:
			ev.Type = "turn_end"
			ev.Error = err.Error()
		}
		s.mu.Lock()
		// Keep the runner reserved until its next-turn settings are installed.
		if syncErr := s.syncRunnerLocked(rr); syncErr != nil {
			ev.Error = errors.Join(err, syncErr).Error()
		}
		rr.busy, rr.cancel = false, nil
		s.broadcastLocked(ev)
		s.mu.Unlock()
		// Name the thread after the turn, not alongside it: a second concurrent
		// request competes for the same rate limit, and a failed turn would
		// likely fail the title too. Title bounds itself, so the turn's
		// cancellation must not cut it short. Title events carry only the
		// title, so they cannot overwrite newer turn state.
		if newThread && err == nil {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				if title := rr.eng.Title(context.WithoutCancel(ctx), text); title != "" {
					s.broadcast(wireEvent{Type: "title", Title: title, Root: root, Session: sessionID})
				}
			}()
		}
	}()
	s.writeJSON(w, map[string]string{"status": "running"})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Allow bool   `json:"allow"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	s.mu.Lock()
	var ch chan bool
	for _, rr := range s.runners {
		if p, ok := rr.pending[body.ID]; ok {
			ch = p.ch
			break
		}
	}
	s.mu.Unlock()
	if ch == nil {
		http.Error(w, "no pending approval with that id", http.StatusNotFound)
		return
	}
	select {
	case ch <- body.Allow:
	default:
	}
	s.writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	root, ok := s.rootFor(body.Path)
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	cancel := context.CancelFunc(nil)
	if rr := s.runners[root]; rr != nil {
		cancel = rr.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.desiredModel = model
	if s.opts.Cfg != nil {
		s.opts.Cfg.Model = model
	}
	for _, rr := range s.runners {
		if !rr.busy {
			rr.eng.SetModel(model)
		}
	}
	s.mu.Unlock()
	s.broadcast(s.stateEvent())
	s.writeJSON(w, map[string]string{"model": model})
}

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	mode := strings.TrimSpace(body.Mode)
	if mode != "ask" && mode != "auto" && mode != "yolo" {
		http.Error(w, "approval must be ask, auto, or yolo", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.desiredApproval = mode
	if s.opts.Cfg != nil {
		s.opts.Cfg.Approval = mode
	}
	var err error
	for _, rr := range s.runners {
		if rr.busy {
			continue
		}
		if e := rr.eng.SetApproval(mode); e != nil {
			err = e
			break
		}
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.broadcast(s.stateEvent())
	s.writeJSON(w, map[string]string{"approval": s.active().eng.Approval()})
}

// reserve claims an idle runner so slow session I/O can run without holding
// s.mu, which every running turn needs to broadcast its events.
func (s *Server) reserve(r *runner) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.busy {
		return false
	}
	r.busy = true
	return true
}

// release frees a reserved runner and applies settings changed meanwhile,
// which syncIdleRunners skipped while it was busy.
func (s *Server) release(r *runner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.busy, r.cancel = false, nil
	return s.syncRunnerLocked(r)
}

// syncRunnerLocked applies an isolated snapshot while the runner is reserved.
func (s *Server) syncRunnerLocked(r *runner) error {
	if s.opts.Cfg != nil {
		return r.eng.Configure(s.opts.Cfg.Clone())
	}
	if s.desiredModel != "" {
		r.eng.SetModel(s.desiredModel)
	}
	if s.desiredApproval != "" {
		return r.eng.SetApproval(s.desiredApproval)
	}
	return nil
}

func (s *Server) syncIdleRunners() error {
	s.mu.Lock()
	var err error
	for _, r := range s.runners {
		if !r.busy {
			err = errors.Join(err, s.syncRunnerLocked(r))
		}
	}
	s.mu.Unlock()
	s.broadcast(s.stateEvent())
	return err
}

func (s *Server) configSnapshot() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Cfg.Clone()
}

// stateEvent is the lightweight broadcast: everything except the transcript.
// Clients only render history when the focused session changes, so steady
// updates (titles, model/approval switches) skip the re-marshal. Use
// stateEventFull when the focused session may have changed.
func (s *Server) stateEvent() wireEvent {
	return s.stateEventWith(false)
}

func (s *Server) stateEventFull() wireEvent {
	return s.stateEventWith(true)
}

func (s *Server) stateEventWith(full bool) wireEvent {
	s.mu.Lock()
	r := s.runners[s.activeRoot]
	spaces := append([]Space(nil), s.spaces...)
	active := s.activeRoot
	busy := false
	if r != nil {
		busy = r.busy
	}
	running := []runningInfo{}
	pending := []pendingInfo{}
	for _, rr := range s.runners {
		if rr.busy {
			running = append(running, runningInfo{Root: rr.root, Session: rr.eng.SessionID(), Approval: len(rr.pending) > 0})
		}
		for id, p := range rr.pending {
			pending = append(pending, pendingInfo{ID: id, Root: rr.root, Session: rr.eng.SessionID(), Tool: p.tool, Args: p.args, Summary: p.summary})
		}
	}
	s.mu.Unlock()
	if r == nil {
		return wireEvent{Type: "state", Spaces: spaces, Active: active}
	}
	ev := wireEvent{
		Type:      "state",
		Provider:  r.eng.ProviderName(),
		Model:     r.eng.Model(),
		Approval:  r.eng.Approval(),
		Session:   r.eng.SessionID(),
		Title:     session.Title(r.eng.Root(), r.eng.SessionID()),
		Workspace: r.eng.Root(),
		Spaces:    spaces,
		Active:    active,
		Root:      active,
		Busy:      busy,
		Running:   running,
		Pending:   pending,
	}
	if full {
		ev.History = historyOf(r.eng.History())
	}
	return ev
}

func historyOf(msgs []provider.Message) []historyMessage {
	// Turns are numbered over the whole thread before trimming, so a fork
	// point means the same message as in the session file.
	turns := make([]int, len(msgs))
	n := 0
	for i, m := range msgs {
		if m.Role == "user" {
			n++
			turns[i] = n
		}
	}
	if len(msgs) > maxHistoryMessages {
		turns = turns[len(msgs)-maxHistoryMessages:]
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}
	out := make([]historyMessage, 0, len(msgs))
	for i, m := range msgs {
		h := historyMessage{Role: m.Role, Text: truncate(m.Content, maxHistoryText), ToolCallID: m.ToolCallID, Turn: turns[i]}
		for _, c := range m.ToolCalls {
			h.Tools = append(h.Tools, historyTool{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
		}
		out = append(out, h)
	}
	return out
}

func (s *Server) broadcast(ev wireEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked(ev)
}

func (s *Server) broadcastLocked(ev wireEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
			// Reconnect delivers authoritative state instead of silently losing control events.
			delete(s.subs, ch)
			close(ch)
		}
	}
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return s.decodeBody(w, r, v)
}

func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func wireEventFromAgent(e agent.Event) wireEvent {
	ev := wireEvent{Text: e.Text, Tool: e.Tool, Args: e.Args, Summary: e.Summary, Result: e.Result, Ms: e.Ms, Usage: e.Usage}
	switch e.Kind {
	case agent.KindThinking:
		ev.Type = "thinking"
	case agent.KindReasoning:
		ev.Type = "reasoning"
	case agent.KindText:
		ev.Type = "text"
	case agent.KindToolStart:
		ev.Type = "tool_start"
	case agent.KindToolEnd:
		ev.Type = "tool_end"
	case agent.KindUsage:
		ev.Type = "usage"
	case agent.KindNotice:
		ev.Type = "notice"
	default:
		ev.Type = e.Kind
	}
	return ev
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return clip.Bytes(s, max) + "\n[truncated]"
}
