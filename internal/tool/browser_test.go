package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestBrowserRejectsInvalidActionsWithoutLaunching(t *testing.T) {
	b := NewBrowser()
	defer b.Close()
	for _, args := range []string{`{"action":"open","url":"file:///etc/passwd"}`, `{"action":"open","url":"javascript:alert(1)"}`, `{"action":"open","url":"https://user:pass@example.com"}`, `{"action":"click","ref":"x"}`, `{"action":"press","ref":"x","key":"Meta"}`, `{"action":"evaluate"}`} {
		if _, err := b.Run(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("accepted %s", args)
		}
		if b.ctx != nil {
			t.Fatal("launched browser for invalid request")
		}
	}
}
func TestBrowserLocalIntegration(t *testing.T) {
	if os.Getenv("CODY_BROWSER_TEST") != "1" {
		t.Skip("set CODY_BROWSER_TEST=1 to exercise an installed browser in a temporary headless profile")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Browser fixture</h1><a href="#features">Explore Features</a><a href="#about">About</a><section id="features">Features section</section><section id="about">About section</section><label>Name<input id="name" aria-label="Name"></label><button onclick="document.querySelector('h1').innerText='Hello '+document.querySelector('#name').value">Greet</button><input type="password" aria-label="Password"><div id="event"></div><script>document.querySelector('#name').addEventListener('input',()=>document.querySelector('#event').innerText='Input received')</script>`)
	}))
	defer ts.Close()
	b := NewBrowser()
	b.options = []chromedp.ExecAllocatorOption{chromedp.Headless}
	defer b.Close()
	run := func(a browserArgs) string {
		t.Helper()
		data, _ := json.Marshal(a)
		out, err := b.Run(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ref := func(out, label string) string {
		t.Helper()
		m := regexp.MustCompile(`\[([^]]+)\] [^\n]*"` + label + `"`).FindStringSubmatch(out)
		if len(m) < 2 {
			t.Fatalf("no %s ref in %s", label, out)
		}
		return m[1]
	}
	out := run(browserArgs{Action: "open", URL: ts.URL})
	if !strings.Contains(out, "h1: Browser fixture") {
		t.Fatal(out)
	}
	old := ref(out, "Greet")
	malformed, _ := json.Marshal(browserArgs{Action: "click", Ref: "[" + old + "] button \"Greet\""})
	if _, err := b.Run(context.Background(), malformed); err == nil || !strings.Contains(err.Error(), `ref "`+old+`"`) {
		t.Fatalf("missing exact ID correction: %v", err)
	}
	summaryArgs, _ := json.Marshal(browserArgs{Action: "click", Ref: old, URL: "https://unrelated.example"})
	if summary := b.Summarize(summaryArgs); !strings.Contains(summary, "Greet") || !strings.Contains(summary, ts.URL) || strings.Contains(summary, "unrelated.example") {
		t.Fatalf("approval lacks target context: %s", summary)
	}
	out = run(browserArgs{Action: "fill", Ref: ref(out, "Name"), Text: "Ada"})
	if !strings.Contains(out, "Input received") {
		t.Fatalf("fill did not notify the app: %s", out)
	}
	stale, _ := json.Marshal(browserArgs{Action: "click", Ref: old})
	if _, err := b.Run(context.Background(), stale); err == nil {
		t.Fatal("accepted stale ref")
	}
	out = run(browserArgs{Action: "fill", Ref: ref(out, "Name"), Text: "Grace"})
	out = run(browserArgs{Action: "fill", Ref: ref(out, "Name"), Text: ""})
	out = run(browserArgs{Action: "fill", Ref: ref(out, "Name"), Text: "Ada"})
	out = run(browserArgs{Action: "click", Ref: ref(out, "Greet")})
	if !strings.Contains(out, "Hello Ada") {
		t.Fatal(out)
	}
	out = run(browserArgs{Action: "click", Ref: ref(out, "Explore Features")})
	if !strings.Contains(out, "URL: "+ts.URL+"/#features") {
		t.Fatalf("fragment navigation not observed: %s", out)
	}
	out = run(browserArgs{Action: "click", Ref: ref(out, "About")})
	if !strings.Contains(out, "URL: "+ts.URL+"/#about") {
		t.Fatalf("second fragment not observed: %s", out)
	}
	out = run(browserArgs{Action: "back"})
	if !strings.Contains(out, "URL: "+ts.URL+"/#features") {
		t.Fatalf("back navigation not observed: %s", out)
	}
	password, _ := json.Marshal(browserArgs{Action: "fill", Ref: ref(out, "Password"), Text: "not-a-real-password"})
	if _, err := b.Run(context.Background(), password); err == nil {
		t.Fatal("automated password input")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Run(cancelled, json.RawMessage(`{"action":"read"}`)); err == nil {
		t.Fatal("ignored cancellation")
	}
	run(browserArgs{Action: "close"})
	if b.ctx != nil {
		t.Fatal("browser still open")
	}
	out = run(browserArgs{Action: "open", URL: ts.URL})
	if !strings.Contains(out, "Browser fixture") {
		t.Fatal("could not reopen browser")
	}
}
