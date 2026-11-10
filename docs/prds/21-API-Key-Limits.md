# PRD 21 — API Key Limits (`app/access/`)

**Status:** todo · **Steps:** 2.2, 2.3

`app/access` does not exist. The `kv` scope and the columns it will read are defined in `PRD 15`.

| Limit | Behavior |
|---|---|
| Model | `apiKeys.accessRestricted` and `accessAllow`. A restricted key may call only the combos and resolved `provider/model` pairs listed. Checked on the resolved model, on combo names and members, and again after a rule reroute. Failure is a 403 in the client's error shape |
| Quota | Types: requests, tokens, cost. Windows: day, month, total (UTC). Stored in `kv` scope `rllmKeyLimits`, key = API key id (additive, invisible to 9router) |

- Usage comes from the analytics rollups (`rollupDaily` is never pruned) plus an in-memory delta for rows not yet flushed.
- Checked before dispatch; counted when the usage row is enqueued. Over the limit is a 429 `insufficient_quota` in the client's shape, with headers for the remaining quota.
- The limit is soft: requests in flight can overshoot by at most the number of concurrent requests (documented and tested).
- Whether the check fails open or closed when analytics is unavailable is decided in step 2.3.

---

## Remaining work
**Steps 2.2 and 2.3.** Nothing built.

- [x] The model list filtered by the allow-list (1.8f, `app/gateway/catalog`: each entry resolved through the resolver, exact comparison); 2.2 reuses that matching for the per-request check
- [ ] Per-key model allow-list, enforced per request; a reroute is re-checked against it. The response cache looks up before account selection, so the check must run before the lookup, or a restricted key could read an answer for a model it may not call
- [ ] Per-key quota: request and token budgets, reset window, 429 `insufficient_quota`

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 25 | Per-key request, token, cost quotas; 429 before dispatch **(planned 2.3)** | **New feature** |

---

## Findings

None recorded.
