package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ajhous44/cody/v3/internal/prompt"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

const (
	DefaultMaxTurns      = 40
	DefaultContextTokens = 32000
)

const exploreNudge = "You did not use any tools. Before asking the user, inspect the workspace with ls/find/grep and read the relevant files. Then answer or make the change."

const toolSyntaxNudge = "Your last reply wrote a tool call as plain text. Tool calls must go through the tool-calling interface, not into the message text. Retry the same step as a proper tool call."

var toolSyntaxRe = regexp.MustCompile(`(?s)<execute_tool>\s*[a-z_]+\s*\{|::[a-z_]+:[a-z_]+\{|<\|?tool_call\|?>|<\|?tool_response\|?>`)

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
	Root            string
	CacheKey        string
	MaxOutputTokens int
	Provider        provider.Provider
	Model           string
	System          string
	Tools           *tool.Registry
	Approver        Approver
	MaxTurns        int
	ContextTokens   int
	Record          func(provider.Message) error // Persist before executing the next action.
	Debug           bool                         // Show raw provider errors and every retry.
}

type Agent struct {
	mu      sync.Mutex
	opts    Options
	history []provider.Message
	// estTotal is the running estimateMessage sum over history. History only
	// grows by append (or is replaced wholesale), so each turn reuses the
	// total instead of re-scanning every message.
	estTotal            int
	baseSystem          string
	instructions        map[string]string
	pendingInstructions map[string]string
	summary             string
	summaryEnd          int
	compactedTokens     int
}

func New(opts Options) *Agent {
	if opts.CacheKey == "" {
		opts.CacheKey = rand.Text()
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	if opts.ContextTokens <= 0 {
		opts.ContextTokens = DefaultContextTokens
	}
	return &Agent{opts: opts, baseSystem: opts.System, instructions: map[string]string{}, pendingInstructions: map[string]string{}}
}

func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Model
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.opts.Model = model
	a.mu.Unlock()
}

// History returns a copy so callers can render it while a turn appends.
func (a *Agent) History() []provider.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]provider.Message(nil), a.history...)
}

func (a *Agent) SetHistory(msgs []provider.Message) {
	a.mu.Lock()
	a.history = append([]provider.Message(nil), msgs...)
	a.summary, a.summaryEnd, a.compactedTokens = "", 0, 0
	a.instructions, a.pendingInstructions = map[string]string{}, map[string]string{}
	a.estTotal = estimate(a.history)
	a.mu.Unlock()
}

func (a *Agent) Append(msg provider.Message) {
	a.mu.Lock()
	a.appendLocked(msg)
	a.mu.Unlock()
}

func (a *Agent) appendHistory(msg provider.Message) error {
	if a.opts.Record != nil {
		if err := a.opts.Record(msg); err != nil {
			return fmt.Errorf("record session: %w", err)
		}
	}
	a.mu.Lock()
	a.appendLocked(msg)
	a.mu.Unlock()
	return nil
}

// appendLocked records one message and its estimate. Callers must hold a.mu.
func (a *Agent) appendLocked(msg provider.Message) {
	a.history = append(a.history, msg)
	a.estTotal += estimateMessage(msg)
}

func (a *Agent) addedSince(n int) []provider.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n > len(a.history) {
		n = len(a.history)
	}
	return append([]provider.Message(nil), a.history[n:]...)
}

// providerErr adds the raw provider detail to err in debug mode.
func (a *Agent) providerErr(err error) error {
	if a.opts.Debug {
		return provider.Debug(err)
	}
	return err
}

// Prompt runs one user turn and returns the messages added to its journal.
func (a *Agent) Prompt(ctx context.Context, input string, h Handler) ([]provider.Message, error) {
	if h == nil {
		h = func(Event) {}
	}
	a.mu.Lock()
	if a.opts.Root != "" {
		a.opts.System = a.baseSystem + prompt.ProjectInstructions(a.opts.Root)
	}
	before := len(a.history)
	a.mu.Unlock()
	if err := a.appendHistory(provider.Message{Role: "user", Content: input}); err != nil {
		return nil, err
	}
	toolUsed := false
	nudge := ""
	nudged := false
	failedEdit := ""
	failedCall := ""
	// A reread permits one retry, but cannot keep an unchanged edit loop alive.
	failedEdits := map[string]int{}
	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		// Instructions from tool results become actionable only on the next model request.
		for key, text := range a.pendingInstructions {
			a.instructions[key] = text
		}
		clear(a.pendingInstructions)
		msgs, outputTokens, err := a.prepare(ctx, h)
		if err != nil {
			return a.addedSince(before), err
		}
		if nudge != "" {
			msgs = append(msgs, provider.Message{Role: "user", Content: nudge})
			nudge = ""
		}
		h(Event{Kind: KindThinking})
		retried := false
		stream, err := a.opts.Provider.Stream(ctx, provider.Request{
			Model:           a.Model(),
			Messages:        msgs,
			Tools:           a.opts.Tools.Specs(),
			CacheKey:        a.opts.CacheKey,
			MaxOutputTokens: outputTokens,
			// One notice per request unless debugging; the final error says
			// what happened if every retry fails.
			OnRetry: func(delay time.Duration) {
				if a.opts.Debug {
					h(Event{Kind: KindNotice, Text: fmt.Sprintf("provider busy; retrying in %s", delay.Round(time.Millisecond))})
				} else if !retried {
					retried = true
					h(Event{Kind: KindNotice, Text: "provider busy, retrying…"})
				}
			},
		})
		if err != nil {
			return a.addedSince(before), a.providerErr(err)
		}
		msg, usage, err := consume(stream, h)
		if usage != nil {
			h(Event{Kind: KindUsage, Usage: usage})
		}
		if err != nil {
			return a.addedSince(before), a.providerErr(err)
		}
		if msg == nil {
			// Some models end the turn with an empty message after running
			// tools. Treat that as done instead of an error.
			if toolUsed {
				return a.addedSince(before), nil
			}
			return a.addedSince(before), errors.New("model returned an empty response")
		}
		if len(msg.ToolCalls) == 0 {
			if leakedToolSyntax(msg.Content) {
				if nudged {
					return a.addedSince(before), errors.New("model repeatedly wrote tool commands as text; no command from that text was executed")
				}
				nudged, nudge = true, toolSyntaxNudge
				h(Event{Kind: KindNotice, Text: "model wrote a tool call as text; retrying with a tool-use reminder"})
				continue
			}
			if !toolUsed && !nudged && asksUser(input, msg.Content) {
				nudged, nudge = true, exploreNudge
				h(Event{Kind: KindNotice, Text: "model asked without inspecting the workspace; retrying with an explore-first reminder"})
				continue
			}
			err := a.appendHistory(*msg)
			return a.addedSince(before), err
		}
		if err := a.appendHistory(*msg); err != nil {
			return a.addedSince(before), err
		}
		toolUsed = true
		var loopErr error
		for _, call := range msg.ToolCalls {
			key, target := callKeys(call)
			if loopErr == nil && target != "" && (key == failedEdit || failedEdits[target] >= 2) {
				loopErr = errors.New("stopped repeated edit loop; re-read the file or change the edit before retrying")
			}
			if loopErr == nil && key == failedCall {
				loopErr = fmt.Errorf("stopped repeated failed %s call; change the arguments or inspect the error before retrying", call.Function.Name)
			}
			var result string
			if loopErr != nil {
				// Finish journaling the batch without executing or approving more calls.
				result = "error: not executed: " + loopErr.Error()
			} else {
				result = a.runTool(ctx, call, h)
				failedCall = ""
				if strings.HasPrefix(result, "error:") || (call.Function.Name == "bash" && strings.HasPrefix(result, "exit ") && !strings.HasPrefix(result, "exit 0 ")) {
					failedCall = key
				}
				failedEdit = ""
				if target != "" && strings.HasPrefix(result, "error:") {
					failedEdit = key
					failedEdits[target]++
				} else if !strings.HasPrefix(result, "error:") && !strings.HasPrefix(result, "user denied") &&
					(call.Function.Name == "edit" || call.Function.Name == "write" ||
						(call.Function.Name == "bash" && strings.HasPrefix(result, "exit 0 "))) {
					clear(failedEdits)
				}
			}
			if err := a.appendHistory(provider.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    result,
			}); err != nil {
				return a.addedSince(before), err
			}
		}
		if loopErr != nil {
			return a.addedSince(before), loopErr
		}
	}
	return a.addedSince(before), fmt.Errorf("stopped after %d tool turns", a.opts.MaxTurns)
}

// Canonical arguments catch repeated calls despite JSON whitespace/key order.
// Edits also track the same path/old text across read/edit cycles.
func callKeys(call provider.ToolCall) (string, string) {
	args := strings.TrimSpace(call.Function.Arguments)
	if args == "" {
		args = "{}"
	}
	var decoded any
	if json.Unmarshal([]byte(args), &decoded) == nil {
		if fields, ok := decoded.(map[string]any); ok && call.Function.Name == "read" {
			if path, err := tool.ReadPath(json.RawMessage(args)); err == nil {
				fields["path"] = path
				delete(fields, "filePath")
			}
		}
		canonical, _ := json.Marshal(decoded)
		args = string(canonical)
	}
	key := call.Function.Name + "\x00" + args
	if call.Function.Name != "edit" {
		return key, ""
	}
	if fields, ok := decoded.(map[string]any); ok {
		target, _ := json.Marshal([]any{fields["path"], fields["old_string"]})
		return key, string(target)
	}
	return key, key
}

func consume(stream provider.Stream, h Handler) (*provider.Message, *provider.Usage, error) {
	defer stream.Close()
	var content strings.Builder
	byIndex := map[int]*provider.ToolCall{}
	var calls []*provider.ToolCall
	var usage *provider.Usage
	var finish string
	for {
		d, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, usage, err
		}
		if d.FinishReason != "" {
			finish = d.FinishReason
		}
		if d.Content != "" {
			content.WriteString(d.Content)
			h(Event{Kind: KindText, Text: d.Content})
		}
		if d.Reasoning != "" {
			h(Event{Kind: KindReasoning, Text: d.Reasoning})
		}
		for _, tc := range d.ToolCalls {
			call, ok := byIndex[tc.Index]
			// Some servers omit index (so it reads 0) and send each parallel
			// call whole; a different id means a new call, not more arguments.
			if !ok || (tc.ID != "" && call.ID != "" && tc.ID != call.ID) {
				call = &provider.ToolCall{Type: "function"}
				byIndex[tc.Index] = call
				calls = append(calls, call)
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
	if finish != "" && finish != "stop" && finish != "tool_calls" && finish != "function_call" {
		return nil, usage, fmt.Errorf("model response incomplete (finish reason: %s); retry or request a smaller change", finish)
	}
	msg := &provider.Message{Role: "assistant", Content: content.String()}
	for i, call := range calls {
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
	var instructions string
	if a.opts.Root != "" && (t.Name() == "read" || t.Name() == "write" || t.Name() == "edit") {
		var pathArgs struct {
			Path string `json:"path"`
		}
		pathErr := json.Unmarshal([]byte(args), &pathArgs)
		if t.Name() == "read" {
			pathArgs.Path, pathErr = tool.ReadPath(json.RawMessage(args))
		}
		if pathErr == nil {
			instructions = prompt.ScopedInstructions(a.opts.Root, pathArgs.Path)
			key := filepath.Dir(pathArgs.Path)
			if a.instructions[key] == instructions {
				instructions = ""
			} else {
				a.pendingInstructions[key] = instructions
				if instructions == "" {
					instructions = "Previously loaded directory instructions for " + key + " were removed."
				} else {
					instructions = "Current directory instructions (replace earlier instructions for this path):\n" + instructions
				}
			}
			if instructions != "" && t.Mutating() {
				return instructions + "\nAction not executed. Follow these instructions and call the tool again."
			}
		}
	}
	summary := strings.TrimSpace(t.Summarize(json.RawMessage(args)))
	if summary == "" {
		summary = args
	}
	if t.Mutating() && a.opts.Approver != nil {
		ok, err := a.opts.Approver.Approve(ctx, t, args)
		if err != nil {
			return "error: " + err.Error()
		}
		if !ok {
			h(Event{Kind: KindToolStart, Tool: t.Name(), Args: args, Summary: summary})
			h(Event{Kind: KindToolEnd, Tool: t.Name(), Result: "user denied this action"})
			return "user denied this action"
		}
	}
	start := time.Now()
	h(Event{Kind: KindToolStart, Tool: t.Name(), Args: args, Summary: summary})
	out, err := t.Run(ctx, json.RawMessage(args))
	if err != nil {
		if ctx.Err() != nil {
			out = "cancelled"
		} else {
			out = "error: " + err.Error()
		}
	}
	if instructions != "" {
		out += "\n" + instructions
	}
	h(Event{Kind: KindToolEnd, Tool: t.Name(), Result: out, Ms: time.Since(start).Milliseconds()})
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
