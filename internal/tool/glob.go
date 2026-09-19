package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var searchSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".next": true, ".cache": true, ".idea": true, ".vscode": true,
	"bin": true, "obj": true, "coverage": true,
}

func skipSearchDir(name string) bool {
	return strings.HasPrefix(name, ".") || searchSkipDirs[name]
}

func globRegexp(pattern string) *regexp.Regexp {
	pattern = filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func matchGlob(re *regexp.Regexp, pattern, rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.Contains(filepath.ToSlash(pattern), "/") {
		return re.MatchString(rel)
	}
	return re.MatchString(path.Base(rel))
}

type Glob struct{ root string }

func NewGlob(root string) *Glob { return &Glob{root: root} }

func (g *Glob) Name() string { return "glob" }

func (g *Glob) Description() string {
	return "Find files by glob pattern relative to the workspace root. Supports ** for recursive matching (e.g. **/*.go). Returns matching paths, sorted. Use this instead of shelling out to find or ls."
}

func (g *Glob) Mutating() bool { return false }

func (g *Glob) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"pattern": property("string", "Glob pattern, e.g. **/*.go or src/*.ts. A pattern without a slash matches file names at any depth."),
		"limit":   property("integer", "Maximum number of paths to return. Default 100."),
	}, "pattern")
}

type globArgs struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
}

func (g *Glob) Summarize(args json.RawMessage) string {
	var a globArgs
	_ = json.Unmarshal(args, &a)
	return a.Pattern
}

func (g *Glob) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a globArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.Pattern = strings.TrimSpace(a.Pattern)
	if a.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	if a.Limit <= 0 {
		a.Limit = 100
	}
	re := globRegexp(a.Pattern)
	var matches []string
	err := filepath.WalkDir(g.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != g.root && skipSearchDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(g.root, p)
		if err != nil {
			return nil
		}
		if matchGlob(re, a.Pattern, rel) {
			matches = append(matches, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no files match " + a.Pattern, nil
	}
	sort.Strings(matches)
	if len(matches) > a.Limit {
		matches = append(matches[:a.Limit], fmt.Sprintf("... (%d more files)", len(matches)-a.Limit))
	}
	return strings.Join(matches, "\n"), nil
}
