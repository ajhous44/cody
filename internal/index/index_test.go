package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEmbed maps words into a small vector space so similar texts score
// similar without a model.
type fakeEmbed struct{}

func (fakeEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, 16)
		for _, word := range strings.Fields(strings.ToLower(text)) {
			var h uint32 = 2166136261
			for _, r := range word {
				h = (h ^ uint32(r)) * 16777619
			}
			v[h%16]++
		}
		out[i] = v
	}
	return out, nil
}

func TestChunksFor(t *testing.T) {
	content := "l1\nl2\nl3\nl4\nl5"
	chunks := chunksFor(content, 3, 1)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].Start != 1 || chunks[0].End != 3 || chunks[0].Text != "l1\nl2\nl3" {
		t.Fatalf("first chunk = %+v", chunks[0])
	}
	if chunks[1].Start != 3 || chunks[1].End != 5 || chunks[1].Text != "l3\nl4\nl5" {
		t.Fatalf("second chunk = %+v", chunks[1])
	}
	if got := chunksFor("  \n\n", 10, 2); got != nil {
		t.Fatalf("blank content produced chunks: %+v", got)
	}
}

func TestIndexIncrementalLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	alpha := filepath.Join(dir, "alpha.go")
	beta := filepath.Join(dir, "beta.go")
	if err := os.WriteFile(alpha, []byte("package main\n\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beta, []byte("package main\n\nfunc Beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	e := fakeEmbed{}
	opts := Options{Model: "fake"}

	stats, err := Index(ctx, dir, dbPath, e, opts)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.Files != 2 || stats.Embedded != 2 || stats.Unchanged != 0 {
		t.Fatalf("initial stats = %+v", stats)
	}

	stats, err = Index(ctx, dir, dbPath, e, opts)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if stats.Embedded != 0 || stats.Unchanged != 2 {
		t.Fatalf("reindex stats = %+v", stats)
	}

	hits, err := Search(ctx, dbPath, e, "", "Alpha", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 || hits[0].Path != "alpha.go" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Start != 1 || hits[0].End != 3 {
		t.Fatalf("hit lines = %d-%d", hits[0].Start, hits[0].End)
	}

	if err := os.WriteFile(alpha, []byte("package main\n\nfunc Alpha() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err = Index(ctx, dir, dbPath, e, opts)
	if err != nil {
		t.Fatalf("reindex changed file: %v", err)
	}
	if stats.Embedded != 1 || stats.Unchanged != 1 {
		t.Fatalf("changed stats = %+v", stats)
	}

	if err := os.Remove(beta); err != nil {
		t.Fatal(err)
	}
	stats, err = Index(ctx, dir, dbPath, e, opts)
	if err != nil {
		t.Fatalf("reindex deleted file: %v", err)
	}
	if stats.Removed != 1 {
		t.Fatalf("removed = %d, want 1", stats.Removed)
	}
	hits, err = Search(ctx, dbPath, e, "", "Beta", 5)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	for _, h := range hits {
		if h.Path == "beta.go" {
			t.Fatalf("deleted file still searchable: %+v", h)
		}
	}
}

func TestCheckDetectsDrift(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	alpha := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(alpha, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	e := fakeEmbed{}
	if _, err := Index(ctx, dir, dbPath, e, Options{Model: "fake"}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if d, err := Check(dir, dbPath); err != nil || d.Stale() {
		t.Fatalf("fresh index drift = %+v (%v)", d, err)
	}
	if err := os.WriteFile(alpha, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(dir, "beta.txt")
	if err := os.WriteFile(beta, []byte("new file"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Check(dir, dbPath)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Changed != 1 || d.New != 1 || d.Deleted != 0 {
		t.Fatalf("drift = %+v", d)
	}
	if _, err := Index(ctx, dir, dbPath, e, Options{Model: "fake"}); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if d, err := Check(dir, dbPath); err != nil || d.Stale() {
		t.Fatalf("drift after reindex = %+v (%v)", d, err)
	}
	// An mtime-only change must be absorbed by reindex, not reported forever.
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(alpha, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(ctx, dir, dbPath, e, Options{Model: "fake"}); err != nil {
		t.Fatalf("reindex after touch: %v", err)
	}
	if d, err := Check(dir, dbPath); err != nil || d.Stale() {
		t.Fatalf("drift after touch = %+v (%v)", d, err)
	}
	if err := os.Remove(beta); err != nil {
		t.Fatal(err)
	}
	d, err = Check(dir, dbPath)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Deleted != 1 {
		t.Fatalf("drift = %+v", d)
	}
}

func TestIndexModelMismatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	e := fakeEmbed{}
	if _, err := Index(ctx, dir, dbPath, e, Options{Model: "one"}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	_, err := Index(ctx, dir, dbPath, e, Options{Model: "two"})
	if err == nil || !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("expected rebuild hint, got %v", err)
	}
	if _, err := Index(ctx, dir, dbPath, e, Options{Model: "two", Rebuild: true}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
}

func TestSearchWithoutIndex(t *testing.T) {
	_, err := Search(context.Background(), filepath.Join(t.TempDir(), "missing.db"), fakeEmbed{}, "", "q", 3)
	if err == nil || !strings.Contains(err.Error(), "not indexed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSkipDirsAndExtensions(t *testing.T) {
	if !skipDir(".git") || !skipDir("node_modules") {
		t.Fatal("generated dirs must be skipped")
	}
	if skipDir("src") {
		t.Fatal("source dirs must be indexed")
	}
}

func TestIndexFileRejectsReplacedSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "changed.txt")); err != nil {
		t.Fatal(err)
	}
	// The path may have changed after the directory walk. It must be rejected before DB or embedding work.
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	f, err := prepareFile(nil, dir, "changed.txt", 16, 0, Options{})
	if err != nil || f != nil {
		t.Fatalf("outside symlink was indexed: file=%v, err=%v", f, err)
	}
}

func TestSearchRefusesIndexFromAnotherEmbeddingModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	if _, err := Index(context.Background(), dir, dbPath, fakeEmbed{}, Options{Model: "nomic-embed-text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(context.Background(), dbPath, fakeEmbed{}, "nomic-embed-text", "alpha", 3); err != nil {
		t.Fatalf("matching model rejected: %v", err)
	}
	_, err := Search(context.Background(), dbPath, fakeEmbed{}, "openrouter:openai/text-embedding-3-small", "alpha", 3)
	if err == nil || !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("mixed vector spaces allowed: %v", err)
	}
}

type countingEmbed struct{ calls *int }

func (c countingEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	*c.calls++
	return fakeEmbed{}.Embed(ctx, texts)
}

// Small files share embed requests: round trips, not tokens, dominate hosted
// embedding time.
func TestIndexBatchesChunksAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.go", i)), []byte(fmt.Sprintf("package f // word%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	stats, err := Index(context.Background(), dir, filepath.Join(t.TempDir(), "index.db"), countingEmbed{&calls}, Options{Batch: 16})
	if err != nil || stats.Files != 40 || stats.Embedded != 40 {
		t.Fatalf("stats %+v err %v", stats, err)
	}
	if calls != 3 {
		t.Fatalf("40 one-chunk files at batch 16 took %d embed calls, want 3", calls)
	}
}
