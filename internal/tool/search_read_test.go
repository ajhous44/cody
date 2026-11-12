package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchHonorsGitignore(t *testing.T) {
	root := t.TempDir()
	if b, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", b, err)
	}
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600)
	os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("fixtureIgnoredMarker\n"), 0600)
	out, err := NewGrep(root).Run(context.Background(), []byte(`{"pattern":"fixtureIgnoredMarker"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ignored.txt") {
		t.Fatal("grep returned contents of a gitignored file")
	}
}
func BenchmarkRead8MiBLimitOne(b *testing.B) {
	root := b.TempDir()
	data := strings.Repeat("x\n", 4<<20)
	if err := os.WriteFile(filepath.Join(root, "large.log"), []byte(data), 0600); err != nil {
		b.Fatal(err)
	}
	tool := NewRead(root)
	args := []byte(`{"path":"large.log","limit":1}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tool.Run(context.Background(), args); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReadBoundsOutputAndHugeLines(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"big.txt": strings.Repeat("x", 2<<20) + "\nlast\n", "many.txt": strings.Repeat(strings.Repeat("a", 500)+"\n", 2000), "skip.txt": strings.Repeat("z", 32<<10)})
	for _, tc := range []struct{ args, want string }{
		{`{"path":"big.txt","offset":2}`, "last"},
		{`{"path":"many.txt","limit":2000}`, "output capped"},
		{`{"path":"skip.txt","offset":2}`, "file has 1 lines"},
	} {
		out, err := NewRead(root).Run(context.Background(), []byte(tc.args))
		if err != nil || !strings.Contains(out, tc.want) || len(out) > maxReadBytes+128 {
			t.Fatalf("read: len=%d, err=%v, want %s", len(out), err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRead(root).Run(ctx, []byte(`{"path":"big.txt"}`)); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestSearchNestedIgnoresAndTrackedFiles(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
	writeTree(t, root, map[string]string{".gitignore": "*.log\n", "tracked.log": "needle", "drop.log": "needle", "sub/.gitignore": "!keep.log\n", "sub/keep.log": "needle", "sub/drop.log": "needle"})
	if out, err := exec.Command("git", "-C", root, "add", "-f", "tracked.log").CombinedOutput(); err != nil {
		t.Fatalf("git add: %s %v", out, err)
	}
	for _, tool := range []Tool{NewGrep(root), NewGlob(root)} {
		args := `{"pattern":"needle"}`
		if tool.Name() == "glob" {
			args = `{"pattern":"**/*.log"}`
		}
		out, err := tool.Run(context.Background(), []byte(args))
		if err != nil || strings.Contains(out, "drop.log") || !strings.Contains(out, "tracked.log") || !strings.Contains(out, "sub/keep.log") {
			t.Fatalf("%s: %s %v", tool.Name(), out, err)
		}
	}
	out, err := NewGrep(root).Run(context.Background(), []byte(`{"pattern":"needle","path":"sub"}`))
	if err != nil || strings.Contains(out, "tracked.log") || !strings.Contains(out, "sub/keep.log") {
		t.Fatalf("subpath: %s %v", out, err)
	}
}
