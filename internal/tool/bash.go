package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultBashTimeout = 120 * time.Second
	maxBashTimeout     = 30 * time.Minute
	maxBashOutput      = 32 << 10
)

type Bash struct{ root string }

func NewBash(root string) *Bash { return &Bash{root: root} }

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Description() string {
	return "Run a shell command from the workspace root. Returns combined stdout/stderr and the exit code. Use for search, git, builds, and tests."
}

func (b *Bash) Mutating() bool { return true }

func (b *Bash) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"command":     property("string", "Shell command to run."),
		"timeout_sec": property("integer", "Timeout in seconds. Default 120."),
	}, "command")
}

type bashArgs struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

func (b *Bash) Summarize(args json.RawMessage) string {
	var a bashArgs
	_ = json.Unmarshal(args, &a)
	return a.Command
}

func (b *Bash) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("command is required")
	}
	timeout := defaultBashTimeout
	if a.TimeoutSec > 0 {
		timeout = time.Duration(a.TimeoutSec) * time.Second
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
	cmd.Dir = b.root
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	code := 0
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Sprintf("command timed out after %s\n%s", timeout, out.String()), nil
			}
			return "", ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return "", err
		}
	}
	body := out.String()
	if out.truncated {
		body += fmt.Sprintf("\n[output truncated at %d bytes]", maxBashOutput)
	}
	head := fmt.Sprintf("exit %d (%s)", code, elapsed.Round(time.Millisecond))
	if strings.TrimSpace(body) == "" {
		return head + "\n(no output)", nil
	}
	return head + "\n" + strings.TrimRight(body, "\n"), nil
}

type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() >= maxBashOutput {
		c.truncated = true
		return len(p), nil
	}
	remaining := maxBashOutput - c.buf.Len()
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
