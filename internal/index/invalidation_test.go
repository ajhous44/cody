package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectsSameSecondEdit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	db := filepath.Join(t.TempDir(), "index.db")
	os.WriteFile(file, []byte("alpha"), 0600)
	old := time.Unix(1700000000, 100000000)
	os.Chtimes(file, old, old)
	if _, err := Index(context.Background(), root, db, fakeEmbed{}, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte("bravo"), 0600)
	next := time.Unix(1700000000, 200000000)
	os.Chtimes(file, next, next)
	drift, err := Check(root, db)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Stale() {
		t.Fatal("same-size edit within the same second reported a fresh index")
	}
}
func TestClearsEmptyFileChunks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	db := filepath.Join(t.TempDir(), "index.db")
	os.WriteFile(file, []byte("alpha"), 0600)
	if _, err := Index(context.Background(), root, db, fakeEmbed{}, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte(""), 0600)
	if _, err := Index(context.Background(), root, db, fakeEmbed{}, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(context.Background(), db, fakeEmbed{}, "", "alpha", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("reindex kept old contents of empty file: %q", hits[0].Text)
	}
}

func TestBinaryReindexClearsOldText(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	db := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(file, []byte("alpha"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(context.Background(), root, db, fakeEmbed{}, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	var progress []string
	stats, err := Index(context.Background(), root, db, fakeEmbed{}, Options{Model: "fake", Progress: func(rel string) { progress = append(progress, rel) }})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 0 || len(progress) != 0 {
		t.Fatalf("binary file reported as indexed: files=%d progress=%v", stats.Files, progress)
	}
	hits, err := Search(context.Background(), db, fakeEmbed{}, "", "alpha", 8)
	if err != nil || len(hits) > 0 {
		t.Fatalf("stale binary contents: %+v %v", hits, err)
	}
}
