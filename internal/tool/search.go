package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ajhous44/cody/v3/internal/index"
)

const maxHitChars = 1500

type Search struct {
	root  string
	db    string
	model string // index identity the query embedding must match
	embed index.Embedder
}

func NewSearch(root, db, model string, embed index.Embedder) *Search {
	return &Search{root: root, db: db, model: model, embed: embed}
}

func (s *Search) Name() string { return "search" }

func (s *Search) Description() string {
	return "Semantic search over the workspace index. Returns the best matching code chunks with file paths and line numbers. Use when you do not know an exact identifier; use bash grep when you do. Requires `cody index` to have been run."
}

func (s *Search) Mutating() bool { return false }

func (s *Search) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"query": property("string", "Natural-language description or code fragment to search for."),
		"limit": property("integer", "Maximum number of chunks to return. Default 8."),
	}, "query")
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *Search) Summarize(args json.RawMessage) string {
	var a searchArgs
	_ = json.Unmarshal(args, &a)
	return a.Query
}

func (s *Search) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", errors.New("query is required")
	}
	hits, err := index.Search(ctx, s.db, s.embed, s.model, a.Query, a.Limit)
	if err != nil {
		return "", err
	}
	var head strings.Builder
	if d, err := index.Check(s.root, s.db); err == nil && d.Stale() {
		fmt.Fprintf(&head, "note: index is stale (%d changed, %d new, %d deleted since the last `cody index`); results may be incomplete\n\n", d.Changed, d.New, d.Deleted)
	}
	if len(hits) == 0 {
		return head.String() + "no matches", nil
	}
	var b strings.Builder
	b.WriteString(head.String())
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s:%d-%d (score %.2f)\n", h.Path, h.Start, h.End, h.Score)
		b.WriteString(truncateText(h.Text, maxHitChars))
		b.WriteString("\n")
	}
	return b.String(), nil
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... [chunk truncated; read the file for the rest]"
}
