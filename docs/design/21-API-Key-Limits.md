# Design 21 — API Key Limits (`app/access/`)

**Status:** done

The model list and the quota are built. The `kv` scope they read is defined in `design doc 15`.

| Limit | Behavior |
|---|---|
| Model | `apiKeys.accessRestricted` and `accessAllow`, enforced. A restricted key may call only the combos and resolved `provider/model` pairs listed; a media request is checked on its resolved identity and a search, fetch, poll or voice list on its provider. The comparison is exact on the resolved identity, and an empty list allows nothing. Failure is a 403 naming the requested model, before the response cache and before any account is chosen |
| Quota | Types: requests, tokens and cost, over a UTC day, month or all time. Stored in the `kv` scope `rllmKeyLimits` under the key's id, so a migrated database is unchanged. Enforced on chat, media, search and fetch before dispatch; a refused call is a 429 `insufficient_quota` naming what is left and when the window resets |

- Usage comes from the analytics rollups (`rollupDaily` is never pruned, and sums one key's day, month or all time) plus an in-memory delta for rows still queued in the writer.
- Checked before dispatch and before the response cache; the row is counted when it is enqueued. A request the provider never served (a row at 400 or above) does not count.
- The limit is soft: a burst can overshoot by the requests in flight, and a queued row can be counted twice or missed for at most one flush.
- When analytics cannot be read, a budgeted key fails closed with a 503 and the failure is logged; an unavailable budget must not spend blind.

---

## Remaining work
**Steps 2.2 and 2.3 are built.**

- [x] The model list filtered by the allow-list ( `app/gateway/catalog`: each entry resolved through the resolver, exact comparison); reuses that matching for the per-request check
- [x] The allow-list is **stored** from the key API (`app/access/auth.ValidateAccess`, 200 entries of 256 characters, normalized) and read by the per-request check below
- [x] Per-key model allow-list, enforced per request before the response cache (`routing.NewAllow`; the pipeline and the catalog share it). A combo is checked by name; member re-checks land with 2.4, which is when members are expanded
- [x] Per-key quota: request, token and cost budgets, the UTC day, month and total windows, the 429 `insufficient_quota`, and the key API field that sets them

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 25 | Per-key request, token and cost budgets over a UTC day, month or all time, refused before dispatch with a 429 and the remaining amount. The reference has no per-key budget. **Built** | **New feature** |
| 334 | A budget whose usage cannot be read fails closed with a 503; a served request alone spends, so a failed call leaves the next caller's budget intact. **Built** | A budget that spends blind is not a budget; the reference has none, so there is nothing to compare (trade-off) |

---

## Findings

| Finding | Decision |
|---|---|
| A budget must not be spent by a failed call. | The reader counts served requests (rollups minus errors) and the writer's delta adds tokens and cost for every row, matching the rollup; a request the provider never served does not spend. |
| A budget that cannot be read fails closed. | A failed usage read answers 503 rather than spending unbounded; the alternative was chosen against, because a bill has no counterpart to an outage. |
