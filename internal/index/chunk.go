package index

import (
	"io/fs"
	"strings"
)

type Chunk struct {
	Start int
	End   int
	Text  string
}

// chunksFor splits content into overlapping line windows. A chunk is at most
// maxLines long; consecutive chunks share overlap lines so symbols that sit on
// a boundary still land whole in at least one chunk.
func chunksFor(content string, maxLines, overlap int) []Chunk {
	content = strings.TrimRight(content, "\r\n")
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if maxLines < 1 {
		maxLines = 1
	}
	if overlap < 0 || overlap >= maxLines {
		overlap = 0
	}
	var out []Chunk
	for start := 0; start < len(lines); {
		end := start + maxLines
		if end > len(lines) {
			end = len(lines)
		}
		text := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(text) != "" {
			out = append(out, Chunk{Start: start + 1, End: end, Text: text})
		}
		if end == len(lines) {
			break
		}
		start = end - overlap
	}
	return out
}

var skipIndexDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".next": true, ".cache": true, ".idea": true, ".vscode": true,
	"bin": true, "obj": true, "coverage": true, "tmp": true, "log": true,
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || skipIndexDirs[name]
}

var skipIndexExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".bmp": true, ".svg": true, ".pdf": true,
	".zip": true, ".gz": true, ".tar": true, ".bz2": true, ".xz": true, ".7z": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".mov": true, ".wav": true, ".webm": true,
	".exe": true, ".dll": true, ".dylib": true, ".so": true, ".a": true,
	".o": true, ".class": true, ".jar": true, ".wasm": true, ".pyc": true,
}

func includeFile(rel string, d fs.DirEntry, maxBytes int64) bool {
	name := d.Name()
	if strings.HasPrefix(name, ".") {
		return false
	}
	if strings.Contains(name, ".min.") {
		return false
	}
	if skipIndexExt[strings.ToLower(ext(name))] {
		return false
	}
	info, err := d.Info()
	if err != nil || info.Size() > maxBytes {
		return false
	}
	return true
}

func ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

func hasBinaryByte(data []byte) bool {
	limit := len(data)
	if limit > 8<<10 {
		limit = 8 << 10
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}
