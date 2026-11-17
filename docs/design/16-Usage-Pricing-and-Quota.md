# Design 16 — Usage, Pricing & Quota (`app/store/analytics/`, `app/store/analytics/`)

**Status:** partial

Built today: `app/store/analytics` — the usage store, writer, rollups, retention, the legacy importer and the dashboard's readers; `app/http/controllers` serves the usage routes; `app/gateway/health` holds the in-flight table.

- The pipeline enqueues one row per request on every path: price from `upstream.Table`, error class, attempt and estimate flag.
- The writer is wired.
- A Claude CLI bypass reply ends in one row: status 200, no provider, zero tokens.
- The readers are built (`analytics.KeyTotals`, `Sums`, `Buckets`, `LastSeen`, `Recent`, `Providers`) and every usage route below reads them.

### Usage

When a request completes, the pipeline enqueues one row to the analytics writer. There is no switch. Nothing else is stored about the request. Cache hits are rows too (`cached` set, zero cost). The modality (chat, embeddings, images, audio, video, search, fetch) is the `endpoint`.

**Readers.** `/api/usage/{stats,chart,history,logs,request-logs,providers,active-providers,stream}` read only `requests` and the rollups:
- `stats` and `chart` take `today`, `24h`, `7d`, `30d`, `60d` or `all`.
- `today` and `24h` read the hour rollups, which live inside the request retention. Longer periods read the day rollups for the days before the oldest kept hour and the hour rollups from that day on, so a day whose hours were pruned is still counted once, whole.
- The reporting zone is the server's own (`TZ`, `time.Local`); a day taken from the day rollups can only keep its UTC date (D360). `24h` is the 24 clock hours ending now (D361).
- `history` answers the all-time totals, as the reference does, and `request-details` stays out because it serves stored bodies.
- `logs` and `request-logs` are the 200 newest rows, one line each.
- `providers` is the distinct provider list from the day rollups, named by node then registry.
- `byApiKey` is keyed by the key's id and shows the masked key, never the key (D362).
- `last10Minutes`, the recent list's de-duplication and the log line's skills suffix are left out (D366).

**Live.** `app/gateway/health`'s in-flight table counts every attempt from the moment an account is chosen until its answer, a stream included, is finished. The release is idempotent, so a double end cannot drive another request's count down (D364).

`stats` carries `pending`, `activeRequests`, `recentRequests` and `errorProvider`; `/api/usage/active-providers` counts the providers with a request running.

`/api/usage/stream` sends those four live fields on connect and again when a request starts or ends or a batch of rows lands. It never recomputes the totals (D363).

### Pricing (`app/llm/upstream/pricing.go`, data `app/llm/upstream/data/pricing/prices.json`; built)

Rates are dollars per million tokens: input, output, cached, reasoning, cache creation. A missing cached or cache-creation rate means the input rate; a missing reasoning rate means the output rate.

A row exists wherever a model departs from its family pattern: `claude-fable-5-2` (8/40, below the fable pattern), `claude-haiku-5-5` (0.10/0.50, a tenth of the haiku pattern) and the `gemini-4-*` pattern (2/10, above the generic `gemini-*`). Claude Fable 5.1 left the catalogs but its pattern rate still bills an existing config.

| Lookup order |
|---|
| Administrator override (`kv` scope `pricing`, `{provider: {model: rates}}`) |
| Embedded provider-specific price |
| Free namespace (`cline-free/`, price zero) |
| Canonical model |
| Glob pattern, first match wins (`*` only, case-insensitive, compiled once, never user-supplied) |

- Provider names are tried as the connection knows them (id and alias). A `vendor/model` name is tried whole and without the prefix. Names over 256 bytes get no price.
- An override replaces the whole rate card of its provider and model; `{}` makes a model free. A malformed entry is skipped with a warning naming provider and model, never a value.
- **Cost:** prompt includes cached and cache-creation tokens (charged at their own rates, the rest at the input rate, floored at zero); completion includes reasoning tokens (reasoning share at the reasoning rate, the rest at the output rate; a count above the completion is cut back). Gemini thought tokens are normalized by the usage extractor first. An OpenAI-shaped answer may spell its cache counts as `prompt_tokens_details.*` or as an SDK's `inputTokenDetails.cacheReadTokens`/`cacheWriteTokens`; both are read.
- Computed at completion and stored in the row, so a later price change never rewrites history.
- The embedded file makes cost work on an empty database. `/api/pricing` reads the merged cards, `PATCH` merges cards into the stored rows and `DELETE ?provider[&model]` restores the embedded rate. A write resets and warms the price table, so the next request prices with the new numbers (D355).

### Quota

The Antigravity limits are already read by `app/llm/quota` for the selector; Gemini 4 Carbon counts toward the general Gemini pool, so its three tiers are among the models whose quota is kept.

`/api/usage/{connectionId}` (step) will show the same readings. Per-provider fetchers report live limits through `/api/usage/{connectionId}` (GitHub, Gemini CLI, Antigravity, Claude, Codex, Kiro, Qoder, iFlow, GLM, MiniMax, Kimi, DeepSeek, OpenCode, Zed, Cursor and others). API key quota limits are a separate feature (design doc 21).

---

## Parity: Usage Routes

| Endpoint | Package | Status |
|---|---|---|
| `/api/usage/stats`, `chart`, `history`, `logs`, `request-logs`, `providers`, `active-providers`, `stream` | `app/http/controllers`, `app/store/analytics`, `app/gateway/health` | done (diverged) |
| `/api/usage/{connectionId}`, `claude-reset`, `codex-reset-credits` | `app/store/analytics` | todo (quota fetchers, 2.6d) |
| `/api/usage/request-details` | none | out (stored bodies) |

---

## Remaining work
**The usage rows, the readers and the dashboard's usage routes are built; the per-connection quota readers wait for step 2.6d.**

- [x] `/api/usage/*` over `requests` and the rollups, with the live feed; `history` answers the all-time totals (D363), and a keyset-paged history is not built
- [x] Pricing lookup over the embedded baseline and the `pricing` overrides; the pipeline's usage hook calls it
- [x] Per-key request, token and cost budgets with the day, month and total windows, checked before dispatch (step 2.3, `design doc 21`)

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 17, 19 | Separate analytics store, structured columns, hourly and daily rollups, async writes; counter there. **(partial)**readers return with usage dashboard (planned) | Small compatibility file, queryable usage, latency independent of the write |
| 117 | Runes of system, tool, message text over four; no usage row. Built as `translate.CountTokens` (images, audio and files add nothing) | Bytes overstate non-ASCII |
| 187 | Report of zeros does not block estimate; cached plus cache-creation tokens never exceed prompt; estimate counts characters of body | Placeholder `usage` cannot hide real output; row cannot charge more than prompt |
| 188 | One hour lifetime writes (always requested for system prompt, last tool) counted from `usage.cache_creation.ephemeral_1h_input_tokens`, priced at `cache_creation_1h`, else twice input rate (PARITY.md §4); first turn of conversation caches its last message; Grok CLI cache key always derived session, as Codex's is | Long write not billed at short rate; client cannot steer another account's cache |
| 77, 110 | Completion includes reasoning; rest at output rate; reasoning at own rate (default equal); thoughts join completion | Billed once, on the right side |
| 107, 108, 109, 111 | One row per request including failures, with error class, `estimated`; one buffer rule; both modes fold cache tokens; tracker reads upstream payload once | One account of what happened |
| 18 | Usage always recorded, metadata only; upstream error becomes class | Prompts, secrets never at rest |
| 143 | One row: status 200, no provider, zero tokens | Every request ends in one row |
| 78 | `Cost` reports "unknown"; patterns split once at load. **(partial)**row still stores 0 for unknown | No per-request compile |
| 354 | A stored rate card is read and written with the field `cache_creation`, and the camel spelling is accepted too **(built)** | A real 9router row spells it `cache_creation`; the strict decoder skipped every override that carried a cache-creation rate, and a card this build wrote would lose it in the reference |
| 360 | Days and labels use the server's zone (`TZ`). `today` and `24h` come from the hour rollups, which cover the request retention; older days come from the day rollups, which are kept forever, so a long chart can switch from local days to UTC days at the retention edge. `lastUsed` is the newest request time when the request rows still hold it (48 h), else the bucket start **(trade-off)** | The reference keeps a local-date rollup for every day; this build reuses the two rollups it already writes instead of a third table, and the seam only touches data older than the retention |
| 361 | `24h` is the 24 clock hours ending now, labelled `HH:00` | The reference buckets back from the current minute, so labels read `14:37` |
| 362 | `byApiKey` is keyed by the key's id and carries `apiKeyMasked`; every period uses the same rule | The reference puts the raw API key in the object key of every long-period `stats` answer and every `history` and stream frame, which hands any dashboard reader the keys |
| 363 | The stream sends only `pending`, `activeRequests`, `recentRequests` and `errorProvider`, on connect and on a change of the tracker generation or the flushed-batch counter | The reference recomputes all-time totals after every request and sends the whole object to a page that reads four fields |
| 364 | A request holds its in-flight slot until its answer, a stream included, is finished; the release is idempotent; a provider with no connection is counted too | The reference zeroes a counter after 60 s while the stream still runs and a double end can drive another request's count down |
| 365 | `providers` comes from the day rollups | The reference reads a `requestDetails` table this build does not have, and its filter forgets a provider once that table is pruned |
| 366 | No `last10Minutes`, no de-duplication of the recent list, no skills suffix in a log line **(scope cut)** | No page reads the first, one row per request already rules out the second, and skills are not built (step 3.2) |
| 355 | Every pricing route answers the merged table: the embedded provider rows with the overrides laid over them, and a write applies to the next request through the reloaded source **(built)** | The reference's PATCH answers only the overrides while GET answers a merged map of two providers, and its edit form sends the whole map back, turning every default into a stored override |

---

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| The analytics store writes usage rows and the pipeline prices each one. | `upstream.Table` (`Lookup`, `Cost`, `ParseOverrides`) supplies the rates. |
| An unknown model must not look free. | `Table.Cost` returns `known=false`; an override of `{}` is the only way to say a model costs nothing. |

