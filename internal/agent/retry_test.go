package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

func TestRepeatedRateLimitsShowOneNoticeAndFriendlyError(t *testing.T) {
	friendly := "xingchen is rate-limited right now (openrouter 429). Try again in a moment or switch models"
	for _, tc := range []struct {
		debug   bool
		notices int
		err     string
	}{
		{false, 1, friendly},
		{true, 2, friendly + " [debug: openrouter: HTTP 429: Provider returned error]"},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"Provider returned error","code":429}}`))
		}))
		a := New(Options{
			Provider: provider.NewOpenAI("openrouter", srv.URL, "key"),
			Model:    "xingchen",
			Tools:    tool.NewRegistry(&echoTool{}),
			Debug:    tc.debug,
		})
		var notices []string
		_, err := a.Prompt(context.Background(), "write a nextjs app", func(e Event) {
			if e.Kind == KindNotice {
				notices = append(notices, e.Text)
			}
		})
		srv.Close()
		if calls.Load() != 3 {
			t.Fatalf("debug=%v: attempts = %d, want 3", tc.debug, calls.Load())
		}
		if len(notices) != tc.notices {
			t.Fatalf("debug=%v: notices = %q, want %d", tc.debug, notices, tc.notices)
		}
		if err == nil || err.Error() != tc.err || strings.Contains(err.Error(), "\n") {
			t.Fatalf("debug=%v: error = %v, want %q", tc.debug, err, tc.err)
		}
	}
}
