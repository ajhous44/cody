package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runSandboxed(t *testing.T, root, mode, command string) (string, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"command": command})
	out, err := NewBash(root).WithSandbox(Sandbox{Mode: mode}).Run(context.Background(), args)
	if strings.Contains(out, "Landlock unavailable") || strings.Contains(out, "needs Landlock network") {
		// CI's Linux kernel has Landlock, so there the check must really run.
		if os.Getenv("CI") != "" {
			t.Fatal("Landlock missing on CI: " + out)
		}
		t.Skip("kernel lacks the Landlock support this check needs: " + out)
	}
	return out, err
}

func TestSandboxConfinesShellWrites(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if _, err := runSandboxed(t, root, "workspace", "echo hi"); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("unsupported OS must refuse, got %v", err)
		}
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder outside the writable set")
	}
	// Temp and cache folders are writable by design, so the forbidden target
	// lives in the home folder.
	outside, err := os.MkdirTemp(home, ".cody-sandbox-test-")
	if err != nil {
		t.Skip("home folder not writable")
	}
	defer os.RemoveAll(outside)

	out, err := runSandboxed(t, root, "workspace",
		fmt.Sprintf(`echo in > in.txt && echo tmp > "${TMPDIR:-/tmp}/cody-sb-$$" && rm "${TMPDIR:-/tmp}/cody-sb-$$"; echo out > %q`, filepath.Join(outside, "out.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "in.txt")); err != nil {
		t.Fatalf("write inside the project was blocked: %s", out)
	}
	if strings.Contains(out, "No such file") || strings.HasPrefix(out, "exit 0 ") {
		t.Fatalf("temp write failed or outside write was not reported: %s", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "out.txt")); err == nil {
		t.Fatalf("sandbox allowed a write outside the project: %s", out)
	}

	// Control: without the sandbox the same write succeeds, so the check above
	// cannot pass vacuously.
	if out, _ := runSandboxed(t, root, "off", fmt.Sprintf("echo ctl > %q", filepath.Join(outside, "ctl.txt"))); !strings.HasPrefix(out, "exit 0 ") {
		t.Fatalf("control write failed: %s", out)
	}
	if _, err := runSandboxed(t, root, "bogus", "true"); err == nil {
		t.Fatal("unknown sandbox mode ran unconfined")
	}
}

func TestSandboxStrictBlocksNetwork(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no OS sandbox here")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	connect := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d && echo connected", ln.Addr().(*net.TCPAddr).Port)
	root := t.TempDir()
	if out, _ := runSandboxed(t, root, "workspace", connect); !strings.Contains(out, "connected") {
		t.Fatalf("workspace mode should keep network: %s", out)
	}
	if out, _ := runSandboxed(t, root, "strict", connect); strings.Contains(out, "connected") {
		t.Fatalf("strict mode allowed a TCP connection: %s", out)
	}
}
