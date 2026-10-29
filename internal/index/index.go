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

	"github.com/ajhous44/cody/v3/internal/config"
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

	dir, err := os.OpenRoot(root)
	if err != nil {
		return stats, err
	}
	defer dir.Close()
	seen := map[string]bool{}
	var pending []*pendingFile
	queued := 0
	err = eachFile(root, opts.MaxFileBytes, func(rel string, size, mtime int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[rel] = true
		f, err := prepareFile(db, dir, rel, size, mtime, opts)
		if err != nil {
			return err
		}
		if f == nil {
			stats.Unchanged++
			return nil
		}
		pending = append(pending, f)
		if queued += len(f.chunks); queued >= opts.Batch {
			err = flushPending(ctx, db, pending, e, opts, &stats)
			pending, queued = pending[:0], 0
		}
		return err
	})
	if err == nil {
		err = flushPending(ctx, db, pending, e, opts, &stats)
	}
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
		return fn(rel, info.Size(), info.ModTime().UnixNano())
	})
}

// pendingFile is a changed file whose chunks wait for a shared embed batch.
type pendingFile struct {
	rel, hash   string
	size, mtime int64
	chunks      []Chunk
}

// prepareFile reads and chunks a changed file. It returns nil when the file is
// unreadable or its content is already indexed.
func prepareFile(db *sql.DB, dir *os.Root, rel string, size, mtime int64, opts Options) (*pendingFile, error) {
	data, err := dir.ReadFile(rel)
	if err != nil {
		return nil, nil
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	var old string
	_ = db.QueryRow(`SELECT hash FROM files WHERE path = ?`, rel).Scan(&old)
	if old == hash {
		// Content is current; refresh metadata so a touched file is not
		// reported as drift forever.
		_, err := db.Exec(`UPDATE files SET size = ?, mtime = ? WHERE path = ?`, size, mtime, rel)
		return nil, err
	}
	f := &pendingFile{rel: rel, hash: hash, size: size, mtime: mtime}
	if !hasBinaryByte(data) {
		f.chunks = chunksFor(string(data), opts.MaxLines, opts.Overlap)
	}
	return f, nil
}

// flushPending embeds the chunks of several files in shared batches, so a
// repository of small files costs one request per batch instead of one per
// file (round trips dominate with hosted embeddings), then commits each file.
func flushPending(ctx context.Context, db *sql.DB, files []*pendingFile, e Embedder, opts Options, stats *Stats) error {
	var texts, owners []string
	for _, f := range files {
		for _, c := range f.chunks {
			texts = append(texts, c.Text)
			owners = append(owners, f.rel)
		}
	}
	vecs := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += opts.Batch {
		end := min(start+opts.Batch, len(texts))
		batch, err := e.Embed(ctx, texts[start:end])
		if err != nil {
			return fmt.Errorf("embed %s: %w", owners[start], err)
		}
		if len(batch) != end-start {
			return fmt.Errorf("embed %s: got %d vectors for %d chunks", owners[start], len(batch), end-start)
		}
		vecs = append(vecs, batch...)
	}
	for _, f := range files {
		if err := writeFile(db, f, vecs[:len(f.chunks)]); err != nil {
			return err
		}
		vecs = vecs[len(f.chunks):]
		if len(f.chunks) == 0 {
			// Binary or empty: recorded so it is not reported as drift, but nothing was indexed.
			continue
		}
		stats.Files++
		stats.Chunks += len(f.chunks)
		stats.Embedded += len(f.chunks)
		if opts.Progress != nil {
			opts.Progress(f.rel)
		}
	}
	return nil
}

func writeFile(db *sql.DB, f *pendingFile, vecs [][]float32) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chunks WHERE path = ?`, f.rel); err != nil {
		return err
	}
	for i, c := range f.chunks {
		if _, err := tx.Exec(
			`INSERT INTO chunks(path, start_line, end_line, text, vec) VALUES(?, ?, ?, ?, ?)`,
			f.rel, c.Start, c.End, c.Text, encodeVec(vecs[i]),
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO files(path, hash, size, mtime) VALUES(?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET hash = excluded.hash, size = excluded.size, mtime = excluded.mtime`,
		f.rel, f.hash, f.size, f.mtime,
	); err != nil {
		return err
	}
	return tx.Commit()
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
// vector database extension. A non-empty model must match the one the index
// was built with; vectors from different models are not comparable.
func Search(ctx context.Context, dbPath string, e Embedder, model, query string, limit int) ([]Hit, error) {
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
	var built string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'embed_model'`).Scan(&built); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if model != "" && built != "" && built != model {
		return nil, fmt.Errorf("index was built with embedding model %q but %q is configured; run `cody index --rebuild`", built, model)
	}
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
