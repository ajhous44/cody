package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ajhous44/cody/internal/provider"
	"github.com/ajhous44/cody/internal/tool"
)

const (
	DefaultMaxTurns      = 40
	DefaultContextTokens = 32000
)

const exploreNudge = "You did not use any tools. Before asking the user, inspect the workspace with ls/find/grep and read the relevant files. Then answer or make the change."

const toolSyntaxNudge = "Your last reply wrote a tool call as plain text. Tool calls must go through the tool-calling interface, not into the message text. Retry the same step as a proper tool call."

var toolSyntaxRe = regexp.MustCompile(`::[a-z_]+:[a-z_]+\{|<\|?tool_call\|?>|<\|?tool_response\|?>`)

// leakedToolSyntax reports whether a reply contains raw model chat-template
// tool markers instead of a structured tool call.
func leakedToolSyntax(s string) bool {
	return toolSyntaxRe.MatchString(s)
}

var smallTalkWords = map[string]bool{
	"hi": true, "hii": true, "hello": true, "hey": true, "yo": true, "sup": true,
	"thanks": true, "thank": true, "you": true, "ty": true, "ok": true, "okay": true,
	"cool": true, "nice": true, "great": true, "bye": true, "goodbye": true,
	"good": true, "morning": true, "evening": true, "afternoon": true, "there": true,
}

// asksUser reports whether a tool-less reply is asking the user for information
// the workspace could answer. Greetings and plain chat never qualify, and only
// the first qualifying reply in a turn is nudged.
func asksUser(user, reply string) bool {
	if smallTalk(user) || !strings.Contains(reply, "?") {
		return false
	}
	lower := strings.ToLower(reply)
	for _, p := range []string{
		"which file", "what file", "file path", "file name", "html file", "css file",
		"referring to", "refer to", "more context", "more information", "more details",
		"provide the", "specify the", "which one", "which part", "what exactly",
		"point me to", "which page", "what page", "clarify",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func smallTalk(s string) bool {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(words) == 0 || len(words) > 4 {
		return false
	}
	for _, w := range words {
		if !smallTalkWords[w] {
			return false
		}
	}
	return true
}

const (
	KindThinking  = "thinking"
	KindReasoning = "reasoning"
	KindText      = "text"
	KindToolStart = "tool_start"
	KindToolEnd   = "tool_end"
	KindUsage     = "usage"
	KindNotice    = "notice"
)

type Event struct {
	Kind    string
	Text    string
	Tool    string
	Args    string
	Summary string
	Result  string
	Ms      int64
	Usage   *provider.Usage
}

type Handler func(Event)

type Approver interface {
	Approve(ctx context.Context, t tool.Tool, args string) (bool, error)
}

type ApproverFunc func(ctx context.Context, t tool.Tool, args string) (bool, error)

func (f ApproverFunc) Approve(ctx context.Context, t tool.Tool, args string) (bool, error) {
	return f(ctx, t, args)
}

type Options struct {
	Provider      provider.Provider
	Model         string
	System        string
	Tools         *tool.Registry
	Approver      Approver
	MaxTurns      int
	ContextTokens int
}

type Agent struct {
	opts    Options
	history []provider.Message
}

func New(opts Options) *Agent {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	if opts.ContextTokens <= 0 {
		opts.ContextTokens = DefaultContextTokens
	}
	return &Agent{opts: opts}
}

func (a *Agent) Model() string { return a.opts.Model }

func (a *Agent) SetModel(model string) { a.opts.Model = model }

func (a *Agent) History() []provider.Message { return a.history }

func (a *Agent) SetHistory(msgs []provider.Message) { a.history = msgs }

func (a *Agent) Append(msg provider.Message) { a.history = append(a.history, msg) }

// Prompt runs one user turn to completion and returns every message appended
// to the history during the turn, including the user message.
// messages returns the system prompt followed by the conversation history,
// trimmed to the context budget. The system prompt is never stored in the
// history, so it stays out of session files and resume replays.
func (a *Agent) messages() []provider.Message {
	if a.opts.System == "" {
		return TrimContext(a.history, a.opts.ContextTokens)
	}
	msgs := make([]provider.Message, 0, len(a.history)+1)
	msgs = append(msgs, provider.Message{Role: "system", Content: a.opts.System})
	msgs = append(msgs, a.history...)
	return TrimContext(msgs, a.opts.ContextTokens)
}

func (a *Agent) Prompt(ctx context.Context, input string, h Handler) ([]provider.Message, error) {
	if h == nil {
		h = func(Event) {}
	}
	before := len(a.history)
	a.history = append(a.history, provider.Message{Role: "user", Content: input})
	toolUsed := false
	nudge := ""
	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		msgs := a.messages()
		if nudge != "" && !toolUsed {
			msgs = append(msgs, provider.Message{Role: "user", Content: nudge})
		}
		h(Event{Kind: KindThinking})
		stream, err := a.opts.Provider.Stream(ctx, provider.Request{
			Model:    a.opts.Model,
			Messages: msgs,
			Tools:    a.opts.Tools.Specs(),
		})
		if err != nil {
			return a.history[before:], err
		}
		msg, usage, err := consume(stream, h)
		if err != nil {
			return a.history[before:], err
		}
		if usage != nil {
			h(Event{Kind: KindUsage, Usage: usage})
		}
		if msg == nil {
			// Some models end the turn with an empty message after running
			// tools. Treat that as done instead of an error.
			if toolUsed {
				return a.history[before:], nil
			}
			return a.history[before:], errors.New("model returned an empty response")
		}
		if len(msg.ToolCalls) == 0 {
			if !toolUsed && nudge == "" {
				switch {
				case leakedToolSyntax(msg.Content):
					nudge = toolSyntaxNudge
					h(Event{Kind: KindNotice, Text: "model wrote a tool call as text; retrying with a tool-use reminder"})
					continue
				case asksUser(input, msg.Content):
					nudge = exploreNudge
					h(Event{Kind: KindNotice, Text: "model asked without inspecting the workspace; retrying with an explore-first reminder"})
					continue
				}
			}
			a.history = append(a.history, *msg)
			return a.history[before:], nil
		}
		a.history = append(a.history, *msg)
		toolUsed = true
		for _, call := range msg.ToolCalls {
			result := a.runTool(ctx, call, h)
			a.history = append(a.history, provider.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    result,
			})
		}
	}
	return a.history[before:], fmt.Errorf("stopped after %d tool turns", a.opts.MaxTurns)
}

func consume(stream provider.Stream, h Handler) (*provider.Message, *provider.Usage, error) {
	defer stream.Close()
	var content strings.Builder
	calls := map[int]*provider.ToolCall{}
	var order []int
	var usage *provider.Usage
	for {
		d, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, usage, err
		}
		if d.Content != "" {
			content.WriteString(d.Content)
			h(Event{Kind: KindText, Text: d.Content})
		}
		if d.Reasoning != "" {
			h(Event{Kind: KindReasoning, Text: d.Reasoning})
		}
		for _, tc := range d.ToolCalls {
			call, ok := calls[tc.Index]
			if !ok {
				call = &provider.ToolCall{Type: "function"}
				calls[tc.Index] = call
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Name != "" {
				call.Function.Name = tc.Name
			}
			call.Function.Arguments += tc.Args
		}
		if d.Usage != nil {
			usage = d.Usage
		}
	}
	msg := &provider.Message{Role: "assistant", Content: content.String()}
	for _, i := range order {
		call := calls[i]
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", i)
		}
		msg.ToolCalls = append(msg.ToolCalls, *call)
	}
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		return nil, usage, nil
	}
	return msg, usage, nil
}

func (a *Agent) runTool(ctx context.Context, call provider.ToolCall, h Handler) string {
	t, ok := a.opts.Tools.Get(call.Function.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", call.Function.Name)
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	args := strings.TrimSpace(call.Function.Arguments)
	if args == "" {
		args = "{}"
	}
	summary := strings.TrimSpace(t.Summarize(json.RawMessage(args)))
	if summary == "" {
		summary = args
	}
	start := time.Now()
	if t.Mutating() && a.opts.Approver != nil {
		ok, err := a.opts.Approver.Approve(ctx, t, args)
		if err != nil {
			return "error: " + err.Error()
		}
		if !ok {
			h(Event{Kind: KindToolEnd, Tool: t.Name(), Result: "denied by user", Ms: time.Since(start).Milliseconds()})
			return "user denied this action"
		}
	}
	h(Event{Kind: KindToolStart, Tool: t.Name(), Args: args, Summary: summary})
	out, err := t.Run(ctx, json.RawMessage(args))
	if err != nil {
		if ctx.Err() != nil {
			out = "cancelled"
		} else {
			out = "error: " + err.Error()
		}
	}
	h(Event{Kind: KindToolEnd, Tool: t.Name(), Result: out, Ms: time.Since(start).Milliseconds()})
	return out
}

const elidedMarker = "[tool output elided to fit the context window]"

// TrimContext returns a copy of msgs that fits the token budget, eliding the
// oldest tool outputs first and dropping old messages after that.
func TrimContext(msgs []provider.Message, budgetTokens int) []provider.Message {
	if budgetTokens <= 0 || estimate(msgs) <= budgetTokens {
		return msgs
	}
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	total := estimate(out)
	for i := range out {
		if i == 0 || out[i].Role != "tool" || out[i].Content == elidedMarker {
			continue
		}
		total += (len(elidedMarker) - len(out[i].Content)) / 4
		out[i].Content = elidedMarker
		if total <= budgetTokens {
			return out
		}
	}
	// Tool call arguments carry whole file bodies for write and edit. Eliding
	// them keeps the turn structure and the outcome text in context.
	for i := range out {
		if i == 0 || len(out[i].ToolCalls) == 0 {
			continue
		}
		needs := false
		for j := range out[i].ToolCalls {
			args := out[i].ToolCalls[j].Function.Arguments
			if args != "" && args != "{}" {
				needs = true
				break
			}
		}
		if !needs {
			continue
		}
		calls := make([]provider.ToolCall, len(out[i].ToolCalls))
		copy(calls, out[i].ToolCalls)
		out[i].ToolCalls = calls
		for j := range calls {
			args := calls[j].Function.Arguments
			if args == "" || args == "{}" {
				continue
			}
			total += (len("{}") - len(args)) / 4
			calls[j].Function.Arguments = "{}"
		}
		if total <= budgetTokens {
			return out
		}
	}
	for len(out) > 1 && total > budgetTokens {
		total -= estimateMessage(out[1])
		out = append(out[:1], out[2:]...)
	}
	for len(out) > 1 && (out[1].Role == "tool" || out[1].Role == "assistant") {
		total -= estimateMessage(out[1])
		out = append(out[:1], out[2:]...)
	}
	return out
}

func estimate(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessage(m)
	}
	return total
}

func estimateMessage(m provider.Message) int {
	n := len(m.Content)
	for _, c := range m.ToolCalls {
		n += len(c.Function.Name) + len(c.Function.Arguments)
	}
	return 4 + n/4
}
