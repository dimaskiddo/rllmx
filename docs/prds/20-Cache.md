# PRD 20 — Cache (`app/platform/cache/`)

**Status:** partial · **Steps:** 1.3, 1.8f, 1.8g, 2.1, 2.6

`Cache` is the cache. `app/platform/cache` adds only what it lacks and defines no store or client: grouped, generational keys (`<group>:<generation>:<key>`, since the facade cannot purge a group), credential groups pinned to memory, a read-through `Load` that runs the loader once per key, and fail-open. It takes the facade as an argument, so a test passes a fake.

| Store | Selected by | Notes |
|---|---|---|
| Memory | `default`: `memory` in `config/cache.go` | A `sync.Map` where each `Put` starts an expiry timer. No entry or byte cap, and `Get` returns the stored value, not a copy — so every key gets a TTL, values stay small and read-only, and unbounded groups (`limiter`, `response`) are bounded by key design |
| Redis | `redis` store in `config/cache.go`, used by shared groups only when `REDIS_HOST` is set; otherwise `app/platform/cache` is built `WithoutShared`. `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB`; no username, no `*tls.Config`, so the driver builds a plain host:port | `goravel/redis` over go-redis. Also Valkey (never run against a real server). TLS only if `config/database.go` set a `*tls.Config`, which it does not, so `rediss://` is not a supported spelling today. Other timeouts and retries are go-redis defaults |

### Fail Open

- `Put` returns an error and `Get` returns the default on a failure. Our wrapper in `app/platform/cache` turns every error into a miss and every failed write into a no-op, so a request never fails because of the cache.
- The Redis driver is built with no client when the first connection fails (its constructor returns `nil, nil` after a failed ping), and the facade keeps that driver, so every later call on it panics instead of returning an error. The wrapper recovers the panic and treats it as a miss. A store the facade cannot build at all is the same miss.
- The first failure of the process is logged once, with no value and no panic text; later ones are silent. A breaker that skips a down store, and a measurement of how long a stopped Redis delays each call, are not built: they need a real Redis, and `TASKS.md` lists them in Open items.

### Loading & Purge

| Item | Behavior |
|---|---|
| Load | `app/platform/cache`'s `Load(ctx, group, key, ttl, fn)` is the read-through load, built on `golang.org/x/sync/singleflight` so concurrent callers of one key share a single load. The facade's `Remember` is not used: it does not collapse concurrent loads. The load runs on a context detached from the callers, so one that leaves does not fail the rest |
| Purge | The facade has no purge by group. A group's keys carry a generation number (`<group>:<generation>:<key>`) read from a counter key; a purge calls `Increment` on the counter, so older keys are never read again and expire by TTL. `Flush` clears a whole store and is not used for this |

### What Is Cached

Built in 1.8f (the node model list) and 1.8g (the response-cache switch and the per-key flag). Rows marked *planned* are read by later steps.

| Value | Namespace and key | TTL | Dropped by |
|---|---|---|---|
| Naming-filter flag (secret-free projection of the settings row) *(planned)* | `settings` / `pipeline` | 30 s | any settings write |
| Response-cache switch and limits (built in 1.8g: `{enabled, ttl, maxKb}` with defaults filled in) | `settings` / `response` | 30 s | any settings write |
| Provider overrides (header values may be credentials) | `overrides` (memory only) / `all` | 30 s | any settings write |
| Per-key response-cache flag (built in 1.8g) | `apikeys` / `rc:<key id>` | 10 s | `KeyStore.Changed` (the key API, 2.1, calls it when it writes the flag) |
| A user node's own model list (built in 1.8f; an empty list when the node failed). Key: SHA-256 of node id and base URL, so an edited address misses by itself; the value is the model list and never a credential. The assembled catalog is not cached: it is a few small reads per request | `models` / `node:<hash>` | 30 s | expiry only |
| Key lookups | `apikeys` / SHA-256 of the key | 10 s | `KeyStore.Changed` |

- A settings write calls `Store.OnSettingsChange` after commit: the app purges `settings`, `overrides` and `response`, and resets the selector's settings copy and the auth snapshot. A write from another process (`rllmx artisan password:reset`) is not announced, so it shows only after the TTL.
- The selector reads connections live, because locks and sticky counters change per request. `Selector.settingsDoc`, `auth.Snapshot` and `routing.Snapshot` are small typed copies held in process. The refresh-result cache (10 s, memory only, collapses concurrent renewals of one refresh token) is built in 1.9b.

### Namespaces

Only `settings`, `apikeys`, `models`, `limiter`, `response` and `toolcalls` may use the configured store. **Every other group, including one nobody listed yet, is read and written through `Cache().Store("memory")` only**, so a new credential-bearing group is safe by default. The allow-list lives in one unexported function in `app/platform/cache`, which picks the store itself; product code names a group and never a store.

| Namespace | Holds | Backend |
|---|---|---|
| `settings` | Secret-free settings projections | Configured driver |
| `apikeys` | Key lookups, keyed by SHA-256 of the key | Configured driver |
| `models` | Model catalog state and live-catalog lists | Configured driver |
| `overrides` | Provider override headers | Memory only (not on the Redis list) |
| `limiter` | Login failure counters | The memory store always applies (the in-process floor), plus the configured store when it is Redis. Counters are small and expire by TTL, so the store's missing size cap does not matter |
| `response` | Cached completions | Configured driver |
| `toolcalls` | Gemini thought signatures by tool call, so a client that drops one does not break the next turn, owned by the Gemini codec in `app/llm/translate`. Key: SHA-256 of the API key id and the call id, never the bare id; value: model family and signature; 1 hour; a signature is never offered to a model of another family | Configured driver |
| `connections` | The Copilot token a GitHub account's requests carry, for 10 s after it is minted so concurrent requests share one mint (`oauth.Minter`); the account's own copy lives sealed in its provider data | Memory only |
| `refresh` | Refreshed credentials (10 s result cache) | Memory only |
| `oauth` | Login sessions with PKCE verifiers or private keys (reserved: the 1.9a store is an in-process map, because `Begin` has to be one atomic step) | Memory only |
| `discovery` | One identity provider's OpenID metadata and signing keys, 1 h, dropped when the settings row is written | Memory only: it holds no credential, but a sign-in must finish on the instance that started it, so sharing it buys nothing |
| SSO stores (not a group) | Pending sign-ins (`app/access/sso`, 10,000, ten minutes) and the SAML assertion replay set (`app/access/saml`, 100,000, until the assertion's own expiry, a day at most) | Dedicated in-process maps with their own caps, never Redis: a sign-in must finish on the instance that started it, and a shared replay set would put a lock on the request path for nothing |

- `config/database.go` passes `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` and `REDIS_DB` to `goravel/redis`, which builds a plain `host:port`; there is no TLS toggle and no `rediss://` spelling. A non-loopback `REDIS_HOST` logs one start-up warning at WARN: the connection is not encrypted. The choice of a trusted network is the operator's.
- Someone who can write to Redis can lock people out of the dashboard but cannot lift a lock, because the in-process floor holds.
- Several instances: a dashboard OAuth login or SSO sign-in must finish on the instance that started it (sticky routing for the dashboard), and connection data can be stale across instances for up to its short TTL.

### Response Cache

Off by default. Built in 1.8g: `app/gateway/pipeline/response_cache.go` (switches, key, lookup, store), `app/gateway/pipeline/canonical.go` (the canonical body), `cache.Budget` (the in-process bound) and `repository.KVRepo.ResponseCacheEnabled`. No reference has a response cache, so there is no reference behavior to follow or diverge from.

| Item | Behavior |
|---|---|
| Switches | Setting `responseCache` `{enabled, ttlSeconds, maxEntryKb}` **and**, per API key, kv scope `rllmResponseCache` (key = key id, value `{"enabled":true}`; no row, a malformed row or a value other than a JSON `true` is off). Both must be on; a failed read of either is off. `ttlSeconds` defaults to 3600 and must be 1 to 604800, `maxEntryKb` to 512 and 1 to 4096; a missing or out-of-range value takes the default, because `Effective` merges defaults only at the top level. The setting is read through the `settings` group (30 s) and the flag through `apikeys` (10 s) |
| Applies to | A non-stream request from a real API key (not the anonymous local caller) that sends `X-Rllm-Cache: use` or an explicit `temperature` of exactly 0 (`temperature`, `generationConfig.temperature`, `options.temperature`; an absent, `null` or string value does not count). `X-Rllm-Cache: bypass` wins over a zero temperature. A request body over 4 MiB, or one whose JSON repeats a key at any depth, is not cached: the codecs read the first of two equal keys and `encoding/json` the last, so such a body has no single meaning to key on |
| Key | SHA-256 over length-prefixed parts: API key id (first), client format, provider, resolved model, effective thinking (mode, level, budget), and the canonical body (the JSON without the top-level `stream`, keys sorted at every depth, numbers kept as written). Two keys never share an entry. The context marker (`[1m]`) is not a separate part: it is stripped from the model name before the body is read, changes nothing upstream, and so shares the plain request's entry. **The client tool joins the key since 1.9c**, after the provider, because the Claude disguise (D216) makes the upstream body depend on it; the thinking display joins it too |
| Never cached | Non-200 answers, answers that contain tool calls (in the message or the finish reason), entries over `maxEntryKb` or 4 MiB, streams |
| Hit | The lookup runs after the model is resolved and before the image prefetch, the translation and any account selection. The stored client-format body is written as one 200 `application/json` response with `X-Rllm-Cache: hit`. No account is selected, no lock or counter is touched, no upstream call. The usage row has `cached` set, status 200, the resolved provider and model, no connection, attempt 0, cost 0, and replays the first call's prompt, completion, cached and reasoning counts (`CacheWrite` is 0). The stored body already carries the client buffer (D218) |
| Headers | `X-Rllm-Cache: hit` or `miss` on a delivered 200 when the cache was consulted; a miss is reported whether or not the answer was then stored. An error answer carries none. `bypass` skips lookup and store and adds no header. The header is on the CORS expose and allow lists |
| Invalidation | Any settings write through the store purges the `settings` and `response` groups, so a switched-off cache stops answering at once. A write from another process (`rllmx artisan password:reset`) shows after the 30 s projection TTL, and cached answers last at most their TTL. Node and connection edits do not purge |
| Redis | Cached completions leave the process when `REDIS_HOST` is set; keep it on a trusted network. A Redis with persistence on writes answers to its own disk, which is the operator's choice. This program writes an answer to no file and no log, and an entry holds no request text and no credential |
| Limits | The facade's memory store has no size cap, so `cache.Budget` bounds the response group in process: it stops storing at `CACHE_RESPONSE_MAX_ENTRIES` (1000) or `CACHE_RESPONSE_MAX_MB` (64), whichever comes first, counting sizes and expiry times only. With `REDIS_HOST` set there is no budget; the server's own limit applies. Quota step 2.3 counts a hit like any usage row unless decided there. Two identical requests that miss together both call the upstream and both store the same key |

Non-hit pricing is unchanged: provider-reported cached tokens at the cached rate (the input rate when none is set), cache writes at the write rate. This is the shape OpenRouter and OpenCode Zen price with.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 189 | Store is shared cache (memory, Redis, Valkey) under `toolcalls` namespace; key is hash of API key id + call id only; 1 hour; one signature per entry (16 KiB); no disk tier; signature read back only by a model of the family that issued it | Bare id would let one API key guess another's entry; lost signature costs one re-ask |
| 23 | TTL cache (memory or Redis), singleflight loading: key lookups, secret-free settings projection, provider overrides (memory only), model catalog state. Connection lists live; refresh-result cache built in 1.9b. Credential namespaces memory-only; any failure a miss; settings or catalog write drops entries | Lower latency; one node fetch per window instead of per call |
| 24, 148 | Optional, off by default, on only when setting and API key's own flag both on; key starts with API key id; no streams, errors, tool-call answers. Hit recorded `cached` at zero cost, replays first call's token counts (reference has no cache, so no rule to follow) | **New feature** |
| 199 | Retired: the cache is the `Cache` facade, not a store of our own | Recorded so the number stays citable (framework choice) |

---

## Remaining work

Built and tested: `app/platform/cache` (groups, generational purge, the memory-only credential groups, `Load` with singleflight, fail-open, `Budget`), the node model list in the `models` group (1.8f) and the response cache (1.8g).

- [x] The response cache: switches, key, lookup, store, header, budget, the kv reader, the purge on a settings write (1.8g)
- [x] The first readers of the cache: the node model list (1.8f), the response-cache projection and the per-key flag (1.8g)
- [ ] Writing the per-key flag, and calling `KeyStore.Changed` and removing the kv row when a key is deleted: the key API (2.1)
- [ ] Writing the `responseCache` setting: the settings API (2.6)
- [x] Client tool in the key (1.9c)

---

## Findings

`app/platform/cache` and its provider are built and tested. The facade's `Remember` does not collapse concurrent loads, so `app/platform/cache.Load` adds singleflight.

**The framework's memory store has no size cap**, and arms one timer per `Put` (an earlier timer can drop a later value stored under the same key, which only costs a miss). An entry count alone does not bound memory, since one entry may be 4 MiB, so `Budget` counts both entries and bytes.

**The body is keyed on its canonical form, and a body with a repeated key is refused.** Two JSON readers disagree about which of two equal keys wins, so a cached answer could otherwise belong to a request the upstream never saw.

**There is no way to switch the cache on from the API yet.** Until 2.1 and 2.6 the flag and the setting are written by hand in the database; the 1.8g curl check does exactly that on a scratch data directory.
