package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFixture(t, outside, "private.txt", "private sentinel")
	writeFixture(t, root, "local.txt", "local sentinel")
	for name, target := range map[string]string{"escape": outside, "inside": "local.txt"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		tool Tool
		args string
	}{
		{NewRead(root), `{"path":"escape/private.txt"}`},
		{NewRead(root), `{"filePath":"escape/private.txt"}`},
		{NewEdit(root), `{"path":"escape/private.txt","old_string":"private","new_string":"changed"}`},
		{NewWrite(root), `{"path":"escape/new/sub.txt","content":"changed"}`},
		{NewGrep(root), `{"path":"escape/private.txt","pattern":"sentinel"}`},
	} {
		if _, err := tc.tool.Run(context.Background(), []byte(tc.args)); err == nil {
			t.Errorf("%s allowed an outside symlink", tc.tool.Name())
		}
	}
	if got, err := NewGrep(root).Run(context.Background(), []byte(`{"pattern":"private sentinel"}`)); err != nil || got != "no matches" {
		t.Fatalf("recursive grep followed outside link: %q %v", got, err)
	}
	got, err := NewRead(root).Run(context.Background(), []byte(`{"path":"inside"}`))
	if err != nil || !strings.Contains(got, "local sentinel") {
		t.Fatalf("safe relative symlink rejected: %q %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(outside, "private.txt"))
	if string(data) != "private sentinel" {
		t.Fatal("outside file modified")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("outside directory created")
	}
}

// Auto mode approves file tools without asking, so a write to .git/config
// (core.fsmonitor) would run a command on the next git status or ls-files.
func TestFileToolsRefuseGitMetadata(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".git", "sub/.git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(root, d), "config", "[core]\n")
	}
	if err := os.Symlink(".git", filepath.Join(root, "meta")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool Tool
		args string
	}{
		{NewWrite(root), `{"path":".git/config","content":"[core]\n\tfsmonitor = touch pwned\n"}`},
		{NewWrite(root), `{"path":".GIT/config","content":"x"}`},
		{NewWrite(root), `{"path":".git./config","content":"x"}`},
		{NewWrite(root), `{"path":"sub/.git/config","content":"x"}`},
		{NewWrite(root), `{"path":".git/hooks/pre-commit","content":"x"}`},
		{NewWrite(root), `{"path":"meta/config","content":"x"}`},
		{NewWrite(root), `{"path":"meta/hooks/pre-commit","content":"x"}`},
		{NewEdit(root), `{"path":".git/config","old_string":"[core]","new_string":"[core]\n\tfsmonitor = touch pwned"}`},
		{NewEdit(root), `{"path":"meta/config","old_string":"[core]","new_string":"x"}`},
	} {
		if _, err := tc.tool.Run(context.Background(), []byte(tc.args)); err == nil {
			t.Errorf("%s %s modified git metadata", tc.tool.Name(), tc.args)
		}
	}
	for _, d := range []string{".git", "sub/.git"} {
		if data, _ := os.ReadFile(filepath.Join(root, d, "config")); string(data) != "[core]\n" {
			t.Fatalf("%s/config modified: %q", d, data)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks")); !os.IsNotExist(err) {
		t.Fatal("hooks directory created")
	}
	for _, p := range []string{".gitignore", ".github/workflows/ci.yml", "git/notes.txt"} {
		args, _ := json.Marshal(map[string]string{"path": p, "content": "ok"})
		if _, err := NewWrite(root).Run(context.Background(), args); err != nil {
			t.Errorf("write %s rejected: %v", p, err)
		}
	}
}

func TestShellCancellationStopsDescendants(t *testing.T) {
	root := t.TempDir()
	command := `(sleep 1; echo survived > after.txt) & echo ready > ready.txt; wait`
	if runtime.GOOS == "windows" {
		command = `$p = Start-Process powershell.exe -PassThru -NoNewWindow -ArgumentList '-NoProfile', '-Command', 'Start-Sleep -Seconds 1; Set-Content after.txt survived'; Set-Content ready.txt ready; Wait-Process -Id $p.Id`
	}
	args, _ := json.Marshal(map[string]string{"command": command})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := NewBash(root).Run(ctx, args); done <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not return")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "after.txt")); !os.IsNotExist(err) {
		t.Fatal("descendant wrote after cancellation")
	}
}
