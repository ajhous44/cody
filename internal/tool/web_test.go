package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
)

type webRoundTrip func(*http.Request) (*http.Response, error)

func (f webRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func webResponse(r *http.Request, content string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(content)), Request: r}
}

func TestPublicWebBoundaries(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.com", "http://localhost", "http://127.0.0.1", "http://[::1]", "http://10.1.2.3", "http://169.254.169.254", "http://100.64.0.1", "https://example.com:8080", "http://printer.local", "http://x.localhost"} {
		if _, err := publicURL(raw); err == nil {
			t.Errorf("allowed %s", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "fc00::1", "100.100.100.100", "0.0.0.0", "224.0.0.1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("allowed IP %s", raw)
		}
	}
	if !publicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("blocked public address")
	}
	client := publicWebClient()
	calls := 0
	client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := webResponse(r, "")
		resp.StatusCode = 302
		resp.Header.Set("Location", "http://127.0.0.1/private")
		return resp, nil
	})
	if _, _, err := webGet(context.Background(), client, "https://example.com", ""); err == nil || calls != 1 {
		t.Fatalf("private redirect: calls=%d err=%v", calls, err)
	}
}
func TestWebFetchExtractsBoundedUntrustedText(t *testing.T) {
	w := NewWebFetch()
	w.client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatal("credentials attached")
		}
		return webResponse(r, `<title>Guide</title><script>SECRET_SCRIPT</script><style>HIDDEN_STYLE</style><p>Hello &amp; world</p><a href="/docs">Docs</a><a href="javascript:evil()">Bad</a><p hidden>SECRET_HIDDEN</p>`), nil
	})
	out, err := w.Run(context.Background(), json.RawMessage(`{"url":"https://example.com/start"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"untrusted", "Hello & world", "https://example.com/docs"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	for _, no := range []string{"SECRET_", "HIDDEN_STYLE", "javascript:"} {
		if strings.Contains(out, no) {
			t.Errorf("leaked %q", no)
		}
	}
	w.client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		return webResponse(r, strings.Repeat("a", webLimit+1)), nil
	})
	if _, err := w.Run(context.Background(), json.RawMessage(`{"url":"https://example.com"}`)); err == nil {
		t.Fatal("accepted oversized body")
	}
}
func TestSearchQueryKeyAndResultSources(t *testing.T) {
	w := NewWebSearch("test-search-secret")
	w.client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("q") != "Go & HTTP" || r.Header.Get("X-Subscription-Token") != "test-search-secret" {
			t.Fatal("incorrect search request")
		}
		resp := webResponse(r, `{"web":{"results":[{"title":"Go","url":"https://go.dev/doc/","description":"Official <b>documentation</b>"}]}}`)
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})
	out, err := w.Run(context.Background(), json.RawMessage(`{"query":"Go & HTTP"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://go.dev/doc/") || !strings.Contains(out, "Official documentation") || strings.Contains(out, "test-search-secret") {
		t.Fatalf("bad result: %s", out)
	}
	if _, err := NewWebSearch("").Run(context.Background(), json.RawMessage(`{"query":"Go"}`)); err == nil {
		t.Fatal("missing key accepted")
	}
	// Cross-origin redirects never carry the search API credential.
	client := publicWebClient()
	calls := 0
	client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := webResponse(r, "ok")
		if calls == 1 {
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://other.example/")
		} else if r.Header.Get("X-Subscription-Token") != "" {
			t.Fatal("key leaked on redirect")
		}
		return resp, nil
	})
	if _, _, err := webGet(context.Background(), client, "https://example.com/", "secret"); err != nil {
		t.Fatal(err)
	}
}

func TestWebFetchPublicIntegration(t *testing.T) {
	if os.Getenv("CODY_WEB_TEST") != "1" {
		t.Skip("set CODY_WEB_TEST=1 for a public HTTPS request")
	}
	out, err := NewWebFetch().Run(context.Background(), json.RawMessage(`{"url":"https://go.dev/doc/"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Documentation") || !strings.Contains(out, "https://go.dev/doc/") {
		t.Fatal("missing documentation and source URL")
	}
}

func TestWebFetchPaginatesForSmallModels(t *testing.T) {
	w := NewWebFetch()
	w.client.Transport = webRoundTrip(func(r *http.Request) (*http.Response, error) {
		resp := webResponse(r, strings.Repeat("λ", webOutputLimit)+"last page")
		resp.Header.Set("Content-Type", "text/plain")
		return resp, nil
	})
	out, err := w.Run(context.Background(), json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil || strings.Contains(out, "last page") || !strings.Contains(out, "offset 4000") {
		t.Fatalf("first excerpt: %v %s", err, out)
	}
	out, err = w.Run(context.Background(), json.RawMessage(`{"url":"https://example.com","offset":4000}`))
	if err != nil || !strings.Contains(out, "last page") || strings.Contains(out, "λ") {
		t.Fatalf("continued excerpt: %v %s", err, out)
	}
	for _, offset := range []int{-1, 99999} {
		data, _ := json.Marshal(map[string]any{"url": "https://example.com", "offset": offset})
		if _, err := w.Run(context.Background(), data); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
}
