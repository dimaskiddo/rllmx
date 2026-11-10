# PRD 16 — Usage, Pricing & Quota (`app/analytics/`, `app/usage/`)

**Status:** partial · **Steps:** 1.4, 1.8, 2.3

Built today: `app/analytics` (the usage store, writer, rollups, retention and the legacy importer). `app/usage` — the query surface, pricing and quota — does not exist. The pipeline enqueues one row per request on every path since 1.8c (price from `upstream.Table`, error class, attempt, estimate flag); the writer is wired since 1.8d, and a Claude CLI bypass reply ends in one row (status 200, no provider, zero tokens).

### Usage

When a request completes, the pipeline enqueues one row to the analytics writer. There is no switch. Nothing else is stored about the request. Cache hits are rows too (`cached` set, zero cost). The modality (chat, embeddings, images, audio, video, search, fetch) is the `endpoint`.

`/api/usage/*` (stats, charts, history, per key, per connection) reads only `requests` and the rollups. History pages use a keyset cursor. A bounded in-memory ring and an SSE emitter feed `/api/usage/stream`; active requests are tracked in memory.

### Pricing (`app/upstream/pricing.go`, data `app/upstream/data/pricing/prices.json`; built in 1.7a)

Rates are dollars per million tokens: input, output, cached, reasoning, cache creation. A missing cached or cache-creation rate means the input rate; a missing reasoning rate means the output rate.

| Step | Lookup order |
|---|---|
| 1 | Administrator override (`kv` scope `pricing`, `{provider: {model: rates}}`) |
| 2 | Embedded provider-specific price |
| 3 | Free namespace (`cline-free/`, price zero) |
| 4 | Canonical model |
| 5 | Glob pattern, first match wins (`*` only, case-insensitive, compiled once, never user-supplied) |

- Provider names are tried as the connection knows them (id and alias). A `vendor/model` name is tried whole and without the prefix. Names over 256 bytes get no price.
- An override replaces the whole rate card of its provider and model; `{}` makes a model free. A malformed entry is skipped with a warning naming provider and model, never a value.
- **Cost:** prompt includes cached and cache-creation tokens (charged at their own rates, the rest at the input rate, floored at zero); completion includes reasoning tokens (reasoning share at the reasoning rate, the rest at the output rate; a count above the completion is cut back). Gemini thought tokens are normalized by the usage extractor first.
- Computed at completion and stored in the row, so a later price change never rewrites history.
- The embedded file makes cost work on an empty database. `/api/pricing` (step 2.6) edits the overrides; removing one restores the embedded rate.

### Quota

The Antigravity limits are already read by `app/quota` (1.10g) for the selector; `/api/usage/{connectionId}` (step 2.3) will show the same readings. Per-provider fetchers report live limits through `/api/usage/{connectionId}` (GitHub, Gemini CLI, Antigravity, Claude, Codex, Kiro, Qoder, iFlow, GLM, MiniMax, Kimi, DeepSeek, OpenCode, Zed, Cursor and others). API key quota limits are a separate feature (PRD 21).

---

## Parity: Usage Routes

| Endpoint | Package | Status | Step |
|---|---|---|---|
| `/api/usage/*` (stats, chart, history, providers, stream, per-connection quota) | `app/http/controllers` | diverged | 2.6 |
| `/api/usage/{connectionId}/claude-reset`, `codex-reset-credits` | `app/usage` | todo (quota fetchers) | 2.6 |
| `/api/usage/request-details`, `request-logs` | none | out | - |

---

## Remaining work
**Steps 1.8 and 2.3.** The analytics store writes usage rows; `app/usage` is unbuilt.

- [ ] `/api/usage/*` query surface over `requests` and the rollups, with the keyset cursor and the SSE stream
- [x] Pricing lookup over the embedded baseline and the `pricing` overrides (1.7a); the pipeline's usage hook calls it (1.8c)
- [ ] Per-key request and token budgets with a reset window, counted from the usage row and checked before dispatch (2.3)

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 17, 19 | Separate analytics store, structured columns, hourly and daily rollups, async writes; counter there. **(partial)** readers return with usage dashboard **(planned 2.6)** | Small compatibility file, queryable usage, latency independent of the write |
| 117 | Runes of system, tool, message text over four; no usage row. Built in 1.8e as `translate.CountTokens` (images, audio and files add nothing) | Bytes overstate non-ASCII |
| 187 | Report of zeros does not block estimate; cached plus cache-creation tokens never exceed prompt; estimate counts characters of body | Placeholder `usage` cannot hide real output; row cannot charge more than prompt |
| 188 | One hour lifetime writes (always requested for system prompt, last tool) counted from `usage.cache_creation.ephemeral_1h_input_tokens`, priced at `cache_creation_1h`, else twice input rate (PARITY.md §4); first turn of conversation caches its last message; Grok CLI cache key always derived session, as Codex's is | Long write not billed at short rate; client cannot steer another account's cache |
| 77, 110 | Completion includes reasoning; rest at output rate; reasoning at own rate (default equal); thoughts join completion | Billed once, on the right side |
| 107, 108, 109, 111 | One row per request including failures, with error class, `estimated`; one buffer rule; both modes fold cache tokens; tracker reads upstream payload once | One account of what happened |
| 18 | Usage always recorded, metadata only; upstream error becomes class | Prompts, secrets never at rest |
| 143 | One row: status 200, no provider, zero tokens | Every request ends in one row |
| 78 | `Cost` reports "unknown"; patterns split once at load. **(partial)** row still stores 0 for unknown | No per-request compile |

---

## Findings

The analytics store writes usage rows. The price table is built (`upstream.Table`: `Lookup`, `Cost`, `ParseOverrides`); the pipeline prices every row with it since 1.8c. `app/usage` — the query surface and quota — is unbuilt.

**An unknown model is not a free model.** `Table.Cost` returns `known=false` when nothing matches, and an administrator override of `{}` is the only way to say a model costs nothing. Rates left out of a card fall back to the input or output rate, but an explicit `0` stays `0`.
