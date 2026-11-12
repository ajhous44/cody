# Browser control

How cody drives a browser with small local models, why each piece exists, and what was measured. The target is a model that fits an 8 GB machine.

## How it works

The `browser` tool runs one [agent-browser](https://github.com/vercel-labs/agent-browser) command per call in cody's own headless session. Around that one command cody does six things. None of them costs a model turn.

**1. Shows the page after every action.** cody waits for the page to settle (network idle, at most 5 seconds), then appends what changed since the last look: the changed lines, the whole page after a navigation, or "The page did not change." It watches the whole page, text included, so a result that appears as plain text is visible without a separate read. The model never needs a snapshot call of its own and never acts on stale element refs.

**2. Leads a long page with what was asked for.** A page too big to show whole is reduced to its controls, and the result starts with two things taken from the user's request by plain word matching: the page's own text about the rarest word of the request (the browser CLI's section filter), and the lines that mention the request, rarest words first. Truncation then cuts the least relevant part, not the answer.

**3. Reads arguments as they were meant.** Small models get the call shape wrong in predictable ways. cody accepts the command under another key name, as a JSON string, or as one plain string, and drops a repeated program name. A ref handed to `read` is dropped. The literal placeholder `@eN` is refused with a plain message before it reaches the browser.

**4. Repairs a wrong action once.** When an action fails or changes nothing, cody asks the browser CLI for the element's role (about 10 ms) and reruns the action that fits it, then tells the model what it ran.

| The action was aimed at | cody runs |
|---|---|
| an option inside a dropdown | `select` on the dropdown, with the option the model named, or else that option |
| a dropdown, with a value | `select` |
| a text or number field, with a value | `fill` |
| a checkbox or radio button | `check` |
| a file chooser, with a path | `upload` |
| a file chooser, without a path | `upload` of the one workspace file the user's request names; otherwise it says to use `upload` with a path |

Nothing is looked up when an action works, and only one repair is tried per action.

**5. Keeps files in the project folder.** `upload` and `download` paths are resolved inside the workspace and refused outside it. A relative upload path is made absolute, because Chrome ignores relative ones; a folder the model invented falls back to the workspace file of the same name. Downloads land in the workspace unless `AGENT_BROWSER_DOWNLOAD_PATH` is set, and cody reports each file that arrives, since a download changes nothing on the page.

**6. Knows when to stop.**

- After four browser actions in a row with no effect, further actions are refused until the page is reopened.
- A repeat of a call that just failed is refused once, with its batch, and the model gets one turn to recover. A second repeat ends the turn.
- An empty reply is asked for once more with reasoning off.

Three things outside the tool matter as much:

- **The context window.** Ollama loads every model at 4,096 tokens unless told otherwise, and its OpenAI-compatible endpoint cannot be told. cody uses Ollama's native API and sets the window on every request (`context_tokens`, 8,192 by default). One tool result is capped at about a quarter of the window, and cody corrects its size estimates with the token counts the provider reports.
- **Stale pages.** When the window fills, page listings that a later page replaced become a one-line stub before any summary is paid for. Text the model read from a page is kept, and the saved session is untouched.
- **Browser-only mode.** `--browser-only` gives the model the browser and nothing else, with a short prompt of its own. Fixed overhead is about 390 tokens instead of roughly 2,000, and the model has no shell or file tools to reach for.

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
| The answer was on the page but never seen | An order was placed, then answered without its confirmation code: the listing showed controls only | Show the whole page, text included, when it fits |
| A fact far down a long page | GitHub repo page: a 23 KB listing, the license past the 4 KB shown; one run searched for seven minutes, one guessed wrong | Lead long pages with the lines that mention the request |
| A fact in the text of a long page | Python docs page: "Python" matched nearly every line, and the model guessed a version | The page's text about the rarest word of the request, first |
| One page filled most of the window | A Wikipedia article took 57% of an 8,192-token window | Drop replaced page listings before summarizing |
| The right value, the wrong option | `select` aimed at "Widget" while naming "Gadget" | The named value wins |
| Told how to upload, the model gave up | After clicking a file chooser it asked the user to pick the file | Upload the one file the request names |

## Results

`qwen3.5:4b` in browser-only mode with an 8,192-token window, on an Apple M5 with 24 GB, Ollama 0.40.15, agent-browser 0.42.0 and Chrome 157, in November 2026. It passed every run: 57 of 57.

| Local test pages (5 runs each) | Passed | Median |
|---|---|---|
| Fill a form and click a button | 5/5 | 24 s, 3 calls |
| Follow a link, read a table | 5/5 | 16 s, 2 calls |
| Follow a link, read a paragraph | 5/5 | 16 s, 2 calls |
| Dropdown, number, checkbox, submit | 5/5 | 31 s, 5 calls |
| Upload a file | 5/5 | 18 s, 2 calls |
| Download a file | 5/5 | 14 s, 2 calls |

| Real sites (3 runs each) | Passed | Median |
|---|---|---|
| Wikipedia: search, open the article, answer from it | 3/3 | 20 s, 3 calls |
| httpbin.org order form: fill, submit, read the server's echo | 3/3 | 29 s, 5 calls |
| the-internet.herokuapp.com: read a value from a data table | 3/3 | 10 s, 1 call |
| the-internet.herokuapp.com: follow a redirect link, list what is there | 3/3 | 20 s, 2 calls |
| GitHub: find a repository's license | 3/3 | 11 s, 1 call |
| Hacker News: the title of the top story | 3/3 | 11 s, 1 call |

| Real sites never used while building this (3 runs each) | Passed | Median |
|---|---|---|
| Python docs: the version a function was added in, far down a long page | 3/3 | 9 s, 1 call |
| IANA: the RFCs cited for example domains | 3/3 | 10 s, 1 call |
| Selenium's demo form: text, dropdown, checkbox, submit | 3/3 | 33 s, 5 calls |

Every answer was checked against something only the finished task reveals, such as a server's echo or a value on the final page.

How `qwen3.5:4b` moved:

| Build | Local pages | Real sites |
|---|---|---|
| Before this work | 6/12 | not measured |
| First self-healing build | 17/18 | 10/12 |
| This build | 30/30 | 27/27 |

`qwen3.5:4b` used 3.8 GB of memory at an 8,192-token window, so it should fit an 8 GB machine alongside a headless browser, tightly.

`qwen3.5:2b`, the next size down (3.1 GB on disk), passed 34 of 36 runs on this build: 12 of 12 on the six real sites and 6 of 6 on the three unseen ones (two runs each), and 16 of 18 on the local pages (three runs each). Both misses were the model stopping to ask the user instead of acting. On the first self-healing build it passed 13 of 18 local runs.

## Limits

- These are small samples: three to five runs per task, fifteen tasks. Passing all of them is not a guarantee for every site.
- Timings are from an M5. Speed on an M1 is unmeasured.
- Sites that need a login, show a CAPTCHA or block automation are out of scope: cody is told not to bypass them.
- Leading a long page with what was asked is word matching. A request that shares no words with the page gets the top of the page, as before.
- Only `upload` and `download` paths are confined to the workspace; other file-writing commands such as `screenshot` and `pdf` are not.
- A large download can finish after cody checks for new files. `wait --download` covers that case.
- The test pages and the runner are not in the repository yet, so these numbers cannot be reproduced with `cody bench`.
