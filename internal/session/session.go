package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ajhous44/cody/internal/config"
	"github.com/ajhous44/cody/internal/provider"
)

type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type Record struct {
	Time       time.Time         `json:"time"`
	Type       string            `json:"type"` // meta | message | usage
	Role       string            `json:"role,omitempty"`
	Text       string            `json:"text,omitempty"`
	ToolCalls  []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
	Usage      *provider.Usage   `json:"usage,omitempty"`
}

type Store struct {
	id   string
	path string
	file *os.File
}

func New(root, id string) *Store {
	if id == "" {
		id = NewID()
	}
	return &Store{id: id, path: filepath.Join(ProjectDir(root), id+".jsonl")}
}

func NewID() string {
	return time.Now().Format("20060102-150405") + "-" + randHex(3)
}

func ProjectDir(root string) string {
	sum := sha1.Sum([]byte(root))
	base := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ' ':
			return '-'
		}
		return r
	}, filepath.Base(root))
	return filepath.Join(config.Dir(), "sessions", base+"-"+hex.EncodeToString(sum[:])[:8])
}

func (s *Store) ID() string   { return s.id }
func (s *Store) Path() string { return s.path }

func (s *Store) Append(rec Record) error {
	if s.file == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		s.file = f
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.file.Sync()
}

func (s *Store) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Load resolves id ("last" allowed) and returns the resolved id and records.
func Load(root, id string) (string, []Record, error) {
	if id == "last" {
		infos, err := List(root)
		if err != nil {
			return "", nil, err
		}
		if len(infos) == 0 {
			return "", nil, fmt.Errorf("no sessions recorded for this workspace")
		}
		id = infos[0].ID
	}
	path := filepath.Join(ProjectDir(root), id+".jsonl")
	recs, err := loadFile(path, 0)
	if err != nil {
		return "", nil, err
	}
	return id, recs, nil
}

func loadFile(path string, limit int) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
		if limit > 0 && len(recs) >= limit {
			break
		}
	}
	return recs, sc.Err()
}

type Info struct {
	ID      string
	Started time.Time
	First   string
	Path    string
}

func List(root string) ([]Info, error) {
	dir := ProjectDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info := Info{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Path: path, Started: fi.ModTime()}
		recs, _ := loadFile(path, 40)
		for _, r := range recs {
			if r.Type == "meta" {
				if !r.Time.IsZero() {
					info.Started = r.Time
				}
				continue
			}
			if r.Role == "user" && info.First == "" {
				info.First = firstLine(r.Text, 80)
			}
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Started.After(infos[j].Started) })
	return infos, nil
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}
