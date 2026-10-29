package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/index"
)

func wordEmbedder(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, 8)
		for _, w := range strings.Fields(strings.ToLower(text)) {
			var h uint32 = 2166136261
			for _, r := range w {
				h = (h ^ uint32(r)) * 16777619
			}
			v[h%8]++
		}
		out[i] = v
	}
	return out, nil
}

func TestSearchTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package auth\n\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "index.db")
	e := index.EmbedFunc(wordEmbedder)
	if _, err := index.Index(context.Background(), dir, db, e, index.Options{Model: "fake"}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	s := NewSearch(dir, db, "", e)
	out, err := s.Run(context.Background(), []byte(`{"query":"Login"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "auth.go:1-3") || !strings.Contains(out, "func Login") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "stale") {
		t.Fatalf("fresh index reported stale: %q", out)
	}

	if err := os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package auth\n\nfunc Logout() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = s.Run(context.Background(), []byte(`{"query":"Login"}`))
	if err != nil {
		t.Fatalf("Run after edit: %v", err)
	}
	if !strings.Contains(out, "index is stale") {
		t.Fatalf("stale index not reported: %q", out)
	}

	s = NewSearch(dir, filepath.Join(t.TempDir(), "missing.db"), "", e)
	if _, err := s.Run(context.Background(), []byte(`{"query":"Login"}`)); err == nil || !strings.Contains(err.Error(), "not indexed") {
		t.Fatalf("err = %v", err)
	}
}
