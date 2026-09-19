// Package server exposes the engine over local HTTP: a JSON/SSE API plus an
// embedded web client. It is the same engine the CLI uses, so the GUI is a
// thin subscriber, not a second agent stack.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ajhous44/cody/internal/agent"
	"github.com/ajhous44/cody/internal/provider"
	"github.com/ajhous44/cody/internal/tool"
)

//go:embed index.html
var indexHTML []byte

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
}

type Server struct {
	eng   Engine
	host  string
	token string

	mu      sync.Mutex
	subs    map[chan []byte]bool
	pending map[string]chan bool
	cancel  context.CancelFunc
	busy    bool
}

// New creates a server. host is the address host the HTTP listener will bind
// to; when it is a loopback address, requests with non-loopback Host headers
// are rejected to block DNS-rebinding. An empty token generates a random one.
func New(eng Engine, host, token string) *Server {
	if token == "" {
		token = randHex(16)
	}
	return &Server{
		eng:     eng,
		host:    host,
		token:   token,
		subs:    map[chan []byte]bool{},
		pending: map[string]chan bool{},
	}
}

func (s *Server) Token() string { return s.token }

// Approve implements agent.Approver for the browser: it publishes an approval
// request and waits for a response from any connected client.
func (s *Server) Approve(ctx context.Context, t tool.Tool, args string) (bool, error) {
	id := randHex(8)
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	summary := t.Summarize(json.RawMessage(args))
	s.broadcast(wireEvent{Type: "approval", ID: id, Tool: t.Name(), Args: args, Summary: summary})
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

	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Approval  string `json:"approval,omitempty"`
	Session   string `json:"session,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Busy      bool   `json:"busy,omitempty"`

	History []historyMessage `json:"history,omitempty"`
}

type historyMessage struct {
	Role       string        `json:"role"`
	Text       string        `json:"text,omitempty"`
	Tools      []historyTool `json:"tools,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
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
	w.Header().Set("Cache-Control", "no-store")
	w.Write(indexHTML)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, s.stateEvent())
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

	writeEvent := func(b []byte) {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	if b, err := json.Marshal(s.stateEvent()); err == nil {
		writeEvent(b)
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			writeEvent(b)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		http.Error(w, "a turn is already running", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.busy = true
	s.cancel = cancel
	s.mu.Unlock()

	s.broadcast(wireEvent{Type: "turn_start"})
	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.cancel = nil
			s.mu.Unlock()
			cancel()
		}()
		err := s.eng.Send(ctx, text, func(e agent.Event) { s.broadcast(wireEventFromAgent(e)) })
		switch {
		case err == nil:
			s.broadcast(wireEvent{Type: "turn_end"})
		case errors.Is(err, context.Canceled):
			s.broadcast(wireEvent{Type: "turn_cancelled"})
		default:
			s.broadcast(wireEvent{Type: "turn_end", Error: err.Error()})
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
	ch := s.pending[body.ID]
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
	s.mu.Lock()
	cancel := s.cancel
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
	s.eng.SetModel(model)
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
	if err := s.eng.SetApproval(strings.TrimSpace(body.Mode)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.broadcast(s.stateEvent())
	s.writeJSON(w, map[string]string{"approval": s.eng.Approval()})
}

func (s *Server) stateEvent() wireEvent {
	ev := wireEvent{
		Type:      "state",
		Provider:  s.eng.ProviderName(),
		Model:     s.eng.Model(),
		Approval:  s.eng.Approval(),
		Session:   s.eng.SessionID(),
		Workspace: s.eng.Root(),
	}
	s.mu.Lock()
	ev.Busy = s.busy
	s.mu.Unlock()
	ev.History = historyOf(s.eng.History())
	return ev
}

func historyOf(msgs []provider.Message) []historyMessage {
	if len(msgs) > maxHistoryMessages {
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}
	out := make([]historyMessage, 0, len(msgs))
	for _, m := range msgs {
		h := historyMessage{Role: m.Role, Text: truncate(m.Content, maxHistoryText), ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			h.Tools = append(h.Tools, historyTool{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
		}
		out = append(out, h)
	}
	return out
}

func (s *Server) broadcast(ev wireEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- b:
		default: // slow client: drop rather than block the agent
		}
	}
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
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
	return s[:max] + "\n[truncated]"
}
