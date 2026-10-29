// Isolated browser-test fixture: real Cody engine/server, local mock model, no keys or network services.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/engine"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/server"
	"github.com/ajhous44/cody/v3/internal/session"
)

func main() {
	tmp, err := os.MkdirTemp("", "cody-browser-")
	must(err)
	defer os.RemoveAll(tmp)
	must(os.Setenv("CODY_CONFIG_DIR", filepath.Join(tmp, "config")))
	for _, key := range []string{"OPENROUTER_API_KEY", "CODY_API_KEY", "CODY_MODEL", "CODY_PROVIDER", "OLLAMA_HOST"} {
		must(os.Unsetenv(key))
	}
	must(os.Setenv("CODY_NO_AUTOSTART", "1"))
	root := filepath.Join(tmp, "project & space")
	must(os.MkdirAll(filepath.Join(root, "nested & files"), 0700))
	must(os.WriteFile(filepath.Join(root, "nested & files", "sample.txt"), []byte("preview works"), 0600))
	model := httptest.NewServer(http.HandlerFunc(mockModel))
	defer model.Close()
	cfg := config.Default()
	cfg.Providers["ollama"] = config.Provider{BaseURL: model.URL + "/offline"}
	cfg.Providers["openrouter"] = config.Provider{BaseURL: model.URL}
	srv, err := server.New(server.Options{
		Root: root, Host: "127.0.0.1", Token: "browser-test", Cfg: cfg,
		NewEngine: func(root string) (server.Engine, error) {
			return engine.New(engine.Options{Root: root, Cfg: cfg, Store: session.New(root, ""), DeferModel: true})
		},
	})
	must(err)
	defer srv.Close()
	log.Print("browser fixture: http://127.0.0.1:4173/?token=browser-test")
	must(http.ListenAndServe("127.0.0.1:4173", srv.Handler()))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mockModel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/models" {
		fmt.Fprint(w, `{"data":[{"id":"fixture","name":"fixture","context_length":8192}]}`)
		return
	}
	if r.URL.Path != "/chat/completions" {
		http.Error(w, "offline", http.StatusServiceUnavailable)
		return
	}
	var req struct{ Messages []provider.Message }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "user" && last.Content == "rate limit" {
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":{"message":"Provider returned error","code":429}}`, http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	text := "small answer"
	if strings.HasPrefix(req.Messages[0].Content, "You name chat threads.") {
		text = `{"title":"browser test thread"}`
	} else if last.Role == "user" && last.Content == "wait" {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	} else if last.Role == "user" && last.Content == "write sample" {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"write%d\",\"type\":\"function\",\"function\":{\"name\":\"write\",\"arguments\":\"{\\\"path\\\":\\\"created.txt\\\",\\\"content\\\":\\\"approved write\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", len(req.Messages))
		return
	} else if last.Role == "user" && last.Content == "fail command" {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"bash%d\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"exit 7\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", len(req.Messages))
		return
	}
	quoted, _ := json.Marshal(text)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", quoted)
}
