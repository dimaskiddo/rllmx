# Design 12 — Health, Circuit Breaker & Probe (`app/health/`, `app/probe/`)

**Status:** todo

The health probe and the circuit breaker do not exist; each arrives with its own step.

The breaker lives in `app/health` (open after repeated failures, half-open after a pause) because the framework has none; `app/platform/cache` reuses it if the Redis measurement in `design doc 20` needs one.

| Part | Behavior |
|---|---|
| Health | Per provider: EMA latency (alpha 0.2), success rate and a circuit breaker. Five consecutive failures open it for 60 s, then one half-open trial decides. Routing honors the breaker: account selection and combo ordering skip an open provider when another candidate exists |
| Probe | A minimal request to the models of a new or edited connection (concurrency 3, 10 s timeout, results cached 5 minutes) to learn which models answer |
| Suggestion | `GET /api/combos/suggest?weights=reliability,latency,cost,quality&prompt=` ranks models by tier and weighted score. `POST /api/combos/suggest/accept` creates a combo and never overwrites one |
| Latency stream | `GET /api/health/latency-stream`, an SSE feed of the health table |
| Live-catalog guard | For kiro, codex, cursor, kimchi and qoder the account's live model list (5-minute cache) is checked before dispatch; a model not on it gets 400. If the list cannot be fetched, the request goes ahead |

---

## Parity: Probe and Suggested-Model Routes

| Endpoint | Package | Status |
|---|---|---|
| `/api/combos/suggest`, `/suggest/accept`, `/api/health/latency-stream`, `/api/usage/active-providers` | `app/gateway`, `app/health` | todo |
| `/api/headroom/*` (status, proxy) | `app/saver` | todo |
| `/api/skills`, `/api/skills/sync-ecc` (dashboard action, not CLI) | `app/skill` | todo |
| `/api/auto-plan/settings` | `app/plan` | todo |

---

## Remaining work
**Step.** Nothing built.

- [ ] `app/health`: liveness, circuit breaker, latency SSE
- [ ] `app/probe`: scheduled probe, model probe on create and update (concurrency 3, 10s timeout, 5-minute cache)
- [ ] `providers/test-batch`, `providers/{id}/test-models`, `models/test` through the pipeline

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 7, 8 | Routing honors the breaker (planned); IP limit becomes 5-minute model lock | Decorative in reference, or needs pools (Deferred) (planned) |

---

## Findings

None recorded.
