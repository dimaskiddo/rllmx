# Design 07 — Format Translation (`app/llm/translate/`)

**Status:** done

OpenAI Chat is the hub: `app/llm/translate`, whose hub is a typed Go value (`Request`, `Message`, `Part`, `Response`, `Chunk`), never a map.

- A codec per format reads a body into the hub and writes the hub back (`ReadRequest`, `WriteRequest`, `ReadResponse`, `WriteResponse`), registered by name.
- Every conversion goes `source -> hub -> target`. Direct pairs are not built; a new format needs one codec.
- Each codec has a stream half: `NewStreamReader` turns its events into hub `Chunk`s, `NewStreamWriter` turns chunks into frames (`stream_<format>.go`).
- `app/llm/stream` runs them (design doc 9), and `gateway/pipeline` is the only caller.

Formats: `openai`, `openai-responses`, `claude`, `gemini`, `gemini-cli`, `vertex`, `codex`, `antigravity`, `kiro`, `cursor`, `ollama`, `commandcode`.

| Direction | Codecs |
|---|---|
| Request, to hub | claude, gemini, gemini-cli, antigravity, openai-responses to openai |
| Request, from hub | openai to claude, gemini, gemini-cli, antigravity, vertex, kiro, cursor, ollama, commandcode, openai-responses |
| Response, to hub | claude, gemini family, vertex, kiro, cursor, ollama, commandcode, openai-responses to openai |
| Response, from hub | openai to claude, antigravity (Gemini SSE), openai-responses |

**Around every translation:** strip unsupported content types, normalize thinking intent, give every tool call an id, add missing tool results, filter to the target's accepted fields, prepare Claude messages (cache control, trailing user turn), adapt tool names for Claude login tokens (and restore them on the way back).

**Detection:** by endpoint first (`/v1/responses` is Responses, `/v1/messages` is Claude), then by body: `input` without `messages` is Responses; `request.contents` with an Antigravity marker is Antigravity; `contents` is Gemini; OpenAI-only fields are OpenAI; Claude content blocks are Claude.

### Audit 2 Alignment

The reference translators were compared with base 9router 0.5.116 and the improvements recorded as divergences 183 to 189. The rules the implementation keeps:

| Rule | What it means |
|---|---|
| A history ending on a function call | The answer is a `functionResponse` |
| A system-only Gemini request | Becomes a user turn |
| Responses streams | Send a finish chunk on close and accept `response.done` |
| `thinking` | Dropped when the last message is not the user's |
| The Claude system prefix | Data (`thinking.claudeSystemPrefix`), not a literal |
| An OpenAI-shaped body | Loses the `developer` role, blocks of other APIs and blank messages |
| An empty Ollama answer | Carries `content: ""` |
| `metadata.user_id` | Follows the reference key order |
| An all-zero `usage` | Does not block the estimate |

### Semantics to Preserve

| Item | Rule |
|---|---|
| Thinking | Carried as `reasoning_content` in the hub. Claude thinking deltas and Gemini `thought` parts map to it; OpenAI to Claude opens a thinking block before text |
| Tool-call arguments | Buffered per index when the target needs a complete JSON string; the content block is opened once per index |
| Finish reasons | `end_turn`/`stop_sequence` and `stop`, `max_tokens` and `length`, `tool_use` and `tool_calls`, safety reasons and `content_filter`. A final `stop` becomes `tool_calls` when tool calls were emitted |
| Usage | Normalized per provider into prompt, completion, cached, cache-creation and reasoning tokens |
| Stream state | Each stream holds a state value (message id, tool calls by index, finish reason, usage, content block indices; Responses adds sequence numbers and text/argument buffers). A `nil` chunk means flush: translators close open blocks |

### Capabilities (`app/llm/upstream/capabilities.go`, built)

Four embedded files under `data/capabilities/` — `capabilities.json`, `thinking.json`, `vision.json`, `params.json` — decoded strictly, size capped and validated at start.

| Call | Resolution |
|---|---|
| `For(provider, model)` | The provider's own row (full name, then the name after the last slash; provider aliases share a table), the exact model row, the first matching ordered glob (case-insensitive, `*` only), the defaults, then the synced catalog. Only a provider row is returned as is; the other steps turn vision on when the name says so, and a name on the not-vision list never does |
| `Base(provider, model)` | The same without the synced catalog: what the tables say alone, which a sync compares against |
| `Levels(provider, model, declared)` | nil for a model that does not reason. Otherwise the registry's declared levels, then the first matching override (provider-qualified rows only for that provider), then the compatible-node rule, then the format's set, then the base set. `none` is removed when the model cannot disable thinking |
| `Budget(level)` | The token budget of a level |
| `ParamRules(provider, model)` | The `params.json` rules (drop or rename a parameter, drop or rename an assistant message field, flatten content, cap output tokens) in file order. The translator applies them last, to the final upstream body, on every pair |

The Claude and Gemini placeholder thinking signatures are data in `thinking.json`. A registry model may state its own `contextLength`: the window its gateway truncates to, where it differs from the vendor's. It only stops a catalog sync from writing a context delta; the capability answer still comes from the tables.

**The synced catalog.** `DATA_DIR/model-catalog.json` holds deltas from models.dev. A runner loads it at start and refreshes it every 24 hours (`app/providers/catalog_sync.go`; `CATALOG_SYNC=false` stops the timer only), and `POST /api/models/catalog-sync` refreshes it on demand. The file carries its own version, so a copy this build did not write is ignored and rebuilt.

- `models["<provider>:<baseId>"]` lists the input modalities models.dev declares for that gateway. A model is filed under every local id requests arrive with and under the upstream id, and a base id drops a `vendor/` prefix and a `:suffix` and is lower-cased. Modalities only ever turn a capability on.
- `providers[<provider>][<model>]` holds the gateway's own window and answer cap where either differs from the tables by more than 10 %, judged by `Base`. A stated `contextLength` never yields a context delta.
- The lookup runs after the exact-model and glob steps and never over a provider row. It is one atomic pointer swap per request and a map read per name.
- `GET /api/models/catalog-sync` answers the last sync and the installed file's summary; `POST` syncs now and answers 503 when one is already running.

The upstream JSON is about 4.3 MB. It is decoded one provider at a time under a 32 MiB byte cap and count caps, so a hostile answer cannot hold more than that; a failure is a class (`answered 502`, `did not answer in time`), never a piece of the body.

### Claude Request Shape

**Built (divergence 216).** `translate.Cloak` shapes a finished Claude body and `pipeline` decides when and with which headers. Claude *preparation* below is separate and was Built, because an Anthropic API key needs it.

**Follow the reference.** No gate, no opt-out: a deployer who does not want that request shape does not connect a Claude login.

- The registry's `oauth.disguise` flag says which provider uses it (only `claude`).
- Client headers never reach the upstream raw; the pipeline passes derived values.
- The shape follows 9router 0.5.119 (Clean-Room). It changes only when a step says so, and the rows kept are the ones a security rule or a router improvement requires.

A request is adapted when all of these hold: the upstream format is Claude, the connection is a login of a provider whose data sets `disguise`, its token begins `sk-ant-oat`, and the client is not Claude Code (a Claude Code client is native: its body is already right). Then, in this order, on the finished body:
1. a billing block becomes `system[0]` (a string system becomes two blocks; nothing is added if `system[0]` already starts with the prefix). Its `cch` is the first five hex characters of SHA-256 over the body as it stood before this step, the build part is three random hex characters, and the version is the one this router advertises (`UPSTREAM_CLAUDE_CLI_VERSION`, else the latest published Claude Code, never below the build's floor `.322`);
2. `metadata.user_id` is added when the client sent none: `{"device_id":…,"account_uuid":…,"session_id":…}`, the ids derived from the token and the session from `app/gateway/conversation` with any `claude:` prefix removed;
3. every `tool_result` carries `is_error`, true when the client reported a failure and false otherwise, because the Zed wire refuses a block without it;
4. every client tool without a `type` is renamed with the suffix `_ide`, in the tool list, in every `tool_use` of the history and in a forced `tool_choice` that names a renamed tool; twenty placeholder tools follow. A body with no tools gets steps 1, 2 and 3 only.

| Item | Behavior |
|---|---|
| Name map | Built for each attempt on each account, so a retry never carries another account's names |
| Answers | A tool name in the answer is restored by the map; a trailing `_ide` the map misses is dropped too, on every Claude answer, as the reference does (divergence 193, retired). A placeholder name is left alone, and so is a name that strips back to a placeholder, so the client sees "unavailable" rather than a silent call of a real tool. Non-stream: when the answer is read. Stream to a translated format: in the Claude stream reader, at the one event that names a tool. Stream to a Claude client: `stream.Job.Rewrite` on that event |
| `anthropic-beta` | Built per request from the registry's `oauth.beta`: the base flags, the heavy flags for models starting with one of its prefixes, minus the summary flag when the body says `thinking.display` is `summarized`, then the client's own flags (read from the allow-listed `Anthropic-Beta` header, each a plain token, at most 32, header at most 2 KiB), without repeats |
| Session header | `x-claude-code-session-id` carries the session the user id holds |
| Identity headers | The `oauth.identity` set (user agent, `X-App`, `X-Stainless-*`, the browser-access header) and the `oauth.urlSuffix` (`?beta=true`), for a login's token only; an API key on the same provider gets neither |
| Other nodes | A node whose base URL is not Anthropic's also gets the key as a bearer token and loses `x-app`, the browser-access header and `claude-code-20250219` |
| Client headers | Never copied. `Input.Header` holds only the headers the pipeline reads (`pipeline.PickHeaders`) |

### Thinking Writers for OpenAI-Format Upstreams

The OpenAI codec writes thinking by the model's `thinkingFormat` (`app/llm/translate/openai`, `WriteOpenAIThinking`). A format the data does not name keeps `reasoning_effort` with the level as the client asked.

| Format | Written (the reference's rule, `thinkingUnified.js`) | Levels |
|---|---|---|
| `nous` | `reasoning: {"enabled": true, "effort": <level>}`, the effort left out when there is no level; `auto` is `medium`; a budget is the level it stands for (the reference's thresholds, `budgetLevel`); `none` on a model that can switch off writes nothing | none, low, medium, high |
| `zai` | `thinking: {"type":"enabled"}`, plus `reasoning_effort` (`low` and `minimal` give `low`, `medium` and `high` give `high`, anything else `max`) only on a model whose capability row has `thinkingEffortSupported` (GLM 5.2 and later); `none` on a model that can switch off writes `enable_thinking: false` | none, thinking |
| `kimi` | `reasoning_effort`: `auto` is `high`, `minimal` is `low`, `xhigh` and `ultra` are `max`, `low`, `medium`, `high` and `max` pass, any other level is left out; `none` on a model that can switch off writes `thinking: {"type":"disabled"}` | none, low, medium, high, max |

A model that cannot switch thinking off is asked for `minimal` instead of `none`, in both formats.

- The rows are data: `providers.nous` per model id; `providers.kimi` per model id with `thinkingCanDisable: false` for `kimi-k3`, `k3`, `kimi-for-coding*` and `kimi-k2.7-code*`.
- A Responses-format upstream such as Muse keeps the Responses writer.
- The level lists decide what the catalog and the dashboard offer. The writer sends the level the request carries, as the reference does.
- Only Codex moves a level to the nearest listed one (`design doc 8`).

### Wire Format by Model

A model entry's `targetFormat` picks the codec **and** the address.

- A provider whose transport names `responsesUrl` or `messagesUrl` sends a Responses-format model to the first and a Claude-format model to the second; any other model goes to `baseUrl`. Both stay on the base address's host. GitHub Copilot is the first user.
- A Claude-named model goes to `/v1/messages` by its name, listed or not, because the live catalog gets new Claude models before a data file does.
- Only `gpt-5.3-codex` is data (`/responses`): the reference learns that from a 400 answer and we do not learn at run time (divergence 234).

A provider whose client format has its own endpoint is served there with no translation: the data's `lanes` map names one endpoint per wire format, each with its own query string, static headers and credential scheme. Kimi is the first user (divergence 244, retired).

### Provider Quirks

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
| Request writer | Sends `store:false`, clamps call ids to 64 characters, flattens tool declarations. A Codex or Grok CLI call is then reshaped by its executor (design doc 8): the codec's body is the input, the backend's allowlist the output |
| Stream writer | Holds `response.completed` until the usage trailer arrives (Close sends it otherwise), numbers every event, ends with it. An error chunk is answered by the transport with `response.failed` (`failure.Frame`) |

### Gemini Codec

`GeminiCodec` joins `generateContent` to the hub.

| Direction | Behavior |
|---|---|
| Request reader | Takes `systemInstruction`, text, `thought` parts (reasoning on the assistant turn), `inlineData` as image, audio or PDF blocks, `functionCall` and `functionResponse` (a call without an id gets one; a result without an id answers the oldest unanswered call of its name), `toolConfig` and the JSON response settings. Upper-case schema types are lowered |
| Request writer | Cleans every tool schema to the subset Gemini accepts (`cleanGeminiSchema`), sanitizes function names, makes every call id unique over the whole history, pairs each result with its call, merges neighbouring turns of one role, starts and ends on a user turn, drops sampling fields for Gemini 4 |
| Signatures | A thought signature rides on the tool call as `extra_content.google.thought_signature` and is sent back when present. When a client returns the call without it, `Ctx.Calls` (the `toolcalls` cache group, scoped per API key; `design doc 20`) restores it; a signature the client kept wins. The capability table's placeholder is used only for a first call no Gemini model issued |
| Stream writer | Holds tool calls until the turn ends (a Gemini call carries whole arguments) and the closing chunk until the usage trailer arrives |
| Usage | Completion tokens include thought tokens in both directions |

---

## Parity: Translators

All go through the OpenAI hub; no direct pairs (design doc 7). References under `translator/`.

| Translator | Reference | Status |
|---|---|---|
| claude to/from openai | `request/claude-to-openai.js`, `request/openai-to-claude.js` | done (D115) |
| gemini to/from openai | `request/gemini-to-openai.js`, `request/openai-to-gemini.js` | done |
| openai to/from openai-responses | `request/openai-responses.js`, `response/openai-responses.js`, `formats/responsesApi.js` | done (request, body and stream, both ways; D119-120) |
| openai to/from ollama | `*/*ollama*.js` | done (all four directions; D136) |
| Concerns: thinking, tool calls, usage, finish reasons, images, modality, params | `concerns/*` | done (also same-format, D138; param rules in `params.json`: claude temperature, groq, openai, GitHub; OpenAI-shaped bodies drop `developer`, foreign blocks, blank messages as `filterToOpenAIFormat`; audit 2 fixes D183-D188) |
| Capability, thinking-level and vision tables, and the models.dev catalog overlay (`/api/models/catalog-sync`) | `providers/{capabilities,thinkingLevels,visionPatterns}.js`, `lib/modelCatalog/sync.js`, `providers/catalogOverride.js` | done (`app/llm/upstream`, `data/capabilities/*.json`; diverged 381 to 386) |
| gemini-cli, antigravity (from openai, and the answer back) | `request/openai-to-gemini.js`, `response/gemini-to-openai.js` | done |
| openai to vertex | `request/openai-to-vertex.js` | done |
| kiro (openai to kiro; the answer is read as events) | `request/openai-to-kiro.js`, `request/claude-to-kiro.js`, `concerns/kiroConversation.js`, `config/kiroConstants.js` | done |
| cursor, commandcode (to and from openai) | `*cursor*.js`, `*commandcode*.js` | done (; `translate/cursor` flattens the conversation, `translate/commandcode` wraps it; D273 refuses an image with the client's 400) |
| Webtools translators for web providers | `*webTools*` | deferred |

---

## Remaining work
**Every client format and concern is built, the catalog overlay included; the deferred web translators are not.**

- [x] The OpenAI hub and the OpenAI, Claude, Responses, Gemini and Ollama codecs for request and non-stream answer
- [x] Claude preparation: cache markers (at most four, tool-call prefixes byte-stable), history repair, thinking-block signatures, `max_tokens` cap, tool-choice relaxing
- [x] The concerns: tool-call ids and results, finish reasons, usage invariants, modality, thinking, parameter rules applied last
- [x] The capability tables in `app/llm/upstream`, and the models.dev catalog overlay (`overlay.go`, `overlay_sync.go`; the runner and the file in `app/providers`)
- [x] The stream direction of every codec: readers and writers for OpenAI, Claude, Responses, Gemini and Ollama; hold-for-usage; the `<think>` tag split for Responses; Claude `Read` tool argument repair
- [x] Image prefetch (`app/gateway/prefetch`,): Gemini and Ollama targets only, magic bytes decide the type
- [x] The Claude request shape: tool suffix, placeholder tools, billing block, user id, restore on body and stream
- [x] Built: gemini-cli, antigravity and vertex (`CloudCodeCodec`, `VertexCodec`; the Gemini writer takes a placeholder signature and the safety settings through `Ctx`)
- [x] Built: the kiro format (`translate/kiro.go`, `kiro_wire.go`, `kiro_canon.go`, `kiro_stream.go`, `kiro_alias.go`) Built
- [x] Built: the cursor and commandcode codecs (`app/llm/translate/cursor`, `.../commandcode`), both upstream-only and blank-imported by the pipeline
- [x] Layout: one package per client format (`translate/openai`, `claude`, `gemini`, `responses`, `ollama`), beside the upstream-only `kiro`, `commandcode` and `cursor`. The root keeps the hub, the registry, `Ctx`, the frames and the concerns; the helpers two formats share are exported there rather than copied, and the per-format usage readers stay in the root's `usage_events.go` because they need nothing from a codec

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 169 | Caller system, developer turns dropped; fixed prompt the only system turn; user content keeps typed blocks | Gateway accepts one fixed shape; data file and test state it, so the loss is deliberate and visible |
| 80 | Both removed first, any order, in resolver. **(partial)**nothing sends `[1m]` beta header yet | Name with both resolves |
| 115 | Each kept or carried; one id rule | No silent loss |
| 112, 113, 114, 118 | Strict JSON (unknown fields, inverted ranges, repeated patterns, secret-shaped names stop start); level tables, signature, `dropOutputConfig` as data; commandcode, kiro special cases leave lookup; catalog overlay | Bad data stops the start |
| 121, 122, 123, 124 | Full Gemini codec both ways. Model id `^[A-Za-z0-9_.-]+$`; unknown action Gemini 404; list honors key access. Real signatures carried; placeholder only on first unsigned call of a model turn, never over real one. Every call id unique; results paired with oldest unanswered call | Gemini clients keep tools, attachments; bad id cannot change the path |
| 131, 132 | Explicit `stream` wins; original names restored; collisions get numeric suffix | Clients get the mode they asked for |
| 136 | Real Ollama codec, four directions; exactly one `done` line with `done_reason`, counts; `"stream": false` returns one JSON body; Ollama error shape; Ollama upstreams spoken natively | Tools, images, counts work |
| 138 | Same-format non-OpenAI bodies: thinking normalization, provider param rules only; OpenAI bodies: all concerns **(trade-off)** | OpenAI to OpenAI repaired. Cost: native bodies keep missing tool ids or results; hub round trip would lose native fields |
| 245 | **Retired.** The Kimi and Nous thinking writers follow the reference (`thinkingUnified.js`); "cannot switch off" comes from the capability row's `thinkingCanDisable`, which is data in both | Recorded so the number stays citable |
| 163 | Model entry may carry `defaultThinking` (used only when name, request, client fields ask nothing) and `thinking` (fixed by model id, beating request); both use name-suffix grammar. DeepSeek V4 Pro defaults to high, declares low to max, passes level asked; `-low`, `-medium`, `-high`, `-xhigh`, `-max`, `-none` pinned variants. Model declaring no levels keeps two-level wire | Coding client gets reasoning without naming it; every level reachable; default is operator-editable data |
| 168 | Per-format transport naming no headers inherits base ones; own URL suffix never inherited | Data stays short; header cannot be forgotten on one format |
| 183 | Codec also maps seed, stop, both penalties, `think`, `response_format` both ways; `developer` becomes `system`; tool calls numbered by running counter; content on final line kept; each answer opens with role chunk; `content: ""` for empty reply. Options with no hub equivalent (`top_k`, `repeat_penalty`, `min_p`, `num_ctx`, `keep_alive`) and `tool_choice` not forwarded | Endpoint has no tool choice field; rest keeps what a client asked for |
| 184 | Kept: native `reasoning{effort,summary}` for Responses upstreams, `output_config` keys, every Gemini system turn joined, Gemini `tool_choice`, `stop`, `seed`, `response_format`, Claude `top_p`, `user`, `stop`, Responses `incomplete` mapping, `reasoning_text.delta`, Gemini thought tokens counted apart from candidates, cache tokens kept on `message_stop` fallback, tool block reopened when its id changes, id-less tool result taking id at its position | Client settings reach upstream instead of vanishing in the hop |
| 185 | `metadata.user_id` built in reference order (`device_id`, `account_uuid`, `session_id`); billing block, placeholder tools, renamed tools re-encoded with sorted keys; `cch` hashes our encoded bytes, so the five characters differ from reference for the same request | Go maps have no order; only a byte fingerprint changes; hand-ordered encoder for the whole body costs more than it gains |
| 186 | Rules for every upstream format (so `temperature` also dropped from native Claude body); strip after injection; exclusion one glob; image fetch up to 3 redirects, 20 s limit, through SSRF guard; rules validated on load (no empty rule, no negative cap, no credential-shaped pattern, no exclude without match) | One rule table for every route; bad table stops the start instead of misrouting traffic |
| 190 | Endpoint alone decides: `/v1/chat/completions` OpenAI, `/v1/messages` Claude, `/v1/responses` Responses, `/v1beta/models/*` Gemini, `/v1/api/chat` Ollama. Body of another shape gets that format's error | One rule per route; wrong-shape body fails loudly instead of being served as a format the client did not choose |
| 142 | Every Claude-format body bound for Anthropic or a node fronting it | Removes only what Anthropic rejects; no dependence on a header |
| 193 | **Retired.** A trailing `_ide` is dropped from a tool name on every Claude answer when the map misses it, as the reference does; a placeholder name, or a name that strips back to one, is left alone | Recorded so the number stays citable. The map is still rebuilt for every attempt, so it is never lost across a retry |
| 216 | The Claude request shape (tool suffix, placeholder tools, billing block, user id) was built with the Claude OAuth provider in instead of | It needs an OAuth account to exist and to test against; Claude preparation, which an API key needs, is built |
| 217 | Every writer holds its closing event until the usage report or `STREAM_USAGE_WAIT` | A usage trailer sent after the finish chunk reaches Claude and Gemini clients too |
| 309 | The Claude Code version the fingerprint advertises is refreshed on a runner timer, not from a request: the value is read from memory and no request waits on npm | The reference's first caller after the cache expires starts the lookup in the background; a timer keeps the same freshness with no request path at all |
| 218 | An upstream error event (Claude `error`, Responses `response.failed`, Gemini or Ollama error body, an OpenAI `error` object) fails the stream with the client's own error frame | The reference dropped it and the stream ended with no finish and no error |
| 381 | The synced catalog is one immutable value behind an atomic pointer, applied after the exact-model and glob steps and never over a provider row; a sync compares against `Base`, so it never reads its own last answer **(built)** | The reference detaches its reader for the whole rebuild, so requests lose the catalog meanwhile, and leaving it attached made each delta relative to the last one, so the file erased itself over two runs |
| 382 | The upstream catalog is decoded one provider at a time under a 32 MiB byte cap and count caps, and a failure names a class, never an upstream body **(built)** | The reference parses all 4.3 MB in one object with no cap, and one hostile or broken answer holds the whole process |
| 383 | The catalog file carries version 3; a file the reference wrote (version 2) is ignored and rebuilt **(trade-off)** | Its deltas were computed against its tables, so applying them here would publish numbers no table of ours ever decided |
| 384 | No trimmed copy of the whole upstream catalog is written **(scope cut)** | Nothing but the reference's add-models skill reads it |
| 385 | The file is read once at start and swapped in memory; a hand edit applies at restart or the next sync **(trade-off)** | The reference stats the file on every capability lookup and parses it whenever the mtime moved, which a request pays for |
| 386 | The status answer names the file, not its absolute path, and a failure is bounded text **(built)** | The reference hands a dashboard reader the host's data path and stores any error text it receives |

---

**The 0.5.116 sync.**

- A remote image part carries a concrete MIME in `fileData`, read from its address: `.jpg`/`.jpeg` → `image/jpeg`, `.webp`, `.gif`, `.bmp`, else `image/png`. The upstream refuses a wildcard.
- A compatibility-node's effort is clamped to what such a node reads (`auto` → `high`, `minimal` → `low`, an unknown level dropped), and its budget is sent as an explicit `thinking` object.
- Pinned by tests, already true here: the DashScope family keeps a Claude client's cache markers; a Gemini turn keeps every parallel tool result ahead of its text; a tool-result turn keeps its thinking request.

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| A same-format request must not lose what the hub does not model. | Unknown top-level fields ride in `Extra` and are written back to the same format. |
| Two encodings of one request must be byte-identical so an upstream can read its prompt cache. | Every object is written through an ordered encoder, never a Go map. |
| A conversation ending with many parallel tool results was the only input that could exceed four cache markers. | A dedicated test now produces it. |
| An enum whose declared type was `integer` came out `integer` or `string` depending on walk order. | An enum is always strings with an explicit `string` type. |
| Go's JSON decoder names an unknown key in its error, and a key can be a pasted secret. | Loader errors give a fixed reason only; file content is never repeated. |
| Every codec must meet at one chunk shape, and every writer must hold its closing event. | Every reader emits hub `Chunk`s; each writer holds its close until usage arrives. |
| Gemini and Ollama carry a tool call in one part, so writing each fragment produced two calls. | Their writers collect fragments and send the call with the turn. |
| The request shape must not disturb any other byte of the client's body. | `Cloak` runs on the finished body with `gjson`/`sjson`, so key order and cache markers survive. |
| A Messages stream names a tool once, at `content_block_start`. | The same stream reader restores it for translated and passthrough paths alike. |
| `Uncloak` refused to strip a placeholder tool name, but no placeholder ends in `_ide`. | The guard could never fire and was removed; the comment says why. |
| The hub kept thinking mode, level and budget but not `display`. | `display` is carried now and joins the response-cache key. |

