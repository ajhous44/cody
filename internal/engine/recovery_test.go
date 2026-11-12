package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/session"
)

func TestJournalBeforeNextModelRequest(t *testing.T) {
	var calls atomic.Int32
	waiting := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			w.Write([]byte("data: " + toolCallResponse("write", `{"path":"changed.txt","content":"written"}`)[0] + "\n\ndata: [DONE]\n\n"))
			return
		}
		close(waiting)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.Send(ctx, "write", nil) }()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("did not reach second model request")
	}
	_, recs, err := session.Load(root, eng.SessionID())
	if err != nil {
		t.Errorf("completed file write has no journal while next model request is pending: %v", err)
	} else {
		found := false
		for _, r := range recs {
			if r.Role == "tool" {
				found = true
			}
		}
		if !found {
			t.Error("completed tool action missing from journal")
		}
	}
	cancel()
	<-done
}

func TestJournalFailureStopsBeforeProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	root := t.TempDir()
	eng := newEngine(t, testConfig(srv, "yolo"), root, nil)
	if err := os.MkdirAll(config.Dir()+"/sessions", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.ProjectDir(root), []byte("blocks session directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Send(context.Background(), "change files", nil); err == nil {
		t.Fatal("expected journal failure")
	}
	if calls.Load() != 0 {
		t.Fatal("contacted provider despite journal failure")
	}
}

func TestInterruptedToolCallsAreRepaired(t *testing.T) {
	recs := []session.Record{
		{Type: "message", Role: "assistant", ToolCalls: []session.ToolCall{{ID: "a", Name: "write"}, {ID: "b", Name: "write"}}},
		{Type: "message", Role: "tool", ToolCallID: "a", Text: "done"},
	}
	msgs := MessagesFromRecords(recs)
	if len(msgs) != 3 || msgs[2].ToolCallID != "b" || !strings.Contains(msgs[2].Content, "outcome unknown") {
		t.Fatalf("invalid recovery: %+v", msgs)
	}
}

func TestDeferredModelNeedsNoRunningProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "offline", 503) }))
	defer srv.Close()
	cfg := config.Default()
	cfg.Providers["ollama"] = config.Provider{BaseURL: srv.URL}
	root := t.TempDir()
	eng, err := New(Options{Root: root, Cfg: cfg, Store: session.New(root, ""), DeferModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if eng.Model() != "" || calls.Load() != 0 {
		t.Fatal("startup should not probe an unconfigured provider")
	}
}
