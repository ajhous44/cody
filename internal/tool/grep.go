package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	grepMaxFileBytes = 2 << 20
	grepMaxLineChars = 300
	grepMaxOutput    = 16 << 10
)

type Grep struct{ root string }

func NewGrep(root string) *Grep { return &Grep{root: root} }

func (g *Grep) Name() string { return "grep" }

func (g *Grep) Description() string {
	return "Search file contents with a regular expression. Returns path:line: text matches. Use this instead of shelling out to grep; it needs no approval."
}

func (g *Grep) Mutating() bool { return false }

func (g *Grep) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"pattern":     property("string", "Regular expression to match per line."),
		"path":        property("string", "File or directory to search, relative to the workspace root. Default \".\"."),
		"include":     property("string", "Optional file glob filter, e.g. *.go. A pattern without a slash matches file names at any depth."),
		"ignore_case": property("boolean", "Case-insensitive matching. Default false."),
		"limit":       property("integer", "Maximum number of matches. Default 50, max 500."),
	}, "pattern")
}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Include    string `json:"include"`
	IgnoreCase bool   `json:"ignore_case"`
	Limit      int    `json:"limit"`
}

func (g *Grep) Summarize(args json.RawMessage) string {
	var a grepArgs
	_ = json.Unmarshal(args, &a)
	return a.Pattern
}

func (g *Grep) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a grepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return "", errors.New("pattern is required")
	}
	expr := a.Pattern
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	target := strings.TrimSpace(a.Path)
	if target == "" {
		target = "."
	}
	dir, base, err := OpenWorkspace(g.root, target)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	files := dir.FS()
	base = filepath.ToSlash(base)
	info, err := fs.Stat(files, base)
	if err != nil {
		return "", err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	var includeRe *regexp.Regexp
	if strings.TrimSpace(a.Include) != "" {
		includeRe = globRegexp(a.Include)
	}

	state := grepState{limit: limit}
	if info.IsDir() {
		err = walkSearch(ctx, g.root, base, func(rel string) error {
			if includeRe != nil && !matchGlob(includeRe, a.Include, rel) {
				return nil
			}
			grepFile(files, rel, re, &state)
			if state.full() {
				return fs.SkipAll
			}
			return nil
		})
	} else {
		grepFile(files, base, re, &state)
	}
	if err != nil {
		return "", err
	}
	if state.matches == 0 {
		return "no matches", nil
	}
	out := state.b.String()
	if state.truncated {
		out += "... [truncated]\n"
	}
	return out, nil
}

type grepState struct {
	b         strings.Builder
	matches   int
	limit     int
	truncated bool
}

func (s *grepState) full() bool {
	return s.matches >= s.limit || s.b.Len() >= grepMaxOutput
}

func grepFile(files fs.FS, rel string, re *regexp.Regexp, state *grepState) {
	info, err := fs.Stat(files, rel)
	if err != nil || !info.Mode().IsRegular() || info.Size() > grepMaxFileBytes {
		return
	}
	data, err := fs.ReadFile(files, rel)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return
	}
	for i, line := range strings.Split(string(data), "\n") {
		if state.full() {
			state.truncated = true
			return
		}
		line = strings.TrimRight(line, "\r")
		if !re.MatchString(line) {
			continue
		}
		if len(line) > grepMaxLineChars {
			line = line[:grepMaxLineChars] + "..."
		}
		fmt.Fprintf(&state.b, "%s:%d: %s\n", filepath.ToSlash(rel), i+1, line)
		state.matches++
	}
}
