# Browser control

How cody drives a browser with small local models, why each piece exists, and what was measured. The target is a model that fits an 8 GB machine.

## How it works

The `browser` tool runs one [agent-browser](https://github.com/vercel-labs/agent-browser) command per call in cody's own headless session. Around that one command cody does five things.

**1. Shows the page after every action.** cody waits for the page to settle (network idle, at most 5 seconds), then appends what changed since the last snapshot: the changed lines, the whole page after a navigation, or "The page did not change." The model never needs a snapshot call of its own and never acts on stale element refs.

**2. Reads arguments as they were meant.** Small models get the call shape wrong in predictable ways. cody accepts the command under another key name, as a JSON string, or as one plain string, and drops a repeated program name. A ref handed to `read` is dropped. The literal placeholder `@eN` is refused with a plain message before it reaches the browser.

**3. Repairs a wrong action once, without a model turn.** When an action fails or changes nothing, cody asks the browser CLI for the element's role (about 10 ms) and reruns the action that fits it, then tells the model what it ran.

| The action was aimed at | cody runs |
|---|---|
| an option inside a dropdown | `select` on the dropdown, with that option |
| a dropdown, with a value | `select` |
| a text or number field, with a value | `fill` |
| a checkbox or radio button | `check` |
| a file chooser, with a path | `upload` |
| a file chooser, without a path | nothing; it says to use `upload` with a path |

Nothing is looked up when an action works, and only one repair is tried per action.

**4. Keeps files in the project folder.** `upload` and `download` paths are resolved inside the workspace and refused outside it. A relative upload path is made absolute, because Chrome ignores relative ones; a folder the model invented falls back to the workspace file of the same name. Downloads land in the workspace unless `AGENT_BROWSER_DOWNLOAD_PATH` is set, and cody reports each file that arrives, since a download changes nothing on the page.

**5. Knows when to stop.**

- After four browser actions in a row with no effect, further actions are refused until the page is reopened.
- A repeat of a call that just failed is refused once, with its batch, and the model gets one turn to recover. A second repeat ends the turn.
- An empty reply is asked for once more with reasoning off.

Two things outside the tool matter as much:

- **The context window.** Ollama loads every model at 4,096 tokens unless told otherwise, and its OpenAI-compatible endpoint cannot be told. cody uses Ollama's native API and sets the window on every request (`context_tokens`, 8,192 by default). One tool result is capped at about a quarter of the window, and cody corrects its size estimates with the token counts the provider reports.
- **Browser-only mode.** `--browser-only` gives the model the browser and nothing else, with a short prompt of its own. Fixed overhead is about 380 tokens instead of roughly 2,000, and the model has no shell or file tools to reach for.

## Why each piece exists

Every mechanism above answers a failure that was observed and measured, not anticipated.

| What was found | Evidence | Answer |
|---|---|---|
| Ollama ran at 4,096 tokens while cody budgeted 8,000 | A request asking for 16,384 loaded at 4,096; a model preloaded at 8,192 was reset by the next request | Native Ollama API with `num_ctx` |
| Page snapshots were far bigger than counted | `snapshot -i` on four real sites: 13.6 to 44 KB against a 12,000-character cap; 12,000 characters measured 5,088 tokens, estimated as 3,000 | Output cap scaled to the window; estimates follow real token counts |
| The CLI's built-in guide swamped the window | `skills get core` is 16 to 28 KB depending on the CLI version, and the prompt required it first | Removed; the loop is in the prompt |
| Correct work, no answer | The model did every step, then ended inside its reasoning with empty text | One retry with reasoning off |
| The page was reported as unchanged after a form submit | A snapshot taken right after the click, 5 of 5 times | Wait for the page to settle first |
| Dropdowns failed for both a 4B and a 9B model | Options have their own refs; `select` on one reports "Done" and does nothing; `click` on one returns a low-level Chrome error | Repair by element role |
| One repeated call ended the turn | The order was placed, then a repeated failing read stopped everything | One refusal before stopping |
| A stuck model used all 40 turns | 34 browser calls and three minutes on one task | Stop after four ineffective actions |
| A 2B model left the browser for the shell | It ran `open`, `curl` and `sed`, and edited a page's HTML file on disk | Browser-only mode |
| Uploads arrived empty | Chrome reported 0 bytes for a relative path | Paths resolved inside the workspace |
| Downloads vanished | A clicked download link saved to `~/Downloads` and the page showed no change | Workspace download folder; arriving files reported |
| Malformed calls | Arguments under the wrong key or as one string; `@eN` typed literally | Read as meant; placeholder refused |

## Results

Six tasks on local test pages, three runs each, in browser-only mode with an 8,192-token window. Measured on an Apple M5 with 24 GB, Ollama 0.40.15, agent-browser 0.42.0 and Chrome 157, in November 2026.

| Task | `qwen3.5:4b` | `qwen3.5:2b` |
|---|---|---|
| Fill a form and click a button | 3/3 | 2/3 |
| Follow a link, read a table | 3/3 | 3/3 |
| Follow a link, read a paragraph | 3/3 | 3/3 |
| Dropdown, number, checkbox, submit | 2/3 | 1/3 |
| Upload a file | 3/3 | 1/3 |
| Download a file | 3/3 | 3/3 |
| **Total** | **17/18** | **13/18** |

For `qwen3.5:4b` the median task took 13 to 31 seconds and 2 to 6 browser calls. Its one failure placed the order correctly and then answered without reading the confirmation code.

How `qwen3.5:4b` moved on the first four tasks:

| Build | Passed |
|---|---|
| Before this work | 6/12 |
| Self-healing, all tools offered | 14/14 (the dropdown task run five times) |
| Self-healing, browser-only | 11/12 |

`qwen3.5:4b` used 3.8 GB of memory at an 8,192-token window, so it should fit an 8 GB machine alongside a headless browser, tightly. `qwen3.5:2b` is not reliable enough: its failures are the model's own (invented answers, loops, example values copied literally), not gaps cody can close.

## Limits

- Three to five runs per task is a small sample, and the pages are local test pages. Real sites are untested.
- Timings are from an M5. Speed on an M1 is unmeasured.
- Only `upload` and `download` paths are confined to the workspace; other file-writing commands such as `screenshot` and `pdf` are not.
- A large download can finish after cody checks for new files. `wait --download` covers that case.
- The test pages and the runner are not in the repository yet, so these numbers cannot be reproduced with `cody bench`.
