# Design 16 — Usage, Pricing & Quota (`app/store/analytics/`, `app/llm/quota/`)

**Status:** done

Built today: `app/store/analytics` — the usage store, writer, rollups, retention, the legacy importer and the dashboard's readers; `app/http/controllers` serves the usage routes; `app/gateway/health` holds the in-flight table; `app/llm/quota` holds the Antigravity selector gate and the per-connection readers the quota routes call.

- The pipeline enqueues one row per request on every path: price from `upstream.Table`, error class, attempt and estimate flag.
- The writer is wired.
- A Claude CLI bypass reply ends in one row: status 200, no provider, zero tokens.
- The readers are built (`analytics.KeyTotals`, `Sums`, `Buckets`, `LastSeen`, `Recent`, `Providers`) and every usage route below reads them.
- `app/llm/quota` answers 22 providers from a per-connection route, and the two refill routes spend a grant or a credit.

### Usage

When a request completes, the pipeline enqueues one row to the analytics writer. There is no switch. Nothing else is stored about the request. Cache hits are rows too (`cached` set, zero cost). The modality (chat, embeddings, images, audio, video, search, fetch) is the `endpoint`.

**Readers.** `/api/usage/{stats,chart,history,logs,request-logs,providers,active-providers,stream}` read only `requests` and the rollups:
- `stats` and `chart` take `today`, `24h`, `7d`, `30d`, `60d` or `all`.
- `today` and `24h` read the hour rollups, which live inside the request retention. Longer periods read the day rollups for the days before the oldest kept hour and the hour rollups from that day on, so a day whose hours were pruned is still counted once, whole.
- The reporting zone is the server's own (`TZ`, `time.Local`); a day taken from the day rollups can only keep its UTC date (D360). `24h` is the 24 clock hours ending now (D361).
- `history` answers the all-time totals, as the reference does, and `request-details` stays out because it serves stored bodies.
- `logs` and `request-logs` are the 200 newest rows at or after the period's start (seven days by default), one line each, newest by time (D434). A line reads time, model, provider, account, API key, in, out, status, then the skills and savers when set; a failed status carries its error class beside the code, because a spent key budget and a provider's pace are both 429s (D451). The watermark remover is filed with the savers because its prompt changes the request.
- The breakdown opens on the API key grouping. A key is one summary row that opens into the models under it: the server keeps an entry per key, model and provider, and the page folds the entries of one key into one row, so a 60-day range does not list the same key once per model (D442). An opened row reads the provider first and the model second, in all four groupings. A key that is not a stored row but a caller of ours is read under its own name: the health probe's spend reads System Probe, from the identity, not the id prefix. A read a client abandoned, a poll cancelled by a reload, is not logged as a failure, at the handler or at the database logger (D442).
- `providers` is the distinct provider list from the day rollups, named by node then registry.
- `byApiKey` is keyed by the key's id and shows the masked key, never the key (D362).
- `last10Minutes`, the recent list's de-duplication and the log line's skills suffix are left out (D366).

**Live.** `app/gateway/health`'s in-flight table counts every attempt from the moment an account is chosen until its answer, a stream included, is finished. The release is idempotent, so a double end cannot drive another request's count down (D364).

`stats` carries `pending`, `activeRequests`, `recentRequests` and `errorProvider`; `/api/usage/active-providers` counts the providers with a request running.

`/api/usage/stream` sends those four live fields on connect and again when a request starts or ends or a batch of rows lands. It never recomputes the totals (D363).

**Page.** `/dashboard/usage` reads the totals, the chart and the log for every role, and the live feed, the active count and the latency stream only for an admin, because the server refuses those three to a standard user. Period and tab live in the URL. The topology, the two bar charts and the breakdown table are drawn from the same stats answer, so a period switch is one read (doc 18, D427).

### Metrics endpoint (`app/gateway/metrics`, `GET /metrics`; built)

The analytics writer is wrapped by `metrics.Sink`, so every finished request — chat and media alike — is counted before the row is queued. The registry is in memory, holds the same tuple the rollups group by (`provider`, `model`, `connectionId`, `apiKeyId`, `endpoint`), and serves it as OpenMetrics 1.0 text from its own handler; no other module imports it.

- Counters: requests (by status class and cache), tokens (five kinds), spend where the price is known, and the count with no price.
- Histograms: request time and time to first token, per provider and model, in ten buckets.
- Read at scrape time: the in-flight table, one `_info` series per key (name and owning user, never the key) and per account (the page's own account name), the build, and the process and Go gauges.
- Names stay out of the counters, so a rename does not split a series.
- At most 20000 tuples; past it a tuple keeps its provider and model and files the rest under `other`, counted in a fold family.
- `METRICS_TOKEN` or `METRICS_USERNAME` plus `METRICS_PASSWORD` gates the route; with neither it is not registered (404). A refusal counts toward the sign-in limiter.
- `docs/METRICS.md` is the operator guide — how to connect Prometheus and Grafana, every family, the query cookbook — and a test fails when the guide and the registry name different families.

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

The Antigravity limits are read by `app/llm/quota` for the selector; Gemini 4 Carbon counts toward the general Gemini pool, so its three tiers are among the models whose quota is kept.

**Readers.** `GET /api/usage/{connectionId}` answers what is left of one connection's plan through `quota.Reader`. The reader dispatches on the provider through one table (the parity list below) and resolves the credential the way a request does: the login's token renewed first when due, else the stored key.

A connection this build cannot read answers the reference's two messages: an unsupported provider, and an auth type the provider's reader does not serve. A refusal the provider typed as a 401 or 403 is retried once after a renewal, never after matching a message (D367).

The key-only readers added with the 0.5.123 sync: Synthetic (`/v2/quotas`: two request buckets and a weekly pool shown as a share), DeepRelay (the wallet in dollars, a burn rate only when above zero, and a best-effort spend row that never delays the wallet) and OpenDesign Go (the plan's 5 hour and 7 day credit windows and the wallet, read with the account's control key by `amr-api.open-design.ai` only, one workspace never guessed, credits kept as integers until the share is known, D445).

- One field carries one unit: a window counts in its provider's own numbers, a balance is a credit with a unit, and the share left is always a percentage (D368).
- A reading and the Codex refill list are cached by connection id for five minutes, `force=1` skips the reading's cache, and nothing is keyed by a token (D369, D463).
- A window's row key is the provider's own name; its display name is written like the page's other rows: Codex's pairs read `Session` and `Weekly`, and a prefixed pair keeps its prefix capitalized, `Review session` and `Spark weekly`.
- Every call has an 8 s deadline and a 1 MiB cap, and a whole reading 25 s, under the dashboard's 60 s write deadline (D370).
- A failure answers the provider and its status, never the upstream body (D371).

**Refills.** `POST /api/usage/{connectionId}/claude-reset` spends one of Anthropic's free grants and `GET`/`POST .../codex-reset-credits` lists and spends one of Codex's. Both are irreversible and sit behind a dashboard session. A grant id is checked against its own shape before any call, and a missing or bad one is a 400 (D378). A reading lists every grant with its label, uses left and total, pause, usable-now, start and end, the windows it clears, the next grant id and its cooldown; Codex's credit answer is read in either spelling. The page sends the next grant id and lists both providers' credits in a dialog. Every Claude OAuth call — usage, profile and reset — presents the CLI surface, `User-Agent: claude-cli/<live version> (external, cli)`, because Anthropic withholds the `cedar_ember` grants from a caller whose agent says SDK even while the quota windows answer; the version is the one the chat identity advertises, the floor when no store is wired. The Codex refill list is cached beside the reading under `<connection>/credits` and a 2xx spend drops both, so reopening the dialog does not call the provider (D463).

**Providers.** Read: GitHub, Gemini CLI, Antigravity (through the selector's own fetcher), Claude, Codex, Kiro, GLM, MiniMax, Kimi, DeepSeek, OpenCode, Zed, Cursor, Grok CLI, CodeBuddy, Cline, Freebuff, Groq, Ollama, Vercel AI Gateway and Command Code.

MiniMax Code (signed account calls, its key configured not literal, D456), Xiaomi MiMo (the desktop session cookie, with the key path best effort) and Qoder (a personal token exchanged for a job token, cached) are read by `app/llm/quota`; nothing remains deferred there. API key quota limits are a separate feature (design doc 21).

**Page.** `/dashboard/quota` lists the connections `/api/providers/client` says a reading answers for, reads one card per connection through `/api/usage/{id}`, and offers the two refill routes and the active switch behind a confirmation. The expiry dialog takes the wide size, because its Claude table carries five columns, and its Refills column names a window the way the quota table above it does — Session (5h), Weekly (7d), Weekly `<model>` (7d) — for every provider that reports one. A reading's 401 is the provider refusing the stored credential and is shown on the card, never treated as a session end. Hidden rows (`quotaVisibility`) and the bulk enable/disable actions are not offered (doc 18, D429).

**Auto-ping.** A runner ticks once a minute; with `claudeAutoPing` or `codexAutoPing` on, an active login of that provider whose short window has passed its reset, and which it has not warmed yet, gets one tiny request through the pipeline (`System Probe`, 1 token, the registry's first chat model), pinned to the connection whose quota was read so a refusal locks that account and no other. One ping per window, a 15-minute cooldown after a failure, and the switch is read every tick. The reference's anchored slot schedule is not built (D453).

---

## Parity: Usage Routes

| Endpoint | Package | Status |
|---|---|---|
| `/api/usage/stats`, `chart`, `history`, `logs`, `request-logs`, `providers`, `active-providers`, `stream` | `app/http/controllers`, `app/store/analytics`, `app/gateway/health` | done (diverged) |
| `/api/usage/{connectionId}`, `claude-reset`, `codex-reset-credits` | `app/llm/quota`, `app/http/controllers` | done (diverged); MiniMax Code, Xiaomi MiMo and Qoder built (4.20) |
| `/api/providers/client` | `app/http/controllers` | done (`design doc 4`) |
| `/api/usage/request-details` | none | out (stored bodies) |
| `/metrics` | `app/gateway/metrics`, `app/http/controllers` | done (our own; OpenMetrics text, no reference route) |

---

## Remaining work
**The usage rows, the readers, every usage route, the per-connection quota readers, the auto-ping runner and the keyset-paged log are built. Each row carries the savers and skills that acted, as names only, and whether its cost came from a rate.**

A standard user's read is narrowed to the keys assigned to them inside the query (`Sums`, `Buckets`, `LastSeen`, `Recent`, `RecentServed` and `Providers` take the key list; nil means every key, an empty list means none). The in-flight counts stay admin-only because that table holds no key id (`design doc 2`). A narrowed read also leaves out every upstream account's name, email and connection id: those belong to the operator, and a user's usage is by key.

- [x] `/api/usage/*` over `requests` and the rollups, with the live feed; `history` answers the all-time totals (D363), and `/api/usage/logs?before=<ms>-<id>` pages the log with a `(ts,id)` keyset cursor and an `X-Next-Cursor` (the page's Load older)
- [x] Pricing lookup over the embedded baseline and the `pricing` overrides; the pipeline's usage hook calls it
- [x] Per-key request, token and cost budgets with the day, month and total windows, checked before dispatch (step 2.3, `design doc 21`)
- [x] `/api/usage/{connectionId}` and the two refill routes for every provider it serves: MiniMax Code's signed account calls, Xiaomi MiMo's session cookie and Qoder's job-token exchange are built (D379 retires); a refill list carries each grant's label, uses, pause, end and next id
- [x] Quota auto-ping: the runner and the tiny request that warms a window; the anchored slots stay out (D453)

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 17, 19 | Separate analytics store, structured columns, hourly and daily rollups, async writes; counter there. **(built)** readers and routes exist; the dashboard pages are doc 18's work | Small compatibility file, queryable usage, latency independent of the write |
| 117 | Runes of system, tool, message text over four; no usage row. Built as `translate.CountTokens` (images, audio and files add nothing) | Bytes overstate non-ASCII |
| 187 | Report of zeros does not block estimate; cached plus cache-creation tokens never exceed prompt; estimate counts characters of body | Placeholder `usage` cannot hide real output; row cannot charge more than prompt |
| 188 | One hour lifetime writes (always requested for system prompt, last tool) counted from `usage.cache_creation.ephemeral_1h_input_tokens`, priced at `cache_creation_1h`, else twice input rate (PARITY.md §4); first turn of conversation caches its last message; Grok CLI cache key always derived session, as Codex's is | Long write not billed at short rate; client cannot steer another account's cache |
| 77, 110 | Completion includes reasoning; rest at output rate; reasoning at own rate (default equal); thoughts join completion | Billed once, on the right side |
| 107, 108, 109, 111 | One row per request including failures, with error class, `estimated`; one buffer rule; both modes fold cache tokens; tracker reads upstream payload once | One account of what happened |
| 18 | Usage always recorded, metadata only; upstream error becomes class | Prompts, secrets never at rest |
| 143 | One row: status 200, no provider, zero tokens | Every request ends in one row |
| 78 | A request with no rate stores cost 0 with `costKnown` false; the stats answer counts those rows as `unknownCostRequests`. Patterns split once at load **(built)** | Zero can be read as free or as unpriced |
| 354 | A stored rate card is read and written with the field `cache_creation`, and the camel spelling is accepted too **(built)** | A real 9router row spells it `cache_creation`; the strict decoder skipped every override that carried a cache-creation rate, and a card this build wrote would lose it in the reference |
| 360 | Days and labels use the server's zone (`TZ`). `today` and `24h` come from the hour rollups, which cover the request retention; older days come from the day rollups, which are kept forever, so a long chart can switch from local days to UTC days at the retention edge. `lastUsed` is the newest request time when the request rows still hold it (48 h), else the bucket start **(trade-off)** | The reference keeps a local-date rollup for every day; this build reuses the two rollups it already writes instead of a third table, and the seam only touches data older than the retention |
| 361 | `24h` is the 24 clock hours ending now, labelled `HH:00` | The reference buckets back from the current minute, so labels read `14:37` |
| 362 | `byApiKey` is keyed by the key's id and carries `apiKeyMasked`; every period uses the same rule | The reference puts the raw API key in the object key of every long-period `stats` answer and every `history` and stream frame, which hands any dashboard reader the keys |
| 363 | The stream sends only `pending`, `activeRequests`, `recentRequests` and `errorProvider`, on connect and on a change of the tracker generation or the flushed-batch counter | The reference recomputes all-time totals after every request and sends the whole object to a page that reads four fields |
| 364 | A request holds its in-flight slot until its answer, a stream included, is finished; the release is idempotent; a provider with no connection is counted too | The reference zeroes a counter after 60 s while the stream still runs and a double end can drive another request's count down |
| 365 | `providers` comes from the day rollups | The reference reads a `requestDetails` table this build does not have, and its filter forgets a provider once that table is pruned |
| 366 | No `last10Minutes`, no de-duplication of the recent list **(scope cut; the skills suffix was added later)** | No page reads the first, and one row per request already rules out the second. The requests table now carries `savers` and `skills` as names only, and the log line appends them when set |
| 451 | The log line names the API key after the account: time, model, provider, account, key, in, out, status, then the skills and savers when set. A failure carries its class beside the code (`429 key_quota`); the watermark remover's prompt half is filed with the savers | The reference's line stops at the status, so a reader cannot tell which key made a call, why a request failed, or that a fixed transform is a saver beside RTK |
| 355 | Every pricing route answers the merged table: the embedded provider rows with the overrides laid over them, and a write applies to the next request through the reloaded source **(built)** | The reference's PATCH answers only the overrides while GET answers a merged map of two providers, and its edit form sends the whole map back, turning every default into a stored override |
| 367 | A refused credential is retried once when the fetcher saw a 401 or 403, and the refusal is typed, not read out of a message **(built)** | The reference retries when the answer's message contains `expired`, `authentication`, `unauthorized`, `401` or `re-authorize`, so a provider's wording decides whether a token is renewed |
| 368 | One unit per field: a window counts in the provider's own numbers, a balance is `isCreditBalance` with a `unit`, and the share left is always `remainingPercentage` **(built)** | The reference's `remaining` is a count for GitHub, Kiro and Qoder, a percentage for Claude and Codex, and unclamped for Vercel, so the page cannot render one bar rule |
| 369 | A reading and the Codex refill list are cached by connection id for five minutes, `force=1` skips the reading, and a failed read is not cached **(built)** | The reference caches by the raw access token, never evicts, and its Claude entry keeps returning a failed result until a forced read; five minutes is its own usage TTL and serves the dashboard's minute poll without a provider call per tick |
| 370 | Every call is bounded at 8 s and 1 MiB, and a reading at 25 s **(built)** | The reference sets no timeout on most fetchers and reads the whole body, so one silent endpoint holds a dashboard request open |
| 371 | A failed reading answers the provider's name and status; no upstream body reaches a message **(built)** | The reference echoes the full body in GitHub's and Kiro's errors and in Zed's, which can carry account data |
| 372 | Claude's legacy organization fallback (`/v1/settings`, then `/v1/organizations/{id}/usage`) is not built **(scope cut)** | It returns the raw organization usage as if it were quotas, and it joins an upstream-supplied id into a path unencoded |
| 373 | Grok CLI's gRPC-web fallback is not built **(scope cut)** | It only fires when the billing calls answer no rows, and it hand-decodes a protobuf frame for one percentage |
| 374 | Vercel's remaining share is taken against the balance plus what was spent, and `Used (USD)` carries the spend **(built)** | The reference divides by a hardcoded $5 monthly credit, which overstates a funded account and inverts the two rows' meaning |
| 375 | An Ollama window that names no `remaining_percent` is left out **(built)** | The reference reads a missing share as 100% left, so an unknown window looks full |
| 376 | Kimi's `limits[]` entries each keep a row **(built)** | The reference writes every entry to one `Ratelimit` key, so only the last survives |
| 377 | MiniMax's refusal is read from the status and the plan's own `base_resp` code, and a 200 body is never scanned **(built)** | The reference runs an invalid-key regex over the body of every answer, so a successful answer that mentions "coding plan" is reported as a bad key |
| 378 | A reset grant id outside its shape answers 400 before any call **(built)** | The reference throws from the service and answers 500 for a missing or malformed grant |
| 379 | MiniMax Code, Xiaomi MiMo and Qoder have no reading: their calls need a signed account API, a cookie SSO chain and a pasted token exchanged for a job token **(scope cut)** | Each is a handshake of its own; the three land together with the design that covers them |
| 393 | Kiro's quota is read from CodeWhisperer's GET, then its POST (`x-amz-target`), then the Q host's GET, and the first answer wins; a refusal on all three is a message that says chat may still work, not an error that triggers a renewal **(built)** | The reference reads the same three and words its messages by sign-in method; this build had tried one host and built a second it never called. The live endpoints were never called here, so the POST attempt is unverified |
| 394 | Text a provider returns in a Claude reset answer is cut to 200 bytes on a character boundary before it reaches the dashboard **(built)** | The reference passes `result`, `reason` and `error.message` through uncut, so one long answer from the vendor fills the page |
| 380 | Quota auto-ping is not built; the settings are read and written in the reference's shape only **(retired: built in 4.18 as D453)** | The scheduler needed its own design pass; it now has one |
| 456 | MiniMax Code's signing key is `UPSTREAM_MINIMAX_SIGN_SECRET`, configuration with an empty default, never a literal | A shipped signing key is a credential every copy shares; the operator sets it, and an unset one leaves that reading unavailable instead of failing a request |
| 457 | Retired: the translator playground was removed at the operator's request; only the console-log routes remain under `/api/translator` | The feature was out of scope |
| 453 | Auto-ping runs a one-minute tick and pings once per window after its reset, with a 15-minute failure cooldown and the registry's first chat model; the reference's anchored per-account schedule, its pinned ping models and its plan-window checks are not built | One switch per provider, one ping per window, nothing scheduled per account; a pinned model id rots, a registry model is one the account can call |
| 434 | The log read is bounded by the page's period, seven days by default, and ordered by time rather than by row id | The reference returns the 200 newest rows with no bound, so a month-old import fills the list; ordering by id lets an imported old row outrank a newer live one |
| 442 | The usage table folds an API key's rows into one summary row, summing its requests, tokens and cost across models and providers, and opens that row into the per-model detail (doc 18); a read a client abandoned is not logged as a failure, at the handler or at the database logger. The reference's table has one row per key, model and provider. **(built)** | One key shown as several rows made a 60-day total look like several keys, and a poll cancelled by a reload filled the log with errors that meant nothing |
| 460 | The request log's feature line is one fixed order — RTK, CAVEMAN, PONYTAIL, ALLOW REA, WATERMARK-REMOVER, HEADROOM, then the used skills once — with upper-case ON/OFF/SKIPPED; CUSTOMPROMPT follows PONYTAIL when it acted. RTK names the filters it compacted (`RTK ON (git-diff,grep)`), says `RTK ON` when it ran with nothing, and `RTK OFF` when disabled: it has no routing mode, so it never says SKIPPED | The reference prints colon tokens in saver order with no per-skill summary and no skipped wording; a reader scanning many lines wants one order, the skills named once, and a line that can be matched to its usage row |
| 463 | The Codex refill list is cached per connection for the reading's TTL (five minutes) under `<connection>/credits`, and a 2xx spend drops both it and the reading **(built)** | The reference fetches the list on every expiry-dialog open with `cache: no-store`, so every open costs a provider call; the list is display-only and the provider rate-limits |

---

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| The analytics store writes usage rows and the pipeline prices each one. | `upstream.Table` (`Lookup`, `Cost`, `ParseOverrides`) supplies the rates. |
| A value taken from a vendor's answer must not reach the next query as it came. | Command Code's organisation id is escaped when it is joined into the billing address, as the reference's `URLSearchParams` does. |
| A pricing row that cannot be read must never be replaced by the write that follows it. | `mergeCards` and `dropCard` refuse a stored row that is not an object and leave it as it was. |
| A failed read of the operator's prices must not become the price list for good. | The source returns the embedded table together with `ErrPricingOverrides`; requests price with it, and the next call reads again. |
| Filtering a user's rows in the handler would still read every row, and a forgotten filter shows another account's numbers. | The key scope is a query parameter on every reader; an empty scope returns nothing rather than everything. |
| An unknown model must not look free. | `Table.Cost` returns `known=false`; an override of `{}` is the only way to say a model costs nothing. |
| Anthropic withholds the Claude reset grants from a caller whose user agent says SDK, while the quota windows still answer. | The usage, profile and reset calls name the CLI surface with the live fingerprint; the floor is the fallback when no store is wired. |
| 4.34: the auto-ping read one connection's quota and sent its request unpinned, so a refusal could land on another account of the provider. | `pingInput` pins the warm-up to the connection whose window was read. |
| 4.44: a key refused for its budget logged a line of dashes and a bare `429`, so the operator could tell neither what was asked nor the key's budget from a provider's rate limit. | The chat pipeline names the resolved target in the row before the access and budget checks, as the media pipeline already did, and both media budget refusals store `key_quota`. A failed log line carries its class beside the code, and the page turns `key_quota` and `rate_limit` into "Budget Rate Limited" and "Upstream Rate Limited", the code kept. |
| 4.45: the class alone read `Failed 429 key_quota`, and the account cell was a dash because the budget is checked before any account is chosen. | `statusWord` spells the two 429s out with their code, and a refusal peeks at the selector: the same pick with nothing recorded, so the row names the account the request would have reached while no rotation moves. |
