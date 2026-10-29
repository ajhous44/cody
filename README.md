# cody

A small coding agent for the terminal. One Go binary, no runtime dependencies, seven core tools, optional web tools, append-only sessions. Built for fast cold start and low context tax on local models (Ollama) and hosted ones (OpenRouter).

**Status:** v3.0.0 development (unreleased). Interactive CLI, one-shot runs, sessions, a local-model benchmark suite, a multi-project web GUI (`cody serve`), and a macOS app.

The CLI and browser GUI are built for macOS, Windows, and Linux. The native app is macOS-only. The `bash` tool uses Bash on macOS/Linux and PowerShell on Windows; Git is needed for change previews.

Runtime regression tests have passed on macOS (Apple Silicon) and Linux (ARM64 and emulated x64). Windows and Intel macOS builds are cross-compiled; native runtime validation is pending.

## Principles

- **Seven core tools.** `read`, `write`, `edit`, `bash`, `grep`, `glob`, plus `search` once a workspace is indexed. Public web research and browser control are separate opt-ins, and tools that are off are never sent to the model; no plugin system.
- **No hidden machinery.** No always-on indexing, no snapshot VCS, no background daemons. Git is the checkpoint. The semantic index is built explicitly with `cody index`, lives outside the repo, and stays optional.
- **Context is a budget.** Tool output is capped, reads are paginated, and older context is summarized while preserving the current request and latest tool exchange.
- **Append-only sessions.** JSONL on disk, resumable and replayable. Browser control additionally uses a temporary profile in the system temp directory, removed when its browser closes.
- **Local settings and sessions.** BYOK from env or `~/.cody/config.json` (0600 on Unix). No telemetry or Cody-hosted service. Hosted model providers receive the context sent to them; their retention policies apply.
- **Subtraction cadence.** Every release asks what can be removed as models improve.

## Install

Building from source requires Go 1.27.2 or newer. CI and release builds use the version in `go.mod`.

Version 3 is not published yet. The existing v1/v2 releases are the older Python application. Until v3 is published, build from this v3 checkout:

```sh
make build            # builds ./cody
make check            # gofmt, vet, tests
./cody version
```

After v3.0.0 is published, the one-line installers download the latest release for your OS and CPU, verify it against the release's `checksums.txt`, and install the `cody` CLI (which includes the browser GUI). Neither needs admin rights.

macOS and Linux (installs to `~/.local/bin`):

```sh
curl -fsSL https://github.com/ajhous44/cody/releases/latest/download/install.sh | sh
```

Windows PowerShell (installs to `%LOCALAPPDATA%\Programs\cody` and adds it to your user `Path`):

```powershell
irm https://github.com/ajhous44/cody/releases/latest/download/install.ps1 | iex
```

Set `CODY_VERSION` (for example `v3.0.0`) to pin a release or `CODY_INSTALL_DIR` to choose the folder. Rerunning an installer upgrades in place. Package managers and manual downloads also work:

```sh
go install github.com/ajhous44/cody/v3@v3.0.0
brew install ajhous44/tap/cody                                    # once the tap is published
scoop bucket add ajhous44 https://github.com/ajhous44/scoop-bucket # Windows, once the bucket is published
scoop install cody
```

Archives for every OS and architecture are on [GitHub Releases](https://github.com/ajhous44/cody/releases).

### Optional PATH setup

Adding Cody to PATH lets you type `cody` from any project folder. It is optional: `./cody` runs a local build, and an absolute path runs an installed binary.

`go install` and `make install` put the executable in `GOBIN` when configured, otherwise the `bin` directory of the first `GOPATH` entry. Check your locations with `go env GOBIN GOPATH`; the default is `$HOME/go/bin` on macOS/Linux or `%USERPROFILE%\go\bin` on Windows. See [Go's installation guide](https://go.dev/doc/tutorial/compile-install).

For the default Go location on macOS/Linux, add this line to your shell configuration (`~/.zshrc` for zsh or `~/.bashrc` for interactive Bash), then open a new terminal:

```sh
export PATH="$HOME/go/bin:$PATH"
```

On Windows, add the executable's folder to your **user** `Path` through Environment Variables, then reopen PowerShell. To try it for the current PowerShell session only:

```powershell
$env:Path = "$HOME\go\bin;$env:Path"
```

If you downloaded an archive, add the folder where you extracted `cody`/`cody.exe` instead. Homebrew handles the executable location once Homebrew itself is on PATH.

The macOS app also contains the CLI. If installed in your user Applications folder, run it directly from any project directory without changing PATH:

```sh
"$HOME/Applications/Cody.app/Contents/Resources/cody"
```

Use `/Applications/Cody.app/Contents/Resources/cody` instead if the app is installed in the system Applications folder. The CLI and GUI share the current user's `~/.cody` settings and sessions, including the saved provider, model, and API key. A key saved in GUI Settings does not need to be exported again. `CODY_CONFIG_DIR` selects a separate settings/session directory, and environment variables can override saved settings.

### Upgrading from v1/v2

Version 3 is a Go rewrite of the earlier Python tool. Python environments, `.env` files, indexes, and settings from v1/v2 are not automatically migrated. Configure your provider in v3 Settings or through the documented environment variables. The old version tags remain available for users who need the previous application.

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

The GUI opens even when no model is installed or Ollama is offline. Configure a provider and model in Settings before the first prompt.

The GUI is a thin client over the same `internal/engine` the CLI uses: same provider, same tools, same sessions, same approval policy. Stream text, tool calls, and reasoning; approve or deny tool calls from the browser; interrupt; switch model and approval mode live. Sessions started in the GUI can be resumed in the CLI (`cody resume last`) and vice versa.

Layout — three panes, both sidebars collapse and remember their state:

- **Left:** a projects tree. Each project expands (caret) to show its threads — `new thread` plus the existing conversations for that folder, with the active one highlighted. `new thread` takes effect immediately (it shows as *untitled*), and the first ask names it before the real turn runs. Naming is a separate tool-free model call capped at 15s that asks for a small JSON object (`{"title":"3 to 5 lowercase words"}`); the reply is parsed directly, with fallbacks to the first words of the ask, then the date and time, so a slow or confused model never blocks the turn. Rename anytime and the title sticks. Expanding a project makes it active; clicking a thread in another project switches over and opens it. Threads are per project, not one flat list. Projects are persisted in `~/.cody/spaces.json`.

Right-click a project or thread for rename and delete (double-clicking the name also renames). Deletes ask for confirmation first. Renaming a thread stores a custom title beside its transcript; deleting the active thread starts a fresh one, and deleting a project only removes the cody entry — the folder on disk is untouched. The last project cannot be deleted, and these actions are refused while a turn is running.

Adding a project opens the operating system's own folder dialog: `NSOpenPanel` in the macOS app, and `osascript` (macOS), `zenity`/`kdialog` (Linux), or a PowerShell `FolderBrowserDialog` (Windows) from the server in browser mode. When no dialog helper exists, it falls back to an in-app browser with an editable absolute-path field plus click-through navigation, so any depth (including hidden folders) is reachable.
- **Middle:** the chat, always the largest panel; it flexes to fill whatever the sidebars leave. The composer, model, and approval controls live inside this pane, so the chat is bounded by the sidebars rather than spanning the window.
- **Right:** tabs for `changes` and `files`. Changes shows the branch and git status; click a file for its diff (including untracked files). Files is a lazy directory tree; click a file for a read-only preview or its diff when modified. The last tab is remembered.

The `cody` mark sits at the top of the left sidebar, and each sidebar carries its own collapse control; when one is collapsed a small restore button appears in the header. Projects are independent. Each project owns its own engine, session store, and turn, so switching or adding a project — or opening any thread — never blocks on a running turn, and a turn keeps running in the background while you work elsewhere. One turn runs per project at a time: prompting a project that is already running is refused with a clear notice and a "take me to the thread" link rather than an error. If a background project is waiting on an approval, its project and thread are highlighted in turquoise in the sidebar, and the status line reads "waiting for approval" when you land on it; running threads show a pulsing cyan dot. Model and approval settings are global and reach background projects on their next turn.

### Models and settings

The model control in the composer is a searchable picker grouped by provider. It shows model names; hover one for its size and quantization (Ollama) or context window and price (OpenRouter). If Ollama is installed but stopped, opening the picker starts it, and the picker says so when Ollama cannot be reached. OpenRouter models (id, context, prompt price) only appear once an OpenRouter key is configured, so the default local setup does not fetch a hosted model catalog. Opt-in web tools can access the internet independently. Selecting a model switches the provider and model together. Typing a name that is not in the list warns subtly — without blocking — and uses it anyway, so custom or unlisted model ids still work.

The gear at the bottom of the projects sidebar opens a settings view in the middle pane:

- provider, model, and approval
- the OpenRouter API key
- independent web research and browser control toggles, plus an optional Brave Search API key, and
- a table of every installed local model — parameters, quantization, size, context window, and whether it is currently loaded in memory (with VRAM)

Settings live in `~/.cody/config.json`. Saved keys are plaintext, protected by owner-only file permissions on Unix and your profile directory's ACL on Windows; Cody does not use an OS credential vault. Configured keys are omitted from settings responses and session metadata, and there is no telemetry. Prompts and tool output are saved in sessions and may contain secrets you supply. Hosted providers receive prompts and tool results; free pricing and zero data retention depend on the selected provider/endpoint and account policies.

Drag the dividers to resize either sidebar (170–560px, remembered per browser). Below 820px wide the sidebars become overlay drawers so the chat keeps the space.

Security defaults: binds to loopback only, requires a per-run token on every API call, rejects foreign `Host` headers (DNS rebinding), and never trusts the browser with file access beyond the same workspace-bound tools. Binding to a non-loopback address prints a warning; anyone with the token URL can then use cody.

### Optional web research and browser control

Both features are off by default and work through the shared CLI/GUI engine. Enable them independently in Settings → web & browser, or with environment variables:

```sh
CODY_WEB=true cody "Read https://go.dev/doc/ and summarize the documentation"
# Set BRAVE_SEARCH_API_KEY in your environment to also enable web_search.
CODY_BROWSER=true cody "Open http://localhost:3000 and check the homepage"
```

- `web_fetch` reads public HTTP(S) pages into bounded text with source links. It needs no API key, browser, or OS computer-use permission. Each fetch requires approval in `ask` and `auto` modes, since a URL can carry workspace data off the machine. It does not execute JavaScript, reuse cookies, or access private/local addresses. Responses are capped at 2 MiB and each excerpt at 4,000 characters. Longer pages return a character offset for the next excerpt.
- `web_search` returns up to five results through the [Brave Search API](https://api-dashboard.search.brave.com/documentation/guides/authentication). It appears only when web research is enabled and a key is configured. Queries go to Brave and require approval in `ask` and `auto` modes; you supply your own key and are responsible for any API charges. The key is stored locally or read from `BRAVE_SEARCH_API_KEY`, never returned through the settings API.
- `browser` starts your installed Chrome/Chromium or Edge in a visible window with a separate, temporary profile. It supports opening a URL, reading page text, clicking an observed element, filling text fields, pressing a small set of keys, going back, and closing. It can reach local development servers. Actions require approval in `ask` and `auto` modes; `yolo` skips approval. No macOS Accessibility or Screen Recording permission is needed.

Browser control uses Chrome's debugging pipe, with no listening debugging port, bundled browser, browser extension, or Node runtime. Set `CODY_BROWSER_PATH` to a browser executable if automatic discovery does not find it. It does not attach to normal tabs or logins, automate passwords or uploads, or permit downloads. Sign in manually in its separate window if needed. The temporary profile closes with its engine or when you disable the feature.

This first version controls one page through bounded DOM text excerpts and up to 30 element references. Safari, full desktop control, screenshots/vision, canvas interactions, and iframe traversal are not included. Page contents are untrusted input; inspect actions before approving consequential submissions. Disabling either feature in settings cancels active turns before applying the change. Saved settings also apply to future CLI runs.

Optional integration tests use a disposable headless profile and a public documentation page:

```sh
CODY_BROWSER_TEST=1 CODY_WEB_TEST=1 go test ./internal/tool -run 'TestBrowser|TestWebFetch'
```

### macOS app

```sh
make macapp                # builds dist/Cody.app
```

`Cody.app` is a native window (AppKit + WKWebView) with no browser: it bundles the cody binary, launches `cody serve` as a child process on a free loopback port with a private token, and loads the same UI. On first launch it asks for a workspace folder and remembers it; `Cmd+O` switches folders, `Cmd+Q` stops the engine. The engine also exits if the app crashes or is force-quit, so no server is left running.

The window is borderless: there is no system title bar. Close, minimize, and zoom live in the app's own header and talk to AppKit through a small JS bridge, the header itself is the drag handle, and thin edge/corner overlays provide native-feeling resize cursors. Browser mode hides the custom dots and uses the browser's own chrome instead.

Local builds are ad-hoc signed. Set `CODY_ARCH=universal` to include Apple Silicon and Intel, or `arm64`/`x86_64` for one architecture. Public distribution requires a **Developer ID Application** certificate (an Apple Development certificate is insufficient) and notarization:

```sh
# Store existing notarization credentials interactively in Keychain once:
xcrun notarytool store-credentials cody-release
CODY_ARCH=universal \
  CODY_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" \
  CODY_NOTARY_PROFILE=cody-release make macapp
```

The script signs both executables and the app with the hardened runtime, submits to Apple, staples and validates the ticket, then creates a ZIP and SHA-256 checksum. Signing or notarization failures stop the build.

The native Mac app is optional. The CLI and browser GUI work without it. Signed apps can be downloaded from GitHub Releases; Mac App Store distribution is not required. The wrapper currently has no automatic updater: install a newer release to update it.

In the interactive loop: `/help`, `/model`, `/provider`, `/approval`, `/think`, `/index`, `/clear`, `/sessions`, `/exit`. Ctrl+C cancels the running turn; press it again to quit.

### Approvals

| Mode | File writes | Shell commands / web and browser actions |
| --- | --- | --- |
| `ask` (default) | confirm | confirm |
| `auto` | automatic | confirm |
| `yolo` | automatic | automatic |

Set with `--approval`, `--yolo`, or `CODY_APPROVAL`. At a prompt, `a` approves everything for the rest of the session. File tools and previews reject paths and symlinks that escape the workspace, and `write`/`edit` refuse to modify `.git` metadata (hooks and config can run commands). Shell commands run with your user permissions and are not sandboxed; approve them accordingly. Cancelling a shell tool terminates its process tree, and background children are cleaned up when the tool exits.

### Semantic index

`cody index` builds a semantic index: files are chunked, embedded (with a local Ollama model, `nomic-embed-text`, by default), and stored in SQLite under `~/.cody/index/`. The `search` tool then answers "where is X handled" questions when no exact identifier is known; exact text still goes through grep.

- Local and private by default: embeddings go to your Ollama server only, even when chat runs against a hosted provider.
- OpenRouter opt-in: set `"embed_provider": "openrouter"` (or `CODY_EMBED_PROVIDER=openrouter`) to embed without Ollama, using your OpenRouter key. The model defaults to `openai/text-embedding-3-small`; set `embed_model` to any [OpenRouter embedding model](https://openrouter.ai/collections/embedding-models). **Every indexed file and each search query is sent to OpenRouter**, and `cody index` says so each run. Only an explicit `openrouter` value opts in; anything else stays local.
- Fast: chunks from many small files share one embedding request, and only changed files are re-embedded; deleted files are pruned.
- Safe to switch: the index records which provider and model built it. Searching with a different one is refused until you run `cody index --rebuild`.
- Optional: without an index, the `search` tool is not offered to the model at all, so it costs no tokens. It appears on the next turn after `cody index` runs.
- Disposable: delete `~/.cody/index` at any time.

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
  "embed_provider": "ollama",
  "web_enabled": false,
  "browser_enabled": false,
  "brave_search_key": "",
  "providers": {
    "ollama": { "base_url": "http://localhost:11434/v1" },
    "openrouter": { "base_url": "https://openrouter.ai/api/v1", "api_key": "" }
  }
}
```

Environment overrides: `CODY_PROVIDER`, `CODY_MODEL`, `CODY_APPROVAL`, `CODY_API_KEY`, `CODY_NO_AUTOSTART`, `CODY_EMBED_PROVIDER`, `OLLAMA_HOST`, `OPENROUTER_API_KEY`, `CODY_WEB`, `CODY_BROWSER`, `CODY_DEBUG`, `BRAVE_SEARCH_API_KEY`. `CODY_WEB`, `CODY_BROWSER`, and `CODY_DEBUG` accept `true`/`false` or `1`/`0`. Debug mode (also `--debug[=BOOL]` or the GUI settings toggle) appends the provider's raw status and message to errors and lists every retry. Overrides apply only to the running process; saving GUI settings writes just the values you changed, so environment values and env-provided keys never reach `config.json`. `CODY_CONFIG_DIR` overrides the default `~/.cody` directory for settings, sessions, and indexes.

Any OpenAI-compatible endpoint works as an extra provider: add a `providers.<name>.base_url` entry and pass `-p <name>`.

Project instructions can live in `AGENTS.md` or the existing `CODY.md` at the workspace root (8 KiB per file). Both are loaded; `AGENTS.md` takes precedence where they conflict. Root instructions refresh each user turn. For `read`, `write`, and `edit`, Cody also discovers nested `AGENTS.md` files between the root and the target file. Deeper instructions apply to their subtree. A mutation with newly discovered or changed instructions returns those instructions before the model can retry the action. Shell commands still rely on the model reading applicable instructions before changing files. Instruction discovery stays inside the workspace.

Long sessions summarize older context while retaining the current request and most recent tool exchange verbatim. The full transcript stays saved; summaries are held in memory and rebuilt if needed after resume. Compaction uses additional model requests only when necessary. An active request or tool result that cannot fit produces an explicit error. Cody reserves output and tool-schema space, uses OpenRouter model limits and Ollama's loaded context window when available, and falls back to conservative estimates otherwise.

Prompt caching uses a stable, opaque session ID for OpenRouter routing and OpenAI cache affinity. Claude models on OpenRouter also receive the supported automatic ephemeral cache hint; other models use provider-managed caching. Cached input and cache-write token counts appear in the CLI/GUI and session usage records when the provider reports them. Benchmark JSON includes these counts too. Cache hits depend on the provider, model, prompt size, and reuse; cache writes may have a cost. See [OpenRouter's caching documentation](https://openrouter.ai/docs/guides/best-practices/prompt-caching). Cody does not cache generated answers or file contents.

The `read` tool accepts `filePath` as an alias for `path` (both must match if supplied together). File reads stream the requested line range and cap text output at 50 KiB. Search and glob honor Git ignore rules in Git workspaces when Git is installed; otherwise they retain the built-in generated-directory exclusions. Provider requests retry transient failures up to two times before streaming begins, honor bounded `Retry-After` delays, and remain cancellable. Interrupted or length-limited responses are reported as incomplete; tools are never replayed automatically.

Cody stops consecutive identical failed tool calls before executing them again, including reads and failed shell commands. JSON key order and the read path alias do not bypass this guard. Cody also stops immediately repeated failed edits. It also stops read/edit cycles once the same edit has failed twice without a successful write, edit, or shell command. The session retains the failed and skipped calls; a new prompt can resume the work. If a model writes a tool command as chat text, Cody requests a proper tool call at most once and stops if the format is still invalid; text is never executed as a command.

### Sessions

Every message is appended and synced to `~/.cody/sessions/<project>-<hash>/<id>.jsonl` before the next action. A crash can still occur between a tool's side effect and its result being recorded; resume marks such calls as interrupted with an unknown outcome instead of silently rerunning them. A damaged trailing JSON record does not swallow later messages. Cody does not add configured credentials or environment variables to the journal, but prompts and tool output can contain secrets you supply. Files use owner-only permissions on Unix; on Windows they inherit your profile directory's ACL.

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
packaging/macos          Cody.app shell (AppKit + WKWebView) and build script
internal/agent           streaming agent loop, tool dispatch, context compaction
internal/provider        OpenAI-compatible SSE client, Ollama/OpenRouter helpers
internal/tool            seven core tools; opt-in web_fetch, web_search, browser
internal/index           SQLite semantic index, chunking, embeddings, cosine search
internal/session         append-only JSONL store
internal/bench           benchmark runner and tasks
internal/ui              terminal rendering and approvals
internal/clip            UTF-8-safe text clipping
internal/config          config file and env resolution
internal/prompt          system prompt and scoped AGENTS.md/CODY.md loading
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
- [x] Add CI checks for pull requests.
- [ ] Publish the v3 rewrite on GitHub and accept PRs.

### Phase 3 - Later, only if it earns its place

- [x] Thin web GUI over the same engine (`cody serve`).
- [ ] Plan gate: propose a short plan before acting on multi-step tasks.
- [ ] Small TUI: scrollback, session picker, live token/timing budget.
- [ ] Anthropic provider adapter.
- [ ] OS-level sandbox profile and a documented container profile.
- [ ] Optional skills directory with lazy loading (no always-on MCP).
- [ ] `cody bench --suite` for regression tracking and a PERFORMANCE.md with measured budgets.
- [ ] Session fork/replay-from-message.

Deliberately out of scope: multi-agent swarms, always-on MCP bundles, whole-tree indexing, internal snapshot VCS, telemetry.

## Releasing

Releases are cut by pushing a version tag. GitHub Actions runs Go/race and browser regressions on Linux, Windows, Apple Silicon, and Intel Macs, then publishes CLI archives for all three OSes (arm64/amd64), checksums, and the `install.sh`/`install.ps1` installers. Each CLI includes the browser GUI. Windows archives are ZIP files. The Homebrew formula and Scoop manifest are updated when configured. CI also runs both installers end to end against a local release mirror on every OS, including a tampered archive that must be refused.

To also publish the native Mac app, set the Actions repository variable `RELEASE_MACOS_APP=true` and configure these Actions secrets: `APPLE_DEVELOPER_ID_P12` (base64 exported certificate plus private key), `APPLE_DEVELOPER_ID_PASSWORD`, `APPLE_SIGN_IDENTITY`, `APPLE_ID`, `APPLE_TEAM_ID`, and `APPLE_APP_PASSWORD` (Apple app-specific password). After the CLI release succeeds, a separate job builds, signs, and notarizes the universal app before attaching it to the release. A signing failure prevents the app upload; the CLI release remains available. Credentials are imported into a temporary CI keychain and removed afterward. Never commit them.

For local browser regressions, run `make test-browser` (Node.js 22+). Playwright is a development dependency only; Cody's shipped binary has no JavaScript runtime or browser automation dependency. The fixture uses a temporary workspace and mock model, so it requires no API key or installed Ollama.

```sh
git tag v3.0.0
git push origin v3.0.0

# local dry run, no publishing (requires goreleaser v2)
make snapshot
```

The Homebrew update needs an `ajhous44/homebrew-tap` repository and a `HOMEBREW_TAP_TOKEN` Actions secret with write access to it (a fine-grained PAT). Without the secret, the formula step is skipped and the GitHub release still publishes — `go install` users are unaffected either way. A reference formula lives in `packaging/homebrew/cody.rb` for manual tap setup. Scoop works the same way: an `ajhous44/scoop-bucket` repository and a `SCOOP_BUCKET_TOKEN` secret with write access to it; without the secret, that step is skipped too.

## Contributing

cody is MIT-licensed and contributions are welcome via GitHub pull requests. Keep changes focused, prefer the standard library, and preserve the small tool surface. Run `make check` before opening a PR. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Reference documents

The original research notes and product drafts that informed cody are kept locally under `docs/` and are deliberately excluded from this repository. They describe a broader product under a different working name; they are not a committed feature list.

## License

MIT
