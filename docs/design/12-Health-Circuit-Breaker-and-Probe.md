# Design 12 — Health, Circuit Breaker & Probe (`app/gateway/health`, `app/gateway/probe`)

**Status:** partial

**Built:** the health table and the breaker (`app/gateway/health`), routing on the breaker (combo order), the model probe with its cache and its background runner (`app/gateway/probe`, `probeRunner`), the test and suggest routes, and the latency feed. `active-providers` stays in `design doc 16` with the usage routes.

| Part | Behavior |
|---|---|
| Health | One entry per provider id, in memory and per process: a second instance keeps its own, and a restart starts clean. A node's id counts as a provider. EMA latency (alpha 0.2) is updated by a success only; the success and failure counters are scaled by 0.99 once they reach 100, so the rate stays about today's |
| Breaker | Five failures in a row open it for 60 s. After that it is half-open: traffic goes through and the first outcome decides. A success closes it from any state; a failure while half-open starts the wait again. Half-open is derived on read, never stored, so reading health cannot change routing |
| What counts | A failure is recorded only where the account loop moved on (`Reporter.Fail` fell back). A request-scoped 4xx, an empty answer, an expired or gated account, the live-catalog 400 and a client that left record nothing. A success is recorded where the account is accepted, with its time to first byte. A stream that opened and then broke is a failure. A cache hit records nothing |
| Routing on it | Combo order moves a member whose provider is open to the end, after the strategy and the capability order, for `fallback`, `round-robin` and `smart`. Nothing is dropped: an open provider still answers when it is all that is left. `fusion` is not reordered, because its panel asks every member anyway. Account selection does not read it: `Select` serves one provider, so there is never another candidate to skip to (divergence 7) |
| Probe | One ping is an ordinary pipeline request pinned to one connection (`Input.Connection`), with the cache bypassed and a fixed internal caller. It can lock, clear and spend, as the reference's does through its own chat route. Body: `"hi"`, `max_tokens` 1024, no stream, 10 s. A pinned request never falls back to another account: if the pinned connection is not a candidate, the answer is 503 |
| Probe candidates | The connection's own `models` or `providerSpecificData.enabledModels` with `defaultModel` first, else the provider's list (custom models in, disabled out, chat kind only) capped at 5, else the node's `defaultModel` under its prefix. The first candidate runs alone so a token refresh happens once; the rest run three at a time |
| Probe cache | In memory, keyed connection and model, 5 minutes, 2,000 entries with the oldest evicted. `test-models` reads through it with force; `suggest` reads it without spending |
| Auto-probe | A bounded queue of 64 connection ids, de-duplicated, drained by `probeRunner` one connection at a time on the runner's context. A full queue drops the job. `Create`, an `Update` that leaves the connection active, and a passing `/test` enqueue; a bulk add does not |
| Suggestion | `GET /api/combos/suggest` ranks the cached probe results, the health table and the real price table; it never probes. Weights default to 0.4/0.3/0.2/0.1 and a bad value falls back to them. The score is `r·reliability + l·(1−min(lat/10000,1)) + c·(1−min(cost/10,1)) + q·tier/100`, times a prompt boost (1.3 reasoning, 1.2 mini, 1.15 code model for a code prompt). The answer takes the best of reasoning, flagship, standard and mini first, then fills by score |
| Accept | `POST /api/combos/suggest/accept` creates the combo with `kind:"auto"`. Any existing name is 409: accepting never overwrites, an auto combo included |
| Latency feed | `GET /api/health/latency-stream`: SSE of the health table, sent on connect and again only when it changes, with a `: keepalive` comment every 30 s. Providers with an active connection are seeded so a quiet provider is still on the feed. It needs a dashboard session |
| Live-catalog guard | For kiro, codex, cursor, kimchi and qoder the account's live model list (5-minute cache) is checked before dispatch; a model not on it gets 400. If the list cannot be fetched, the request goes ahead |

`POST /api/providers/test-batch`, `POST /api/providers/{id}/test-models` and `POST /api/models/test` are dashboard routes and need a session. The three of them and the latency feed are exempt from the dashboard write deadline (`apphttp.IsLongDashboardCall`); each bounds its own work instead: 5 minutes for a batch, 2 minutes for `test-models`, the writer's timeout per frame for the feed.

---

## Parity: Probe and Suggested-Model Routes

| Endpoint | Package | Status |
|---|---|---|
| `/api/combos/suggest`, `/suggest/accept` | `app/gateway/combo`, `app/http/controllers` | done |
| `/api/health/latency-stream` | `app/http/controllers`, `app/gateway/health` | done |
| `/api/usage/active-providers` | `app/gateway/health`, `app/http/controllers` | done (the usage routes, `design doc 16`) |
| `/api/providers/test-batch`, `/api/providers/{id}/test-models`, `/api/models/test` | `app/http/controllers`, `app/gateway/probe` | done (llm models only) |
| `/api/headroom/{status,proxy}` | `app/http/controllers` | done |
| `/api/skills`, `/api/skills/sync-ecc` (dashboard action, not CLI) | `app/skill` | todo |
| `/api/auto-plan/settings` | `app/plan` | todo |

---

## Remaining work
**Health, the breaker, the probe and the live usage route are built. The later areas wait for their steps.**

- [x] `app/gateway/health`: the health table, the breaker, latency and success rate
- [x] Routing on the breaker: combo order
- [x] `app/gateway/probe`: the model probe, its cache and the background runner
- [x] `providers/test-batch`, `providers/{id}/test-models`, `models/test` through the pipeline
- [x] `/api/combos/suggest` and `/suggest/accept`, `/api/health/latency-stream`
- [x] `/api/usage/active-providers` and the in-flight table with the usage routes (`design doc 16`)
- [ ] `/api/skills*`, `/api/auto-plan/settings` with their areas

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 7, 8 | Routing honors the breaker: combo members of an open provider go last. **Built.** Account selection does not read it, and the IP limit stays a 5-minute model lock | The reference's breaker only feeds its dashboard and its suggest fallback, and it can stay open while the provider serves; a pool-based skip needs pools (Deferred) |
| 339 | A success closes the breaker from any state, a failure while half-open starts the wait again, half-open is derived rather than stored, and the counters decay as floats. **Built** | The reference leaves the breaker open after a success and its counter decay only fires from 100, so a provider can look dead while it answers |
| 340 | A stream that opened and then broke counts as a provider failure. **Built** | The reference records nothing there, so a provider that drops every stream keeps its reliability score |
| 341 | The probe is an in-process request pinned to its connection, with the cache bypassed and an internal caller instead of a borrowed API key, and the pipeline records the outcome once. **Built** | One attempt on the connection asked about; no key is spent impersonating a client; a probe is not counted twice |
| 342 | `test-models` caps the list at 50 and runs it three at a time. **Built** | The reference sends every model at once with no ceiling |
| 343 | `suggest` never probes: it ranks cached results, the health table and the real price table. **Built** | A GET that spends tokens and changes locks is a surprise, and the reference's cost weight never meant anything because its price map is empty |
| 344 | Accept never overwrites a combo: any existing name is 409. **Built** | The reference deletes an auto combo and creates it again, which changes its id under a client that just read it |
| 345 | The latency feed needs a dashboard session, sends no wildcard CORS header, and polls the table instead of subscribing to an emitter. **Built** | The reference's feed is public and its listener cleanup never runs, so its listeners leak |
| 346 | `test-batch` has no proxy test and no OAuth retry. **Built** (scope cut) | This router has no outbound proxy, and a second token exchange is the refresher's job |
| 347 | `models/test` tests chat models only; a media kind is refused with a 400. **Built** (scope cut) | Media models answer through their own routes and their own probe |

---

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| A breaker read that changes state cannot be cached, ordered or raced safely. | Half-open is computed in `Open` and in `Snapshot`; `Record` is the only writer. |
| A provider-level breaker inside a one-provider selector would turn every candidate away. | The breaker orders combo members instead; the selector keeps its lock rules. |
| A probe through the pipeline needs exactly one attempt or it measures the wrong account. | `Input.Connection` pins the selector, and a pin that is not a candidate ends the request. |
| A dashboard GET that spends is a trap for a polling UI. | Suggest reads the probe cache and never calls a provider. |
| A feed that repeats an unchanged table every two seconds is noise on a phone. | The first frame is unconditional and later frames only on a change. |
