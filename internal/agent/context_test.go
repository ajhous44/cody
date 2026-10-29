package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

func TestOversizedRequestNeverDisappears(t *testing.T) {
	p := &fakeProvider{}
	a := newTestAgent(p, &echoTool{}, nil)
	a.SetLimits(2048, 0)
	request := strings.Repeat("request", 4000)
	if _, err := a.Prompt(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "context budget") {
		t.Fatalf("oversize: %v", err)
	}
	if len(p.reqs) != 0 || a.History()[0].Content != request {
		t.Fatal("oversized request was sent or lost")
	}
}

func TestCompactionPreservesRequestToolsAndJournal(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{
		{{Content: "Keep the public API unchanged; old edits tested."}},
		{{Content: "Keep the public API unchanged; old edits tested."}},
		{{Content: "done"}},
	}}
	a := newTestAgent(p, &echoTool{}, nil)
	a.SetLimits(8192, 1024)
	old := []provider.Message{
		{Role: "user", Content: "Keep the public API unchanged."},
		{Role: "assistant", Content: strings.Repeat("older findings ", 1800)},
	}
	a.SetHistory(old)
	if _, err := a.Prompt(context.Background(), "finish the fix", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 3 || len(p.reqs[0].Tools) != 0 {
		t.Fatalf("requests: %d", len(p.reqs))
	}
	last := p.reqs[len(p.reqs)-1]
	if last.Messages[len(last.Messages)-1].Content != "finish the fix" || !strings.Contains(last.Messages[1].Content, "public API") {
		t.Fatalf("lost task context: %+v", last.Messages)
	}
	if !reflect.DeepEqual(a.History()[:len(old)], old) {
		t.Fatal("compaction mutated journal")
	}
	if last.MaxOutputTokens != 1024 {
		t.Fatalf("output budget %d", last.MaxOutputTokens)
	}
	// No second compaction while the compacted view still fits.
	p.script = [][]provider.Delta{{{Content: "next"}}}
	if _, err := a.Prompt(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 4 {
		t.Fatal("summary wasn't reused")
	}
}

func TestCompactionRetainsLatestToolExchange(t *testing.T) {
	p := &fakeProvider{script: [][]provider.Delta{{{Content: "Earlier tool checked dependencies."}}}}
	a := newTestAgent(p, &echoTool{}, nil)
	a.SetLimits(8192, 1024)
	history := []provider.Message{
		{Role: "user", Content: "fix the issue"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "old", Function: provider.Func{Name: "echo", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "old", Content: strings.Repeat("old output ", 2400)},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "recent", Function: provider.Func{Name: "echo", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "recent", Content: "latest evidence must remain verbatim"},
	}
	a.SetHistory(history)
	msgs, _, err := a.prepare(context.Background(), func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if msgs[len(msgs)-1].Content != history[4].Content || msgs[len(msgs)-2].ToolCalls[0].ID != "recent" {
		t.Fatal("lost latest exchange")
	}
	found := false
	for _, m := range msgs {
		found = found || m.Content == "fix the issue"
	}
	if !found {
		t.Fatal("lost original request")
	}
	if !reflect.DeepEqual(a.History(), history) {
		t.Fatal("mutated history")
	}
}

func TestIncompleteToolCallNeverExecutes(t *testing.T) {
	ran := false
	p := &fakeProvider{script: [][]provider.Delta{{{FinishReason: "length", ToolCalls: []provider.ToolCallDelta{{ID: "c", Name: "echo", Args: "{}"}}}}}}
	a := newTestAgent(p, &echoTool{run: func(string) (string, error) { ran = true; return "", nil }}, nil)
	if _, err := a.Prompt(context.Background(), "go", nil); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("finish: %v", err)
	}
	if ran || len(a.History()) != 1 {
		t.Fatal("incomplete tool call executed or recorded")
	}
}

func TestScopedInstructionsPrecedeMutationAndRefresh(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0700)
	file := filepath.Join(root, "sub", "AGENTS.md")
	os.WriteFile(file, []byte("Use tabs in this subtree."), 0600)
	a := New(Options{Root: root, Tools: tool.NewRegistry(tool.NewWrite(root))})
	args, _ := json.Marshal(map[string]string{"path": "sub/new.txt", "content": "new"})
	call := provider.ToolCall{Function: provider.Func{Name: "write", Arguments: string(args)}}
	out := a.runTool(context.Background(), call, func(Event) {})
	if !strings.Contains(out, "Use tabs") || !strings.Contains(out, "not executed") {
		t.Fatalf("instructions: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "new.txt")); !os.IsNotExist(err) {
		t.Fatal("mutated before instructions")
	}
	for key, text := range a.pendingInstructions {
		a.instructions[key] = text
	}
	clear(a.pendingInstructions)
	a.runTool(context.Background(), call, func(Event) {})
	if b, err := os.ReadFile(filepath.Join(root, "sub", "new.txt")); err != nil || string(b) != "new" {
		t.Fatalf("retry: %s %v", b, err)
	}
	os.WriteFile(file, []byte("New rule."), 0600)
	if out := a.runTool(context.Background(), call, func(Event) {}); !strings.Contains(out, "New rule") || !strings.Contains(out, "not executed") {
		t.Fatalf("stale instructions: %s", out)
	}
}

func TestReadAliasLoadsScopedInstructions(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0700)
	os.WriteFile(filepath.Join(root, "sub/AGENTS.md"), []byte("Follow subtree guidance."), 0600)
	os.WriteFile(filepath.Join(root, "sub/a.txt"), []byte("file content"), 0600)
	a := New(Options{Root: root, Tools: tool.NewRegistry(tool.NewRead(root))})
	out := a.runTool(context.Background(), provider.ToolCall{Function: provider.Func{Name: "read", Arguments: `{"filePath":"sub/a.txt"}`}}, func(Event) {})
	if !strings.Contains(out, "file content") || !strings.Contains(out, "Follow subtree guidance.") {
		t.Fatal(out)
	}
}

func TestInstructionsGateEveryMutationInSameBatch(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0700)
	os.WriteFile(filepath.Join(root, "sub/AGENTS.md"), []byte("Keep changes minimal."), 0600)
	calls := []provider.ToolCallDelta{
		{Index: 0, ID: "a", Name: "write", Args: `{"path":"sub/a.txt","content":"a"}`},
		{Index: 1, ID: "b", Name: "write", Args: `{"path":"sub/b.txt","content":"b"}`},
	}
	p := &fakeProvider{script: [][]provider.Delta{{{ToolCalls: calls}}, {{ToolCalls: calls}}, {{Content: "done"}}}}
	a := New(Options{Provider: p, Model: "test", Root: root, Tools: tool.NewRegistry(tool.NewWrite(root))})
	if _, err := a.Prompt(context.Background(), "write both files", nil); err != nil {
		t.Fatal(err)
	}
	instructions := 0
	for _, msg := range p.reqs[1].Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "not executed") {
			instructions++
		}
	}
	if instructions != 2 {
		t.Fatalf("only %d of two same-batch writes waited for instructions", instructions)
	}
	for _, path := range []string{"sub/a.txt", "sub/b.txt"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
}
