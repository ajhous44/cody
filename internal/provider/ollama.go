package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Model struct {
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Modified     string   `json:"modified_at"`
	Capabilities []string `json:"capabilities"`
	Details      struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
		Quantization  string `json:"quantization_level"`
		ContextLength int    `json:"context_length"`
	} `json:"details"`
}

// SupportsTools reports whether the model can chat with tools. Older Ollama
// versions omit capabilities, so unknown is treated as supported.
func (m Model) SupportsTools() bool {
	if len(m.Capabilities) == 0 {
		return true
	}
	for _, c := range m.Capabilities {
		if c == "tools" || c == "completion" {
			return true
		}
	}
	return false
}

// ollamaTransport is shared by every Ollama API call so connections are
// reused (keep-alive) instead of re-dialed per request.
var ollamaTransport = &http.Transport{
	MaxIdleConns:        16,
	MaxIdleConnsPerHost: 8,
	IdleConnTimeout:     90 * time.Second,
}

func ollamaClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: ollamaTransport, Timeout: timeout}
}

func OllamaHost(baseURL string) string {
	h := strings.TrimRight(baseURL, "/")
	return strings.TrimSuffix(h, "/v1")
}

func ListOllamaModels(ctx context.Context, baseURL string) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OllamaHost(baseURL)+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := ollamaClient(5 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w (is ollama running?)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Size < out.Models[j].Size })
	return out.Models, nil
}

// EnsureOllama makes sure an Ollama server is reachable. When the endpoint is
// local and nothing answers, it starts the Ollama app (macOS) or the ollama
// binary and waits for the server to come up. Remote endpoints are only
// checked, never started. The returned bool reports whether cody started it.
func EnsureOllama(ctx context.Context, baseURL string) (bool, error) {
	if ollamaUp(ctx, baseURL) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !isLocalHost(baseURL) {
		return false, fmt.Errorf("ollama at %s is not reachable", OllamaHost(baseURL))
	}
	if os.Getenv("CODY_NO_AUTOSTART") != "" {
		return false, errors.New("ollama is not running and autostart is disabled (CODY_NO_AUTOSTART)")
	}
	if err := startOllama(); err != nil {
		return false, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ollamaUp(ctx, baseURL) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
	return true, errors.New("started ollama but it did not become ready within 20s")
}

func ollamaUp(ctx context.Context, baseURL string) bool {
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, OllamaHost(baseURL)+"/api/version", nil)
	if err != nil {
		return false
	}
	resp, err := ollamaClient(time.Second).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isLocalHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

func startOllama() error {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, app := range []string{"/Applications/Ollama.app", filepath.Join(home, "Applications", "Ollama.app")} {
			if _, err := os.Stat(app); err == nil {
				return exec.Command("open", "-a", "Ollama").Run()
			}
		}
	}
	path, err := exec.LookPath("ollama")
	if err != nil {
		return errors.New("ollama is not running and no ollama binary was found; install Ollama or start it manually")
	}
	cmd := exec.Command(path, "serve")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ollama: %w", err)
	}
	go cmd.Wait()
	return nil
}

var errEmbedEndpointMissing = errors.New("embed endpoint missing")

// OllamaEmbed returns one embedding vector per input using the local Ollama
// server. It prefers the batched /api/embed endpoint and falls back to
// /api/embeddings for older servers.
func OllamaEmbed(ctx context.Context, baseURL, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	vecs, err := ollamaEmbedBatch(ctx, baseURL, model, inputs)
	if err == nil {
		return vecs, nil
	}
	if !errors.Is(err, errEmbedEndpointMissing) {
		return nil, err
	}
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		vec, err := ollamaEmbedOne(ctx, baseURL, model, in)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func ollamaEmbedBatch(ctx context.Context, baseURL, model string, inputs []string) ([][]float32, error) {
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	err := ollamaPost(ctx, OllamaHost(baseURL)+"/api/embed", map[string]any{
		"model": model,
		"input": inputs,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama: embed returned %d vectors for %d inputs", len(out.Embeddings), len(inputs))
	}
	return out.Embeddings, nil
}

func ollamaEmbedOne(ctx context.Context, baseURL, model, input string) ([]float32, error) {
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	err := ollamaPost(ctx, OllamaHost(baseURL)+"/api/embeddings", map[string]any{
		"model":  model,
		"prompt": input,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Embedding) == 0 {
		return nil, errors.New("ollama: embedding response was empty")
	}
	return out.Embedding, nil
}

func ollamaPost(ctx context.Context, url string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ollamaClient(2 * time.Minute).Do(req)
	if err != nil {
		return fmt.Errorf("ollama: %w (is ollama running?)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errEmbedEndpointMissing
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type RunningModel struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ContextLength int    `json:"context_length"`
	ExpiresAt     string `json:"expires_at"`
}

// OllamaRunning lists models currently loaded into memory.
func OllamaRunning(ctx context.Context, baseURL string) ([]RunningModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OllamaHost(baseURL)+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := ollamaClient(5 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Models []RunningModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// OllamaLoadedContext returns the context window of the running instance of
// model, or 0 when the model is not loaded. The OpenAI-compatible endpoint
// silently ignores num_ctx, so this is the window the server will actually
// use for requests.
func OllamaLoadedContext(ctx context.Context, baseURL, model string) int {
	models, err := OllamaRunning(ctx, baseURL)
	if err != nil {
		return 0
	}
	for _, m := range models {
		if matchModelName(m.Name, model) {
			return m.ContextLength
		}
	}
	return 0
}

// OllamaModelLoaded reports whether model is currently loaded in Ollama. The
// second result is false when the server cannot be reached, so callers can
// tell "cold" apart from "unknown" and only announce a startup they know
// is real.
func OllamaModelLoaded(ctx context.Context, baseURL, model string) (loaded, known bool) {
	models, err := OllamaRunning(ctx, baseURL)
	if err != nil {
		return false, false
	}
	for _, m := range models {
		if matchModelName(m.Name, model) {
			return true, true
		}
	}
	return false, true
}

func matchModelName(have, want string) bool {
	return have == want || strings.TrimSuffix(have, ":latest") == strings.TrimSuffix(want, ":latest")
}

var modelPreference = []string{
	"qwen3-coder", "qwen2.5-coder", "qwen2.5", "qwen3",
	"deepseek-coder", "deepseek-r1", "codellama", "granite-code",
	"llama3", "mistral", "phi",
}

// PickOllamaModel prefers a small, tool-calling-friendly coding model.
// Models should come from ListOllamaModels (sorted smallest first).
func PickOllamaModel(models []Model) (best string) {
	if len(models) == 0 {
		return ""
	}
	sorted := make([]Model, len(models))
	copy(sorted, models)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Size < sorted[j].Size })
	for _, pref := range modelPreference {
		for _, m := range sorted {
			if m.SupportsTools() && strings.Contains(strings.ToLower(m.Name), pref) {
				return m.Name
			}
		}
	}
	for _, m := range sorted {
		if m.SupportsTools() {
			return m.Name
		}
	}
	return ""
}

func HumanSize(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}
