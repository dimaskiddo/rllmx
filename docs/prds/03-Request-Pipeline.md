# PRD 03 — Request Pipeline (`app/pipeline/`)

**Status:** partial · **Steps:** 1.8c, 1.8d, 1.8e, 1.8f, 1.8g

Built in 1.8c as a library, reached since 1.8d by `POST /v1/chat/completions`: `app/pipeline` has no HTTP types. `Pipeline.Handle(ctx, Input, Opener) Output` parses the body through the client codec, resolves the model, translates once, walks the provider's accounts, answers (relay a stream, collect a forced stream into JSON, or translate a buffered body) and enqueues exactly one usage row on every path. The 1.8d controller turns a request into an `Input`, supplies an `Opener` that starts the client's stream, and writes an `Output` that is not `Streamed`. The stream is opened by its first frame, so an upstream that fails before sending anything is still answered with a plain error and the next account is tried.

The per-request engine. One call walks these stages; each is a package or a function in one.

```mermaid
flowchart TD
    A[Parse body, session id] --> B[Key check, key access, key quota]
    B --> C{Claude CLI probe?}
    C -- Yes --> D[Answer locally, one usage row]
    C -- No --> E[Skills, Auto Plan]
    E --> F[Capabilities: vision, pdf, audio, video]
    F --> G[Combo expansion or single model]
    G --> H[Account loop]
    H --> I[Core for one account]
    I --> J{Upstream OK?}
    J -- No --> K[Classify, lock, exclude, next account]
    K --> H
    J -- Yes --> L[Stream, SSE to JSON, or translate body]
    L --> M[Enqueue one usage row]
```

| Stage | Behavior |
|---|---|
| 1. Parse | Parse the body; strip the `[1m]` marker from `model`; resolve a session identity (PRD 11) |
| 2. Key checks | `requireApiKey`, key access, key quota (PRD 21) |
| 3. Bypass | A request from a Claude CLI user agent that is a warmup, a `count` probe, a title probe, a prefill `{`, or (setting `ccFilterNaming`) a topic-naming probe is answered locally in the client's format with one fixed text. It still ends in one usage row with no tokens and no provider |
| 4. Skills, Auto Plan | May prepend prompts or reroute the model (PRD 11) |
| 5. Capabilities | `translate.DetectNeeds` reads the input kinds the current turn carries (vision, pdf, audio, video) and `Needs.Met` compares them with a model's capability row. Older turns never count. The combo step (2.4) is the caller; a single model is not rerouted |
| 6. Combo | Expand a combo (PRD 4), or use the single model |
| 7. Account loop | Select an account, refresh its token if due, run the core, classify a failure, lock, exclude, retry with the next account (PRD 5) |
| 8. Core | See below |
| 9. Usage | Enqueued as one metadata-only row when the stream completes |

**Core for one account:**
1. Detect the source format: endpoint first, then body shape.
2. Choose the target format: a model's declared format, a provider's lossless transport for the source format, or the registry default. Custom OpenAI-compatible nodes map to `openai` or `openai-responses`, Anthropic-compatible nodes to `claude`.
3. Apply the provider thinking override and the upstream model id.
4. Decide streaming: the client's `stream`, unless the provider forces streaming or the model is an image model. `Accept: application/json` (without `text/event-stream`) turns off a stream the format starts by default (Ollama) when the body has no `stream` field; an explicit `stream: true` and a Gemini URL action are never overridden.
5. **Native passthrough** when client and provider speak the same native dialect (Claude Code to Anthropic, Gemini CLI, Antigravity, Codex). The client tool is read from headers only (user agent, `x-app`, `originator`, ...) and never trusted for access. A native client is never disguised; every Claude-format body still gets the cleanup Anthropic needs.
6. Otherwise strip modalities the model cannot read, prefetch remote images, translate.
7. De-duplicate tools; run token savers and skill prompts (fail open).
8. `Executor.Execute`. On 401 or 403, refresh credentials (up to three tries), persist them, run once more.
9. Non-OK upstream: parse the error and return it to the account loop. OK: stream it, convert SSE to JSON, or translate a non-stream body. An OK answer whose type is HTML, or is neither a stream nor JSON on a streaming call, is a 502 in the client's shape with the page title; nothing is locked or cleared.

---

## Parity: Client API

| Endpoint | Package | Status | Step |
|---|---|---|---|
| `POST /v1/chat/completions` | `app/http/controllers` | done (1.8d) | 1.8d |
| `X-Rllm-Cache` header: request `use`/`bypass`, response `hit`/`miss` | `app/pipeline`, `app/http/controllers` | done (1.8g; D24, D148) | 1.8g |
| `POST /v1/messages` | `app/http/controllers` | done (1.8e) | 1.8e |
| `POST /v1/messages/count_tokens` | `app/http/controllers`, `app/translate` | done (1.8e: local estimate, runes over four, no upstream, no row; D117) | 1.8e |
| `POST /v1/responses`, `/v1/responses/compact` | `app/http/controllers` | done (1.8e: compact is served as a plain Responses call, nothing written into the body; the upstream `/compact` path comes with Codex, D120) | 1.8e, 1.10 |
| `POST /v1beta/models/{m}:generateContent`, `:streamGenerateContent` | `app/http/controllers` | done (1.8e; D224) | 1.8e |
| `GET /v1/models`, `/v1/models/info`, `/v1/models/{m}`, `GET /v1beta/models` | `app/http/controllers`, `app/catalog` | done (1.8f; D225, D226) | 1.8f |
| `POST /v1/api/chat` (Ollama) | `app/http/controllers` | done (1.8e: NDJSON by default, JSON for `stream:false` or `Accept: application/json`; D136) | 1.8e |
| `POST /v1/embeddings` | `app/media` | todo | 1.11 |
| `POST /v1/images/generations` | `app/media` | todo | 1.11 |
| `POST /v1/audio/speech`, `/transcriptions`, `GET /v1/audio/voices` | `app/media` | todo | 1.11 |
| `POST /v1/videos/*`, `GET /v1/videos/{id}` | `app/media` | todo | 1.11 |
| `POST /v1/search`, `POST /v1/web/fetch` | `app/media` | todo | 1.11 |
| `POST /v1/systemone` | `app/combo` | todo | 2.4 |
| URL rewrites (`/codex/*`, `/responses`, `/v1/v1/*`, `/systemone`; ref `next.config.mjs`) | `app/http` | done (pre-guard; non-canonical paths refused) | 1.5 |

---

## Parity: Pipeline Stages

| Stage | Reference | Package | Status | Step |
|---|---|---|---|---|
| Guard (key, session, loopback) | `dashboardGuard.js`, `sse/services/auth.js` | `app/http`, `app/auth` | partial (class table, locality, deny-all authenticator; key lookup and session 1.6; restricted-key check 2.2) | 1.6 |
| Key access restriction | `sse/services/keyAccess.js` | `app/access` | todo | 2.2 |
| Session identity | `utils/sessionManager.js` | `app/conversation` | built in 1.8c (client-named id by reference priority, else opening assistant text, else per connection, two-hour life; `Bound` keyed hash; the pipeline reads it since 1.9c, once an account is chosen: the Claude user id and session header, and a keyed hash for Codex's `session_id`; continuity store 3.2) | 1.8c |
| Bypass (warmup, title) | `utils/bypassHandler.js` | `app/pipeline` | done (1.8d: runs after parse and before model resolution, so it works for a combo name and with no account; D143 one row, D223 the client's own codec writes the reply) | 1.8d |
| Capability detection (current-turn needs: vision, pdf, audio, video) | `services/combo.js` (`detectRequiredCapabilities`) | `app/translate` | todo (`DetectNeeds`, `Needs.Met`; combo step calls it) | 1.8 |
| Capacity adapter (fallback pools per capability) | `services/capacityAdapter.js` | `app/combo` | todo | 2.4 |
| Model resolution, aliases, nodes, inference | `services/model.js`, `sse/services/model.js` | `app/routing` | done (resolver, snapshot; D80-81; a combo name is answered 404 until 2.4) | 1.7 |
| Combos: fallback, round-robin, smart (System One), fusion | `services/combo.js`, `services/systemoneRouter.js` | `app/combo` | todo | 2.4 |
| Empty-stream failover | `services/emptyFailover.js` | `app/stream` | todo | 2.4 |
| Request-scoped 4xx combo advance | `services/combo.js` | `app/combo` | todo | 2.4 |
| Account selection and strategies | `sse/services/auth.js` | `app/account` | done (selector; D82-83, D212; called by the pipeline loop since 1.8c) | 1.8c |
| Error rules and backoff | `config/errorConfig.js`, `services/accountFallback.js` | `app/account` | done (D84-85; called by the pipeline loop since 1.8c) | 1.8c |
| Per-model locks (`modelLock_*`) | `sse/services/auth.js` | `app/account`, `app/repository` | done (write and clear in `app/repository`, read by the selector since 1.7b) | 1.7 |
| Token refresh: proactive, reactive, background, dedup | `services/tokenRefresh*`, `sse/services/backgroundTokenRefresh.js` | `app/oauth` | done (1.9b: proactive before a request, reactive on 401 and 403 with one retry, background loop, the connection test; D155-157) | 1.9b |
| Format detection, transport, target format | `services/provider.js` | `app/pipeline` | built in 1.8c (endpoint decides the client format; a model's `targetFormat`, else the transport format; nodes by type and `apiType`) | 1.8c |
| Native passthrough | `utils/clientDetector.js` | `app/pipeline` | partial (the stream layer passes through when client and upstream formats are equal, whatever the client tool; client tool detection from headers since 1.9c, from an allow-listed header set (`pipeline.PickHeaders`); a Claude Code client is never disguised; Claude normalization in `app/translate`, D142; the Codex and Gemini CLI body shapes 1.10) | 1.8, 1.9c |
| Claude request preparation (`prepareClaudeRequest`) | `translator/formats/claude.js` | `app/translate` | todo (D118) | 1.8 |
| Tool-name cloaking | `utils/claudeCloaking.js` | `app/translate`, `app/pipeline` | done (1.9c: tool suffix, decoys, billing block, user id, restore on the body and on both stream paths; D185, D193, D216) | 1.9c |
| Executor base, retry, URL fallback | `executors/base.js` | `app/executor` | built in 1.8c (in-place retry by status, data overrides, 2 s default delay; `baseUrls` fallback never tried: no shipped provider needs it, D102; a 401 or 403 on a login renews its token and retries once since 1.9b, D222 retired) | 1.8c |
| Streaming: SSE parse and emit, usage, timers | `utils/stream.js`, `streamHandler.js` | `app/stream` | done (1.8b; run by the pipeline since 1.8c) | 1.8b |
| Forced SSE to JSON, non-stream translation | `handlers/chatCore/*` | `app/stream`, `app/pipeline` | built in 1.8c (`Collect` for a stream-only provider; a buffered body through the codecs) | 1.8c |
| Usage save, daily aggregate, cost | `src/lib/db/repos/usageRepo.js` | `app/analytics`, `app/pipeline` | partial (one row per request on every path with price and error class, built in 1.8c; the writer is wired in 1.8d; `app/usage` query surface unbuilt) | 1.8c |
| Response cache (opt-in, per key and global, no streams or tool-call answers) | none in the references | `app/pipeline`, `app/providers` | done (1.8g: lookup after resolution and before translation, store in `deliver` after the account accepted; a hit selects no account and its row is `cached` at cost 0; D24, D148) | 1.8g |
| Request details (body dumps) | `src/lib/db/repos/requestDetailsRepo.js` | none | out | - |
| Live-catalog guards: kiro, codex, cursor, kimchi, qoder (extended) | `handlers/chatCore.js` | `app/catalog` | todo | 1.10 |

---

## Remaining work
**Phase 1 continues with 1.10** (1.9, the OAuth login, the refresh and the disguise, is built). The engine (1.8c), every chat route (1.8d, 1.8e), the model catalog (1.8f) and the response cache (1.8g) are built.

- [x] Parse, resolve, target format, the account loop, the per-account core, the usage row on every path (1.8c)
- [x] Request-scoped 4xx handling, per-format error bodies, HTML upstream error cleanup (a 2xx HTML page or non-stream type on a streaming call is a 502, D149) (1.8c)
- [x] The `POST /v1/chat/completions` route, the controller, the lazy pipeline source in `app/providers`, bypass, image prefetch (1.8d)
- [x] `/v1/messages`, `/v1/messages/count_tokens`, `/v1/responses`, `/v1/responses/compact`, `/v1beta/models/*`, `/v1/api/chat`, the `Accept: application/json` rule, the NDJSON opener (1.8e)
- [x] The model catalog routes (1.8f)
- [x] The response cache (1.8g)
- [ ] Key checks, skills, auto plan, token savers, combos: the steps that own them (2.x, 3.x)
- [ ] Capability detection (vision, pdf, audio, video): `translate.DetectNeeds` and `Needs.Met`, current turn only; the combo step (2.4) is the caller
- **Done when:** every endpoint passes golden and end-to-end tests with `-race`; the same prompt through OpenAI, Anthropic, Responses and Gemini yields consistent usage rows.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 49 | Connection aborted | Truncation visible |
| 192 | Short instruction text of our own writing (reference's is the official prompt, which we do not copy) | Backend refuses request with no instructions. Whether it checks the text unverified; real account must confirm (`TASKS.md` Open items) |
| 120 | Never written; upstream `/compact` path with Codex **(planned 1.10)** | Strict upstreams reject unknown fields |
| 220 | Locks clear only after the answer is accepted: a non-empty body that parsed, or a stream that delivered an event. A page, an empty body or a broken stream clears nothing and locks nothing (reference clears on the status line) | An upstream that returns 200 with junk does not heal an account |
| 221 | A non-stream upstream body has a total time cap (10 minutes); the reference has none | A server that sends headers and then goes quiet cannot hold a request for ever |
| 223 | A bypass reply is a typed response written by the client's own codec, and a prefill `{` matches plain string content too. The reference builds an OpenAI reply by hand and translates it: Claude non-stream came back with empty content and no stop reason, Claude streams ended with `[DONE]`, OpenAI streams carried a stray `data: null`, and a missing `stream` meant a stream | The reply is well formed in every client format and a missing `stream` is false (D191) |

---

## Findings

**The pipeline is built on the first request.** The stores open in a runner after every provider registered, so the route table cannot hold a finished pipeline. `PipelineSource.Get` builds it once on success and retries after a failure; a request that arrives before the stores are open gets a 503 in the client's format.

**The bypass runs before model resolution.** A warmup naming a combo, or a model with no account, still gets its answer and makes no upstream call. Only the OpenAI and Claude client formats can carry a Claude CLI probe, so a Gemini body is never matched; a test pins it.

**The test route needed a log.** The framework logs a malformed JSON body through a package variable the application sets and a test never does; `tests.NewRoute` now sets it, because the first bad-JSON controller test crashed the driver.

**The cache lookup sits between resolution and translation.** A hit never pays for an image fetch or a body translation, and the canonical key is taken from the client's own bytes, so what the codecs later do to the request cannot change it.

**One controller serves five formats.** `ChatController.Serve` reads the path only to pick the stream writer (`NewNDJSON` for Ollama, `NewSSE` otherwise); the pipeline decides the client format. The fuzz target for the Gemini path found `..0`, which the segment regex allowed; segments containing `..` are now refused.

**No Ollama upstream exists yet.** The registry accepts four upstream formats, so the prefetch gate for an Ollama target is tested directly and its pipeline test arrives with that format.
