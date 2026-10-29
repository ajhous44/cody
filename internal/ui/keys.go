package ui

import (
	"io"
	"strings"
	"time"
)

// keyReader is the single reader of user input. The line editor, the session
// picker, and the y/n prompts all take bytes from it, so none can strand
// input in another's buffer. Reads happen only when a byte is wanted: nothing
// waits on the terminal while a turn runs.
type keyReader struct {
	want    chan struct{}
	got     chan chunk
	buf     []byte
	err     error
	pending bool
}

type chunk struct {
	b   []byte
	err error
}

func newKeyReader(r io.Reader) *keyReader {
	k := &keyReader{want: make(chan struct{}), got: make(chan chunk)}
	go func() {
		for range k.want {
			b := make([]byte, 4096)
			n, err := r.Read(b)
			k.got <- chunk{b[:n], err}
		}
	}()
	return k
}

// fill waits up to timeout (forever when 0) for more input. A read left
// waiting by a timeout stays outstanding and its bytes arrive in order later.
func (k *keyReader) fill(timeout time.Duration) bool {
	if k.err != nil {
		return false
	}
	if !k.pending {
		k.want <- struct{}{}
		k.pending = true
	}
	var timer <-chan time.Time
	if timeout > 0 {
		timer = time.After(timeout)
	}
	select {
	case c := <-k.got:
		k.pending = false
		k.buf = append(k.buf, c.b...)
		if c.err != nil {
			k.err = c.err
		}
		return len(c.b) > 0
	case <-timer:
		return false
	}
}

func (k *keyReader) readByte() (byte, error) {
	for len(k.buf) == 0 {
		if !k.fill(0) && len(k.buf) == 0 && k.err != nil {
			return 0, k.err
		}
	}
	b := k.buf[0]
	k.buf = k.buf[1:]
	return b, nil
}

// next returns the following byte if it arrives within d: escape sequences
// arrive together, a lone Esc key does not.
func (k *keyReader) next(d time.Duration) (byte, bool) {
	if len(k.buf) == 0 && !k.fill(d) {
		return 0, false
	}
	if len(k.buf) == 0 {
		return 0, false
	}
	b := k.buf[0]
	k.buf = k.buf[1:]
	return b, true
}

// readLine reads a cooked-mode line (the terminal already handled editing).
func (k *keyReader) readLine() (string, error) {
	var sb strings.Builder
	for {
		b, err := k.readByte()
		if err != nil {
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err
		}
		if b == '\n' {
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
}
