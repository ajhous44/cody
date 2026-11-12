package bench

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportMediansAndPassRate(t *testing.T) {
	results := []Result{
		{Task: "edit", OK: true, WallMS: 100, TokensIn: 1000, ToolCalls: 2},
		{Task: "edit", OK: false, WallMS: 9000, TokensIn: 1200, ToolCalls: 9}, // one bad run cannot skew a median
		{Task: "edit", OK: true, WallMS: 120, TokensIn: 1100, ToolCalls: 3},
		{Task: "read", OK: true, WallMS: 50, TokensIn: 800},
	}
	r := newReport(Options{ProviderName: "ollama", Version: "test"}, "m", 3, results)
	if len(r.Summary) != 2 || r.Summary[0].Task != "edit" || r.Summary[1].Task != "read" {
		t.Fatalf("summary order: %+v", r.Summary)
	}
	e := r.Summary[0]
	if e.PassRate < 0.66 || e.PassRate > 0.67 || e.WallMS != 120 || e.TokensIn != 1100 || e.ToolCalls != 3 {
		t.Fatalf("edit summary: %+v", e)
	}
	if median([]int{4, 1, 3, 2}) != 2 {
		t.Fatal("even-length median")
	}
}

func TestCompareFlagsRegressions(t *testing.T) {
	base := Report{Provider: "ollama", Model: "m", Summary: []TaskSummary{
		{Task: "edit", PassRate: 1, TokensIn: 1000, WallMS: 100},
		{Task: "read", PassRate: 1, TokensIn: 800, WallMS: 50},
		{Task: "shell", PassRate: 0.5, TokensIn: 900},
	}}
	cur := Report{Provider: "ollama", Model: "m", Summary: []TaskSummary{
		{Task: "edit", PassRate: 1, TokensIn: 1090, WallMS: 900}, // +9% tokens, slower: fine
		{Task: "read", PassRate: 2.0 / 3, TokensIn: 800},         // pass rate dropped
		{Task: "shell", PassRate: 1, TokensIn: 1000},             // +11% tokens
		{Task: "fix-test", PassRate: 1, TokensIn: 5000},          // no baseline yet
	}}
	var out bytes.Buffer
	regs := Compare(&out, base, cur)
	if len(regs) != 2 || !strings.HasPrefix(regs[0], "read: pass rate") || !strings.HasPrefix(regs[1], "shell: median tokens in") {
		t.Fatalf("regressions: %q\n%s", regs, out.String())
	}
	if !strings.Contains(out.String(), "fix-test   new task") {
		t.Fatalf("new task not reported:\n%s", out.String())
	}
	out.Reset()
	cur.Model = "other"
	Compare(&out, base, cur)
	if !strings.Contains(out.String(), "baseline ran ollama/m") {
		t.Fatal("model mismatch not noted")
	}
	if _, err := loadReport(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing baseline accepted")
	}
}
