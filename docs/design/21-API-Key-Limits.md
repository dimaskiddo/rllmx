# Design 21 — API Key Limits (`app/access/`)

**Status:** done

The model list and the quota are built. The `kv` scope they read is defined in `design doc 15`.

| Limit | Behavior |
|---|---|
| Model | `apiKeys.accessRestricted` and `accessAllow`, enforced. A restricted key may call only the combos and resolved `provider/model` pairs listed; a media request is checked on its resolved identity and a search, fetch, poll or voice list on its provider. The comparison is exact on the resolved identity, and an empty list allows nothing. Failure is a 403 naming the requested model, before the response cache and before any account is chosen |
| Quota | Types: requests, tokens and cost, over a day, month or all time in the server's own zone (`TZ`). Stored in the `kv` scope `rllmKeyLimits` under the key's id, so a migrated database is unchanged. Enforced on chat, media, search and fetch before dispatch; a refused call is a 429 `insufficient_quota` naming what is left and when the window resets |

- Usage comes from the analytics rollups (`rollupDaily` is never pruned and answers whole UTC days; a window that starts elsewhere reads the hourly rollups from its first hour, so a window that begins inside an hour counts that whole hour) plus an in-memory delta for rows still queued in the writer, held to the same rule as the rollups.
- Checked before dispatch and before the response cache; the row is counted when it is enqueued, and the queued delta follows the stored rule: one request only for a row at 200–399, tokens and cost for every row. A request the provider never served (a row at 400 or above) does not count. A stream the client left after tokens flowed was served: its row keeps a countable status and the reason in its error class.
- The limit is soft: a burst can overshoot by the requests in flight, and a queued row can be counted twice or missed for at most one flush.
- When analytics cannot be read, a budgeted key fails closed with a 503, class `budget_unavailable` in the row, and the failure is logged; an unavailable budget must not spend blind. A request whose context ends during the read is the client's cancel (499) or the call's own deadline (504), never a 503.
- A client tool that asks how much it has used is answered from this budget, and a key with one has its standing on every answer (D436). `Quota.Standing` reads the largest share spent of any limited type in each window. It never reads over 100, and a key with no budget costs no usage query. `GET /api/oauth/usage` and `GET /v1/organizations/{org}/usage` answer Claude's `five_hour` (the day) and `seven_day` (the month) with `utilization` and `resets_at`; `GET /v1/usage` answers OpenCode's `usage.rolling` and `usage.monthly` with `percent` and `resetsAt`. They are client routes behind the key check. The chat controller sets `anthropic-ratelimit-unified-status` (`allowed`, or `rejected` at 100) and `-reset` (Unix seconds) before the pipeline runs, only for a key with a budget; a budget read that fails leaves the headers off and never stops the answer.

---

## Remaining work
**Steps 2.2 and 2.3 are built.**

- [x] The model list filtered by the allow-list ( `app/gateway/catalog`: each entry resolved through the resolver, exact comparison); reuses that matching for the per-request check
- [x] The allow-list is **stored** from the key API (`app/access/auth.ValidateAccess`, 200 entries of 256 characters, normalized) and read by the per-request check below
- [x] Per-key model allow-list, enforced per request before the response cache (`routing.NewAllow`; the pipeline and the catalog share it). A combo is checked by name; member re-checks land with 2.4, which is when members are expanded
- [x] Per-key quota: request, token and cost budgets, the day, month and total windows in the server's zone, the 429 `insufficient_quota`, and the key API field that sets them

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 25 | Per-key request, token and cost budgets over a day, month or all time in the server's zone, refused before dispatch with a 429 and the remaining amount. The reference has no per-key budget. **Built** | **New feature** |
| 392 | A queued row is counted against its key before it is sent to the writer, and taken back if the queue refuses it. **(built)** | Counting after the send let the flusher write and remove the row first, leaving a count that no flush ever took off, so a key's budget shrank for the life of the process |
| 334 | A budget whose usage cannot be read fails closed with a 503; a served request alone spends, so a failed call leaves the next caller's budget intact. **Built** | A budget that spends blind is not a budget; the reference has none, so there is nothing to compare (trade-off) |
| 436 | A client tool that asks how much it has used is answered from its key's own budget: `GET /api/oauth/usage` and `GET /v1/organizations/{org}/usage` in Claude's `five_hour`/`seven_day` shape (the day is the session, the month the week), `GET /v1/usage` in OpenCode's `rolling`/`monthly` shape, and every chat answer for a key with a budget carries `anthropic-ratelimit-unified-status` and `-reset`. The percent is the largest share spent of any limited type, never over 100, and a key with no budget gets `0` and no invented headers. The reference answers none of these itself and only passes an upstream's `anthropic-ratelimit-*` headers through. **(built)** | A key's limit is the router's, not the provider's: the provider's own figure says nothing about when this key will be refused. Claude Code and OpenCode read these to pace themselves, so a true answer stops them running into a 429 they could have seen coming |

---

## Findings

| Finding | Decision |
|---|---|
| A budget must not be spent by a failed call. | The reader counts served requests (rollups minus errors) and the writer's delta counts a request only for a row at 200–399 while adding tokens and cost for every row, matching the rollup; a request the provider never served does not spend, not even while it is queued. |
| A row must be counted before anyone can remove it. | `Writer.enqueue` adds to the pending totals first and subtracts on a refusal; a test parks the flusher inside the flush and asserts the count is already there. |
| A request that was paid for must spend the budget however it ended. | A client that leaves a stream after tokens flowed leaves a row at 200 with class `client_closed`; one that leaves before any token is still an error row. |
| A budget that cannot be read fails closed. | A failed usage read answers 503 `budget_unavailable` rather than spending unbounded, and a read the request abandoned is filed as the cancel or deadline it was; the fail-closed choice was kept because a bill has no counterpart to an outage. |
| 4.44: a refused call's usage row named no model or provider, so its log line was a row of dashes, and the media gateways filed the refusal as the provider's `rate_limit`. | The chat pipeline writes the resolved target into the row before the access and budget checks, as the media pipeline already did; a media budget refusal stores `key_quota` and a failed read `budget_unavailable`, and the log line carries the class beside the code. |
| 4.45: the account cell was a dash, though the operator wanted to see where the request would have gone. | A refused request peeks at the selector: `Peek` makes the same pick as `Select` with no `recordUse`, so the row names the account it would have reached at attempt 0, and no rotation counter or last-used time moves. A combo names no account: it has none until it is walked. |
| 4.69: a budget read that failed answered a bare 503 filed as `unknown`, and one the client abandoned answered 503 instead of 499. | The gate answers `stopped(ctx)` when the request ended and files a genuine read failure as `budget_unavailable`; the media gateways do the same, the refusal staying `key_quota`. The page shows "Budget Unavailable", separate from a spent budget. |
