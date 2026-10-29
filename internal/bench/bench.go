package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ajhous44/cody/v3/internal/agent"
	"github.com/ajhous44/cody/v3/internal/prompt"
	"github.com/ajhous44/cody/v3/internal/provider"
	"github.com/ajhous44/cody/v3/internal/tool"
)

type Options struct {
	ProviderName string
	BaseURL      string
	APIKey       string
	Model        string
	Runs         int
	Keep         bool
	JSON         bool
	Out          io.Writer
	Progress     io.Writer
}

type Result struct {
	Task             string  `json:"task"`
	Run              int     `json:"run"`
	OK               bool    `json:"ok"`
	Note             string  `json:"note,omitempty"`
	WallMS           int64   `json:"wall_ms"`
	TTFTMS           int64   `json:"ttft_ms"`
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	TokensPerSec     float64 `json:"tokens_per_sec"`
	ToolCalls        int     `json:"tool_calls"`
	Error            string  `json:"error,omitempty"`
}

func SelectTasks(name string) ([]Task, error) {
	all := Builtin()
	if name == "" {
		return all, nil
	}
	for _, t := range all {
		if t.Name == name {
			return []Task{t}, nil
		}
	}
	return nil, fmt.Errorf("unknown task %q (use --list)", name)
}

func PrintList(w io.Writer, tasks []Task) {
	fmt.Fprintln(w, "benchmark tasks:")
	for _, t := range tasks {
		fmt.Fprintf(w, "  %-12s %s\n", t.Name, t.Description)
	}
}

func Run(ctx context.Context, opts Options, tasks []Task) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Progress == nil {
		opts.Progress = os.Stderr
	}
	if opts.Runs < 1 {
		opts.Runs = 1
	}
	model := opts.Model
	if model == "" {
		if opts.ProviderName != "ollama" {
			return fmt.Errorf("no model specified; pass --model")
		}
		models, err := provider.ListOllamaModels(ctx, opts.BaseURL)
		if err != nil {
			return err
		}
		model = provider.PickOllamaModel(models)
		if model == "" {
			return fmt.Errorf("no Ollama models installed (try: ollama pull qwen2.5-coder:7b)")
		}
	}
	p := provider.New(opts.ProviderName, opts.BaseURL, opts.APIKey)
	fmt.Fprintf(opts.Progress, "cody bench: %s/%s, %d task(s) x %d run(s)\n", opts.ProviderName, model, len(tasks), opts.Runs)

	var results []Result
	for _, task := range tasks {
		for run := 1; run <= opts.Runs; run++ {
			fmt.Fprintf(opts.Progress, "  running %s (%d/%d)...\n", task.Name, run, opts.Runs)
			r := runTask(ctx, p, model, task, run, opts.Keep)
			results = append(results, r)
			if !opts.JSON {
				note := r.Note
				if r.Error != "" {
					note = strings.TrimSpace(note + " error: " + r.Error)
				}
				fmt.Fprintf(opts.Out, "%-12s %s  %6dms  ttft %5dms  %5.1f tok/s  in/out %d/%d  tools %d  %s\n",
					r.Task, okMark(r.OK), r.WallMS, r.TTFTMS, r.TokensPerSec, r.TokensIn, r.TokensOut, r.ToolCalls, note)
			}
		}
	}
	if opts.JSON {
		enc := json.NewEncoder(opts.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	summarize(opts.Out, results)
	return nil
}

func runTask(ctx context.Context, p provider.Provider, model string, task Task, run int, keep bool) Result {
	res := Result{Task: task.Name, Run: run}
	dir, err := os.MkdirTemp("", "cody-bench-"+task.Name+"-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if !keep {
		defer os.RemoveAll(dir)
	}
	if task.Setup != nil {
		if err := task.Setup(dir); err != nil {
			res.Error = "setup: " + err.Error()
			return res
		}
	}
	timeout := task.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ag := agent.New(agent.Options{
		Provider:      p,
		Model:         model,
		System:        prompt.Base(dir),
		Root:          dir,
		Tools:         tool.NewRegistry(tool.NewRead(dir), tool.NewWrite(dir), tool.NewEdit(dir), tool.NewBash(dir)),
		Approver:      agent.ApproverFunc(func(context.Context, tool.Tool, string) (bool, error) { return true, nil }),
		ContextTokens: 16000,
	})
	var answer strings.Builder
	var firstText time.Duration
	start := time.Now()
	_, promptErr := ag.Prompt(tctx, task.Prompt, func(e agent.Event) {
		switch e.Kind {
		case agent.KindText:
			if firstText == 0 {
				firstText = time.Since(start)
			}
			answer.WriteString(e.Text)
		case agent.KindToolStart:
			if firstText == 0 {
				firstText = time.Since(start)
			}
			res.ToolCalls++
		case agent.KindUsage:
			if e.Usage != nil {
				res.TokensIn += e.Usage.PromptTokens
				res.TokensOut += e.Usage.CompletionTokens
				if d := e.Usage.PromptDetails; d != nil {
					res.CacheReadTokens += d.CachedTokens
					res.CacheWriteTokens += d.CacheWriteTokens
				}
			}
		}
	})
	res.WallMS = time.Since(start).Milliseconds()
	res.TTFTMS = firstText.Milliseconds()
	if res.WallMS > 0 {
		res.TokensPerSec = float64(res.TokensOut) / (float64(res.WallMS) / 1000)
	}
	if promptErr != nil {
		res.Error = promptErr.Error()
	}
	if task.Verify != nil {
		ok, note := task.Verify(dir, answer.String())
		res.OK = ok && promptErr == nil
		res.Note = note
	}
	if keep {
		res.Note = strings.TrimSpace(res.Note + " kept: " + dir)
	}
	return res
}

func summarize(w io.Writer, results []Result) {
	if len(results) == 0 {
		return
	}
	var okCount, tokensIn, tokensOut, tools int
	var wall, ttft int64
	for _, r := range results {
		if r.OK {
			okCount++
		}
		wall += r.WallMS
		ttft += r.TTFTMS
		tokensIn += r.TokensIn
		tokensOut += r.TokensOut
		tools += r.ToolCalls
	}
	n := int64(len(results))
	fmt.Fprintf(w, "\n%d/%d passed  avg wall %dms  avg ttft %dms  tokens %d in / %d out  tools %d\n",
		okCount, len(results), wall/n, ttft/n, tokensIn, tokensOut, tools)
}

func okMark(ok bool) string {
	if ok {
		return "pass"
	}
	return "FAIL"
}

func runGoTests(dir string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return false, "go test failed: " + firstLine(text)
	}
	return true, "go test passed"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
