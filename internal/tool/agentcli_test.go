package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// fakeAgentCLI puts a stand-in for bin on PATH that logs its working
// directory and arguments, prints them back, and fails on "boom".
func fakeAgentCLI(t *testing.T, bin string) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a POSIX shell script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$PWD|$*\" >> %q\ncase \"$*\" in *boom*) echo 'boom failed'; exit 3;; esac\necho \"ran $*\"\n", log)
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestAgentCLIPassesCommandsThrough(t *testing.T) {
	root := t.TempDir()
	log := fakeAgentCLI(t, "agent-browser")
	b := NewAgentBrowser(root)
	ctx := context.Background()

	if err := b.Validate(json.RawMessage(`{"args":[]}`)); err == nil {
		t.Fatal("empty command reached approval")
	}
	if got := b.Summarize(json.RawMessage(`{"args":["agent-browser","fill","@e2","Ada Lovelace"]}`)); got != `agent-browser fill @e2 "Ada Lovelace"` {
		t.Fatalf("approval summary: %s", got)
	}
	// A command written as one string is split like a shell would; several commands are refused.
	if got := b.Summarize(json.RawMessage(`{"args":["agent-browser fill @e2 \"Ada Lovelace\" 'it''s' a\\ b \"\""]}`)); got != `agent-browser fill @e2 "Ada Lovelace" its "a b" ""` {
		t.Fatalf("one-string command: %s", got)
	}
	if err := b.Validate(json.RawMessage(`{"args":["type 56","press ="]}`)); err == nil || !strings.Contains(err.Error(), `["click","@e3"]`) {
		t.Fatalf("several commands in one call: %v", err)
	}
	// Arguments arrive unchanged after cody's session flag, even ones that look like flags.
	out, err := b.Run(ctx, json.RawMessage(`{"args":["agent-browser","fill","@e2","--headed"]}`))
	session := b.flags[1]
	if err != nil || !strings.HasPrefix(out, agentNotice) || !strings.Contains(out, "ran --session "+session+" fill @e2 --headed") {
		t.Fatalf("run: %q %v", out, err)
	}
	if _, err := b.Run(ctx, json.RawMessage(`{"args":["boom"]}`)); err == nil || !strings.Contains(err.Error(), "status 3") || !strings.Contains(err.Error(), "boom failed") {
		t.Fatalf("failure not reported with output: %v", err)
	}
	b.Close()
	b.Close() // closes once, only after use
	calls, _ := os.ReadFile(log)
	realRoot, _ := filepath.EvalSymlinks(root)
	want := []string{"fill @e2 --headed", "boom", "close"}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != len(want) {
		t.Fatalf("calls:\n%s", calls)
	}
	for i, line := range lines {
		dir, args, _ := strings.Cut(line, "|")
		if d, _ := filepath.EvalSymlinks(dir); d != realRoot || args != "--session "+session+" "+want[i] {
			t.Fatalf("call %d ran %q in %s, want %q in the workspace", i, args, dir, want[i])
		}
	}

	// Desktop has no session, and a second browser gets its own.
	fakeAgentCLI(t, "agent-desktop")
	if out, err := NewAgentDesktop(root).Run(ctx, json.RawMessage(`{"args":["list-apps"]}`)); err != nil || !strings.HasSuffix(out, "ran list-apps") {
		t.Fatalf("desktop: %q %v", out, err)
	}
	if NewAgentBrowser(root).flags[1] == session {
		t.Fatal("engines share a browser session")
	}
}

func TestAgentCLIOutputAndInstallErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := NewAgentDesktop(t.TempDir()).Run(context.Background(), json.RawMessage(`{"args":["list-apps"]}`)); err == nil || !strings.Contains(err.Error(), "npm install -g agent-desktop") {
		t.Fatalf("missing CLI: %v", err)
	}
	fakeAgentCLI(t, "agent-desktop")
	long := strings.Repeat("x", agentOutputLimit)
	out, err := NewAgentDesktop(t.TempDir()).Run(context.Background(), json.RawMessage(`{"args":["get","`+long+`"]}`))
	if err != nil || !strings.HasSuffix(out, "[output truncated; narrow the command, e.g. snapshot -i or a smaller scope]") || len(out) > agentOutputLimit+200 {
		t.Fatalf("output not capped: %d bytes, %v", len(out), err)
	}
}

// Drives the real CLIs: npm install -g agent-browser agent-desktop. Desktop
// actions also need Accessibility permission on macOS, so only list-apps runs.
func TestAgentCLIsLive(t *testing.T) {
	if os.Getenv("CODY_AGENT_CLI_TEST") != "1" {
		t.Skip("set CODY_AGENT_CLI_TEST=1 to drive the installed agent-browser and agent-desktop")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Fixture</h1><label>Name <input id="n"></label><button onclick="document.querySelector('h1').innerText='Hello '+document.querySelector('#n').value">Greet</button>`)
	}))
	defer ts.Close()
	b := NewAgentBrowser(t.TempDir())
	defer b.Close()
	run := func(args ...string) string {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"args": args})
		out, err := b.Run(context.Background(), data)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	ref := func(snapshot, role, name string) string {
		t.Helper()
		m := regexp.MustCompile(role + ` "` + name + `" \[ref=(e\d+)\]`).FindStringSubmatch(snapshot)
		if m == nil {
			t.Fatalf("no %s %q in:\n%s", role, name, snapshot)
		}
		return "@" + m[1]
	}
	run("open", ts.URL)
	snap := run("snapshot", "-i")
	run("fill", ref(snap, "textbox", "Name"), "Ada")
	run("click", ref(snap, "button", "Greet"))
	if out := run("get", "text", "h1"); !strings.Contains(out, "Hello Ada") {
		t.Fatalf("page did not update: %s", out)
	}
	data, _ := json.Marshal(map[string]any{"args": []string{"list-apps"}})
	if out, err := NewAgentDesktop(t.TempDir()).Run(context.Background(), data); err != nil || !strings.Contains(out, `"command":"list-apps","data":{"apps":[`) {
		t.Fatalf("desktop list-apps: %q %v", out, err)
	}
}
