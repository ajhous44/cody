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
// directory and arguments, prints them back, and fails on "boom". It also
// mimics a page with a dropdown: acting on option @e7 or button @e9 changes
// nothing, and selecting on the dropdown @e4 does.
func fakeAgentCLI(t *testing.T, bin string) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a POSIX shell script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	noop := filepath.Join(dir, "noop")
	script := fmt.Sprintf(`#!/bin/sh
echo "$PWD|$*" >> %q
case "$*" in
*boom*) echo 'boom failed'; exit 3;;
*"click @e5"*) echo data > report.txt;;
*"select @e7"*|*"click @e9"*|*"click @e3"*) touch %q;;
*"select @e4 Gadget"*|*"upload @e3"*) rm -f %q;;
*"get attr @e3 type") echo file; exit 0;;
*--json) printf '%%s\n' '{"data":{"refs":{"e4":{"role":"combobox","name":"Product"},"e7":{"role":"option","name":"Gadget"},"e9":{"role":"button","name":"Go"},"e3":{"role":"button","name":"Attachment"}},"snapshot":"- combobox \"Product\" [expanded=false, ref=e4]: Widget\n  - option \"Gadget\" [ref=e7]\n- button \"Go\" [ref=e9]"}}'; exit 0;;
*--delta) if [ -f %q ]; then echo 'unchanged (revision 2)'; exit 0; fi;;
esac
echo "ran $*"
`, log, noop, noop, noop)
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
	// Predictable slips are read as meant: a wrong key with the array as a JSON
	// string, the command as a bare string, a ref handed to read. The literal
	// placeholder @eN is refused before it reaches the browser.
	for in, want := range map[string]string{
		`{"arguments":"[\"open\", \"http://x\"]"}`: "agent-browser open http://x",
		`{"args":"click @e3"}`:                     "agent-browser click @e3",
		`{"args":["read","@e9"]}`:                  "agent-browser read",
	} {
		if got := b.Summarize(json.RawMessage(in)); got != want {
			t.Fatalf("%s read as %q, want %q", in, got, want)
		}
	}
	if err := b.Validate(json.RawMessage(`{"args":["click","@eN"]}`)); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("placeholder ref: %v", err)
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
	want := []string{"fill @e2 --headed", "wait --load networkidle --timeout 5000", "snapshot -i --delta", "boom", "close"} // every action is followed by the settled page
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

	// Self-healing: an action aimed at the wrong kind of element is rerun once
	// with the verb that fits it, without a model turn.
	out, err = b.Run(ctx, json.RawMessage(`{"args":["select","@e7","Gadget"]}`))
	if err != nil || !strings.Contains(out, "cody ran: select @e4 Gadget") || !strings.Contains(out, "ran --session "+session+" select @e4 Gadget") {
		t.Fatalf("option not healed to its dropdown: %q %v", out, err)
	}
	// Knowing when to stop: four actions in a row without effect, then no more until the page is reopened.
	for i := range 4 {
		if out, err := b.Run(ctx, json.RawMessage(`{"args":["click","@e9"]}`)); err != nil || !strings.Contains(out, "The page did not change.") {
			t.Fatalf("ineffective click %d: %q %v", i, out, err)
		}
	}
	if _, err := b.Run(ctx, json.RawMessage(`{"args":["click","@e9"]}`)); err == nil || !strings.Contains(err.Error(), "stopped: 4 browser actions") {
		t.Fatalf("stuck model not stopped: %v", err)
	}
	if _, err := b.Run(ctx, json.RawMessage(`{"args":["open","http://x"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(ctx, json.RawMessage(`{"args":["click","@e9"]}`)); err != nil {
		t.Fatalf("reopening did not reset: %v", err)
	}
	// A file chooser cannot be clicked open: say what it is, and upload when the model named a file.
	if out, err := b.Run(ctx, json.RawMessage(`{"args":["click","@e3"]}`)); err != nil || !strings.Contains(out, "use upload @e3 <path>") {
		t.Fatalf("file chooser not explained: %q %v", out, err)
	}
	if out, err := b.Run(ctx, json.RawMessage(`{"args":["fill","@e3","notes.txt"]}`)); err != nil || !strings.Contains(out, "cody ran: upload @e3 notes.txt") {
		t.Fatalf("file chooser not healed: %q %v", out, err)
	}
	// Uploads and downloads stay in the workspace: relative paths are made
	// absolute (Chrome ignores relative ones), outside paths are refused, and a
	// file that arrives is reported even though the page did not change.
	if out, err := b.Run(ctx, json.RawMessage(`{"args":["upload","@e3","notes.txt"]}`)); err != nil || !strings.Contains(out, "upload @e3 "+filepath.Join(root, "notes.txt")) {
		t.Fatalf("upload path not resolved in the workspace: %q %v", out, err)
	}
	if _, err := b.Run(ctx, json.RawMessage(`{"args":["upload","@e3","../secret.txt"]}`)); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("upload outside the workspace allowed: %v", err)
	}
	// An invented folder falls back to the workspace file of that name, also when healing.
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi"), 0o600)
	if out, err := b.Run(ctx, json.RawMessage(`{"args":["upload","@e3","/Users/someone/project/notes.txt"]}`)); err != nil || !strings.Contains(out, "upload @e3 "+filepath.Join(root, "notes.txt")) {
		t.Fatalf("invented upload folder: %q %v", out, err)
	}
	if out, err := b.Run(ctx, json.RawMessage(`{"args":["click","@e5"]}`)); err != nil || !strings.Contains(out, "Downloaded into the project folder: report.txt") {
		t.Fatalf("download not reported: %q %v", out, err)
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
	// A small window gets a smaller cap: about a quarter of it per result.
	small := NewAgentDesktop(t.TempDir())
	small.Window = func() int { return 8192 }
	if out, _ := small.Run(context.Background(), json.RawMessage(`{"args":["get","`+long+`"]}`)); len(out) > 4096+200 {
		t.Fatalf("small-window cap: %d bytes", len(out))
	}
}

// Drives the real CLIs: npm install -g agent-browser agent-desktop. Desktop
// actions also need Accessibility permission on macOS, so only list-apps runs.
func TestAgentCLIsLive(t *testing.T) {
	if os.Getenv("CODY_AGENT_CLI_TEST") != "1" {
		t.Skip("set CODY_AGENT_CLI_TEST=1 to drive the installed agent-browser and agent-desktop")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/report.txt" {
			fmt.Fprint(w, "quarterly")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Fixture</h1><label>Name <input id="n"></label><button onclick="document.querySelector('h1').innerText='Hello '+document.querySelector('#n').value">Greet</button>
<label>Attachment <input type="file" onchange="document.querySelector('h1').innerText='Got '+this.files[0].size+' bytes'"></label><a href="/report.txt" download>Report</a>`)
	}))
	defer ts.Close()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600)
	b := NewAgentBrowser(root)
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
	snap := run("open", ts.URL) // no snapshot call: every action returns the page
	run("fill", ref(snap, "textbox", "Name"), "Ada")
	run("click", ref(snap, "button", "Greet"))
	if out := run("get", "text", "h1"); !strings.Contains(out, "Hello Ada") {
		t.Fatalf("page did not update: %s", out)
	}
	// A relative upload path reaches Chrome as a real file, and a clicked
	// download lands in the workspace and is reported.
	if out := run("upload", ref(snap, "button", "Attachment"), "notes.txt"); !strings.Contains(out, "Got 5 bytes") {
		t.Fatalf("upload: %s", out)
	}
	out := run("click", ref(snap, "link", "Report"))
	if data, _ := os.ReadFile(filepath.Join(root, "report.txt")); string(data) != "quarterly" || !strings.Contains(out, "Downloaded into the project folder: report.txt") {
		t.Fatalf("download: file %q, output %s", data, out)
	}
	data, _ := json.Marshal(map[string]any{"args": []string{"list-apps"}})
	if out, err := NewAgentDesktop(t.TempDir()).Run(context.Background(), data); err != nil || !strings.Contains(out, `"command":"list-apps","data":{"apps":[`) {
		t.Fatalf("desktop list-apps: %q %v", out, err)
	}
}
