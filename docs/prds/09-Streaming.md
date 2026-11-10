# PRD 09 — Streaming (`app/llm/stream/`)

**Status:** partial · **Steps:** 1.0, 1.8, 2.4

Built in 1.8b, as a library: the 1.8c pipeline calls it; the chat routes reach it since 1.8d and 1.8e. `app/llm/stream` holds the writers (`NewSSE`, `NewNDJSON`, `WriterOf`), the upstream readers (`Reader` for SSE, `Lines` for NDJSON), `Run` (the pump with its three timers, translate and passthrough modes), `Collect` (forced SSE to JSON), `Tracker`, `AddClientBuffer` and the content-type helpers. The stream half of each format lives in `app/llm/translate` (`StreamReader`, `StreamWriter`, `Frame`; `stream_<format>.go`). The empty-stream peek is combo failover and belongs to 2.4.

| Item | Behavior |
|---|---|
| Parse | Upstream SSE is parsed by our own reader in `app/llm/stream` (event limit 1 MiB, also over a single unterminated line; strips a BOM, accepts `\n`, `\r`, `\r\n`; joins multi-line `data`; drops comments, `id`, `retry`; a last event with no blank line still counts). Bare JSON lines (Ollama, commandcode) are not SSE events and go through `Lines`, which takes JSON objects only. The `event:` line is kept for Claude and Responses framing. `tmaxmax/go-sse` is not a dependency here |
| Modes | *Translate* when source and target formats differ; *passthrough* otherwise (fix ids, add `object` and `created`, drop empty `tool_calls`, drop non-JSON lines) |
| Emit | Claude: `event: <type>` + `data:`. Responses: `event: <name>` + `data:`. Ollama clients: one JSON line each through `NewNDJSON` (divergence 219). Everything else: `data:`. `[DONE]` closes OpenAI-style streams and Responses passthrough; Claude, Gemini and Ollama clients never get one |
| Hold for usage | Every writer holds the closing event of a turn (OpenAI finish chunk, Claude `message_delta` + `message_stop`, Responses `response.completed`, Gemini closing chunk, the Ollama `done` line) until the usage report arrives or `STREAM_USAGE_WAIT` (3 s) passes, then closes without it (divergence 217) |
| Codex peek | Before the stream is handed to the pump, the Codex executor reads the start of a 200 answer for an error event (up to 256 KiB, or until the first output delta). It ends at once when no byte arrives for `STREAM_STALL_TIMEOUT`, hands over what it read, and the timers below take over (D228). The bytes read are replayed first, so the client sees the whole answer |
| First chunk | `STREAM_FIRST_CHUNK_TIMEOUT` (200 s) |
| Stall | `STREAM_STALL_TIMEOUT` (360 s; `Timers.Stall` is set per call, so a registry entry can override it when one needs to). Any upstream event restarts it |
| Stream dies after a 200 | A format-appropriate error frame from `failure.Frame`: OpenAI error + `[DONE]`, Claude `event: error`, Responses `response.failed`, Gemini a data frame, Ollama an error line. 504 only for a stall or a missing first chunk, 502 for any other break (divergence 135). An error event inside the upstream stream gets the same frame and never repeats the upstream's text (divergence 218). No frame is written to a client that already left |
| Client disconnect | Cancels the upstream request through the request context |
| Non-SSE upstream error | An HTML page becomes a clean JSON error with the page title |
| Forced SSE to JSON | `Collect` reads the whole stream through the upstream's `StreamReader` and returns one hub `Response` (text, reasoning, tool calls by index, signature, images, finish, usage); the client codec's `WriteResponse` renders it. One implementation for every upstream format |
| Completion | `Run` returns a `Result` (raw usage, estimate flag, finish, content length, first-byte time, whether a byte reached the client, the cause, the failure status); the pipeline turns it into one usage row (1.8c); `Wrote` decides whether the next account may still be tried |

**Peek failover.** For combo members, the first bytes of an SSE answer are held until content, a tool call or an error arrives, or `STREAM_EMPTY_PEEK_DEADLINE` (45 s) passes. An empty generation then fails over to the next member. This is the only place a stream is buffered.

### Usage Extraction

`Tracker` is fed the upstream's own payload in the upstream format, before any translation, so the stored row comes from one reading of what the provider reported. `ExtractUsage(format, data)`:

| Format | Source |
|---|---|
| OpenAI | `usage`; cached from `prompt_tokens_details` or `prompt_cache_hit_tokens`; cache writes from `prompt_tokens_details.cache_creation_tokens` |
| Claude | `usage`, `message_start.message.usage`, `message_delta.usage`; prompt = input + cache read + cache creation |
| Gemini | `usageMetadata`; prompt includes cached; completion = candidates + thoughts, thoughts also reported as reasoning; candidates derived from the total when missing |
| Responses | `response.usage` or `usage` |
| Ollama | The `done:true` line |

Usage is read in one place: `translate.ReadUsageEvent(format, data)` finds the object per event and calls the same per-format readers the non-stream answers use. `Tracker` only merges what it returns.

Partial reports merge by field-wise maximum (`translate.MergeUsage`). Cached and cache-creation counts are cut back to the prompt, reasoning to the completion. With no report, usage is estimated at four characters a token from the request body and the streamed text, and `Usage.Estimated` says so; a stream with no text is not estimated.

**Buffer rule.** `AddClientBuffer(format, data)` adds 2000 tokens to every prompt count, and to the total beside it where the format has one, in every client-bound payload that carries a usage object, streaming or not. A prompt count of 0 stays 0. It is the last step before a write and never reaches the row.

---

## Remaining work
**Step 1.8b is built** (library; no route calls it).

- [x] SSE and NDJSON readers, the NDJSON writer, usage capture, the client buffer, the three timers, translate and passthrough modes, forced SSE to JSON, content-type helpers (1.8b)
- [x] `Run` and `Collect` wired into the pipeline, the usage row recorded, an HTML upstream page answered with a 502 (1.8c)
- [ ] Empty-stream failover and the peek deadline `STREAM_EMPTY_PEEK_DEADLINE` (2.4)
- [ ] Per-provider stall override from the registry (1.10)

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 101, 103 | Body carries `retryAfter`, OpenAI `type`, `code`; timer covers every stream | Combo picks earliest member |
| 29, 41, 135 | Client's format on every route; 413, 408; broken stream 502 frame, stall only 504 | SDKs parse their shape; size, timeout distinguishable |
| 14 | Only formats that stream NDJSON accept it | Stricter parsing |
| 233 | What a stream may add up to is capped at 64 MiB (text, reasoning and tool-call arguments together), for a stream collected into one answer and for a translated stream alike. Past it the stream ends with the client's own error frame ("The upstream answer is too large") and the account is failed over like any broken stream | A forced-stream upstream that keeps sending just under the stall timer grew memory without end; the non-stream answer already had this cap (improvement) |
| 100, 116 | Exactly one `[DONE]` on every path | No double marker |
| 119 | Error chunk (`response.failed` for Responses clients); completion on usage or stream end | Error never an answer; no timer |
| 191 | OpenAI, Claude, Responses: missing `stream` false, as their APIs; Gemini streams by URL action (`:streamGenerateContent`), never body; only Ollama route streams unless body says `"stream": false`, as Ollama does | Matches each vendor's documented default; bare request returns one JSON body |
| 219 | Ollama clients stream NDJSON through `stream.NewNDJSON` | The reference sent them SSE, which an Ollama client does not parse |
| 149 | HTML on any call, or type neither stream nor JSON on streaming one: 502 `upstream_5xx`, client's error shape, page title (no markup); account not locked, answer clears no lock; JSON, missing type pass as before | Client sees failing status instead of 200 carrying an error; gateway page says nothing about the credential |

---

## Findings

- The writers, not the pump, own the hold-for-usage rule. Giving each writer a `Held()` lets the pump arm one usage timer for every format and call `Close()` when it fires, instead of every format re-implementing a timer.
- A Gemini function call and an Ollama tool call arrive whole, so those two writers collect fragments and send them with the turn. Writing a fragment as it came produced two calls for one on the first test run.
- `translate.ReadUsageEvent` made the old "two copies of each usage rule" note false: the stream tracker calls the response readers.
- `NewSSE` and `NewNDJSON` share one `sink` (flush, deadline, sticky error). The sink is embedded by pointer: it holds a mutex, and `go vet` rejects copying one.
- A `\ufeff` escape in a Go string survives; a literal byte-order mark in source is a compile error, so the reader and its tests use the `bom` constant.
