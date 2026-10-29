package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/ajhous44/cody/v3/internal/prompt"
)

const (
	maxSkillBytes = 32 << 10
	maxSkillFiles = 50
)

// Skill loads a discovered skill on demand: its SKILL.md instructions plus a
// list of its other files, or one of those files. Reads stay inside the
// skill's folder.
type Skill struct {
	skills map[string]prompt.Skill
	names  []string
}

func NewSkill(skills []prompt.Skill) *Skill {
	s := &Skill{skills: map[string]prompt.Skill{}}
	for _, sk := range skills {
		s.skills[sk.Name] = sk
		s.names = append(s.names, sk.Name)
	}
	return s
}

func (*Skill) Name() string   { return "skill" }
func (*Skill) Mutating() bool { return false }
func (*Skill) Description() string {
	return "Load a skill's instructions by name (see the Skills list in the system prompt). Pass file to read one of the skill's other files."
}

func (s *Skill) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"name": map[string]any{"type": "string", "enum": s.names, "description": "Skill name."},
		"file": property("string", "Optional file inside the skill folder, as listed when the skill loads."),
	}, "name")
}

type skillArgs struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (*Skill) Summarize(args json.RawMessage) string {
	var a skillArgs
	_ = json.Unmarshal(args, &a)
	return strings.TrimSpace(a.Name + " " + a.File)
}

func (s *Skill) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	sk, ok := s.skills[a.Name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q; available: %s", a.Name, strings.Join(s.names, ", "))
	}
	dir, err := os.OpenRoot(sk.Dir)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if a.File != "" {
		data, err := readCapped(dir, a.File)
		if err != nil {
			return "", err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return "", fmt.Errorf("%s looks like a binary file", a.File)
		}
		return string(data), nil
	}
	data, err := readCapped(dir, "SKILL.md")
	if err != nil {
		return "", err
	}
	out := stripFrontmatter(string(data))
	if files := skillFiles(dir); len(files) > 0 {
		out += "\n\nOther files in this skill (load with file): " + strings.Join(files, ", ")
	}
	return out, nil
}

func readCapped(dir *os.Root, name string) ([]byte, error) {
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSkillBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillBytes {
		return append(data[:maxSkillBytes], "\n[truncated]"...), nil
	}
	return data, nil
}

func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---") {
		return s
	}
	if i := strings.Index(s[3:], "\n---"); i >= 0 {
		rest := s[3+i+4:]
		return strings.TrimLeft(rest, "\r\n")
	}
	return s
}

func skillFiles(dir *os.Root) []string {
	var files []string
	_ = fs.WalkDir(dir.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(files) >= maxSkillFiles {
			return fs.SkipDir
		}
		if d.IsDir() && p != "." && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if !d.IsDir() && p != "SKILL.md" {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}
