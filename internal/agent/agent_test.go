package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

type fakeStream struct {
	deltas []provider.Delta
	i      int
}

func (f *fakeStream) Recv() (provider.Delta, error) {
	if f.i >= len(f.deltas) {
		return provider.Delta{}, io.EOF
	}
	d := f.deltas[f.i]
	f.i++
	return d, nil
}

func (f *fakeStream) Close() error { return nil }

type fakeProvider struct {
	script [][]provider.Delta
	reqs   []provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Stream(ctx context.Context, req provider.Request) (provider.Stream, error) {
	f.reqs = append(f.reqs, req)
	if len(f.script) == 0 {
		return nil, errors.New("no scripted response")
	}
	deltas := f.script[0]
	f.script = f.script[1:]
	return &fakeStream{deltas: deltas}, nil
}

type echoTool struct {
	mutating bool
	run      func(args string) (string, error)
}

func (e *echoTool) Name() string                     { return "echo" }
func (e *echoTool) Description() string              { return "echo arguments" }
func (e *echoTool) Schema() json.RawMessage          { return json.RawMessage(`{"type":"object"}`) }
func (e *echoTool) Mutating() bool                   { return e.mutating }
func (e *echoTool) Summarize(json.RawMessage) string { return "echo summary" }
func (e *echoTool) Run(ctx context.Context, a json.RawMessage) (string, error) {
	if e.run != nil {
		return e.run(string(a))
	}
	return string(a), nil
}

func newTestAgent(p provider.Provider, t tool.Tool, approve Approver) *Agent {
	return New(Options{
		Provider: p,
		Model:    "test",
		System:   "system",
		Tools:    tool.NewRegistry(t),
		Approver: approve,
	})
}

func TestPromptRunsToolLoop(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{
			{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: `{"text":"hi"`}}},
			{ToolCalls: []provider.ToolCallDelta{{Index: 0, Args: `}`}}},
			{FinishReason: "tool_calls"},
		},
		{
			{Content: "done"},
			{Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 2}},
		},
	}}
	a := newTestAgent(p, &echoTool{}, nil)

	var text strings.Builder
	thinking := 0
	added, err := a.Prompt(context.Background(), "go", func(e Event) {
		switch e.Kind {
		case KindText:
			text.WriteString(e.Text)
		case KindThinking:
			thinking++
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if thinking != 2 {
		t.Fatalf("thinking events = %d, want 2", thinking)
	}
	if text.String() != "done" {
		t.Fatalf("text = %q, want done", text.String())
	}
	if len(added) != 4 {
		t.Fatalf("added %d messages, want 4", len(added))
	}
	if added[1].Role != "assistant" || len(added[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool call not recorded: %+v", added[1])
	}
	if got := added[1].ToolCalls[0].Function.Arguments; got != `{"text":"hi"}` {
		t.Fatalf("arguments = %q", got)
	}
	if added[2].Role != "tool" || added[2].Content != `{"text":"hi"}` {
		t.Fatalf("tool result = %+v", added[2])
	}
	if len(p.reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(p.reqs))
	}
	last := p.reqs[1].Messages
	if last[len(last)-1].Role != "tool" {
		t.Fatalf("second request missing tool message: %+v", last)
	}
}

func TestToolStartEventCarriesSummary(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "done"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	var summary string
	if _, err := a.Prompt(context.Background(), "go", func(e Event) {
		if e.Kind == KindToolStart {
			summary = e.Summary
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if summary != "echo summary" {
		t.Fatalf("summary = %q, want %q", summary, "echo summary")
	}
}

func TestPromptSendsSystemMessage(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{{{Content: "done"}}}}
	a := newTestAgent(p, &echoTool{}, nil)
	if _, err := a.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	msgs := p.reqs[0].Messages
	if len(msgs) == 0 || msgs[0].Role != "system" || msgs[0].Content != "system" {
		t.Fatalf("first request message = %+v, want system prompt", msgs)
	}
	if got := a.History(); len(got) != 2 || got[0].Role != "user" {
		t.Fatalf("system prompt leaked into history: %+v", got)
	}
}

func TestPromptNudgesQuestionWithoutTools(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{Content: "Which file should I change?"}},
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "done"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	notices := 0
	added, err := a.Prompt(context.Background(), "change the background", func(e Event) {
		if e.Kind == KindNotice {
			notices++
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if notices != 1 {
		t.Fatalf("notices = %d, want 1", notices)
	}
	if len(p.reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(p.reqs))
	}
	retry := p.reqs[1].Messages
	last := retry[len(retry)-1]
	if last.Role != "user" || last.Content != exploreNudge {
		t.Fatalf("retry missing nudge: %+v", last)
	}
	for _, m := range added {
		if m.Role == "assistant" && strings.Contains(m.Content, "Which file") {
			t.Fatal("discarded question leaked into history")
		}
	}
	if added[len(added)-1].Content != "done" {
		t.Fatalf("final message = %q", added[len(added)-1].Content)
	}
}

func TestPromptNudgesLeakedToolSyntax(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{Content: `::search:search{query:"spinner"}<tool_call|><|tool_response>`}},
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "done"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	notices := 0
	added, err := a.Prompt(context.Background(), "find the spinner", func(e Event) {
		if e.Kind == KindNotice {
			notices++
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if notices != 1 {
		t.Fatalf("notices = %d, want 1", notices)
	}
	if len(p.reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(p.reqs))
	}
	retry := p.reqs[1].Messages
	if last := retry[len(retry)-1]; last.Content != toolSyntaxNudge {
		t.Fatalf("retry nudge = %q", last.Content)
	}
	for _, m := range added {
		if leakedToolSyntax(m.Content) {
			t.Fatalf("leaked tool syntax kept in history: %q", m.Content)
		}
	}
	if added[len(added)-1].Content != "done" {
		t.Fatalf("final = %q", added[len(added)-1].Content)
	}
}

func TestPromptNoNudgeAfterToolUse(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "Should I also update the header?"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	notices := 0
	if _, err := a.Prompt(context.Background(), "go", func(e Event) {
		if e.Kind == KindNotice {
			notices++
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if notices != 0 || len(p.reqs) != 2 {
		t.Fatalf("nudged after tool use: notices=%d requests=%d", notices, len(p.reqs))
	}
}

func TestPromptNoNudgeOnGreeting(t *testing.T) {
	for _, reply := range []string{
		"Hi. What can I code for you?",
		"Hello! Where would you like to start?",
		"Hey, what's up?",
	} {
		p := &fakeProvider{script: [][]provider.Delta{{{Content: reply}}}}
		a := newTestAgent(p, &echoTool{}, nil)
		notices := 0
		if _, err := a.Prompt(context.Background(), "hi", func(e Event) {
			if e.Kind == KindNotice {
				notices++
			}
		}); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		if notices != 0 || len(p.reqs) != 1 {
			t.Fatalf("greeting %q nudged: notices=%d requests=%d", reply, notices, len(p.reqs))
		}
	}
}

func TestPromptNoNudgeOnStatement(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{{{Content: "done"}}}}
	a := newTestAgent(p, &echoTool{}, nil)
	if _, err := a.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(p.reqs))
	}
}

func TestPromptDeniedApproval(t *testing.T) {
	called := false
	p := &fakeProvider{script: [][]provider.Delta{
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "ok"}},
	}}
	a := newTestAgent(p, &echoTool{mutating: true, run: func(string) (string, error) {
		called = true
		return "ran", nil
	}}, ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) {
		return false, nil
	}))
	var events []Event
	added, err := a.Prompt(context.Background(), "go", func(e Event) {
		if e.Kind == KindToolStart || e.Kind == KindToolEnd {
			events = append(events, e)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if called {
		t.Fatal("tool ran despite denial")
	}
	if added[2].Content != "user denied this action" {
		t.Fatalf("tool result = %q", added[2].Content)
	}
	if len(events) != 2 || events[0].Kind != KindToolStart || events[1].Kind != KindToolEnd || events[1].Result != added[2].Content || events[1].Ms != 0 {
		t.Fatalf("denial must have its own tool card and match the journal: %+v", events)
	}
}

func TestPromptToolError(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{Content: "recovered"}},
	}}
	a := newTestAgent(p, &echoTool{run: func(string) (string, error) {
		return "", errors.New("boom")
	}}, nil)
	added, err := a.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.HasPrefix(added[2].Content, "error: boom") {
		t.Fatalf("tool result = %q", added[2].Content)
	}
}

func TestPromptEmptyResponseFails(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{{{FinishReason: "stop"}}}}
	a := newTestAgent(p, &echoTool{}, nil)
	if _, err := a.Prompt(context.Background(), "go", nil); err == nil {
		t.Fatal("expected empty response error")
	}
}

func TestPromptEmptyAfterToolSucceeds(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "c1", Name: "echo", Args: "{}"}}}},
		{{FinishReason: "stop"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	added, err := a.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("added %d messages, want 3", len(added))
	}
}

func TestReasoningEventsStayOutOfHistory(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{Reasoning: "step one"}, {Content: "done"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	var reasoning strings.Builder
	added, err := a.Prompt(context.Background(), "go", func(e Event) {
		if e.Kind == KindReasoning {
			reasoning.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if reasoning.String() != "step one" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if len(added) != 2 || added[1].Content != "done" {
		t.Fatalf("history = %+v", added)
	}
}

func TestConsumeSplitsUnindexedParallelCalls(t *testing.T) {
	msg, _, err := consume(&fakeStream{deltas: []provider.Delta{
		{ToolCalls: []provider.ToolCallDelta{
			{ID: "a", Name: "read", Args: `{"path":"a"}`},
			{ID: "b", Name: "read", Args: `{"path":"b"}`},
		}},
		{ToolCalls: []provider.ToolCallDelta{{Index: 1, ID: "c", Name: "read", Args: `{"path":`}}},
		{ToolCalls: []provider.ToolCallDelta{{Index: 1, Args: `"c"}`}}},
	}}, func(Event) {})
	if err != nil || msg == nil || len(msg.ToolCalls) != 3 {
		t.Fatalf("calls = %+v, err = %v", msg, err)
	}
	for i, want := range []string{`{"path":"a"}`, `{"path":"b"}`, `{"path":"c"}`} {
		if got := msg.ToolCalls[i].Function.Arguments; got != want {
			t.Fatalf("call %d args = %s, want %s", i, got, want)
		}
	}
}
