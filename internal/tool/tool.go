package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ajhous44/cody/v3/internal/provider"
)

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Mutating() bool
	Summarize(args json.RawMessage) string
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	tools map[string]Tool
	order []string
	// specs is built once: schemas never change, so every turn reuses it
	// instead of rebuilding and re-marshaling the same descriptors.
	specs []provider.ToolSpec
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools)), order: make([]string, 0, len(tools))}
	for _, t := range tools {
		r.tools[t.Name()] = t
		r.order = append(r.order, t.Name())
	}
	for _, t := range r.List() {
		r.specs = append(r.specs, provider.ToolSpec{
			Type: "function",
			Function: provider.FuncSpec{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		})
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (r *Registry) Specs() []provider.ToolSpec {
	// Shared and immutable: callers must not modify the returned slice.
	return r.specs
}

// Resolve maps a workspace-relative path to an absolute path and rejects
// lexical escapes. Use OpenWorkspace for filesystem access (including symlinks).
func Resolve(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New(`path is required: use the JSON field "path", not "filePath"`)
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	return abs, nil
}

// OpenWorkspace returns a confined filesystem and its relative path. The caller closes it.
func OpenWorkspace(root, path string) (*os.Root, string, error) {
	abs, err := Resolve(root, path)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, "", err
	}
	dir, err := os.OpenRoot(root)
	return dir, rel, err
}

// OpenWritable is OpenWorkspace for mutating file tools. Git metadata is
// refused: .git/config (core.fsmonitor, filters) and hooks run commands through
// ordinary git calls, which would bypass shell approval in auto mode.
func OpenWritable(root, path string) (*os.Root, string, error) {
	dir, rel, err := OpenWorkspace(root, path)
	if err != nil {
		return nil, "", err
	}
	if inGitDir(root, rel) {
		dir.Close()
		return nil, "", fmt.Errorf("path %q is inside git metadata; file tools do not modify .git", path)
	}
	return dir, rel, nil
}

func inGitDir(root, rel string) bool {
	if hasGitPart(rel) {
		return true
	}
	// A link inside the workspace (docs -> .git) passes the lexical check, so
	// also check the real path of the deepest existing ancestor.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true
	}
	for p := filepath.Join(root, rel); ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			r, err := filepath.Rel(realRoot, real)
			return err != nil || hasGitPart(r)
		}
		if p == root || p == filepath.Dir(p) {
			return false
		}
	}
}

func hasGitPart(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		// Case-insensitive filesystems, Windows name trimming (".git."), alternate
		// streams (".git::$INDEX_ALLOCATION"), and 8.3 names all reach .git.
		part, _, _ = strings.Cut(part, ":")
		part = strings.TrimRight(part, ". ")
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, "git~1") {
			return true
		}
	}
	return false
}

func objectSchema(props map[string]any, required ...string) json.RawMessage {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	b, _ := json.Marshal(m)
	return b
}

func property(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}
