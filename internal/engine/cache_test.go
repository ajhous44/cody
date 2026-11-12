package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/session"
)

func TestCacheIdentityLimitsAndInstructionsAcrossSessions(t *testing.T) {
	requests := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"test","context_length":16384,"top_provider":{"max_completion_tokens":512}}]}`)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests <- req
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2000,\"prompt_tokens_details\":{\"cached_tokens\":1000,\"cache_write_tokens\":500}}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	root := t.TempDir()
	cfg := config.Default()
	cfg.Provider = "openrouter"
	cfg.Model = "test"
	cfg.Providers["openrouter"] = config.Provider{BaseURL: srv.URL}
	eng := newEngine(t, cfg, root, nil)
	oldID := eng.SessionID()
	send := func() map[string]any {
		t.Helper()
		if err := eng.Send(context.Background(), "hello", nil); err != nil {
			t.Fatal(err)
		}
		return <-requests
	}
	first := send()
	second := send()
	if first["session_id"] == nil || first["session_id"] != second["session_id"] || first["max_tokens"] != float64(512) {
		t.Fatalf("cache or limits: %v %v", first, second)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Fresh rule."), 0600); err != nil {
		t.Fatal(err)
	}
	changed := send()
	raw, _ := json.Marshal(changed["messages"])
	var messages []provider.Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[0].Content, "Fresh rule.") {
		t.Fatal("root instructions not refreshed")
	}
	if err := eng.NewSession(); err != nil {
		t.Fatal(err)
	}
	next := send()
	if next["session_id"] == first["session_id"] {
		t.Fatal("new session reused affinity")
	}
	if err := eng.Resume(oldID); err != nil {
		t.Fatal(err)
	}
	resumed := send()
	if resumed["session_id"] != first["session_id"] {
		t.Fatal("resume lost affinity")
	}
	_, records, err := session.Load(root, oldID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Usage != nil && r.Usage.PromptDetails != nil && r.Usage.PromptDetails.CachedTokens == 1000 {
			found = true
		}
	}
	if !found {
		t.Fatal("cache metrics not persisted")
	}
}
