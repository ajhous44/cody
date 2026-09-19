# cody

A small coding agent for the terminal. One Go binary, no runtime dependencies, four tools, append-only sessions. Built for fast cold start and low context tax on local models (Ollama) and hosted ones (OpenRouter).

**Status:** v0.1.0. Interactive CLI, one-shot runs, sessions, a local-model benchmark suite, and a web GUI (`cody serve`) are working.

## Principles

- **Seven tools.** `read`, `write`, `edit`, `bash`, `grep`, `glob`, `search`. Everything else is a shell command the model already knows how to use. No plugin gravity.
- **No hidden machinery.** No always-on indexing, no snapshot VCS, no background daemons. Git is the checkpoint. The semantic index is built explicitly with `cody index`, lives outside the repo, and stays optional.
- **Context is a budget.** Tool output is capped, reads are paginated, and the oldest tool output is elided before a request goes over budget.
- **Append-only sessions.** JSONL on disk, resumable and replayable. Nothing else is written outside the workspace and `~/.cody`.
- **Local keys only.** BYOK from env or `~/.cody/config.json` (0600). No telemetry, no product-side retention.
- **Subtraction cadence.** Every release asks what can be removed as models improve.

## Install

Requires Go 1.24+.

```sh
# Homebrew, once the tap is published
brew install ajhous44/tap/cody

# with Go
go install github.com/ajhous44/cody@latest

# from source
git clone https://github.com/ajhous44/cody && cd cody
make build            # builds ./cody
make check            # gofmt, vet, tests
```

## Quickstart

### Ollama (local, default provider)

```sh
ollama pull qwen3-coder:14b        # strong general choice
ollama pull qwen2.5-coder:7b       # lighter option

cody                               # interactive session in the current directory
cody "fix the failing test"        # one-shot
cody -m qwen2.5-coder:7b "..."     # pick a model
```

If no model is configured, cody picks the smallest installed tool-capable Ollama model, preferring the `qwen*coder` family.

If Ollama is installed but not running, cody starts it automatically (the app on macOS, `ollama serve` elsewhere) and waits for it to come up. Disable with `CODY_NO_AUTOSTART=1`.

### OpenRouter (hosted)

```sh
export OPENROUTER_API_KEY=sk-or-...
cody -p openrouter -m qwen/qwen3-coder "explain the build scripts"
```

## Usage

```
cody                     interactive session in the current directory
cody "prompt"            one-shot prompt
cody run "prompt"        one-shot prompt
cody resume [id|last]    resume a session
cody sessions            list sessions for this workspace
cody replay [id|last]    print a session transcript
cody models [filter]     list models for the current provider
cody index [--rebuild]   build or refresh the local semantic index
cody serve [flags]       web GUI over the same engine (default 127.0.0.1:8765)
cody bench [flags]       run the built-in benchmark suite
cody help                full help
cody version             print the version
```

### Web GUI

```sh
cody serve                 # prints a URL with an access token
cody serve --open          # ...and opens it in your browser
cody serve --addr 127.0.0.1:9000 --token secret
```

The GUI is a thin client over the same `internal/engine` the CLI uses: same provider, same tools, same sessions, same approval policy. Stream text, tool calls, and reasoning; approve or deny tool calls from the browser; interrupt; switch model and approval mode live. Sessions started in the GUI can be resumed in the CLI (`cody resume last`) and vice versa.

Security defaults: binds to loopback only, requires a per-run token on every API call, rejects foreign `Host` headers (DNS rebinding), and never trusts the browser with file access beyond the same workspace-bound tools. Binding to a non-loopback address prints a warning; anyone with the token URL can then use cody.

In the interactive loop: `/help`, `/model`, `/provider`, `/approval`, `/think`, `/index`, `/clear`, `/sessions`, `/exit`. Ctrl+C cancels the running turn; press it again to quit.

### Approvals

| Mode | File writes | Shell commands |
| --- | --- | --- |
| `ask` (default) | confirm | confirm |
| `auto` | automatic | confirm |
| `yolo` | automatic | automatic |

Set with `--approval`, `--yolo`, or `CODY_APPROVAL`. At a prompt, `a` approves everything for the rest of the session. Mutating tools are all workspace-bound: paths outside the workspace root are rejected.

### Semantic index

`cody index` builds a local semantic index: files are chunked, embedded with a local Ollama model (`nomic-embed-text` by default), and stored in SQLite under `~/.cody/index/`. The `search` tool then answers "where is X handled" questions when no exact identifier is known; exact text still goes through grep.

- Local and private: embeddings go to your Ollama server only, even when chat runs against a hosted provider.
- Incremental: only changed files are re-embedded, deleted files are pruned.
- Optional: without an index, `search` reports that the workspace is not indexed and nothing else changes.
- Disposable: delete `~/.cody/index` at any time, or run `cody index --rebuild` after switching embedding models.

```sh
ollama pull nomic-embed-text
cody index
```

## Configuration

`~/.cody/config.json` (all fields optional):

```json
{
  "provider": "ollama",
  "model": "",
  "approval": "ask",
  "embed_model": "nomic-embed-text",
  "providers": {
    "ollama": { "base_url": "http://localhost:11434/v1" },
    "openrouter": { "base_url": "https://openrouter.ai/api/v1", "api_key": "" }
  }
}
```

Environment overrides: `CODY_PROVIDER`, `CODY_MODEL`, `CODY_APPROVAL`, `CODY_API_KEY`, `CODY_NO_AUTOSTART`, `OLLAMA_HOST`, `OPENROUTER_API_KEY`.

Any OpenAI-compatible endpoint works as an extra provider: add a `providers.<name>.base_url` entry and pass `-p <name>`.

Project instructions live in `CODY.md` at the workspace root (capped at 8 KB) and are appended to the system prompt.

### Sessions

Every message is appended to `~/.cody/sessions/<project>-<hash>/<id>.jsonl` (0600). Nothing is rewritten, so a session survives interrupts and can be replayed or resumed later. API keys and environment variables are never recorded.

## Benchmarks

`cody bench` runs deterministic tasks in temporary workspaces and verifies the filesystem, not the model's prose:

| Task | Checks |
| --- | --- |
| `read` | finds a fact in a file |
| `write` | creates a file with required content |
| `edit` | changes one value without collateral edits |
| `shell` | produces a file via bash |
| `fix-test` | fixes a real Go bug so `go test ./...` passes |

```sh
cody bench                       # all tasks, smallest local model
cody bench --task fix-test -m gemma4:e4b
cody bench --list                # tasks + local models
cody bench --json                # machine-readable results
```

Reported per task: pass/fail, wall time, time to first output, tokens in/out, tokens/sec, tool calls. `tokens_in` is the full context bill per request, including system prompt and tool schemas, so the harness tax is visible.

Example (Apple Silicon, Ollama, `fix-test`): `gemma4:e4b` — pass, 110s, 25.4 tok/s, 8 tool calls; `qwen3.5-abliterated:9b` — 6 tool calls and a green test suite in some runs.

### Local models that fit

| Model | Approx. size | Notes |
| --- | --- | --- |
| `gemma4:e4b` | ~9 GB | fast, tool-capable, good for smoke tests |
| `qwen2.5-coder:7b` | ~4.7 GB | broadly available, solid tool calling |
| `qwen2.5-coder:3b` | ~2 GB | lightest useful coder |
| `qwen3-coder:14b` | ~9 GB | strong general testing model; pull when the machine is free |

`cody models` marks the default pick and tags models without tool support.

## Architecture

```
main.go                  entrypoint
internal/cli             argument parsing, REPL, commands, HTTP server (serve)
internal/engine          headless core: provider, agent, tools, approvals, sessions
internal/server          HTTP + SSE API and the embedded web GUI
internal/agent           streaming agent loop, tool dispatch, context trimming
internal/provider        OpenAI-compatible SSE client, Ollama/OpenRouter helpers
internal/tool            read, write, edit, bash, grep, glob, search
internal/index           SQLite semantic index, chunking, embeddings, cosine search
internal/session         append-only JSONL store
internal/bench           benchmark runner and tasks
internal/ui              terminal rendering and approvals
internal/config          config file and env resolution
internal/prompt          system prompt and CODY.md loading
```

Clients never own the model loop. The CLI and the web GUI both drive `internal/engine` and subscribe to the same event stream; the server is a transport, not a second agent stack.

## Roadmap

### Phase 0 - Foundation

- [x] Agree on direction and reference material.
- [x] Choose the implementation language and runtime (Go, one pure-Go SQLite dependency).
- [x] Add an open-source license (MIT).

### Phase 1 - Minimal coding harness

- [x] Streaming agent loop with interrupt handling.
- [x] Ollama and OpenRouter through one OpenAI-compatible provider layer.
- [x] Four workspace-bound tools with approval modes.
- [x] Context budget, tool output caps, and clear errors.
- [x] Append-only JSONL sessions with resume, replay, and listing.
- [x] Local-model benchmark suite with deterministic checks.

### Phase 2 - Public release

- [x] Document installation, configuration, and usage.
- [x] Add contribution guidance.
- [ ] Add CI checks for pull requests.
- [ ] Publish on GitHub and accept PRs.

### Phase 3 - Later, only if it earns its place

- [x] Thin web GUI over the same engine (`cody serve`).
- [ ] Plan gate: propose a short plan before acting on multi-step tasks.
- [ ] Small TUI: scrollback, session picker, live token/timing budget.
- [ ] Anthropic provider adapter.
- [ ] OS-level sandbox profile and a documented container profile.
- [ ] Optional skills directory with lazy loading (no always-on MCP).
- [ ] `cody bench --suite` for regression tracking and a PERFORMANCE.md with measured budgets.
- [ ] Session fork/replay-from-message.

Deliberately out of scope: native desktop apps, multi-agent swarms, always-on MCP bundles, whole-tree indexing, internal snapshot VCS, telemetry.

## Releasing

Releases are cut by pushing a version tag; GitHub Actions runs GoReleaser to cross-compile macOS and Linux binaries (arm64/amd64), publish a GitHub release with `checksums.txt`, and bump the Homebrew formula.

```sh
git tag v0.1.0
git push origin v0.1.0

# local dry run, no publishing (requires goreleaser v2)
make snapshot
```

The Homebrew update needs an `ajhous44/homebrew-tap` repository and a `HOMEBREW_TAP_TOKEN` Actions secret with write access to it (a fine-grained PAT). Without the secret, the formula step is skipped and the GitHub release still publishes — `go install` users are unaffected either way. A reference formula lives in `packaging/homebrew/cody.rb` for manual tap setup.

## Contributing

cody is MIT-licensed and contributions are welcome via GitHub pull requests. Keep changes focused, prefer the standard library, and preserve the four-tool surface. Run `make check` before opening a PR. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Reference documents

The original research notes and product drafts that informed cody are kept locally under `docs/` and are deliberately excluded from this repository. They describe a broader product under a different working name; they are not a committed feature list.

## License

MIT
