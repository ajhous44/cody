package ui

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEditor(t *testing.T, input io.Reader, hist string) (*editor, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return newEditor(newKeyReader(input), &out, func() int { return 20 }, hist), &out
}

func TestEditorKeys(t *testing.T) {
	for _, tc := range []struct{ name, keys, prefill, want string }{
		{"typing", "hello\r", "", "hello"},
		{"insert mid-line", "helo\x1b[Dl\r", "", "hello"},
		{"home and end", "ello\x01h\x05!\r", "", "hello!"},
		{"backspace", "abc\x7f\r", "", "ab"},
		{"delete key", "abc\x01\x1b[3~\r", "", "bc"},
		{"kill word", "one two\x17\r", "", "one "},
		{"kill to start", "abc\x15xyz\r", "", "xyz"},
		{"kill to end", "abcdef\x01\x1b[C\x1b[C\x0b\r", "", "ab"},
		{"word jump", "one two three\x1bb\x1bbX\r", "", "one Xtwo three"},
		{"unicode", "héllo→\r", "", "héllo→"},
		{"bracketed paste keeps newlines", "\x1b[200~line one\r\nline two\x1b[201~\r", "", "line one\nline two"},
		{"long input wraps", strings.Repeat("x", 55) + "\x01y\r", "", "y" + strings.Repeat("x", 55)},
		{"prefill from a fork", "!\r", "draft", "draft!"},
		{"ctrl+c clears a line", "abc\x03def\r", "", "def"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEditor(t, strings.NewReader(tc.keys), "")
			got, err := e.readLine("> ", tc.prefill)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestEditorQuitKeys(t *testing.T) {
	e, _ := testEditor(t, strings.NewReader("\x03"), "")
	if _, err := e.readLine("> ", ""); !errors.Is(err, ErrInterrupt) {
		t.Fatalf("ctrl+c on an empty line: %v", err)
	}
	e, _ = testEditor(t, strings.NewReader("\x04"), "")
	if _, err := e.readLine("> ", ""); !errors.Is(err, io.EOF) {
		t.Fatalf("ctrl+d on an empty line: %v", err)
	}
}

func TestEditorHistoryPersists(t *testing.T) {
	hist := filepath.Join(t.TempDir(), "history")
	e, _ := testEditor(t, strings.NewReader("first\rsecond\nline\r\x1b[A\x1b[A\x1b[B\r"), hist)
	// up, up, down from the bottom of [first second line] lands on "line".
	for _, want := range []string{"first", "second", "line", "line"} {
		if got, err := e.readLine("> ", ""); err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	// A new editor (next run) loads the same history, newest last.
	e, _ = testEditor(t, strings.NewReader("\x1b[A\x1b[A\x1b[A\r"), hist)
	if got, _ := e.readLine("> ", ""); got != "first" {
		t.Fatalf("history not loaded from disk: %q", got)
	}
}

func TestPicker(t *testing.T) {
	items := []string{"Oct 28  fix the login bug", "Oct 27  add dark mode", "Oct 26  release notes"}
	for _, tc := range []struct {
		keys string
		want int
	}{
		{"\r", 0},
		{"\x1b[B\x1b[B\r", 2},
		{"\x1b[B\x1b[B\x1b[B\x1b[A\r", 1},
		{"dark\r", 1},
		{"nothing matches\r", -1},
		{"\x03", -1},
	} {
		e, _ := testEditor(t, strings.NewReader(tc.keys), "")
		if got, err := e.pick("resume", items); err != nil || got != tc.want {
			t.Errorf("keys %q: got %d, %v; want %d", tc.keys, got, err, tc.want)
		}
	}
	// A lone Esc (no sequence follows within the wait) cancels.
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("\x1b"))
		time.Sleep(4 * escWait)
		w.Write([]byte("\r"))
	}()
	e, _ := testEditor(t, r, "")
	if got, _ := e.pick("resume", items); got != -1 {
		t.Fatalf("esc did not cancel: %d", got)
	}
}

// Approval answers and the editor share one reader, so typed-ahead input is
// never lost between them.
func TestKeyReaderSharesInput(t *testing.T) {
	k := newKeyReader(strings.NewReader("y\nnext prompt\r"))
	if line, _ := k.readLine(); line != "y" {
		t.Fatalf("line %q", line)
	}
	e := newEditor(k, io.Discard, func() int { return 80 }, "")
	if got, _ := e.readLine("> ", ""); got != "next prompt" {
		t.Fatalf("editor lost input: %q", got)
	}
}
