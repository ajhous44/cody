package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSkills           = 50
	maxSkillDescription = 200
	skillFrontmatter    = 8 << 10
)

// Skill is a folder with a SKILL.md. Only its name and description sit in the
// prompt; the skill tool loads the body when the model asks for it.
type Skill struct {
	Name        string
	Description string
	Dir         string
	Mod         int64 // SKILL.md modification time, so edits reload the prompt
}

// SkillDirs lists where skills live, highest precedence first.
func SkillDirs(root, configDir string) []string {
	return []string{filepath.Join(root, ".cody", "skills"), filepath.Join(configDir, "skills")}
}

// DiscoverSkills reads <dir>/<name>/SKILL.md from each dir. A name found in an
// earlier dir wins, so project skills override user skills.
func DiscoverSkills(dirs ...string) []Skill {
	seen := map[string]bool{}
	var out []Skill
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name(), "SKILL.md")
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			s := readSkill(path)
			s.Dir, s.Mod = filepath.Dir(path), info.ModTime().UnixNano()
			if s.Name == "" {
				s.Name = e.Name()
			}
			if strings.ContainsAny(s.Name, `/\`) || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > maxSkills {
		out = out[:maxSkills]
	}
	return out
}

// readSkill parses the name and description from SKILL.md frontmatter:
// a "---" block of "key: value" lines, where ">" or "|" folds the indented
// lines that follow. Other keys are ignored.
func readSkill(path string) Skill {
	var s Skill
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, skillFrontmatter))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return s
	}
	var key string
	var folded []string
	flush := func() {
		if key == "description" && len(folded) > 0 {
			s.Description = strings.Join(folded, " ")
		}
		key, folded = "", nil
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		if key != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			folded = append(folded, strings.TrimSpace(line))
			continue
		}
		flush()
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			s.Name = v
		case "description":
			if v == ">" || v == "|" || v == ">-" || v == "|-" {
				key = "description"
			} else {
				s.Description = v
			}
		}
	}
	flush()
	return s
}

// SkillsIndex is the only part of skills that costs tokens up front.
func SkillsIndex(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- Skills: when a task matches one, call skill with its name to load its instructions first.\n")
	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		if r := []rune(desc); len(r) > maxSkillDescription {
			desc = string(r[:maxSkillDescription]) + "…"
		}
		fmt.Fprintf(&b, "  - %s: %s\n", s.Name, desc)
	}
	return b.String()
}
