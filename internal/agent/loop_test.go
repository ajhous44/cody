package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

func editResponse(id, args string) []provider.Delta {
	return []provider.Delta{{ToolCalls: []provider.ToolCallDelta{{ID: id, Name: "edit", Args: args}}, FinishReason: "tool_calls"}}
}

func TestRepeatedFailedCallsStopBeforeExecutionAndJournalBatch(t *testing.T) {
	for _, name := range []string{"read", "bash"} {
		for _, sameBatch := range []bool{false, true} {
			root := t.TempDir()
			args, reordered := `{"filePath":"missing.txt","offset":1}`, `{"offset":1, "path":"missing.txt"}`
			if name == "bash" {
				args, reordered = `{"command":"exit 7","description":"test"}`, `{"description":"test", "command":"exit 7"}`
			}
			first := provider.ToolCallDelta{ID: "first", Name: name, Args: args}
			repeat := provider.ToolCallDelta{ID: "repeat", Name: name, Args: reordered}
			skipped := provider.ToolCallDelta{Index: 1, ID: "skipped", Name: "write", Args: `{"path":"must-not-exist","content":"bad"}`}
			p := &fakeProvider{script: [][]provider.Delta{
				{{ToolCalls: []provider.ToolCallDelta{first}}},
				{{ToolCalls: []provider.ToolCallDelta{repeat, skipped}}},
			}}
			if sameBatch {
				repeat.Index, skipped.Index = 1, 2
				p.script = [][]provider.Delta{{{ToolCalls: []provider.ToolCallDelta{first, repeat, skipped}}}}
			}
			var journal []provider.Message
			a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewRead(root), tool.NewBash(root), tool.NewWrite(root)),
				Record: func(m provider.Message) error { journal = append(journal, m); return nil },
			})
			starts := 0
			h := func(e Event) {
				if e.Kind == KindToolStart {
					starts++
				}
			}
			_, err := a.Prompt(context.Background(), "test failure handling", h)
			if err == nil || !strings.Contains(err.Error(), "stopped repeated failed "+name) || starts != 1 {
				t.Fatalf("%s batch=%v: starts=%d err=%v", name, sameBatch, starts, err)
			}
			if !reflect.DeepEqual(journal, a.History()) {
				t.Fatal("skipped batch not persisted")
			}
			for _, m := range journal {
				if m.Role == "tool" && (m.ToolCallID == "repeat" || m.ToolCallID == "skipped") && !strings.Contains(m.Content, "not executed") {
					t.Fatal(m.Content)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !os.IsNotExist(err) {
				t.Fatal("executed remaining batch")
			}
			p.script = [][]provider.Delta{{{ToolCalls: []provider.ToolCallDelta{first}}}, {{Content: "done"}}}
			if _, err := a.Prompt(context.Background(), "try a new turn", h); err != nil || starts != 2 {
				t.Fatalf("new turn: starts=%d err=%v", starts, err)
			}
		}
	}
}

func TestCorrectedAndSuccessfulReadCallsAreAllowed(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("ok"), 0600)
	var responses [][]provider.Delta
	for i, args := range []string{`{"filePath":"missing.txt"}`, `{"filePath":"a.txt"}`, `{"filePath":"a.txt"}`} {
		responses = append(responses, []provider.Delta{{ToolCalls: []provider.ToolCallDelta{{ID: string(rune('a' + i)), Name: "read", Args: args}}}})
	}
	p := &fakeProvider{script: append(responses, []provider.Delta{{Content: "done"}})}
	a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewRead(root))})
	if _, err := a.Prompt(context.Background(), "read the file", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedFailedEditStopsAndJournalsSkippedCalls(t *testing.T) {
	for _, sameBatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate responses", true: "same response"}[sameBatch], func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(file, []byte("red"), 0600); err != nil {
				t.Fatal(err)
			}
			args := `{"path":"sample.txt","old_string":"red","new_string":"blue"}`
			p := &fakeProvider{script: [][]provider.Delta{editResponse("success", args)}}
			failed := editResponse("failed", args)
			blocked := []provider.ToolCallDelta{
				{Index: 1, ID: "blocked", Name: "edit", Args: "{\n  \"new_string\": \"blue\", \"path\": \"sample.txt\", \"old_string\": \"red\"\n}"},
				{Index: 2, ID: "skipped", Name: "write", Args: `{"path":"should-not-exist","content":"bad"}`},
			}
			if sameBatch {
				failed[0].ToolCalls = append(failed[0].ToolCalls, blocked...)
				p.script = append(p.script, failed)
			} else {
				p.script = append(p.script, failed, []provider.Delta{{ToolCalls: blocked, FinishReason: "tool_calls"}})
			}
			var journal []provider.Message
			approvals := 0
			a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewEdit(root), tool.NewWrite(root)),
				Approver: ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) { approvals++; return true, nil }),
				Record:   func(m provider.Message) error { journal = append(journal, m); return nil },
			})
			_, err := a.Prompt(context.Background(), "change red to blue", nil)
			if err == nil || !strings.Contains(err.Error(), "repeated edit") {
				t.Fatalf("expected loop guard, got %v", err)
			}
			if approvals != 2 {
				t.Fatalf("approvals = %d; repeated failure and later calls must not ask or execute", approvals)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "blue" {
				t.Fatalf("lost successful edit: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
				t.Fatal("executed a later call after the guard stopped the batch")
			}
			if !reflect.DeepEqual(journal, a.History()) {
				t.Fatal("guard result was not persisted")
			}
			for _, id := range []string{"success", "failed", "blocked", "skipped"} {
				count := 0
				for _, m := range journal {
					if m.Role == "tool" && m.ToolCallID == id {
						count++
						if (id == "blocked" || id == "skipped") && !strings.Contains(m.Content, "not executed") {
							t.Fatalf("missing skipped result for %s: %q", id, m.Content)
						}
					}
				}
				if count != 1 {
					t.Fatalf("call %s has %d results", id, count)
				}
			}
			// A new user turn gets a fresh attempt, without poisoned loop state.
			p.script = [][]provider.Delta{editResponse("retry", args), {{Content: "done"}}}
			if _, err := a.Prompt(context.Background(), "retry", nil); err != nil || approvals != 3 {
				t.Fatalf("new turn: approvals=%d, err=%v", approvals, err)
			}
		})
	}
}

func TestEditLoopAllowsProgress(t *testing.T) {
	args := `{"path":"sample.txt","old_string":"red","new_string":"blue"}`
	for _, tc := range []struct {
		name, initial, want string
		responses           [][]provider.Delta
	}{
		{"corrected edit", "green", "blue", [][]provider.Delta{editResponse("failed", args), editResponse("fixed", `{"path":"sample.txt","old_string":"green","new_string":"blue"}`)}},
		{"reread before retry", "green", "green", [][]provider.Delta{editResponse("failed", args), {{ToolCalls: []provider.ToolCallDelta{{ID: "read", Name: "read", Args: `{"path":"sample.txt"}`}}}}, editResponse("retry", args)}},
		{"successful repeated edits", "red", "redxx", [][]provider.Delta{editResponse("first", `{"path":"sample.txt","old_string":"red","new_string":"redx"}`), editResponse("second", `{"path":"sample.txt","old_string":"red","new_string":"redx"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(file, []byte(tc.initial), 0600); err != nil {
				t.Fatal(err)
			}
			p := &fakeProvider{script: append(tc.responses, []provider.Delta{{Content: "done"}})}
			a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewEdit(root), tool.NewRead(root))})
			if _, err := a.Prompt(context.Background(), "make the change", nil); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != tc.want {
				t.Fatalf("file = %q, err=%v; want %q", data, err, tc.want)
			}
		})
	}
}

func TestRepeatedFailedEditWithReadsStops(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "style.css"), []byte("body {}\n````\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := `{"path":"style.css","old_string":"     2\t` + "````" + `","new_string":"","replace_all":true}`
	read := []provider.Delta{{ToolCalls: []provider.ToolCallDelta{{ID: "read", Name: "read", Args: `{"path":"style.css"}`}}}}
	retry := strings.Replace(args, `"new_string":""`, `"new_string":"body {}"`, 1)
	blocked := strings.Replace(args, `"replace_all":true`, `"replace_all":false`, 1)
	p := &fakeProvider{script: [][]provider.Delta{editResponse("first", args), read, editResponse("retry", retry), read, editResponse("blocked", blocked)}}
	approvals := 0
	a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewEdit(root), tool.NewRead(root)),
		Approver: ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) { approvals++; return true, nil }),
	})
	_, err := a.Prompt(context.Background(), "fix the stylesheet", nil)
	if err == nil || !strings.Contains(err.Error(), "repeated edit") || approvals != 2 {
		t.Fatalf("read/edit loop: approvals=%d, err=%v", approvals, err)
	}
	last := a.History()[len(a.History())-1]
	if last.ToolCallID != "blocked" || !strings.Contains(last.Content, "not executed") {
		t.Fatalf("missing skipped result: %+v", last)
	}
}

func TestSuccessfulWriteResetsFailedEditRetries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("green"), 0600); err != nil {
		t.Fatal(err)
	}
	args := `{"path":"sample.txt","old_string":"red","new_string":"blue"}`
	read := []provider.Delta{{ToolCalls: []provider.ToolCallDelta{{ID: "read", Name: "read", Args: `{"path":"sample.txt"}`}}}}
	p := &fakeProvider{script: [][]provider.Delta{
		editResponse("first", args), read, editResponse("retry", args),
		{{ToolCalls: []provider.ToolCallDelta{{ID: "write", Name: "write", Args: `{"path":"sample.txt","content":"red"}`}}}},
		editResponse("fixed", args), {{Content: "done"}},
	}}
	a := New(Options{Provider: p, Model: "test", Tools: tool.NewRegistry(tool.NewEdit(root), tool.NewRead(root), tool.NewWrite(root))})
	if _, err := a.Prompt(context.Background(), "change the file", nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "sample.txt")); err != nil || string(data) != "blue" {
		t.Fatalf("file=%q, err=%v", data, err)
	}
}
