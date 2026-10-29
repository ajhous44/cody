package ui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInterrupt is returned when Ctrl+C is pressed on an empty prompt.
var ErrInterrupt = errors.New("interrupted")

const (
	escWait     = 30 * time.Millisecond
	maxHistory  = 500
	pickerRows  = 10
	pasteStart  = "200~"
	pasteFinish = "\x1b[201~"
	loneEsc     = "\x1b"
)

// editor is a small line editor for raw terminals: cursor movement, word and
// line kills, persistent history, and bracketed paste (a multi-line paste is
// one prompt). It redraws across wrapped rows, so long input stays editable.
type editor struct {
	keys     *keyReader
	out      io.Writer
	width    func() int
	history  []string
	histPath string
	row      int // terminal row of the cursor within the input, for redraws
}

func newEditor(keys *keyReader, out io.Writer, width func() int, histPath string) *editor {
	e := &editor{keys: keys, out: out, width: width, histPath: histPath}
	if f, err := os.Open(histPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var line string
			if json.Unmarshal(sc.Bytes(), &line) == nil && line != "" {
				e.history = append(e.history, line)
			}
		}
		f.Close()
	}
	if len(e.history) > maxHistory {
		e.history = e.history[len(e.history)-maxHistory:]
	}
	return e
}

func (e *editor) remember(line string) {
	if strings.TrimSpace(line) == "" || (len(e.history) > 0 && e.history[len(e.history)-1] == line) {
		return
	}
	e.history = append(e.history, line)
	if e.histPath == "" {
		return
	}
	// One JSON string per line keeps multi-line prompts intact.
	if f, err := os.OpenFile(e.histPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(line)
		f.Write(append(b, '\n'))
		f.Close()
	}
}

func (e *editor) cols() int {
	if w := e.width(); w > 10 {
		return w
	}
	return 80
}

// readLine edits one prompt. prefill starts the buffer (a forked message).
func (e *editor) readLine(prompt, prefill string) (string, error) {
	buf := []rune(prefill)
	pos := len(buf)
	hist, stash := len(e.history), ""
	e.row = 0
	fmt.Fprint(e.out, "\x1b[?2004h")
	defer fmt.Fprint(e.out, "\x1b[?2004l")
	e.render(prompt, buf, pos)
	for {
		b, err := e.keys.readByte()
		if err != nil {
			e.finish(prompt, buf)
			return "", err
		}
		switch b {
		case '\r', '\n':
			e.finish(prompt, buf)
			e.remember(string(buf))
			return string(buf), nil
		case 3: // Ctrl+C clears the line; on an empty line it quits
			if len(buf) > 0 {
				buf, pos = nil, 0
				break
			}
			e.finish(prompt, buf)
			return "", ErrInterrupt
		case 4: // Ctrl+D quits on an empty line, else deletes forward
			if len(buf) == 0 {
				e.finish(prompt, buf)
				return "", io.EOF
			}
			if pos < len(buf) {
				buf = append(buf[:pos], buf[pos+1:]...)
			}
		case 1:
			pos = 0
		case 5:
			pos = len(buf)
		case 2:
			pos = max(pos-1, 0)
		case 6:
			pos = min(pos+1, len(buf))
		case 0x7f, 8:
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
			}
		case 0x15: // Ctrl+U
			buf, pos = append([]rune(nil), buf[pos:]...), 0
		case 0x0b: // Ctrl+K
			buf = buf[:pos]
		case 0x17: // Ctrl+W
			start := wordStart(buf, pos)
			buf, pos = append(buf[:start], buf[pos:]...), start
		case 0x0c: // Ctrl+L
			fmt.Fprint(e.out, "\x1b[H\x1b[2J")
			e.row = 0
		case 0x1b:
			switch seq := e.escape(); seq {
			case "A":
				if hist > 0 {
					if hist == len(e.history) {
						stash = string(buf)
					}
					hist--
					buf = []rune(e.history[hist])
					pos = len(buf)
				}
			case "B":
				if hist < len(e.history) {
					hist++
					line := stash
					if hist < len(e.history) {
						line = e.history[hist]
					}
					buf = []rune(line)
					pos = len(buf)
				}
			case "C":
				pos = min(pos+1, len(buf))
			case "D":
				pos = max(pos-1, 0)
			case "H", "1~", "7~":
				pos = 0
			case "F", "4~", "8~":
				pos = len(buf)
			case "3~":
				if pos < len(buf) {
					buf = append(buf[:pos], buf[pos+1:]...)
				}
			case "b", "1;3D", "1;5D":
				pos = wordStart(buf, pos)
			case "f", "1;3C", "1;5C":
				pos = wordEnd(buf, pos)
			case pasteStart:
				text := []rune(e.paste())
				buf = append(buf[:pos], append(text, buf[pos:]...)...)
				pos += len(text)
			}
		default:
			if r := e.rune(b); r >= 0x20 && r != utf8.RuneError {
				buf = append(buf[:pos], append([]rune{r}, buf[pos:]...)...)
				pos++
			}
		}
		e.render(prompt, buf, pos)
	}
}

// escape reads the rest of a key sequence after Esc: "A" for ESC [ A, "3~"
// for ESC [ 3 ~, "b" for Alt+b, loneEsc for the Esc key itself.
func (e *editor) escape() string {
	b, ok := e.keys.next(escWait)
	if !ok {
		return loneEsc
	}
	if b != '[' && b != 'O' {
		return string(b) // Alt+key
	}
	var seq []byte
	for {
		c, ok := e.keys.next(escWait)
		if !ok {
			return ""
		}
		seq = append(seq, c)
		if c >= 0x40 && c <= 0x7e {
			return string(seq)
		}
	}
}

// paste collects bracketed-paste text up to the end marker.
func (e *editor) paste() string {
	var raw []byte
	for !strings.HasSuffix(string(raw), pasteFinish) {
		b, err := e.keys.readByte()
		if err != nil {
			break
		}
		raw = append(raw, b)
	}
	text := strings.TrimSuffix(string(raw), pasteFinish)
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// rune completes a UTF-8 sequence that starts with b.
func (e *editor) rune(b byte) rune {
	if b < utf8.RuneSelf {
		return rune(b)
	}
	p := []byte{b}
	for !utf8.FullRune(p) && len(p) < utf8.UTFMax {
		c, err := e.keys.readByte()
		if err != nil {
			break
		}
		p = append(p, c)
	}
	r, _ := utf8.DecodeRune(p)
	return r
}

// render redraws prompt and buffer from the input's first row. Newlines show
// as ↵ so the input keeps a predictable shape across wrapped rows.
func (e *editor) render(prompt string, buf []rune, pos int) {
	cols := e.cols()
	shown := strings.ReplaceAll(string(buf), "\n", "↵")
	var sb strings.Builder
	if e.row > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", e.row)
	}
	sb.WriteString("\r\x1b[J" + prompt + shown)
	end := len([]rune(prompt)) + len(buf)
	if end > 0 && end%cols == 0 {
		sb.WriteString("\r\n") // leave the pending-wrap column
	}
	at := len([]rune(prompt)) + pos
	if up := end/cols - at/cols; up > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", up)
	}
	sb.WriteString("\r")
	if col := at % cols; col > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", col)
	}
	e.row = at / cols
	fmt.Fprint(e.out, sb.String())
}

// finish moves below the input so the next output starts on a fresh line.
func (e *editor) finish(prompt string, buf []rune) {
	cols := e.cols()
	end := len([]rune(prompt)) + len(buf)
	if down := end/cols - e.row; down > 0 {
		fmt.Fprintf(e.out, "\x1b[%dB", down)
	}
	fmt.Fprint(e.out, "\r\n")
	e.row = 0
}

func wordStart(buf []rune, pos int) int {
	for pos > 0 && unicode.IsSpace(buf[pos-1]) {
		pos--
	}
	for pos > 0 && !unicode.IsSpace(buf[pos-1]) {
		pos--
	}
	return pos
}

func wordEnd(buf []rune, pos int) int {
	for pos < len(buf) && unicode.IsSpace(buf[pos]) {
		pos++
	}
	for pos < len(buf) && !unicode.IsSpace(buf[pos]) {
		pos++
	}
	return pos
}

// pick shows a filterable list and returns the chosen index into items, or -1
// on Esc or Ctrl+C. Typing filters; arrows move; Enter chooses.
func (e *editor) pick(title string, items []string) (int, error) {
	filter := ""
	sel, top, drawn := 0, 0, 0
	matches := func() []int {
		var out []int
		for i, it := range items {
			if strings.Contains(strings.ToLower(it), strings.ToLower(filter)) {
				out = append(out, i)
			}
		}
		return out
	}
	clear := func() {
		if drawn > 0 {
			fmt.Fprintf(e.out, "\x1b[%dA", drawn)
		}
		fmt.Fprint(e.out, "\r\x1b[J")
		drawn = 0
	}
	draw := func(m []int) {
		clear()
		cols := e.cols()
		lines := []string{title + "  (type to filter, ↑/↓, Enter to open, Esc to cancel)", "> " + filter}
		sel = min(max(sel, 0), max(len(m)-1, 0))
		if sel < top {
			top = sel
		} else if sel >= top+pickerRows {
			top = sel - pickerRows + 1
		}
		for i := top; i < len(m) && i < top+pickerRows; i++ {
			line := []rune(items[m[i]])
			if len(line) > cols-4 {
				line = append(line[:cols-5], '…')
			}
			if i == sel {
				lines = append(lines, "\x1b[7m› "+string(line)+"\x1b[0m")
			} else {
				lines = append(lines, "  "+string(line))
			}
		}
		if len(m) == 0 {
			lines = append(lines, "  (no matches)")
		}
		fmt.Fprint(e.out, strings.Join(lines, "\r\n"))
		drawn = len(lines) - 1
	}
	m := matches()
	draw(m)
	for {
		b, err := e.keys.readByte()
		if err != nil {
			clear()
			return -1, err
		}
		switch b {
		case '\r', '\n':
			clear()
			if len(m) == 0 {
				return -1, nil
			}
			return m[sel], nil
		case 3:
			clear()
			return -1, nil
		case 0x7f, 8:
			if f := []rune(filter); len(f) > 0 {
				filter, sel, top = string(f[:len(f)-1]), 0, 0
			}
		case 0x1b:
			switch e.escape() {
			case loneEsc:
				clear()
				return -1, nil
			case "A":
				sel--
			case "B":
				sel++
			}
		default:
			if r := e.rune(b); r >= 0x20 && r != utf8.RuneError {
				filter, sel, top = filter+string(r), 0, 0
			}
		}
		m = matches()
		draw(m)
	}
}
