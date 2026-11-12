package engine

import (
	"encoding/json"
	"testing"

	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/prompt"
)

// Every request carries the system prompt and tool schemas, so their size is
// a per-turn cost on every model. These budgets (see PERFORMANCE.md) catch
// growth; raise them only on purpose.
func TestRequestOverheadBudget(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	root := t.TempDir()
	overhead := func(e *Engine) int {
		c := e.cfg
		sys := prompt.Base(root) + prompt.SkillsIndex(e.skills) +
			prompt.Optional(prompt.Opts{Web: c.WebEnabled, Browser: c.BrowserEnabled, Desktop: c.DesktopEnabled, Plan: c.PlanGate, Sandbox: c.Sandbox})
		specs, _ := json.Marshal(e.tools().Specs())
		return (len(sys) + len(specs)) / 4 // ~4 characters per token
	}
	def := &Engine{root: root, cfg: config.Default()}
	got := overhead(def)
	t.Logf("default request overhead ~%d tokens (budget 1400)", got)
	if got > 1400 {
		t.Errorf("default request overhead ~%d tokens, budget 1400", got)
	}
	all := &Engine{root: root, cfg: config.Default(), indexed: true,
		skills: []prompt.Skill{{Name: "deploy", Description: "Ship the current branch to staging and run smoke tests."}}}
	all.cfg.PlanGate, all.cfg.Sandbox, all.cfg.WebEnabled, all.cfg.BraveSearchKey, all.cfg.BrowserEnabled, all.cfg.DesktopEnabled = true, "strict", true, "k", true, true
	got = overhead(all)
	t.Logf("all opt-ins request overhead ~%d tokens (budget 2700)", got)
	if got > 2700 {
		t.Errorf("all opt-ins request overhead ~%d tokens, budget 2700", got)
	}
	// Browser-only: one tool and its own short prompt, for small local windows.
	only := &Engine{root: root, cfg: config.Default()}
	only.cfg.BrowserOnly = true
	names := ""
	for _, tl := range only.tools().List() {
		names += tl.Name() + " "
	}
	specs, _ := json.Marshal(only.tools().Specs())
	got = (len(prompt.BrowserOnly(false)) + len(specs)) / 4
	t.Logf("browser-only request overhead ~%d tokens (budget 450)", got)
	if names != "browser " || got > 450 {
		t.Errorf("browser-only: tools %q, overhead ~%d tokens, budget 450", names, got)
	}
}
