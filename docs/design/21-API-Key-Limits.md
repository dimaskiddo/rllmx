# Design 21 — API Key Limits (`app/access/`)

**Status:** partial

The model list is built; the quota is not. The `kv` scope and the columns quota will read are defined in `design doc 15`.

| Limit | Behavior |
|---|---|
| Model | `apiKeys.accessRestricted` and `accessAllow`, enforced. A restricted key may call only the combos and resolved `provider/model` pairs listed; a media request is checked on its resolved identity and a search, fetch, poll or voice list on its provider. The comparison is exact on the resolved identity, and an empty list allows nothing. Failure is a 403 naming the requested model, before the response cache and before any account is chosen |
| Quota | Types: requests, tokens, cost. Windows: day, month, total (UTC). Stored in `kv` scope `rllmKeyLimits`, key = API key id (additive, invisible to 9router) |

- Usage comes from the analytics rollups (`rollupDaily` is never pruned) plus an in-memory delta for rows not yet flushed.
- Checked before dispatch; counted when the usage row is enqueued. Over the limit is a 429 `insufficient_quota` in the client's shape, with headers for the remaining quota.
- The limit is soft: requests in flight can overshoot by at most the number of concurrent requests (documented and tested).
- Whether the check fails open or closed when analytics is unavailable is decided in step.

---

## Remaining work
**Steps and.** Nothing built.

- [x] The model list filtered by the allow-list ( `app/gateway/catalog`: each entry resolved through the resolver, exact comparison); reuses that matching for the per-request check
- [x] The allow-list is **stored** from the key API (`app/access/auth.ValidateAccess`, 200 entries of 256 characters, normalized) and nothing reads it yet
- [x] Per-key model allow-list, enforced per request before the response cache (`routing.NewAllow`; the pipeline and the catalog share it). A combo is checked by name; member re-checks land with 2.4, which is when members are expanded
- [ ] Per-key quota: request and token budgets, reset window, 429 `insufficient_quota`

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 25 | Per-key request, token, cost quotas; 429 before dispatch **(planned)** | **New feature** |

---

## Findings

None recorded.
