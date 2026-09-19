package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Edit struct{ root string }

func NewEdit(root string) *Edit { return &Edit{root: root} }

func (e *Edit) Name() string { return "edit" }

func (e *Edit) Description() string {
	return "Replace an exact string in a file. old_string must match exactly once unless replace_all is true. Include enough surrounding context to make the match unique."
}

func (e *Edit) Mutating() bool { return true }

func (e *Edit) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"path":        property("string", "File path relative to the workspace root."),
		"old_string":  property("string", "Exact text to replace."),
		"new_string":  property("string", "Replacement text."),
		"replace_all": property("boolean", "Replace every occurrence. Default false."),
	}, "path", "old_string", "new_string")
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (e *Edit) Summarize(args json.RawMessage) string {
	var a editArgs
	_ = json.Unmarshal(args, &a)
	return a.Path
}

func (e *Edit) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.OldString == "" {
		return "", errors.New("old_string is empty; use write to create a file")
	}
	if a.OldString == a.NewString {
		return "", errors.New("old_string and new_string are identical")
	}
	abs, err := Resolve(e.root, a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s (re-read the file and copy the exact text, including whitespace)", a.Path)
	case count > 1 && !a.ReplaceAll:
		return "", fmt.Errorf("old_string matches %d times in %s; add more context or set replace_all", count, a.Path)
	}
	updated := strings.Replace(content, a.OldString, a.NewString, 1)
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	removed := strings.Count(a.OldString, "\n") + 1
	added := strings.Count(a.NewString, "\n") + 1
	return fmt.Sprintf("edited %s (-%d +%d lines)", a.Path, removed, added), nil
}
