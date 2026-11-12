package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ajhous44/cody/v3/internal/session"
)

// Spaces are persisted in ~/.cody/spaces.json so a project list survives
// restarts of the CLI and the macOS app.
func loadSpaces(path string) []Space {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var spaces []Space
	if json.Unmarshal(b, &spaces) != nil {
		return nil
	}
	out := spaces[:0]
	for _, sp := range spaces {
		if sp.Path == "" {
			continue
		}
		out = append(out, Space{Name: spaceName(sp.Name, sp.Path), Path: filepath.Clean(sp.Path)})
	}
	return out
}

func (s *Server) saveSpaces() error {
	if err := os.MkdirAll(filepath.Dir(s.spacesFile), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.spaces, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.spacesFile + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.spacesFile)
}

func spaceName(name, path string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return filepath.Base(path)
}

func (s *Server) hasSpace(path string) bool {
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.spaces {
		if sp.Path == path {
			return true
		}
	}
	return false
}

func (s *Server) handleSpaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		spaces := append([]Space(nil), s.spaces...)
		active := s.activeRoot
		s.mu.Unlock()
		s.writeJSON(w, map[string]any{"spaces": spaces, "active": active})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if !s.decode(w, r, &body) {
			return
		}
		path := strings.TrimSpace(body.Path)
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		path = filepath.Clean(path)
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			http.Error(w, "not a directory: "+path, http.StatusBadRequest)
			return
		}
		if path == homeDir() {
			http.Error(w, "the home directory is not a workspace", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if !s.hasSpaceLocked(path) {
			s.spaces = append(s.spaces, Space{Name: spaceName(body.Name, path), Path: path})
		}
		err = s.saveSpaces()
		s.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.switchSpace(path); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.writeJSON(w, map[string]any{"spaces": s.spaceList(), "active": path})
	case http.MethodDelete:
		var body struct {
			Path string `json:"path"`
		}
		if !s.decodeBody(w, r, &body) {
			return
		}
		path := filepath.Clean(strings.TrimSpace(body.Path))
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		active := path == s.activeRoot
		next := ""
		if active {
			for _, sp := range s.spaces {
				if sp.Path != path {
					next = sp.Path
					break
				}
			}
		}
		s.mu.Unlock()
		if active {
			if next == "" {
				http.Error(w, "cannot remove the last project", http.StatusBadRequest)
				return
			}
			if err := s.switchSpace(next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		s.mu.Lock()
		kept := s.spaces[:0]
		for _, sp := range s.spaces {
			if sp.Path != path {
				kept = append(kept, sp)
			}
		}
		s.spaces = kept
		err := s.saveSpaces()
		s.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.dropRunner(path)
		s.broadcast(s.stateEvent())
		s.writeJSON(w, map[string]any{"spaces": s.spaceList()})
	case http.MethodPatch:
		var body struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if !s.decodeBody(w, r, &body) {
			return
		}
		path := filepath.Clean(strings.TrimSpace(body.Path))
		name := strings.TrimSpace(body.Name)
		if path == "" || name == "" {
			http.Error(w, "path and name are required", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		found := false
		for i := range s.spaces {
			if s.spaces[i].Path == path {
				s.spaces[i].Name = name
				found = true
				break
			}
		}
		var err error
		if found {
			err = s.saveSpaces()
		}
		s.mu.Unlock()
		if !found {
			http.Error(w, "unknown project", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.broadcast(s.stateEvent())
		s.writeJSON(w, map[string]any{"spaces": s.spaceList()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) hasSpaceLocked(path string) bool {
	path = filepath.Clean(path)
	for _, sp := range s.spaces {
		if sp.Path == path {
			return true
		}
	}
	return false
}

func (s *Server) handleSpaceSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	path := filepath.Clean(strings.TrimSpace(body.Path))
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := s.switchSpace(path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeJSON(w, map[string]any{"active": path})
}

func (s *Server) spaceList() []Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Space(nil), s.spaces...)
}

// switchSpace only changes which project is focused. Running turns in other
// projects keep going; nothing is cancelled or blocked.
func (s *Server) switchSpace(root string) error {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return errors.New("not a directory: " + root)
	}
	if _, err := s.runnerFor(root); err != nil {
		return err
	}
	s.mu.Lock()
	s.activeRoot = root
	s.mu.Unlock()
	s.broadcast(s.stateEventFull())
	return nil
}

func (s *Server) dropRunner(root string) {
	s.mu.Lock()
	r := s.runners[root]
	delete(s.runners, root)
	if r == nil {
		s.mu.Unlock()
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	s.mu.Unlock()
	r.eng.Close()
}

type sessionInfo struct {
	ID      string    `json:"id"`
	Started time.Time `json:"started"`
	First   string    `json:"first"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	root := s.activeRootPath()
	active := ""
	if rr := s.active(); rr != nil {
		active = rr.eng.SessionID()
	}
	if p := strings.TrimSpace(r.URL.Query().Get("path")); p != "" {
		p = filepath.Clean(p)
		if !s.hasSpace(p) {
			http.Error(w, "unknown project", http.StatusBadRequest)
			return
		}
		root = p
		if root != s.activeRootPath() {
			active = ""
		}
	}
	infos, err := session.List(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]sessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, sessionInfo{ID: info.ID, Started: info.Started, First: info.First})
	}
	s.writeJSON(w, map[string]any{"sessions": out, "active": active})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleSessionDelete(w, r)
		return
	}
	s.handleSessionOpen(w, r)
}

// handleSessionFork copies a thread up to (not including) one of its user
// turns into a new thread and opens it; turn 0 copies the whole thread. The
// reply carries that turn's text so the composer can offer it for editing.
func (s *Server) handleSessionFork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Path string `json:"path"`
		Turn int    `json:"turn"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	root, ok := s.rootFor(body.Path)
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rr, err := s.runnerFor(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.reserve(rr) {
		http.Error(w, "a turn is running in this project", http.StatusConflict)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = rr.eng.SessionID()
	}
	forked, prefill, err := session.Fork(root, id, body.Turn)
	if err == nil {
		err = rr.eng.Resume(forked)
	}
	err = errors.Join(err, s.release(rr))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if root != s.activeRootPath() {
		if err := s.switchSpace(root); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.broadcast(s.stateEventFull())
	s.writeJSON(w, map[string]string{"session": forked, "prefill": prefill})
}

func (s *Server) handleSessionOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	root, ok := s.rootFor(body.Path)
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rr, err := s.runnerFor(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.reserve(rr) {
		http.Error(w, "a turn is running in this project", http.StatusConflict)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" || id == "new" {
		err = rr.eng.NewSession()
	} else {
		err = rr.eng.Resume(id)
	}
	err = errors.Join(err, s.release(rr))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if root != s.activeRootPath() {
		if err := s.switchSpace(root); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.broadcast(s.stateEventFull())
	s.writeJSON(w, map[string]string{"session": rr.eng.SessionID()})
}

func (s *Server) handleSessionRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.ID)
	title := strings.TrimSpace(body.Title)
	if id == "" || title == "" {
		http.Error(w, "id and title are required", http.StatusBadRequest)
		return
	}
	root, ok := s.rootFor(body.Path)
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	if err := session.Rename(root, id, title); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeJSON(w, map[string]string{"title": title})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	root, ok := s.rootFor(body.Path)
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rr, err := s.runnerFor(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.reserve(rr) {
		http.Error(w, "a turn is running in this project", http.StatusConflict)
		return
	}
	if id == rr.eng.SessionID() {
		err = rr.eng.NewSession()
	}
	if err == nil {
		err = session.Delete(root, id)
	}
	err = errors.Join(err, s.release(rr))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.broadcast(s.stateEventFull())
	s.writeJSON(w, map[string]any{"ok": true, "session": rr.eng.SessionID()})
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// rootFor resolves an optional project path, defaulting to the active one.
func (s *Server) rootFor(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return s.activeRootPath(), true
	}
	path = filepath.Clean(path)
	if !s.hasSpace(path) {
		return "", false
	}
	return path, true
}
