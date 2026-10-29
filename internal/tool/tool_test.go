package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/prompt"
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

func TestReadPathAlias(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.txt", "one\ntwo\nthree\n")
	r := NewRead(root)
	for _, args := range []string{
		`{"path":"a.txt","offset":2,"limit":1}`,
		`{"filePath":"a.txt","offset":2,"limit":1}`,
		`{"path":"a.txt","filePath":"a.txt","offset":2,"limit":1}`,
		`{"path":"","filePath":"a.txt","offset":2,"limit":1}`,
	} {
		out, err := r.Run(context.Background(), []byte(args))
		if err != nil || !strings.Contains(out, "2\ttwo") || strings.Contains(out, "1\tone") || strings.Contains(out, "3\tthree") {
			t.Fatalf("alias/range: %q, %v", out, err)
		}
		if r.Summarize([]byte(args)) != "a.txt" {
			t.Fatal("summary did not resolve alias")
		}
	}
	for _, args := range []string{`{}`, `{"filePath":"../outside.txt"}`, `{"filePath":42}`, `{"path":"a.txt","filePath":"other.txt"}`} {
		if _, err := r.Run(context.Background(), []byte(args)); err == nil {
			t.Fatalf("accepted invalid or unsafe arguments: %s", args)
		}
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
	command := "yes | head -c 100000"
	if runtime.GOOS == "windows" {
		command = "Write-Output ('x' * 100000)"
	}
	args, _ := json.Marshal(map[string]string{"command": command})
	out, err := NewBash(t.TempDir()).Run(context.Background(), args)
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

func TestRegistrySpecsCached(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(NewRead(root), NewBash(root))
	first, second := r.Specs(), r.Specs()
	if len(first) != 2 || !reflect.DeepEqual(first, second) {
		t.Fatalf("specs not stable: %+v vs %+v", first, second)
	}
	if first[0].Function.Name != "read" || first[1].Function.Name != "bash" {
		t.Fatalf("spec order or names wrong: %+v", first)
	}
}

func TestSkillToolLoadsLazilyAndStaysInItsFolder(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeFixture(t, outside, "secret.txt", "private")
	skillDir := filepath.Join(dir, "lint")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, skillDir, "SKILL.md", "---\nname: lint\ndescription: run the linter\n---\nRun scripts/lint.sh then fix findings.")
	writeFixture(t, filepath.Join(skillDir, "scripts"), "lint.sh", "echo lint")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(skillDir, "leak")); err != nil {
		t.Fatal(err)
	}
	sk := NewSkill([]prompt.Skill{{Name: "lint", Description: "run the linter", Dir: skillDir}})
	got, err := sk.Run(context.Background(), []byte(`{"name":"lint"}`))
	if err != nil || strings.Contains(got, "description:") || !strings.HasPrefix(got, "Run scripts/lint.sh") || !strings.Contains(got, "scripts/lint.sh") {
		t.Fatalf("load: %q %v", got, err)
	}
	if got, err := sk.Run(context.Background(), []byte(`{"name":"lint","file":"scripts/lint.sh"}`)); err != nil || got != "echo lint" {
		t.Fatalf("file: %q %v", got, err)
	}
	for _, file := range []string{"../../etc/passwd", "leak", filepath.Join(outside, "secret.txt")} {
		args, _ := json.Marshal(map[string]string{"name": "lint", "file": file})
		if got, err := sk.Run(context.Background(), args); err == nil {
			t.Errorf("read %q outside the skill folder: %q", file, got)
		}
	}
	if _, err := sk.Run(context.Background(), []byte(`{"name":"missing"}`)); err == nil || !strings.Contains(err.Error(), "available: lint") {
		t.Fatalf("unknown skill: %v", err)
	}
}
