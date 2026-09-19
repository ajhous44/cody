package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/ajhous44/cody/internal/config"
)

// Embedder turns text into vectors. Implementations must return exactly one
// vector per input, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type EmbedFunc func(ctx context.Context, texts []string) ([][]float32, error)

func (f EmbedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

type Options struct {
	Rebuild      bool
	Model        string
	MaxFileBytes int64
	MaxLines     int
	Overlap      int
	Batch        int
	Progress     func(path string)
}

const defaultMaxFileBytes = 512 << 10

func (o *Options) defaults() {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = defaultMaxFileBytes
	}
	if o.MaxLines <= 0 {
		o.MaxLines = 80
	}
	if o.Overlap <= 0 {
		o.Overlap = 10
	}
	if o.Batch <= 0 {
		o.Batch = 32
	}
}

type Stats struct {
	Files     int
	Chunks    int
	Embedded  int
	Unchanged int
	Removed   int
}

type Hit struct {
	Path  string
	Start int
	End   int
	Text  string
	Score float32
}

// Path is the SQLite file for a workspace, kept under ~/.cody/index. The
// database never lives in the repo and is safe to delete at any time.
func Path(root string) string {
	sum := sha256.Sum256([]byte(root))
	base := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ' ':
			return '-'
		}
		return r
	}, filepath.Base(root))
	return filepath.Join(config.Dir(), "index", base+"-"+hex.EncodeToString(sum[:])[:8]+".db")
}

func Index(ctx context.Context, root, dbPath string, e Embedder, opts Options) (Stats, error) {
	opts.defaults()
	var stats Stats
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return stats, err
	}
	db, err := open(dbPath)
	if err != nil {
		return stats, err
	}
	defer db.Close()
	if opts.Rebuild {
		if _, err := db.Exec(`DELETE FROM chunks; DELETE FROM files; DELETE FROM meta;`); err != nil {
			return stats, err
		}
	}
	if err := checkModel(db, opts.Model, opts.Rebuild); err != nil {
		return stats, err
	}

	seen := map[string]bool{}
	err = eachFile(root, opts.MaxFileBytes, func(rel string, size, mtime int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[rel] = true
		changed, err := indexFile(ctx, db, root, rel, size, mtime, e, opts, &stats)
		if err != nil {
			return err
		}
		if changed {
			if opts.Progress != nil {
				opts.Progress(rel)
			}
		} else {
			stats.Unchanged++
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	removed, err := prune(db, seen)
	if err != nil {
		return stats, err
	}
	stats.Removed = removed
	return stats, nil
}

// Drift counts workspace changes since the last index run.
type Drift struct {
	Changed int
	New     int
	Deleted int
}

func (d Drift) Stale() bool { return d.Changed+d.New+d.Deleted > 0 }

// Check compares the workspace against the stored index without embedding
// anything. It is cheap enough to call before a search.
func Check(root, dbPath string) (Drift, error) {
	var d Drift
	if _, err := os.Stat(dbPath); err != nil {
		return d, err
	}
	db, err := open(dbPath)
	if err != nil {
		return d, err
	}
	defer db.Close()
	type state struct{ size, mtime int64 }
	known := map[string]state{}
	rows, err := db.Query(`SELECT path, size, mtime FROM files`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var p string
		var s state
		if err := rows.Scan(&p, &s.size, &s.mtime); err != nil {
			rows.Close()
			return d, err
		}
		known[p] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	seen := map[string]bool{}
	err = eachFile(root, defaultMaxFileBytes, func(rel string, size, mtime int64) error {
		seen[rel] = true
		prev, ok := known[rel]
		switch {
		case !ok:
			d.New++
		case prev.size != size || prev.mtime != mtime:
			d.Changed++
		}
		return nil
	})
	if err != nil {
		return d, err
	}
	for p := range known {
		if !seen[p] {
			d.Deleted++
		}
	}
	return d, nil
}

func eachFile(root string, maxBytes int64, fn func(rel string, size, mtime int64) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if !includeFile(rel, d, maxBytes) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return fn(rel, info.Size(), info.ModTime().Unix())
	})
}

func indexFile(ctx context.Context, db *sql.DB, root, rel string, size, mtime int64, e Embedder, opts Options, stats *Stats) (bool, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return false, nil
	}
	if hasBinaryByte(data) {
		return false, nil
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	var old string
	_ = db.QueryRow(`SELECT hash FROM files WHERE path = ?`, rel).Scan(&old)
	if old == hash {
		// Content is current; refresh metadata so a touched file is not
		// reported as drift forever.
		if _, err := db.Exec(`UPDATE files SET size = ?, mtime = ? WHERE path = ?`, size, mtime, rel); err != nil {
			return false, err
		}
		return false, nil
	}
	chunks := chunksFor(string(data), opts.MaxLines, opts.Overlap)
	if len(chunks) == 0 {
		return false, nil
	}
	vecs := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += opts.Batch {
		end := start + opts.Batch
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, c.Text)
		}
		batch, err := e.Embed(ctx, texts)
		if err != nil {
			return false, fmt.Errorf("embed %s: %w", rel, err)
		}
		if len(batch) != len(texts) {
			return false, fmt.Errorf("embed %s: got %d vectors for %d chunks", rel, len(batch), len(texts))
		}
		vecs = append(vecs, batch...)
	}

	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chunks WHERE path = ?`, rel); err != nil {
		return false, err
	}
	for i, c := range chunks {
		if _, err := tx.Exec(
			`INSERT INTO chunks(path, start_line, end_line, text, vec) VALUES(?, ?, ?, ?, ?)`,
			rel, c.Start, c.End, c.Text, encodeVec(vecs[i]),
		); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO files(path, hash, size, mtime) VALUES(?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET hash = excluded.hash, size = excluded.size, mtime = excluded.mtime`,
		rel, hash, size, mtime,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	stats.Files++
	stats.Chunks += len(chunks)
	stats.Embedded += len(chunks)
	return true, nil
}

func prune(db *sql.DB, seen map[string]bool) (int, error) {
	rows, err := db.Query(`SELECT path FROM files`)
	if err != nil {
		return 0, err
	}
	var gone []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[p] {
			gone = append(gone, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range gone {
		if _, err := db.Exec(`DELETE FROM chunks WHERE path = ?; DELETE FROM files WHERE path = ?`, p, p); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}

// Search returns the best-matching chunks by cosine similarity. Vectors are
// scored in Go: at workspace scale a full scan is milliseconds and needs no
// vector database extension.
func Search(ctx context.Context, dbPath string, e Embedder, query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 8
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, errors.New("workspace is not indexed yet (run `cody index` first)")
	}
	db, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	vecs, err := e.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, errors.New("embedding model returned no vector for the query")
	}
	q := vecs[0]
	qnorm := norm(q)
	if qnorm == 0 {
		return nil, errors.New("query produced an empty embedding")
	}
	rows, err := db.Query(`SELECT path, start_line, end_line, text, vec FROM chunks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]Hit, 0, limit)
	for rows.Next() {
		var h Hit
		var blob []byte
		if err := rows.Scan(&h.Path, &h.Start, &h.End, &h.Text, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		vn := norm(v)
		if vn == 0 {
			continue
		}
		h.Score = float32(dot(q, v) / (qnorm * vn))
		hits = insertHit(hits, h, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits, nil
}

func insertHit(hits []Hit, h Hit, limit int) []Hit {
	if len(hits) < limit {
		return append(hits, h)
	}
	worst := 0
	for i := range hits {
		if hits[i].Score < hits[worst].Score {
			worst = i
		}
	}
	if h.Score > hits[worst].Score {
		hits[worst] = h
	}
	return hits
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS files (path TEXT PRIMARY KEY, hash TEXT NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS chunks (id INTEGER PRIMARY KEY, path TEXT NOT NULL, start_line INTEGER NOT NULL, end_line INTEGER NOT NULL, text TEXT NOT NULL, vec BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS chunks_path ON chunks(path);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func checkModel(db *sql.DB, model string, rebuild bool) error {
	if model == "" {
		return nil
	}
	var prev string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'embed_model'`).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = db.Exec(`INSERT INTO meta(key, value) VALUES('embed_model', ?)`, model)
		return err
	case err != nil:
		return err
	case prev != model && !rebuild:
		return fmt.Errorf("index was built with embedding model %q; run `cody index --rebuild`", prev)
	}
	return nil
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func norm(v []float32) float64 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	return math.Sqrt(sum)
}
