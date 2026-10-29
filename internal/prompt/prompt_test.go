package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIncludesWorkspaceListing(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"src/components", "node_modules/dep"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"main.go", "src/app.js", "src/components/button.jsx", "node_modules/dep/x.js", ".hidden"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Build(root)
	for _, want := range []string{"main.go", "src/", "app.js", "components/", "button.jsx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("listing missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"node_modules", "x.js", ".hidden"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("listing includes %q:\n%s", unwanted, got)
		}
	}
}

func TestBuildCapsWorkspaceListing(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxListingEntries+10; i++ {
		name := filepath.Join(root, fmt.Sprintf("f%03d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Build(root)
	if !strings.Contains(got, "[listing truncated]") {
		t.Fatalf("expected truncation marker:\n%s", got)
	}
	if !strings.Contains(got, "f000.txt") {
		t.Fatalf("listing missing first entry:\n%s", got)
	}
	if last := fmt.Sprintf("f%03d.txt", maxListingEntries+9); strings.Contains(got, last) {
		t.Fatalf("listing not capped before %s", last)
	}
}

func TestBuildIncludesWorkspaceToolsAndRules(t *testing.T) {
	root := t.TempDir()
	got := Build(root)
	if !strings.Contains(got, root) {
		t.Fatalf("prompt missing workspace root:\n%s", got)
	}
	for _, tool := range []string{"read", "write", "edit", "bash", "grep", "glob"} {
		if !strings.Contains(got, "- "+tool+":") {
			t.Fatalf("prompt missing tool %q:\n%s", tool, got)
		}
	}
	for _, rule := range []string{"Explore before asking", "fix the arguments, and retry"} {
		if !strings.Contains(got, rule) {
			t.Fatalf("prompt missing rule %q:\n%s", rule, got)
		}
	}
}

func TestBuildIncludesProjectInstructions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CODY.md"), []byte("always use tabs"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Build(root)
	if !strings.Contains(got, "Project instructions (from CODY.md):") || !strings.Contains(got, "always use tabs") {
		t.Fatalf("project instructions missing:\n%s", got)
	}
}

func TestBuildTruncatesProjectInstructions(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", maxProjectInstructions+100)
	if err := os.WriteFile(filepath.Join(root, "CODY.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Build(root)
	if !strings.HasSuffix(got, "[truncated]\n") {
		t.Fatalf("expected truncation marker:\n%s", got)
	}
	if !strings.Contains(got, strings.Repeat("x", maxProjectInstructions)) {
		t.Fatalf("expected instructions capped at %d bytes", maxProjectInstructions)
	}
	if strings.Contains(got, strings.Repeat("x", maxProjectInstructions+1)) {
		t.Fatalf("instructions exceed %d bytes", maxProjectInstructions)
	}
}

func TestBuildWithoutProjectInstructions(t *testing.T) {
	got := Build(t.TempDir())
	if strings.Contains(got, "Project instructions") {
		t.Fatalf("unexpected project instructions section:\n%s", got)
	}
}

func TestAgentsScopeAndPrecedence(t *testing.T) {
	root := t.TempDir()
	for path, text := range map[string]string{"CODY.md": "legacy", "AGENTS.md": "root rule", "src/AGENTS.md": "source rule", "src/nested/AGENTS.md": "nearest rule", "other/AGENTS.md": "other rule"} {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := Build(root)
	if strings.Index(base, "legacy") > strings.Index(base, "root rule") || !strings.Contains(base, "root rule") {
		t.Fatal("root instruction precedence missing")
	}
	got := ScopedInstructions(root, "src/nested/new.go")
	if !strings.Contains(got, "source rule") || !strings.Contains(got, "nearest rule") || strings.Contains(got, "other rule") || strings.Index(got, "source rule") > strings.Index(got, "nearest rule") {
		t.Fatalf("scope: %s", got)
	}
	if got := ScopedInstructions(root, "../outside/new.go"); got != "" {
		t.Fatalf("escaped: %s", got)
	}
	if err := os.WriteFile(filepath.Join(root, "src/AGENTS.md"), []byte("changed rule"), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ScopedInstructions(root, "src/new.go"), "changed rule") {
		t.Fatal("stale instructions")
	}
}

func TestOptionalRulesOnlyForEnabledTools(t *testing.T) {
	if got := Optional(false, false, false); got != "" {
		t.Fatalf("disabled tools should add no rules:\n%s", got)
	}
	web, browser := Optional(true, false, false), Optional(false, true, false)
	if plan := Optional(false, false, true); !strings.Contains(plan, "call plan") || strings.Contains(plan, "untrusted") {
		t.Fatalf("plan rules wrong:\n%s", plan)
	}
	if !strings.Contains(web, "web_fetch") || strings.Contains(web, "refs from") || !strings.Contains(web, "untrusted") {
		t.Fatalf("web rules wrong:\n%s", web)
	}
	if !strings.Contains(browser, "refs from") || strings.Contains(browser, "web_fetch") || !strings.Contains(browser, "untrusted") {
		t.Fatalf("browser rules wrong:\n%s", browser)
	}
}
