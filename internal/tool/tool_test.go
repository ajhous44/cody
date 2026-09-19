package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := Resolve(root, "../outside.txt"); err == nil {
		t.Fatal("expected escape to be rejected")
	}
	if _, err := Resolve(root, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path outside root to be rejected")
	}
	got, err := Resolve(root, "sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("resolved outside root: %s", got)
	}
}

func TestReadNumbersLines(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.txt", "one\ntwo\nthree\n")
	out, err := NewRead(root).Run(context.Background(), []byte(`{"path":"a.txt","offset":2,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     2\ttwo") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "three") {
		t.Fatalf("limit not applied: %q", out)
	}
	if !strings.Contains(out, "more lines") {
		t.Fatalf("missing truncation note: %q", out)
	}
}

func TestReadRejectsBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewRead(root).Run(context.Background(), []byte(`{"path":"bin"}`))
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditReplacesOnce(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.txt", "keep\nold\nkeep\n")
	out, err := NewEdit(root).Run(context.Background(), []byte(`{"path":"a.txt","old_string":"old","new_string":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(b) != "keep\nnew\nkeep\n" {
		t.Fatalf("content = %q", b)
	}
	if !strings.Contains(out, "edited a.txt") {
		t.Fatalf("out = %q", out)
	}
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.txt", "dup\ndup\n")
	_, err := NewEdit(root).Run(context.Background(), []byte(`{"path":"a.txt","old_string":"dup","new_string":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("err = %v", err)
	}
	_, err = NewEdit(root).Run(context.Background(), []byte(`{"path":"a.txt","old_string":"dup","new_string":"x","replace_all":true}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(b) != "x\nx\n" {
		t.Fatalf("content = %q", b)
	}
}

func TestWriteCreatesAndOverwrites(t *testing.T) {
	root := t.TempDir()
	w := NewWrite(root)
	out, err := w.Run(context.Background(), []byte(`{"path":"sub/new.txt","content":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "created") {
		t.Fatalf("out = %q", out)
	}
	out, err = w.Run(context.Background(), []byte(`{"path":"sub/new.txt","content":"bye"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "overwrote") {
		t.Fatalf("out = %q", out)
	}
}

func TestBashCapturesExitCode(t *testing.T) {
	out, err := NewBash(t.TempDir()).Run(context.Background(), []byte(`{"command":"echo hi; exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "exit 3") {
		t.Fatalf("out = %q", out)
	}
}

func TestBashOutputCap(t *testing.T) {
	out, err := NewBash(t.TempDir()).Run(context.Background(), []byte(`{"command":"yes | head -c 100000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation notice, got %d bytes", len(out))
	}
	if len(out) > maxBashOutput+200 {
		t.Fatalf("output not capped: %d bytes", len(out))
	}
}

func TestBashTimeout(t *testing.T) {
	out, err := NewBash(t.TempDir()).Run(context.Background(), []byte(`{"command":"sleep 5","timeout_sec":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("out = %q", out)
	}
}
