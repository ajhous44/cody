package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	defaultReadLimit = 400
	maxReadLimit     = 2000
	maxLineWidth     = 500
)

type Read struct{ root string }

func NewRead(root string) *Read { return &Read{root: root} }

func (r *Read) Name() string { return "read" }

func (r *Read) Description() string {
	return "Read a text file. Returns line-numbered content. Use offset and limit for large files."
}

func (r *Read) Mutating() bool { return false }

func (r *Read) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"path":   property("string", "File path relative to the workspace root."),
		"offset": property("integer", "First line to return (1-based). Default 1."),
		"limit":  property("integer", "Maximum number of lines to return. Default 400, max 2000."),
	}, "path")
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (r *Read) Summarize(args json.RawMessage) string {
	var a readArgs
	_ = json.Unmarshal(args, &a)
	return a.Path
}

func (r *Read) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	abs, err := Resolve(r.root, a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory (use bash: ls %s)", a.Path, a.Path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file", a.Path)
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return "(empty file)", nil
	}
	offset := a.Offset
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return fmt.Sprintf("(file has %d lines; offset %d is past the end)", len(lines), offset), nil
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > maxLineWidth {
			line = line[:maxLineWidth] + "... [line truncated]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "-- %d more lines; use offset/limit --\n", len(lines)-end)
	}
	return b.String(), nil
}
