package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/tool"
)

type fakeTool struct{}

func (fakeTool) Name() string            { return "bash" }
func (fakeTool) Description() string     { return "" }
func (fakeTool) Schema() json.RawMessage { return nil }
func (fakeTool) Mutating() bool          { return true }
func (fakeTool) Summarize(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Command
}
func (fakeTool) Run(context.Context, json.RawMessage) (string, error) { return "", nil }

var _ tool.Tool = fakeTool{}

func TestToolStartRendersBashSummary(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Event(agent.Event{Kind: agent.KindToolStart, Tool: "bash", Args: `{"command":"ls -F"}`, Summary: "ls -F"})
	got := out.String()
	if !strings.Contains(got, "$ ls -F") {
		t.Fatalf("bash command not rendered:\n%s", got)
	}
	if strings.Contains(got, `{"command"`) {
		t.Fatalf("raw JSON leaked:\n%s", got)
	}
}

func TestToolStartRendersEditDiff(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Event(agent.Event{
		Kind:    agent.KindToolStart,
		Tool:    "edit",
		Args:    `{"path":"index.html","old_string":"#f0f8ff","new_string":"blue"}`,
		Summary: "index.html",
	})
	got := out.String()
	if !strings.Contains(got, "#f0f8ff → blue") {
		t.Fatalf("edit diff not rendered:\n%s", got)
	}
	if strings.Contains(got, `{"path"`) {
		t.Fatalf("raw JSON leaked:\n%s", got)
	}
}

func TestApproveRendersSummary(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader("n\n"), &out, &errOut)
	ok, err := u.Approve(context.Background(), fakeTool{}, `{"command":"rm -rf build"}`)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if ok {
		t.Fatal("denial expected")
	}
	got := out.String()
	if !strings.Contains(got, "approve bash  $ rm -rf build") {
		t.Fatalf("approval summary missing:\n%s", got)
	}
	if strings.Contains(got, `{"command"`) {
		t.Fatalf("raw JSON leaked:\n%s", got)
	}
}

func TestLoadingStatus(t *testing.T) {
	var out bytes.Buffer
	u := New(strings.NewReader(""), &out, &out)
	u.Event(agent.Event{Kind: agent.KindThinking, Text: "Loading model… local"})
	u.Event(agent.Event{Kind: agent.KindThinking})
	if got := out.String(); got != "Loading model… local\n" {
		t.Fatalf("loading status = %q", got)
	}
}

func TestThinkingSpinner(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.StartThinking()
	if out.Len() != 0 {
		t.Fatal("spinner should be a no-op when not a terminal")
	}

	u.color = true
	u.StartThinking()
	time.Sleep(150 * time.Millisecond)
	u.StopThinking()
	got := out.String()
	if !strings.Contains(got, "[ ●  ● ]") {
		t.Fatalf("spinner face missing: %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Fatalf("spinner frames missing: %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[K") {
		t.Fatalf("spinner line not cleared: %q", got)
	}
	u.StopThinking()
}

func TestThinkingFacesHaveEqualWidth(t *testing.T) {
	width := len([]rune(thinkingFaces[0]))
	for _, f := range thinkingFaces {
		if got := len([]rune(f)); got != width {
			t.Fatalf("face %q has width %d, want %d", f, got, width)
		}
	}
}

func textEvent(s string) agent.Event {
	return agent.Event{Kind: agent.KindText, Text: s}
}

func TestEventStripsBoldMarkersWhenNotTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Event(textEvent("**bold** plain"))
	u.Flush()
	if got := out.String(); got != "bold plain" {
		t.Fatalf("got %q, want %q", got, "bold plain")
	}
}

func TestEventRendersBoldWhenTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.color = true
	u.Event(textEvent("**bold** plain"))
	u.Flush()
	want := "\x1b[1mbold\x1b[0m plain"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEventBoldMarkersSplitAcrossChunks(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.color = true
	u.Event(textEvent("a *"))
	u.Event(textEvent("*b*"))
	u.Event(textEvent("* c"))
	u.Flush()
	want := "a \x1b[1mb\x1b[0m c"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFlushEmitsHeldBackMarker(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Event(textEvent("* item"))
	u.Event(textEvent(" *"))
	if got := out.String(); got != "* item " {
		t.Fatalf("held-back text leaked before flush: %q", got)
	}
	u.Flush()
	if got := out.String(); got != "* item *" {
		t.Fatalf("got %q, want %q", got, "* item *")
	}
}

func TestFlushClosesOpenBold(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.color = true
	u.Event(textEvent("**open"))
	u.Flush()
	if got := out.String(); got != "\x1b[1mopen\x1b[0m" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningHiddenByDefault(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Event(agent.Event{Kind: agent.KindReasoning, Text: "secret"})
	u.Event(agent.Event{Kind: agent.KindText, Text: "answer"})
	u.Flush()
	if got := out.String(); got != "answer" {
		t.Fatalf("got %q, want %q", got, "answer")
	}
}

func TestReasoningShownWhenEnabled(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.SetThinkingVisible(true)
	if !u.ThinkingVisible() {
		t.Fatal("ThinkingVisible = false after enable")
	}
	u.Event(agent.Event{Kind: agent.KindReasoning, Text: "hmm"})
	u.Event(agent.Event{Kind: agent.KindReasoning, Text: " more"})
	u.Event(agent.Event{Kind: agent.KindText, Text: "answer"})
	u.Flush()
	got := out.String()
	if !strings.Contains(got, "thinking: hmm more") {
		t.Fatalf("reasoning not rendered:\n%s", got)
	}
	if strings.Index(got, "answer") < strings.Index(got, "thinking:") {
		t.Fatalf("answer must follow reasoning:\n%s", got)
	}
}

func TestBannerCompactWhenNotTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.Banner("0.1.0", "ollama", "gemma4:e4b", "/tmp/project", "20260101-000000-abcd")
	got := out.String()
	if !strings.Contains(got, "cody 0.1.0  ollama/gemma4:e4b") {
		t.Fatalf("compact banner missing model line:\n%s", got)
	}
	if !strings.Contains(got, "/tmp/project") || !strings.Contains(got, "session 20260101-000000-abcd") {
		t.Fatalf("compact banner missing workspace or session:\n%s", got)
	}
	if strings.Contains(got, "▄") {
		t.Fatalf("art should not print when output is not a terminal:\n%s", got)
	}
}

func TestBannerIsOneDimLineWhenColor(t *testing.T) {
	var out, errOut bytes.Buffer
	u := New(strings.NewReader(""), &out, &errOut)
	u.color = true
	u.Banner("0.1.0", "ollama", "gemma4:e4b", "/tmp/project", "20260101-000000-abcd")
	got := out.String()
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 || !strings.Contains(got, dim+"cody 0.1.0  ollama/gemma4:e4b") {
		t.Fatalf("banner should be one dim line:\n%q", got)
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	if got := truncate("ab→cd", 3); got != "ab..." {
		t.Fatalf("truncate split a rune: %q", got)
	}
}
