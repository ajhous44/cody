package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"time"
)

// Report is a saved suite run: what was measured and the per-task medians a
// later run is compared against.
type Report struct {
	Version  string        `json:"version"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Platform string        `json:"platform"`
	Time     time.Time     `json:"time"`
	Runs     int           `json:"runs"`
	Summary  []TaskSummary `json:"summary"`
	Results  []Result      `json:"results"`
}

// TaskSummary uses medians, which one slow run cannot skew.
type TaskSummary struct {
	Task      string  `json:"task"`
	PassRate  float64 `json:"pass_rate"`
	WallMS    int64   `json:"wall_ms"`
	TTFTMS    int64   `json:"ttft_ms"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	ToolCalls int     `json:"tool_calls"`
}

// tokensInSlack is how far median tokens_in may grow before it counts as a
// regression. Input tokens track harness overhead closely; wall time is
// reported but never fails, because machine load makes it noisy.
const tokensInSlack = 1.10

func newReport(opts Options, model string, runs int, results []Result) Report {
	r := Report{
		Version: opts.Version, Provider: opts.ProviderName, Model: model,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Time: time.Now().UTC(), Runs: runs, Results: results,
	}
	var order []string
	byTask := map[string][]Result{}
	for _, res := range results {
		if _, ok := byTask[res.Task]; !ok {
			order = append(order, res.Task)
		}
		byTask[res.Task] = append(byTask[res.Task], res)
	}
	for _, task := range order {
		rs := byTask[task]
		s := TaskSummary{Task: task}
		var wall, ttft []int64
		var in, out, tools []int
		for _, res := range rs {
			if res.OK {
				s.PassRate++
			}
			wall, ttft = append(wall, res.WallMS), append(ttft, res.TTFTMS)
			in, out, tools = append(in, res.TokensIn), append(out, res.TokensOut), append(tools, res.ToolCalls)
		}
		s.PassRate /= float64(len(rs))
		s.WallMS, s.TTFTMS = median(wall), median(ttft)
		s.TokensIn, s.TokensOut, s.ToolCalls = median(in), median(out), median(tools)
		r.Summary = append(r.Summary, s)
	}
	return r
}

func median[T int | int64](xs []T) T {
	if len(xs) == 0 {
		return 0
	}
	s := append([]T(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func printSummary(w io.Writer, r Report) {
	fmt.Fprintf(w, "\nsuite: %s/%s, %d run(s) per task, cody %s on %s\n", r.Provider, r.Model, r.Runs, r.Version, r.Platform)
	fmt.Fprintf(w, "%-10s %6s %9s %9s %10s %10s %6s\n", "task", "pass", "wall", "ttft", "tokens in", "tokens out", "tools")
	for _, s := range r.Summary {
		fmt.Fprintf(w, "%-10s %5.0f%% %8dms %8dms %10d %10d %6d\n",
			s.Task, s.PassRate*100, s.WallMS, s.TTFTMS, s.TokensIn, s.TokensOut, s.ToolCalls)
	}
}

// Compare prints how cur differs from base and returns the regressions: a
// task's pass rate dropped, or its median tokens_in grew by more than 10%.
func Compare(w io.Writer, base, cur Report) []string {
	if base.Provider != cur.Provider || base.Model != cur.Model {
		fmt.Fprintf(w, "\nnote: the baseline ran %s/%s; comparing anyway\n", base.Provider, base.Model)
	}
	prev := map[string]TaskSummary{}
	for _, s := range base.Summary {
		prev[s.Task] = s
	}
	var regressions []string
	fmt.Fprintf(w, "\nvs baseline (%s, cody %s):\n", base.Time.Format("2006-01-02"), base.Version)
	for _, s := range cur.Summary {
		b, ok := prev[s.Task]
		if !ok {
			fmt.Fprintf(w, "%-10s new task, no baseline\n", s.Task)
			continue
		}
		fmt.Fprintf(w, "%-10s pass %3.0f%% -> %3.0f%%  tokens in %d -> %d  wall %dms -> %dms\n",
			s.Task, b.PassRate*100, s.PassRate*100, b.TokensIn, s.TokensIn, b.WallMS, s.WallMS)
		if s.PassRate < b.PassRate {
			regressions = append(regressions, fmt.Sprintf("%s: pass rate %.0f%% -> %.0f%%", s.Task, b.PassRate*100, s.PassRate*100))
		}
		if b.TokensIn > 0 && float64(s.TokensIn) > float64(b.TokensIn)*tokensInSlack {
			regressions = append(regressions, fmt.Sprintf("%s: median tokens in %d -> %d (more than 10%%)", s.Task, b.TokensIn, s.TokensIn))
		}
	}
	return regressions
}

func loadReport(path string) (Report, error) {
	var r Report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("baseline %s: %w", path, err)
	}
	return r, nil
}
