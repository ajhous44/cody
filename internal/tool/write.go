package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

type Write struct{ root string }

func NewWrite(root string) *Write { return &Write{root: root} }

func (w *Write) Name() string { return "write" }

func (w *Write) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created automatically."
}

func (w *Write) Mutating() bool { return true }

func (w *Write) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"path":    property("string", "File path relative to the workspace root."),
		"content": property("string", "Literal file content. Do not wrap code in Markdown fences unless the file itself requires them."),
	}, "path", "content")
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (w *Write) Summarize(args json.RawMessage) string {
	var a writeArgs
	_ = json.Unmarshal(args, &a)
	return fmt.Sprintf("%s (%d bytes)", a.Path, len(a.Content))
}

func (w *Write) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	dir, rel, err := OpenWritable(w.root, a.Path)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	_, statErr := dir.Stat(rel)
	existed := statErr == nil
	if err := dir.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return "", err
	}
	if err := dir.WriteFile(rel, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	action := "created"
	if existed {
		action = "overwrote"
	}
	return fmt.Sprintf("%s %s (%d bytes)", action, a.Path, len(a.Content)), nil
}
