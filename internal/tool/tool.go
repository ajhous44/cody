package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ajhous44/cody/internal/provider"
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
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools)), order: make([]string, 0, len(tools))}
	for _, t := range tools {
		r.tools[t.Name()] = t
		r.order = append(r.order, t.Name())
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
	specs := make([]provider.ToolSpec, 0, len(r.order))
	for _, t := range r.List() {
		specs = append(specs, provider.ToolSpec{
			Type: "function",
			Function: provider.FuncSpec{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		})
	}
	return specs
}

// Resolve maps a workspace-relative path to an absolute path and rejects
// anything that escapes the workspace root.
func Resolve(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
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
