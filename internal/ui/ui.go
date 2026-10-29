package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/term"
	"github.com/ajhous44/cody/v3/internal/tool"
)

type UI struct {
	keys         *keyReader
	ed           *editor // set by EnableTerminal: the inline TUI is active
	inFd         uintptr
	out          io.Writer
	errOut       io.Writer
	color        bool
	autoAll      bool
	showThinking bool
	inReasoning  bool
	midLine      bool
	mdPending    string
	mdBold       bool
	spinMu       sync.Mutex
	spinStop     chan struct{}
	spinDone     chan struct{}

	// Live turn status, shown on the spinner line and summarized at EndTurn.
	statMu     sync.Mutex
	turnStart  time.Time
	ctxLimit   func() int
	turnIn     int
	turnOut    int
	turnCache  int
	lastPrompt int
}

func New(in io.Reader, out, errOut io.Writer) *UI {
	return &UI{keys: newKeyReader(in), out: out, errOut: errOut, color: isTerminal(out)}
}

// EnableTerminal switches to the inline TUI (line editor with history,
// session picker, live status) when in and out are both terminals, and
// reports whether it did. Otherwise plain line input stays in place. There is
// no alternate screen: the terminal's own scrollback keeps the transcript.
func (u *UI) EnableTerminal(in, out *os.File, histPath string) bool {
	if !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return false
	}
	term.EnableVT(out.Fd())
	u.inFd = in.Fd()
	outFd := out.Fd()
	u.ed = newEditor(u.keys, u.out, func() int { return term.Width(outFd) }, histPath)
	return true
}

func (u *UI) Terminal() bool { return u.ed != nil }

// ReadLine reads the next prompt. prefill starts the input (a forked
// message); without a terminal it is printed so it can be retyped.
func (u *UI) ReadLine(prompt, prefill string) (string, error) {
	if u.ed == nil {
		if prefill != "" {
			u.Printf("%s\n", u.Dim("message to edit and resend: "+prefill))
		}
		return u.Ask(prompt)
	}
	u.StopThinking()
	u.breakLine()
	restore, err := term.MakeRaw(u.inFd)
	if err != nil {
		return u.Ask(prompt)
	}
	defer restore()
	line, err := u.ed.readLine(prompt, prefill)
	return strings.TrimSpace(line), err
}

// Pick shows a filterable list on a terminal and returns the chosen index,
// or -1 when cancelled or when there is no terminal.
func (u *UI) Pick(title string, items []string) (int, error) {
	if u.ed == nil || len(items) == 0 {
		return -1, nil
	}
	u.StopThinking()
	u.breakLine()
	restore, err := term.MakeRaw(u.inFd)
	if err != nil {
		return -1, err
	}
	defer restore()
	return u.ed.pick(title, items)
}

// BeginTurn starts the live status (elapsed time, context use, output
// tokens). limit reports the context window; it can change during the turn.
func (u *UI) BeginTurn(limit func() int) {
	u.statMu.Lock()
	u.turnStart, u.ctxLimit = time.Now(), limit
	u.turnIn, u.turnOut, u.turnCache, u.lastPrompt = 0, 0, 0, 0
	u.statMu.Unlock()
}

// EndTurn prints the turn's one-line summary.
func (u *UI) EndTurn() {
	u.statMu.Lock()
	start, in, out, cache, last, limit := u.turnStart, u.turnIn, u.turnOut, u.turnCache, u.lastPrompt, u.ctxLimit
	u.turnStart = time.Time{}
	u.statMu.Unlock()
	if start.IsZero() {
		return
	}
	parts := []string{fmt.Sprintf("%.1fs", time.Since(start).Seconds())}
	if in > 0 || out > 0 {
		p := "in " + shortCount(in)
		if cache > 0 {
			p += " (cache " + shortCount(cache) + ")"
		}
		parts = append(parts, p, "out "+shortCount(out))
	}
	if l := callLimit(limit); last > 0 && l > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d%% of %s", last*100/l, shortCount(l)))
	}
	u.breakLine()
	u.Printf("%s\n", u.paint(dim, "  "+strings.Join(parts, " · ")))
}

// status is the spinner line's live suffix during a turn.
func (u *UI) status() string {
	u.statMu.Lock()
	start, out, last, limit := u.turnStart, u.turnOut, u.lastPrompt, u.ctxLimit
	u.statMu.Unlock()
	if start.IsZero() {
		return ""
	}
	parts := []string{fmt.Sprintf("%.1fs", time.Since(start).Seconds())}
	if l := callLimit(limit); last > 0 && l > 0 {
		parts = append(parts, "ctx "+shortCount(last)+"/"+shortCount(l))
	}
	if out > 0 {
		parts = append(parts, "out "+shortCount(out))
	}
	return " · " + strings.Join(parts, " · ")
}

func callLimit(f func() int) int {
	if f == nil {
		return 0
	}
	return f()
}

func shortCount(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func isTerminal(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func (u *UI) paint(code, s string) string {
	if !u.color {
		return s
	}
	return code + s + reset
}

func (u *UI) Dim(s string) string  { return u.paint(dim, s) }
func (u *UI) Bold(s string) string { return u.paint(bold, s) }

func (u *UI) Printf(format string, args ...any) { fmt.Fprintf(u.out, format, args...) }
func (u *UI) Errorf(format string, args ...any) { fmt.Fprintf(u.errOut, format, args...) }

// Banner is one dim line: the transcript starts on the next screen row.
func (u *UI) Banner(version, providerName, model, root, sessionID string) {
	u.Printf("%s\n\n", u.Dim(fmt.Sprintf("cody %s  %s/%s  %s  session %s  /help", version, providerName, model, root, sessionID)))
}

func (u *UI) SetThinkingVisible(on bool) { u.showThinking = on }

func (u *UI) ThinkingVisible() bool { return u.showThinking }

// reasoning streams a model's reasoning trace when enabled. When disabled the
// spinner keeps running and the trace is ignored.
func (u *UI) reasoning(s string) {
	if !u.showThinking {
		return
	}
	u.StopThinking()
	if !u.inReasoning {
		u.breakLine()
		fmt.Fprint(u.out, u.paint(dim, "thinking: "))
		u.inReasoning = true
	}
	if s != "" {
		fmt.Fprint(u.out, u.paint(dim, s))
		u.midLine = true
	}
}

func (u *UI) Event(e agent.Event) {
	if e.Kind == agent.KindThinking {
		if e.Text != "" {
			u.StopThinking()
			u.breakLine()
			u.Printf("%s\n", u.Dim(e.Text))
		}
		u.StartThinking()
		return
	}
	if e.Kind == agent.KindReasoning {
		u.reasoning(e.Text)
		return
	}
	u.StopThinking()
	if e.Kind == agent.KindText {
		if u.inReasoning {
			u.breakLine()
			u.inReasoning = false
		}
		u.midLine = true
		u.writeText(e.Text)
		return
	}
	u.Flush()
	switch e.Kind {
	case agent.KindToolStart:
		u.breakLine()
		if e.Tool == "plan" {
			return // the approval prompt already showed the steps
		}
		u.Printf("%s %s  %s\n", u.paint(cyan, "run"), u.paint(bold, e.Tool), u.describe(e.Tool, e.Args, e.Summary))
	case agent.KindToolEnd:
		u.breakLine()
		line := fmt.Sprintf("%s (%dms)", e.Tool, e.Ms)
		if strings.HasPrefix(e.Result, "error:") || strings.HasPrefix(e.Result, "user denied") {
			line = u.paint(red, line)
		} else {
			line = u.paint(dim, line)
		}
		u.Printf("  %s\n", line)
		if e.Tool != "read" {
			if out := strings.TrimRight(e.Result, "\n"); out != "" {
				u.Printf("%s\n", u.paint(dim, indent(truncateLines(out, 12), "  ")))
			}
		}
	case agent.KindUsage:
		u.statMu.Lock()
		inTurn := !u.turnStart.IsZero()
		if inTurn && e.Usage != nil {
			u.turnIn += e.Usage.PromptTokens
			u.turnOut += e.Usage.CompletionTokens
			u.lastPrompt = e.Usage.PromptTokens
			if d := e.Usage.PromptDetails; d != nil {
				u.turnCache += d.CachedTokens
			}
		}
		u.statMu.Unlock()
		// During a turn, usage is summarized once at EndTurn.
		if e.Usage != nil && !inTurn {
			text := fmt.Sprintf("  tokens in/out %d/%d", e.Usage.PromptTokens, e.Usage.CompletionTokens)
			if d := e.Usage.PromptDetails; d != nil {
				text += fmt.Sprintf(" · cache read/write %d/%d", d.CachedTokens, d.CacheWriteTokens)
			}
			u.Printf("%s\n", u.paint(dim, text))
		}
	case agent.KindNotice:
		u.breakLine()
		u.Printf("%s\n", u.paint(dim, e.Text))
	}
}

// writeText renders streamed model text as inline markdown. Bold spans (**)
// become ANSI bold on terminals and are stripped everywhere else. Chunks may
// split a marker, so a trailing "*" is held back until the next chunk or Flush.
func (u *UI) writeText(s string) {
	u.mdPending += s
	for {
		idx := strings.Index(u.mdPending, "**")
		if idx < 0 {
			keep := 0
			if strings.HasSuffix(u.mdPending, "*") {
				keep = 1
			}
			u.emitText(u.mdPending[:len(u.mdPending)-keep])
			u.mdPending = u.mdPending[len(u.mdPending)-keep:]
			return
		}
		u.emitText(u.mdPending[:idx])
		u.mdPending = u.mdPending[idx+2:]
		u.mdBold = !u.mdBold
		if u.color {
			if u.mdBold {
				fmt.Fprint(u.out, bold)
			} else {
				fmt.Fprint(u.out, reset)
			}
		}
	}
}

func (u *UI) emitText(s string) {
	if s != "" {
		fmt.Fprint(u.out, s)
	}
}

// Flush writes any text held back by writeText and closes an open bold span.
func (u *UI) Flush() {
	if u.mdBold {
		if u.color {
			fmt.Fprint(u.out, reset)
		}
		u.mdBold = false
	}
	if u.mdPending != "" {
		fmt.Fprint(u.out, u.mdPending)
		u.mdPending = ""
	}
}

func (u *UI) AutoApproved() bool { return u.autoAll }

// thinkingFaces is a 16-frame loop: center, glance left, blink left, recenter,
// glance right, blink right, then a center double blink. Every face is eight
// characters wide so the spinner never jitters.
var thinkingFaces = []string{
	"[ ●  ● ]",
	"[ ●  ● ]",
	"[●  ●  ]",
	"[●  ●  ]",
	"[●  ●  ]",
	"[-  -  ]",
	"[ ●  ● ]",
	"[ ●  ● ]",
	"[  ●  ●]",
	"[  ●  ●]",
	"[  ●  ●]",
	"[  -  -]",
	"[ ●  ● ]",
	"[ -  - ]",
	"[ ●  ● ]",
	"[ -  - ]",
}

var thinkingDots = []string{"", ".", "..", "..."}

// StartThinking animates a one-line spinner until StopThinking is called.
// It is a no-op when output is not an interactive terminal.
func (u *UI) StartThinking() {
	if !u.color {
		return
	}
	u.spinMu.Lock()
	if u.spinStop != nil {
		u.spinMu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	u.spinStop, u.spinDone = stop, done
	u.spinMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				fmt.Fprint(u.out, "\r\x1b[K")
				return
			case <-ticker.C:
				face := thinkingFaces[i%len(thinkingFaces)]
				dots := thinkingDots[(i/2)%len(thinkingDots)]
				fmt.Fprintf(u.out, "\r\x1b[K%s %s", u.paint(cyan, face), u.paint(dim, "thinking"+dots+u.status()))
			}
		}
	}()
}

// StopThinking clears the spinner line and waits for the animation to end.
// It is safe to call when no spinner is running.
func (u *UI) StopThinking() {
	u.spinMu.Lock()
	stop, done := u.spinStop, u.spinDone
	u.spinStop, u.spinDone = nil, nil
	u.spinMu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func (u *UI) Approve(ctx context.Context, t tool.Tool, args string) (bool, error) {
	u.StopThinking()
	if u.autoAll {
		return true, nil
	}
	u.breakLine()
	summary := strings.TrimSpace(t.Summarize(json.RawMessage(args)))
	if summary == "" {
		summary = args
	}
	u.Printf("%s %s  %s\n", u.paint(yellow, "approve"), u.paint(bold, t.Name()), u.describe(t.Name(), args, summary))
	fmt.Fprint(u.out, "  [y]es  [n]o  [a]ll: ")
	line, err := u.readLine()
	if err != nil {
		// EOF or a broken read is treated as a denial.
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "a", "all":
		u.autoAll = true
		return true, nil
	default:
		return false, nil
	}
}

func (u *UI) Ask(prompt string) (string, error) {
	u.StopThinking()
	u.breakLine()
	fmt.Fprint(u.out, u.paint(bold, prompt))
	line, err := u.readLine()
	return strings.TrimSpace(line), err
}

func (u *UI) readLine() (string, error) { return u.keys.readLine() }

func (u *UI) breakLine() {
	if u.midLine {
		fmt.Fprintln(u.out)
		u.midLine = false
	}
}

// describe renders tool arguments in their natural syntax: shell commands as
// commands, edits as a one-line old-to-new diff, and anything unsummarized as
// plain text.
func (u *UI) describe(tool, args, summary string) string {
	switch tool {
	case "plan":
		return "\n" + indent(summary, "    ")
	case "bash":
		return u.paint(dim, "$") + " " + truncate(normalizeSpace(summary), 160)
	case "edit":
		var a struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal([]byte(args), &a) == nil && a.Path != "" {
			old := truncate(normalizeSpace(a.OldString), 60)
			next := truncate(normalizeSpace(a.NewString), 60)
			return fmt.Sprintf("%s  %s %s %s", a.Path, u.paint(red, old), u.paint(dim, "→"), u.paint(green, next))
		}
	}
	return truncate(normalizeSpace(summary), 160)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "..."
}

func truncateLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	kept := append([]string{}, lines[:max]...)
	kept = append(kept, fmt.Sprintf("... (%d more lines)", len(lines)-max))
	return strings.Join(kept, "\n")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
