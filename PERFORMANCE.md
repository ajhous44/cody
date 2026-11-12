# Performance

Budgets CI enforces without a model, and recent measurements. Raise a budget only on purpose, in the same change that needs it.

## Budgets

| Budget | Limit | Enforced by |
|---|---|---|
| Release binary (`-trimpath -ldflags="-s -w"`, linux/amd64) | 16 MiB | `Size budget` step in `.github/workflows/ci.yml` |
| Per-request overhead, defaults (system prompt + tool schemas) | ~1400 tokens | `TestRequestOverheadBudget` in `internal/engine/budget_test.go` |
| Per-request overhead, every opt-in enabled | ~2700 tokens | same test |
| Per-request overhead, browser-only mode | ~450 tokens | same test |

Token counts are estimated at about 4 characters per token. Run `go test -run TestRequestOverheadBudget -v ./internal/engine` to print the current values.

## Measurements

v3.0.0, Go 1.27.2, darwin/arm64, November 2026:

| Metric | Value |
|---|---|
| Release binary | 12.3 MB (12,333,858 bytes) |
| Per-request overhead, defaults | ~1270 tokens |
| Per-request overhead, every opt-in | ~2670 tokens |
| Per-request overhead, browser-only mode | ~390 tokens |
| `cody bench --task fix-test -m gemma4:e4b` (Ollama, Apple Silicon) | pass, 110 s, 25.4 tok/s, 8 tool calls |

## Regression tracking with a model

`cody bench --suite` runs every task several times and reports per-task medians. Save a baseline and compare later builds against it:

```sh
cody bench --suite -m gemma4:e4b --save baseline.json
cody bench --suite -m gemma4:e4b --baseline baseline.json
```

A comparison fails when a task's pass rate drops or its median input tokens grow by more than 10%. Wall time is reported but never fails.
