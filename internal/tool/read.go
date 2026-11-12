package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	defaultReadLimit = 400
	maxReadLimit     = 2000
	maxLineWidth     = 500
	maxReadBytes     = 50 << 10
)

type Read struct{ root string }

func NewRead(root string) *Read { return &Read{root: root} }

func (r *Read) Name() string { return "read" }

func (r *Read) Description() string {
	return "Read a text file. Returns line-numbered content. Use offset and limit for large files."
}

func (r *Read) Mutating() bool { return false }

func (r *Read) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":     property("string", "File path relative to the workspace root."),
			"filePath": property("string", "Alias for path. Supply either field; if both are supplied they must match."),
			"offset":   property("integer", "First line to return (1-based). Default 1."),
			"limit":    property("integer", "Maximum number of lines to return. Default 400, max 2000."),
		},
		"anyOf": []map[string]any{{"required": []string{"path"}}, {"required": []string{"filePath"}}},
	})
	return b
}

type readArgs struct {
	Path     string `json:"path"`
	FilePath string `json:"filePath"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func decodeReadArgs(args json.RawMessage) (readArgs, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path != "" && a.FilePath != "" && a.Path != a.FilePath {
		return a, fmt.Errorf("path and filePath must match when both are supplied")
	}
	if a.Path == "" {
		a.Path = a.FilePath
	}
	if strings.TrimSpace(a.Path) == "" {
		return a, fmt.Errorf("read requires path or filePath")
	}
	return a, nil
}

// ReadPath also resolves the alias when loading scoped workspace instructions.
func ReadPath(args json.RawMessage) (string, error) {
	a, err := decodeReadArgs(args)
	return a.Path, err
}

func (r *Read) Summarize(args json.RawMessage) string {
	path, _ := ReadPath(args)
	return path
}

func (r *Read) Run(ctx context.Context, args json.RawMessage) (string, error) {
	a, err := decodeReadArgs(args)
	if err != nil {
		return "", err
	}
	dir, rel, err := OpenWorkspace(r.root, a.Path)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	info, err := dir.Stat(rel)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory (use bash: ls %s)", a.Path, a.Path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", a.Path)
	}
	f, err := dir.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Recheck the opened handle in case the path changed after Stat.
	if info, err = f.Stat(); err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", a.Path)
	}
	offset := max(a.Offset, 1)
	limit := a.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}
	limit = min(limit, maxReadLimit)
	reader := bufio.NewReaderSize(f, 16<<10)
	var out strings.Builder
	var line []byte
	number := 1
	truncated := false
	hasLine := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		part, err := reader.ReadSlice('\n')
		hasLine = hasLine || len(part) > 0
		if bytes.IndexByte(part, 0) >= 0 {
			return "", fmt.Errorf("%s looks like a binary file", a.Path)
		}
		if number >= offset {
			content := part
			if err != bufio.ErrBufferFull {
				content = bytes.TrimRight(content, "\r\n")
			}
			room := maxLineWidth - len(line)
			line = append(line, content[:min(len(content), room)]...)
			truncated = truncated || len(content) > room
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return "", err
		}
		if !hasLine && err == io.EOF {
			break
		}
		if number >= offset {
			text := strings.TrimRight(string(line), "\r\n")
			if truncated {
				text += "... [line truncated]"
			}
			entry := fmt.Sprintf("%6d\t%s\n", number, text)
			if out.Len()+len(entry) > maxReadBytes {
				fmt.Fprintf(&out, "-- output capped; continue with offset %d --\n", number)
				return out.String(), nil
			}
			out.WriteString(entry)
		}
		number++
		line = line[:0]
		truncated = false
		hasLine = false
		if err == io.EOF {
			break
		}
		if number-offset >= limit {
			if _, err := reader.Peek(1); err != io.EOF {
				fmt.Fprintf(&out, "-- more lines; use offset %d --\n", number)
			}
			return out.String(), nil
		}
	}
	if number == 1 {
		return "(empty file)", nil
	}
	if out.Len() == 0 {
		return fmt.Sprintf("(file has %d lines; offset %d is past the end)", number-1, offset), nil
	}
	return out.String(), nil
}
