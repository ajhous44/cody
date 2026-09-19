# Contributing to cody

Thanks for wanting to help. cody is deliberately small, and the point of the project is what it refuses to become.

## Ground rules

- **Keep the tool surface at four tools** (`read`, `write`, `edit`, `bash`) unless there is a measured reason to change it. Prefer shell commands the model already knows over new tools.
- **Prefer the standard library.** New dependencies need a clear justification in the PR description.
- **No background work.** No indexing, watchers, daemons, or snapshot systems. Startup and per-tool latency are features.
- **Measure before claiming speed.** If a change affects startup, memory, or tokens per turn, include `cody bench` numbers.
- **Out of scope for now:** GUI clients, multi-agent orchestration, always-on MCP bundles, telemetry, whole-tree indexing, and internal snapshot VCS.

## Development

Requires Go 1.24+.

```sh
make check      # gofmt, go vet, go test ./...
make build      # ./cody
```

To test against a local model:

```sh
ollama pull qwen2.5-coder:7b
make build
./cody -m qwen2.5-coder:7b "list the files in this repo"
./cody bench --task read
```

To test against OpenRouter, export `OPENROUTER_API_KEY` and pass `-p openrouter -m <model>`.

## Pull requests

1. Fork the repo and create a topic branch.
2. Keep the change focused: one concern per PR.
3. Add or update tests. Behavior changes need test coverage, not just a working demo.
4. Run `make check` and paste the result in the PR description.
5. Describe any impact on startup time, memory, context size, or tool behavior.

Bug reports are most useful with: the cody version, provider and model, the prompt, and the session transcript from `cody replay last`.

## Code style

- `gofmt` is enforced; `go vet` must be clean.
- Small functions, explicit errors, no panics outside `main`, no global mutable state.
- Comments explain why, not what.
- User-facing output stays terse. Silent success, loud failure.
