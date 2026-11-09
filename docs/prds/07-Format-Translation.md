# PRD 07 — Format Translation (`app/translate/`)

**Status:** partial · **Steps:** 1.8, 1.10

OpenAI Chat is the hub. Built in 1.8a: `app/translate`, whose hub is a typed Go value (`Request`, `Message`, `Part`, `Response`, `Chunk`), not a map. A codec per format reads a body into the hub and writes the hub back (`Codec`: `ReadRequest`, `WriteRequest`, `ReadResponse`, `WriteResponse`), registered by name; every conversion goes `source -> hub -> target`, direct pairs are not built, and a new format needs one codec. Each codec also has a stream half (1.8b): `NewStreamReader` turns the format's events into hub `Chunk`s and `NewStreamWriter` turns chunks into its frames (`StreamReader`, `StreamWriter`, `Frame`; `stream_<format>.go`). The transport that runs them is `app/stream` (PRD 9). The 1.8c pipeline is the caller; no route reaches it until 1.8d.

Formats: `openai`, `openai-responses`, `claude`, `gemini`, `gemini-cli`, `vertex`, `codex`, `antigravity`, `kiro`, `cursor`, `ollama`, `commandcode`.

| Direction | Codecs |
|---|---|
| Request, to hub | claude, gemini, gemini-cli, antigravity, openai-responses to openai |
| Request, from hub | openai to claude, gemini, gemini-cli, antigravity, vertex, kiro, cursor, ollama, commandcode, openai-responses |
| Response, to hub | claude, gemini family, vertex, kiro, cursor, ollama, commandcode, openai-responses to openai |
| Response, from hub | openai to claude, antigravity (Gemini SSE), openai-responses |

**Around every translation:** strip unsupported content types, normalize thinking intent, give every tool call an id, add missing tool results, filter to the target's accepted fields, prepare Claude messages (cache control, trailing user turn), cloak tool names for Claude OAuth tokens (and restore them on the way back).

**Detection:** by endpoint first (`/v1/responses` is Responses, `/v1/messages` is Claude), then by body: `input` without `messages` is Responses; `request.contents` with an Antigravity marker is Antigravity; `contents` is Gemini; OpenAI-only fields are OpenAI; Claude content blocks are Claude.

### Audit 2 Alignment

The reference translators were compared with base 9router 0.5.112 and the improvements recorded as divergences 183 to 189. These are the rules step 1.8 must implement: a history that ends on a function call is answered with a `functionResponse`; a system-only Gemini request becomes a user turn; Responses streams send a finish chunk on close and accept `response.done`; `thinking` is dropped when the last message is not the user's; the Claude system prefix is data (`thinking.claudeSystemPrefix`); OpenAI-shaped bodies lose the `developer` role, blocks of other APIs and blank messages; an empty Ollama answer has `content: ""`; `metadata.user_id` follows the reference key order; an all-zero `usage` object does not block the estimate.

### Semantics to Preserve

| Item | Rule |
|---|---|
| Thinking | Carried as `reasoning_content` in the hub. Claude thinking deltas and Gemini `thought` parts map to it; OpenAI to Claude opens a thinking block before text |
| Tool-call arguments | Buffered per index when the target needs a complete JSON string; the content block is opened once per index |
| Finish reasons | `end_turn`/`stop_sequence` and `stop`, `max_tokens` and `length`, `tool_use` and `tool_calls`, safety reasons and `content_filter`. A final `stop` becomes `tool_calls` when tool calls were emitted |
| Usage | Normalized per provider into prompt, completion, cached, cache-creation and reasoning tokens |
| Stream state | Each stream holds a state value (message id, tool calls by index, finish reason, usage, content block indices; Responses adds sequence numbers and text/argument buffers). A `nil` chunk means flush: translators close open blocks |

### Capabilities (`app/upstream/capabilities.go`, planned)

Nothing exists yet. The target shape is four embedded files under `data/capabilities/`: `capabilities.json`, `thinking.json`, `vision.json`, `params.json`, decoded strictly, size capped and validated at start.

| Call | Resolution |
|---|---|
| `For(provider, model)` | The provider's own row (full name, then the name after the last slash; provider aliases share a table), the exact model row, the first matching ordered glob (case-insensitive, `*` only), the defaults. Only a provider row is returned as is; the other steps turn vision on when the name says so, and a name on the not-vision list never does |
| `Levels(provider, model, declared)` | nil for a model that does not reason. Otherwise the registry's declared levels, then the first matching override (provider-qualified rows only for that provider), then the compatible-node rule, then the format's set, then the base set. `none` is removed when the model cannot disable thinking |
| `Budget(level)` | The token budget of a level |
| `ParamRules(provider, model)` | The `params.json` rules (drop or rename a parameter, drop or rename an assistant message field, flatten content, cap output tokens) in file order. The translator applies them last, to the final upstream body, on every pair |

The Claude and Gemini placeholder thinking signatures are data in `thinking.json`.

### Claude Disguise

**Built in 1.9c (divergence 216).** `translate.Cloak` shapes a finished Claude body and `pipeline` decides when and with which headers. Claude *preparation* below is separate and was built in 1.8a, because an Anthropic API key needs it.

**Follow the reference.** Add no gate or opt-out: a deployer who does not want the disguise does not connect a Claude login. The registry's `oauth.disguise` flag says which provider asks for it (only `claude`). Client headers never reach the upstream raw; the pipeline passes derived values. Cloaking, like the rest of the core translation, follows 9router 0.5.112 (`AGENTS.md`, Clean-Room): no disguise change is made until a step says so, even where a divergence row once suggested one, and the only rows kept are the ones a security rule or a router improvement requires.

A request is disguised when all of these hold: the upstream format is Claude, the connection is a login of a provider whose data sets `disguise`, its token begins `sk-ant-oat`, and the client is not Claude Code (a Claude Code client is native: its body is already right). Then, in this order, on the finished body:
1. a billing block becomes `system[0]` (a string system becomes two blocks; nothing is added if `system[0]` already starts with the prefix). Its `cch` is the first five hex characters of SHA-256 over the body as it stood before this step, the build part is three random hex characters, and the version is `2.1.280`;
2. `metadata.user_id` is added when the client sent none: `{"device_id":…,"account_uuid":…,"session_id":…}`, the ids derived from the token and the session from `app/conversation` with any `claude:` prefix removed;
3. every client tool without a `type` is renamed with the suffix `_ide`, in the tool list, in every `tool_use` of the history and in a forced `tool_choice` that names a renamed tool; twenty decoy tools follow. A body with no tools gets steps 1 and 2 only.

| Item | Behavior |
|---|---|
| Name map | Built for each attempt on each account, so a retry never carries another account's names |
| Answers | A tool name in the answer is restored by the map; a trailing `_ide` the map misses is dropped too, on every Claude answer, as the reference does (divergence 193, retired). A decoy name is left alone, and so is a name that strips back to a decoy, so the client sees "unavailable" rather than a silent call of a real tool. Non-stream: when the answer is read. Stream to a translated format: in the Claude stream reader, at the one event that names a tool. Stream to a Claude client: `stream.Job.Rewrite` on that event |
| `anthropic-beta` | Built per request from the registry's `oauth.beta`: the base flags, the heavy flags for models starting with one of its prefixes, minus the summary flag when the body says `thinking.display` is `summarized`, then the client's own flags (read from the allow-listed `Anthropic-Beta` header, each a plain token, at most 32, header at most 2 KiB), without repeats |
| Session header | `x-claude-code-session-id` carries the session the user id holds |
| Identity headers | The `oauth.identity` set (user agent, `X-App`, `X-Stainless-*`, the browser-access header) and the `oauth.urlSuffix` (`?beta=true`), for a login's token only; an API key on the same provider gets neither |
| Other nodes | A node whose base URL is not Anthropic's also gets the key as a bearer token and loses `x-app`, the browser-access header and `claude-code-20250219` **(not built: no node serves Claude through this path yet, 1.10)** |
| Client headers | Never copied. `Input.Header` holds only the headers the pipeline reads (`pipeline.PickHeaders`) |

### Thinking Writers for OpenAI-Format Upstreams (1.10d)

The OpenAI codec writes thinking by the model's `thinkingFormat` (`app/translate/openai.go`, `writeOpenAIThinking`). A format the data does not name keeps `reasoning_effort` with the level as the client asked.

| Format | Written (the reference's rule, `thinkingUnified.js`) | Levels |
|---|---|---|
| `nous` | `reasoning: {"enabled": true, "effort": <level>}`, the effort left out when there is no level; `auto` is `medium`; a budget is the level it stands for (the reference's thresholds, `budgetLevel`); `none` on a model that can switch off writes nothing | none, low, medium, high |
| `zai` | `thinking: {"type":"enabled"}`, plus `reasoning_effort` (`low` and `minimal` give `low`, `medium` and `high` give `high`, anything else `max`) only on a model whose capability row has `thinkingEffortSupported` (GLM 5.2 and later); `none` on a model that can switch off writes `enable_thinking: false` | none, thinking |
| `kimi` | `reasoning_effort`: `auto` is `high`, `minimal` is `low`, `xhigh` and `ultra` are `max`, `low`, `medium`, `high` and `max` pass, any other level is left out; `none` on a model that can switch off writes `thinking: {"type":"disabled"}` | none, low, medium, high, max |

A model that cannot switch thinking off is asked for `minimal` instead of `none`, for both formats. The capability rows are data: Nous per model id under `providers.nous`, Kimi per model id under `providers.kimi` (`thinkingCanDisable: false` for `kimi-k3`, `k3`, `kimi-for-coding*` and `kimi-k2.7-code*`). A Responses-format upstream such as Muse keeps the Responses writer. The level lists decide which levels the catalog and the dashboard offer; the writer sends the level the request carries, as the reference does. Only Codex moves a level to the nearest listed one (`PRD 8`).

### Wire Format by Model (1.10c)

A model entry's `targetFormat` picks the codec, and since 1.10c the address too: a provider whose transport names `responsesUrl` or `messagesUrl` sends a Responses-format model to the first and a Claude-format model to the second, and any other model to `baseUrl`. Both stay on the base address's host. GitHub Copilot is the first user. As in the reference, a Claude-named model goes to `/v1/messages` by its name, listed or not, since the live catalog gets new Claude models before a data file does; only `gpt-5.3-codex` is data (`/responses`), because the reference learns that from a 400 answer and this router does not learn at run time (divergence 234).

A provider whose client format has its own endpoint is served there with no translation: the data's `lanes` map names one endpoint per wire format, each with its own query string, static headers and credential scheme. Kimi is the first user (divergence 244, retired).

### Provider Quirks (1.10a)

A provider's `transport` names the few body edits it needs, as data. The codecs read them from `Ctx.Entry`; nothing else in the pipeline knows a provider by name.

| Name | Effect |
|---|---|
| `quirks.preserveCacheControl` | A Claude client's cache marker on a text block is kept when the upstream is OpenAI format and reads it (the alicode family). Without it the marker never leaves, and a plain text message stays a string |
| `quirks.requireClaudeToolType` | A Claude-format tool with no `type` is sent as `custom` (MiniMax). A typed tool is never touched |
| `reasoningInject` (`all`, `toolCalls`) | An assistant turn with no reasoning text gets a single space as `reasoning_content`, for upstreams that refuse a thinking conversation without it. `toolCalls` limits it to turns that called a tool. With no provider setting the model decides: `kimi-*` gets `toolCalls`, a name holding `deepseek` gets `all`. The provider's scope beats the model's |
| model `upstreamId` | The id sent on the wire when it differs from the id clients use (blackbox). The usage row keeps the client's id |
| Capability `dropOutputConfig` | MiniMax drops `output_config` (a `capabilities.json` pattern row, as before) |

### Claude Preparation (`prepareClaude`)

Every body bound for a Claude upstream, translated or not, ends here.
- `output_config` goes when the provider quirk `dropOutputConfig` says so; `context_management` goes unless the provider is Anthropic or an `anthropic-compatible-*` node.
- A rejected `disabled` thinking type becomes the model's `thinkingOffType`; forced tool choice is relaxed where the model refuses it; `max_tokens` is cut to the model's `maxOutput` and kept above the thinking budget.
- Cache markers: the last system block and the last cacheable tool (not `defer_loading`) get 1h, the last assistant block 5m, and the final tool results of an agent loop 5m while fewer than four markers are set; a conversation with no assistant turn yet marks its last message instead. Client markers are dropped, so at most four are ever sent. A tool-call conversation keeps its tools, system and earlier messages byte-identical from turn to turn (tested), which is what lets the upstream read its cache. Cache writes of the one hour lifetime are priced at `cache_creation_1h`, else twice the input rate (`PARITY.md` §4).
- History repair: blank messages go, text after a `tool_use` goes, neighbouring turns of one role merge with tool results first, a user turn is restored when the client did not end on the assistant.
- Thinking blocks are kept, re-signed or dropped by provider. The signature check accepts the single and double base64 forms with the 0x12 marker; the placeholder is the `claudeSignature` field of `thinking.json`; a blank block never survives.
- Outside Anthropic, tools are limited to the provider's `claudeSupportedToolTypes` and tool-result images move into the user turn.

### Responses Codec

`ResponsesCodec` joins the Responses API to the hub.

| Direction | Behavior |
|---|---|
| Request reader | `instructions` becomes the system turn; an empty `input` becomes a `...` user turn; `reasoning` items become `reasoning_content` and `encrypted_content` on the next assistant turn; `function_call` and `custom_tool_call` items become tool calls (a call with no name is skipped); hosted tools without a name are dropped |
| Custom tools | Declared to a chat upstream as a function with one string parameter `input`. The name is kept on `Ctx.CustomTools` (the one field a request reader writes), so the answer and the stream name it as a `custom_tool_call` again |
| Request writer | Sends `store:false`, clamps call ids to 64 characters, flattens tool declarations. A Codex or Grok CLI call is then reshaped by its executor (PRD 8): the codec's body is the input, the backend's allowlist the output |
| Stream writer | Holds `response.completed` until the usage trailer arrives (Close sends it otherwise), numbers every event, ends with it. An error chunk is answered by the transport with `response.failed` (`failure.Frame`) |

### Gemini Codec

`GeminiCodec` joins `generateContent` to the hub.

| Direction | Behavior |
|---|---|
| Request reader | Takes `systemInstruction`, text, `thought` parts (reasoning on the assistant turn), `inlineData` as image, audio or PDF blocks, `functionCall` and `functionResponse` (a call without an id gets one; a result without an id answers the oldest unanswered call of its name), `toolConfig` and the JSON response settings. Upper-case schema types are lowered |
| Request writer | Cleans every tool schema to the subset Gemini accepts (`cleanGeminiSchema`), sanitizes function names, makes every call id unique over the whole history, pairs each result with its call, merges neighbouring turns of one role, starts and ends on a user turn, drops sampling fields for Gemini 4 |
| Signatures | A thought signature rides on the tool call as `extra_content.google.thought_signature` and is sent back when present. When a client returns the call without it, `Ctx.Calls` (the `toolcalls` cache group, scoped per API key; `PRD 20`) restores it; a signature the client kept wins. The capability table's placeholder is used only for a first call no Gemini model issued |
| Stream writer | Holds tool calls until the turn ends (a Gemini call carries whole arguments) and the closing chunk until the usage trailer arrives |
| Usage | Completion tokens include thought tokens in both directions |

---

## Parity: Translators

All go through the OpenAI hub; no direct pairs (PRD 7). References under `translator/`.

| Translator | Reference | Status | Step |
|---|---|---|---|
| claude to/from openai | `request/claude-to-openai.js`, `request/openai-to-claude.js` | todo (D115) | 1.8 |
| gemini to/from openai | `request/gemini-to-openai.js`, `request/openai-to-gemini.js` | todo | 1.8 |
| openai to/from openai-responses | `request/openai-responses.js`, `response/openai-responses.js`, `formats/responsesApi.js` | todo (request, body, stream, both ways; D119-120) | 1.8 |
| openai to/from ollama | `*/*ollama*.js` | todo (all four directions; D136) | 1.8 |
| Concerns: thinking, tool calls, usage, finish reasons, images, modality, params | `concerns/*` | todo (also same-format, D138; param rules in `params.json`: claude temperature, groq, openai, GitHub; OpenAI-shaped bodies drop `developer`, foreign blocks, blank messages as `filterToOpenAIFormat`; audit 2 fixes D183-D188) | 1.8 |
| Capability, thinking-level and vision tables | `providers/{capabilities,thinkingLevels,visionPatterns}.js` | todo (`app/upstream`, `data/capabilities/*.json`; catalog overlay 2.6, commandcode and kiro rules 1.10) | 1.8 |
| gemini-cli, antigravity (to and from openai) | `request/gemini-to-openai.js`, `openai-to-gemini.js`, `antigravity-to-openai.js` | todo | 1.10 |
| openai to vertex | `request/openai-to-vertex.js` | todo | 1.10 |
| kiro, cursor, commandcode (to and from openai) | `*/*kiro*.js`, `*cursor*.js`, `*commandcode*.js` | todo | 1.10 |
| Webtools translators for web providers | `*webTools*` | deferred | - |

---

## Remaining work
**Steps 1.8a and 1.8b are built.**

- [x] The OpenAI hub and the OpenAI, Claude, Responses, Gemini and Ollama codecs for request and non-stream answer (1.8a)
- [x] Claude preparation: cache markers (at most four, tool-call prefixes byte-stable), history repair, thinking-block signatures, `max_tokens` cap, tool-choice relaxing (1.8a)
- [x] The concerns: tool-call ids and results, finish reasons, usage invariants, modality, thinking, parameter rules applied last (1.8a)
- [x] The capability tables in `app/upstream` (1.8a)
- [x] The stream direction of every codec: readers and writers for OpenAI, Claude, Responses, Gemini and Ollama; hold-for-usage; the `<think>` tag split for Responses; Claude `Read` tool argument repair (1.8b)
- [x] Image prefetch (`app/prefetch`, 1.8d): Gemini and Ollama targets only, magic bytes decide the type
- [x] Claude cloak: tool suffix, decoys, billing block, user id, restore on body and stream (1.9c)
- [ ] Step 1.10: gemini-cli, antigravity, vertex, kiro, cursor and commandcode translators

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 169 | Caller system, developer turns dropped; fixed prompt the only system turn; user content keeps typed blocks | Gateway accepts one fixed shape; data file and test state it, so the loss is deliberate and visible |
| 80 | Both removed first, any order, in resolver. **(partial)** nothing sends `[1m]` beta header yet | Name with both resolves |
| 115 | Each kept or carried; one id rule | No silent loss |
| 112, 113, 114, 118 | Strict JSON (unknown fields, inverted ranges, repeated patterns, secret-shaped names stop start); level tables, signature, `dropOutputConfig` as data; commandcode, kiro special cases leave lookup **(planned 1.10)**; catalog overlay **(planned 2.6)** | Bad data stops the start |
| 121, 122, 123, 124 | Full Gemini codec both ways. Model id `^[A-Za-z0-9_.-]+$`; unknown action Gemini 404; list honors key access. Real signatures carried; placeholder only on first unsigned call of a model turn, never over real one. Every call id unique; results paired with oldest unanswered call | Gemini clients keep tools, attachments; bad id cannot change the path |
| 131, 132 | Explicit `stream` wins; original names restored; collisions get numeric suffix | Clients get the mode they asked for |
| 136 | Real Ollama codec, four directions; exactly one `done` line with `done_reason`, counts; `"stream": false` returns one JSON body; Ollama error shape; Ollama upstreams spoken natively | Tools, images, counts work |
| 138 | Same-format non-OpenAI bodies: thinking normalization, provider param rules only; OpenAI bodies: all concerns **(trade-off)** | OpenAI to OpenAI repaired. Cost: native bodies keep missing tool ids or results; hub round trip would lose native fields |
| 245 | **Retired.** The Kimi and Nous thinking writers follow the reference (`thinkingUnified.js`); "cannot switch off" comes from the capability row's `thinkingCanDisable`, which is data in both | Recorded so the number stays citable |
| 163 | Model entry may carry `defaultThinking` (used only when name, request, client fields ask nothing) and `thinking` (fixed by model id, beating request); both use name-suffix grammar. DeepSeek V4 Pro defaults to high, declares low to max, passes level asked; `-low`, `-medium`, `-high`, `-xhigh`, `-max`, `-none` pinned variants. Model declaring no levels keeps two-level wire | Coding client gets reasoning without naming it; every level reachable; default is operator-editable data |
| 168 | Per-format transport naming no headers inherits base ones; own URL suffix never inherited | Data stays short; header cannot be forgotten on one format |
| 183 | Codec also maps seed, stop, both penalties, `think`, `response_format` both ways; `developer` becomes `system`; tool calls numbered by running counter; content on final line kept; each answer opens with role chunk; `content: ""` for empty reply. Options with no hub equivalent (`top_k`, `repeat_penalty`, `min_p`, `num_ctx`, `keep_alive`) and `tool_choice` not forwarded | Endpoint has no tool choice field; rest keeps what a client asked for |
| 184 | Kept: native `reasoning{effort,summary}` for Responses upstreams, `output_config` keys, every Gemini system turn joined, Gemini `tool_choice`, `stop`, `seed`, `response_format`, Claude `top_p`, `user`, `stop`, Responses `incomplete` mapping, `reasoning_text.delta`, Gemini thought tokens counted apart from candidates, cache tokens kept on `message_stop` fallback, tool block reopened when its id changes, id-less tool result taking id at its position | Client settings reach upstream instead of vanishing in the hop |
| 185 | `metadata.user_id` built in reference order (`device_id`, `account_uuid`, `session_id`); billing block, decoys, renamed tools re-encoded with sorted keys; `cch` hashes our encoded bytes, so the five characters differ from reference for the same request | Go maps have no order; only a byte fingerprint changes; hand-ordered encoder for the whole body costs more than it gains |
| 186 | Rules for every upstream format (so `temperature` also dropped from native Claude body); strip after injection; exclusion one glob; image fetch up to 3 redirects, 20 s limit, through SSRF guard; rules validated on load (no empty rule, no negative cap, no credential-shaped pattern, no exclude without match) | One rule table for every route; bad table stops the start instead of misrouting traffic |
| 190 | Endpoint alone decides: `/v1/chat/completions` OpenAI, `/v1/messages` Claude, `/v1/responses` Responses, `/v1beta/models/*` Gemini, `/v1/api/chat` Ollama. Body of another shape gets that format's error | One rule per route; wrong-shape body fails loudly instead of being served as a format the client did not choose |
| 142 | Every Claude-format body bound for Anthropic or a node fronting it | Removes only what Anthropic rejects; no dependence on a header |
| 193 | **Retired.** A trailing `_ide` is dropped from a tool name on every Claude answer when the map misses it, as the reference does; a decoy name, or a name that strips back to one, is left alone | Recorded so the number stays citable. The map is still rebuilt for every attempt, so it is never lost across a retry |
| 216 | The Claude disguise (tool suffix, decoys, billing block, user id) was built with the Claude OAuth provider in step 1.9c instead of 1.8 **(done, 1.9c)** | It needs an OAuth account to exist and to test against; Claude preparation, which an API key needs, is built in 1.8a (planned) |
| 217 | Every writer holds its closing event until the usage report or `STREAM_USAGE_WAIT` | A usage trailer sent after the finish chunk reaches Claude and Gemini clients too |
| 218 | An upstream error event (Claude `error`, Responses `response.failed`, Gemini or Ollama error body, an OpenAI `error` object) fails the stream with the client's own error frame | The reference dropped it and the stream ended with no finish and no error |

---

## Findings

**The hub is typed so a conversion cannot silently invent or lose a field.** Unknown top-level fields of a body ride in `Extra` and are written back to the same format, which is what keeps a same-format request from losing what the hub does not model. A map hub would have made every codec a copy of the same guesswork.

**Encoding is deterministic.** A Go map has no order, so every object is written through an ordered encoder. Two encodings of one request are byte-identical, which is what lets an upstream read its prompt cache from one turn of a tool-call conversation to the next; a test pins that the system, tools and earlier messages do not change when a turn is appended.

**The cache-marker budget is reached by one case.** A conversation that ends with many parallel tool results was the only input that could exceed four markers, and the first draft's tests never produced it. A dedicated test now does, and it is why the final-results step is bounded.

**A schema cleaner must not depend on map order.** An enum whose declared type was `integer` came out `integer` or `string` depending on which key was walked first. The rule (an enum is always strings with an explicit `string` type) now runs after the walk.

**A decoder error can carry the input.** Go's JSON decoder names an unknown key in its message, and a key can be a secret pasted in the wrong place. Loader errors give a fixed reason only (this is true of the registry and the capability tables alike).

**Streams meet at the hub chunk, and the writer owns the hold.** Every reader emits `Chunk`s whose first one carries the role, with at most one finish and usage as a separate report merged by maximum; every writer holds its closing event until usage arrives (`Held`, `Close`). That is what let one usage timer in `app/stream` serve five formats, and what makes the all-pair test (25 reader/writer pairs over one scripted turn) a real check rather than five unrelated ones.

**A whole-call format cannot be fed fragments.** Gemini and Ollama carry a tool call in one part or line, so their writers collect fragments and send the call with the turn. The first version wrote each fragment and produced two calls for one.

**The disguise is a body transform, not a codec concern.** `Cloak` runs on the finished Claude body with `gjson` and `sjson`, so every other byte of the client's request, the cache markers `prepareClaude` placed and the key order stay as they were; a test counts markers before and after and another checks a tool list is identical from turn to turn. The codec learned two things only: to carry `thinking.display` (the beta list depends on it) and to give a tool name back through `Ctx.ToolName`.

**Restoring a name happens where the name appears.** A Messages stream names a tool once, in `content_block_start`. The translated path restores it in the stream reader, the collected path goes through the same reader, and the passthrough path (a Claude client on a Claude upstream) forwards frames untouched, so it gets a one-event `Rewrite` hook that is nil when nothing was renamed.

**One mutant was an equivalent branch.** `Uncloak` first refused to strip a decoy name, but no decoy ends in `_ide`, so the guard could never fire; it was removed and the comment says why.

**A thinking field was being dropped.** The hub kept the thinking mode, level and budget but not `display`, so a Claude client that asked for summaries lost them even on a Claude upstream. It is carried now, and it joins the response-cache key.

**Not verified without a live account:** that Anthropic accepts a disguised request, that the nine beta flags are still the current list, and that the version strings (`claude-cli/2.1.280`, `codex_cli_rs/0.159.0`) are still accepted.
