package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSkillsPrecedenceAndFrontmatter(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	writeSkill(t, user, "deploy", "---\nname: deploy\ndescription: user version\n---\nbody")
	writeSkill(t, project, "deploy", "---\nname: deploy\ndescription: project version\n---\nbody")
	writeSkill(t, user, "release-notes", "---\ndescription: >\n  Write release notes\n  from git history\n---\nbody")
	writeSkill(t, user, "no-frontmatter", "just text")
	if err := os.MkdirAll(filepath.Join(user, "empty-folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	skills := DiscoverSkills(project, user)
	got := map[string]string{}
	for _, s := range skills {
		got[s.Name] = s.Description
	}
	if len(skills) != 3 || got["deploy"] != "project version" || got["release-notes"] != "Write release notes from git history" {
		t.Fatalf("skills = %+v", skills)
	}
	if _, ok := got["no-frontmatter"]; !ok {
		t.Fatal("a skill without frontmatter should still load by folder name")
	}
	index := SkillsIndex(skills)
	if !strings.Contains(index, "  - deploy: project version") || !strings.Contains(index, "no-frontmatter: (no description)") {
		t.Fatalf("index:\n%s", index)
	}
	if SkillsIndex(nil) != "" {
		t.Fatal("no skills must cost no tokens")
	}
}
