package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ajhous44/cody/v3/internal/provider"
)

func TestExecuteToolTextGetsOneRecoveryAfterToolUse(t *testing.T) {
	bad := []provider.Delta{{Content: `<execute_tool> browser{action: open, url: "http://127.0.0.1:3137/"} </execute_tool>`}}
	real := []provider.Delta{{ToolCalls: []provider.ToolCallDelta{{ID: "actual", Name: "echo", Args: `{}`}}}}
	for _, afterTool := range []bool{false, true} {
		p := &fakeProvider{script: [][]provider.Delta{bad, real, {{Content: "done"}}}}
		expected := 1
		if afterTool {
			p.script = append([][]provider.Delta{real}, p.script...)
			expected++
		}
		ran := 0
		a := newTestAgent(p, &echoTool{run: func(string) (string, error) { ran++; return "ok", nil }}, nil)
		if _, err := a.Prompt(context.Background(), "inspect the demo", nil); err != nil {
			t.Fatal(err)
		}
		if ran != expected {
			t.Fatalf("executed text or lost real calls: %d", ran)
		}
		for _, m := range p.reqs[len(p.reqs)-1].Messages {
			if m.Content == toolSyntaxNudge {
				t.Fatal("recovery reminder persisted after success")
			}
		}
	}
}

func TestRepeatedExecuteToolTextStopsWithoutExecution(t *testing.T) {
	bad := []provider.Delta{{Content: `<execute_tool> echo{} </execute_tool>`}}
	p := &fakeProvider{script: [][]provider.Delta{bad, bad, bad}}
	a := newTestAgent(p, &echoTool{run: func(string) (string, error) { t.Fatal("executed a text command"); return "", nil }}, nil)
	if _, err := a.Prompt(context.Background(), "inspect the demo", nil); err == nil || !strings.Contains(err.Error(), "repeatedly wrote tool commands") {
		t.Fatalf("missing bounded stop: %v", err)
	}
	if len(p.reqs) != 2 {
		t.Fatalf("retried %d times", len(p.reqs))
	}
}
