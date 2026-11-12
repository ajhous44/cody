package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackpressureForcesResync(t *testing.T) {
	ch := make(chan []byte, subBuffer)
	healthy := make(chan []byte, subBuffer)
	s := &Server{subs: map[chan []byte]bool{ch: true, healthy: true}}
	for i := 0; i < subBuffer; i++ {
		ch <- []byte(`{"type":"text"}`)
	}
	s.broadcast(wireEvent{Type: "turn_end"})
	if s.subs[ch] {
		t.Fatal("overflowed subscriber was not disconnected")
	}
	for range ch {
	} // Must close so EventSource reconnects and receives a full state.
	var ev wireEvent
	if err := json.Unmarshal(<-healthy, &ev); err != nil || ev.Type != "turn_end" {
		t.Fatalf("healthy subscriber: %+v %v", ev, err)
	}
	s.broadcast(wireEvent{Type: "approval"}) // Must not send to a closed channel.
}

func TestFilesRequestPinsProject(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(first, "first.txt"), []byte("first"), 0600)
	os.WriteFile(filepath.Join(second, "second.txt"), []byte("second"), 0600)
	ts, s := newTestServer(t, &stubEngine{root: first})
	defer ts.Close()
	s.spaces = append(s.spaces, Space{Path: second})
	for _, endpoint := range []string{"/api/files?", "/api/file?path=second.txt&", "/api/git?", "/api/git/diff?path=second.txt&"} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://localhost"+endpoint+"root="+url.QueryEscape(second)+"&token=test-token", nil))
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", endpoint, rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["root"] != second || strings.Contains(rr.Body.String(), "first.txt") {
			t.Fatalf("wrong project: %s", rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	s.handleFiles(rr, httptest.NewRequest("GET", "/api/files?root="+url.QueryEscape(t.TempDir()), nil))
	if rr.Code != 400 {
		t.Fatalf("unregistered project accepted: %d", rr.Code)
	}
}
