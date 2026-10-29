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
	"sync"
	"time"

	"github.com/ajhous44/cody/v3/internal/clip"
	"github.com/ajhous44/cody/v3/internal/config"
	"github.com/ajhous44/cody/v3/internal/provider"
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
	mu   sync.Mutex // serializes appends (a background title write can race a turn)
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
	return s.AppendBatch([]Record{rec})
}

// AppendBatch writes and syncs records before the caller continues execution.
func (s *Store) AppendBatch(recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return err
	}
	now := time.Now()
	for i := range recs {
		if recs[i].Time.IsZero() {
			recs[i].Time = now
		}
		b, err := json.Marshal(recs[i])
		if err != nil {
			return err
		}
		if _, err := s.file.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return s.file.Sync()
}

func (s *Store) openLocked() error {
	if s.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// A crash may leave a partial JSON line; isolate it before the next append.
	info, err := f.Stat()
	if err == nil && info.Size() > 0 {
		var last [1]byte
		_, err = f.ReadAt(last[:], info.Size()-1)
		if err == nil && last[0] != '\n' {
			_, err = f.Write([]byte("\n"))
		}
	}
	if err != nil {
		f.Close()
		return err
	}
	s.file = f
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// validID reports whether id is safe to use as a session filename. Session
// ids arrive over the local API, so anything that could escape the session
// directory (separators, parent references) is rejected outright.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return !strings.Contains(id, "..")
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
	if !validID(id) {
		return "", nil, fmt.Errorf("invalid session id")
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
	titles := loadTitles(root)
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
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		info := Info{ID: id, Path: path, Started: fi.ModTime()}
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
		if t := titles[id]; t != "" {
			info.First = t
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Started.After(infos[j].Started) })
	return infos, nil
}

// Rename gives a session a custom title. Titles live beside the transcripts so
// long sessions never need a full scan.
func Rename(root, id, title string) error {
	if !validID(id) {
		return fmt.Errorf("invalid session id")
	}
	if _, err := os.Stat(filepath.Join(ProjectDir(root), id+".jsonl")); err != nil {
		return err
	}
	titlesMu.Lock()
	defer titlesMu.Unlock()
	titles := loadTitles(root)
	titles[id] = strings.TrimSpace(title)
	return saveTitles(root, titles)
}

// Delete removes a session transcript and any custom title. Deleting a
// missing session is a no-op so clients can retry safely.
func Delete(root, id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid session id")
	}
	if err := os.Remove(filepath.Join(ProjectDir(root), id+".jsonl")); err != nil && !os.IsNotExist(err) {
		return err
	}
	titlesMu.Lock()
	defer titlesMu.Unlock()
	titles := loadTitles(root)
	if _, ok := titles[id]; ok {
		delete(titles, id)
		return saveTitles(root, titles)
	}
	return nil
}

// titlesMu serializes titles.json updates (a background auto-title can race a
// rename) and guards titlesCache.
// ponytail: in-process lock; two cody processes renaming in one project at the same instant can still lose one update.
var (
	titlesMu    sync.Mutex
	titlesCache = map[string]cachedTitles{}
)

// cachedTitles lets every state broadcast skip re-parsing an unchanged file.
type cachedTitles struct {
	mod    time.Time
	size   int64
	titles map[string]string
}

// Title returns a session's custom title, or "" when it has none.
func Title(root, id string) string {
	path := titlesPath(root)
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	titlesMu.Lock()
	defer titlesMu.Unlock()
	c, ok := titlesCache[path]
	if !ok || !c.mod.Equal(info.ModTime()) || c.size != info.Size() {
		c = cachedTitles{mod: info.ModTime(), size: info.Size(), titles: loadTitles(root)}
		titlesCache[path] = c
	}
	return c.titles[id]
}

func titlesPath(root string) string {
	return filepath.Join(ProjectDir(root), "titles.json")
}

func loadTitles(root string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(titlesPath(root))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func saveTitles(root string, titles map[string]string) error {
	dir := ProjectDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(titles, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file keeps concurrent saves from writing into each other.
	f, err := os.CreateTemp(dir, "titles-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), titlesPath(root))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > max {
		s = clip.Bytes(s, max) + "..."
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
