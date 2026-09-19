package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxProjectInstructions = 8 << 10

const (
	maxListingEntries = 150
	maxListingDepth   = 3
)

var skipListingDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".next": true, ".cache": true, ".idea": true, ".vscode": true,
}

func Build(root string) string {
	listing := workspaceListing(root)
	if listing == "" {
		listing = "(empty)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `You are cody, a terse coding agent working in a terminal.
Workspace root: %s

Workspace files (shallow listing; hidden and generated directories omitted):
%s
Tools:
- read: view a file with line numbers (use offset/limit for large files).
- write: create or overwrite a file.
- edit: replace an exact string in a file. Provide enough context to match once.
- bash: run a shell command from the workspace root.
- grep: search file contents with a regex, without approval.
- glob: find files by glob pattern, without approval.
- search: semantic search over a local index of the workspace. Use when the
  exact name is unknown; the user runs "cody index" to build it.

Rules:
- Explore before asking. If the request does not name a file, or is ambiguous,
  run ls, find, or grep to find candidates first. Only ask the user when no
  tool can answer the question.
- Inspect files before changing them. Never invent contents.
- Prefer targeted edits over rewriting files.
- Use grep and glob for code search; use bash for git, tests, and builds.
- Verify changes with the relevant command when one exists.
- State a one-line plan before multi-step work. Keep replies short.
- If a tool call fails, read the error, fix the arguments, and retry.
- Report what changed and what you verified. If something fails, show the failure.
`, root, listing)
	if s := projectInstructions(root); s != "" {
		b.WriteString("\nProject instructions (from CODY.md):\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

func workspaceListing(root string) string {
	var b strings.Builder
	entries, truncated := 0, false
	var walk func(dir, prefix string, depth int)
	walk = func(dir, prefix string, depth int) {
		if truncated || depth > maxListingDepth {
			return
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, it := range items {
			name := it.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if entries >= maxListingEntries {
				truncated = true
				return
			}
			if it.IsDir() {
				if skipListingDirs[name] {
					continue
				}
				fmt.Fprintf(&b, "%s%s/\n", prefix, name)
				entries++
				walk(filepath.Join(dir, name), prefix+"  ", depth+1)
				continue
			}
			fmt.Fprintf(&b, "%s%s\n", prefix, name)
			entries++
		}
	}
	walk(root, "", 1)
	if truncated {
		b.WriteString("... [listing truncated]\n")
	}
	return b.String()
}

func projectInstructions(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "CODY.md"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) > maxProjectInstructions {
		s = s[:maxProjectInstructions] + "\n[truncated]"
	}
	return s
}
