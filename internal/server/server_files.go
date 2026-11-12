package server

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"net/http"

	"github.com/ajhous44/cody/v3/internal/clip"
	"github.com/ajhous44/cody/v3/internal/tool"
)

const (
	maxPreviewBytes = 512 << 10
	maxDiffBytes    = 200 << 10
	maxDiffLines    = 2000
	gitTimeout      = 10 * time.Second
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, "venv": true, "dist": true, "build": true,
	"target": true, ".next": true, ".cache": true,
}

type fileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// handleFiles lists one directory of the active workspace. The client walks
// the tree lazily, so large repositories stay cheap.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootFor(r.URL.Query().Get("root"))
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		rel = "."
	}
	dir, rel, err := tool.OpenWorkspace(root, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer dir.Close()
	entries, err := fs.ReadDir(dir.FS(), filepath.ToSlash(rel))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && skipDirs[name] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{Name: name, Dir: e.IsDir(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	relOut := filepath.ToSlash(rel)
	if relOut == "." {
		relOut = ""
	}
	s.writeJSON(w, map[string]any{"root": root, "path": relOut, "entries": out})
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootFor(r.URL.Query().Get("root"))
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	dir, rel, err := tool.OpenWorkspace(root, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer dir.Close()
	info, err := dir.Stat(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > maxPreviewBytes {
		s.writeJSON(w, map[string]any{"root": root, "path": rel, "size": info.Size(), "tooLarge": true})
		return
	}
	data, err := dir.ReadFile(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if bytes.IndexByte(data, 0) >= 0 {
		s.writeJSON(w, map[string]any{"root": root, "path": rel, "size": info.Size(), "binary": true})
		return
	}
	s.writeJSON(w, map[string]any{"root": root, "path": rel, "size": info.Size(), "content": string(data)})
}

// handleFS is a directory-only browser used by the "add project" picker.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = homeDir()
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		http.Error(w, "not a directory: "+abs, http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	type dirEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, dirEntry{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	s.writeJSON(w, map[string]any{
		"path":   abs,
		"parent": parent,
		"home":   homeDir(),
		"dirs":   dirs,
	})
}

type gitFile struct {
	Path  string `json:"path"`
	Code  string `json:"code"`
	Label string `json:"label"`
}

func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootFor(r.URL.Query().Get("root"))
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	// One probe serves every tab switch and turn end for a few seconds; the
	// mutex also serializes concurrent clicks instead of storming git.
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if e, ok := s.gitCache[root]; ok && time.Since(e.at) < gitStatusTTL {
		s.writeJSON(w, map[string]any{"root": root, "repo": true, "branch": e.branch, "files": e.files})
		return
	}
	ctx := r.Context()
	out, err := runGit(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		s.writeJSON(w, map[string]any{"root": root, "repo": false, "files": []gitFile{}})
		return
	}
	branch, _ := runGit(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	status, _ := runGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	branch = strings.TrimSpace(branch)
	files := parseGitStatus(status)
	s.gitCache[root] = gitStatusCache{at: time.Now(), branch: branch, files: files}
	s.writeJSON(w, map[string]any{
		"root":   root,
		"repo":   true,
		"branch": branch,
		"files":  files,
	})
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	root, ok := s.rootFor(r.URL.Query().Get("root"))
	if !ok {
		http.Error(w, "unknown project", http.StatusBadRequest)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	diff, err := gitDiff(r.Context(), root, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	diff, truncated := capDiff(diff)
	s.writeJSON(w, map[string]any{"root": root, "path": rel, "diff": diff, "truncated": truncated})
}

// capDiff bounds diff output on line boundaries so a huge change cannot
// freeze the client with tens of thousands of nodes.
func capDiff(diff string) (string, bool) {
	truncated := false
	if len(diff) > maxDiffBytes {
		diff = clip.Bytes(diff, maxDiffBytes)
		truncated = true
	}
	if strings.Count(diff, "\n")+1 > maxDiffLines {
		idx := 0
		for i := 0; i < maxDiffLines; i++ {
			j := strings.IndexByte(diff[idx:], '\n')
			if j < 0 {
				idx = len(diff)
				break
			}
			idx += j + 1
		}
		diff = diff[:idx]
		truncated = true
	}
	return diff, truncated
}

func parseGitStatus(out string) []gitFile {
	files := []gitFile{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, `"`)
		files = append(files, gitFile{Path: path, Code: code, Label: gitLabel(code)})
	}
	return files
}

func gitLabel(code string) string {
	switch code {
	case "??":
		return "untracked"
	case "AM": // the fallback below would call it modified
		return "added"
	case "C":
		return "copied"
	case "UU":
		return "conflict"
	case "":
		return "clean"
	}
	switch {
	case strings.Contains(code, "M"):
		return "modified"
	case strings.Contains(code, "A"):
		return "added"
	case strings.Contains(code, "D"):
		return "deleted"
	case strings.Contains(code, "R"):
		return "renamed"
	}
	return code
}

func gitDiff(ctx context.Context, root, rel string) (string, error) {
	if rel == "" {
		out, _ := runGit(ctx, root, "diff", "HEAD")
		return out, nil
	}
	if _, err := tool.Resolve(root, rel); err != nil {
		return "", err
	}
	untracked, _ := runGit(ctx, root, "ls-files", "--others", "--exclude-standard", "--", rel)
	if strings.TrimSpace(untracked) != "" {
		out, _ := runGit(ctx, root, "diff", "--no-index", "--", os.DevNull, filepath.Join(root, rel))
		return out, nil
	}
	out, err := runGit(ctx, root, "diff", "HEAD", "--", rel)
	if err != nil && strings.TrimSpace(out) == "" {
		out, _ = runGit(ctx, root, "diff", "--", rel)
	}
	return out, nil
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
