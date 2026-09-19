package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGrepTool(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"a.go":              "package a\nfunc Alpha() {}\n",
		"b.txt":             "alpha beta\n",
		"node_modules/x.js": "Alpha\n",
	})
	ctx := context.Background()
	g := NewGrep(dir)

	out, err := g.Run(ctx, []byte(`{"pattern":"Alpha"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "a.go:2") {
		t.Fatalf("missing match:\n%s", out)
	}
	if strings.Contains(out, "b.txt") || strings.Contains(out, "node_modules") {
		t.Fatalf("case or skip leak:\n%s", out)
	}

	out, err = g.Run(ctx, []byte(`{"pattern":"alpha","include":"*.txt","ignore_case":true}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "b.txt:1") || strings.Contains(out, "a.go") {
		t.Fatalf("include filter failed:\n%s", out)
	}

	if _, err := g.Run(ctx, []byte(`{"pattern":"["}`)); err == nil {
		t.Fatal("invalid regex accepted")
	}
	out, err = g.Run(ctx, []byte(`{"pattern":"zzz"}`))
	if err != nil || out != "no matches" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestGlobTool(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"main.go":               "package main\n",
		"internal/ui/ui.go":     "package ui\n",
		"internal/agent/ag.go":  "package agent\n",
		"node_modules/dep/x.js": "module.exports\n",
		"README.md":             "# readme\n",
	})
	ctx := context.Background()
	g := NewGlob(dir)

	out, err := g.Run(ctx, []byte(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"main.go", "internal/ui/ui.go", "internal/agent/ag.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("generated dir leaked:\n%s", out)
	}

	out, err = g.Run(ctx, []byte(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "ui.go") {
		t.Fatalf("basename matching failed:\n%s", out)
	}

	out, err = g.Run(ctx, []byte(`{"pattern":"*.go","limit":1}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "2 more files") {
		t.Fatalf("limit note missing:\n%s", out)
	}

	out, err = g.Run(ctx, []byte(`{"pattern":"*.rs"}`))
	if err != nil || !strings.Contains(out, "no files match") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
