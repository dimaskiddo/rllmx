# Router-LLM — Divergences from the Reference

Deliberate differences from 9router. **The detail lives in each design doc's `Divergences` section**, beside the area it describes; this file is only the index.

Each row carries a stable identifier, what we do differently and why it is better, including the reference mechanism it replaces. The reference version is pinned in [`PARITY.md`](PARITY.md); framework choices are not divergences ([`ARCHITECTURE.md`](ARCHITECTURE.md)).

**Identifiers never change,** because other documents cite them. Add a row to the design document of the area, then update the count here.

| Design doc | Area | Rows | Link |
|---|---|---|---|
| 1 | HTTP Server & Routing | 14 | [01-…](design/01-HTTP-Server-and-Routing.md) |
| 2 | Authentication & Access | 37 | [02-…](design/02-Authentication-and-Access.md) |
| 3 | Request Pipeline | 7 | [03-…](design/03-Request-Pipeline.md) |
| 4 | Model Resolution & Combos | 29 | [04-…](design/04-Model-Resolution-and-Combos.md) |
| 5 | Account Selection, Error Rules & Backoff | 5 | [05-…](design/05-Account-Selection-and-Backoff.md) |
| 6 | OAuth & Token Refresh | 33 | [06-…](design/06-OAuth-and-Token-Refresh.md) |
| 7 | Format Translation | 29 | [07-…](design/07-Format-Translation.md) |
| 8 | Executors | 60 | [08-…](design/08-Executors.md) |
| 9 | Streaming | 10 | [09-…](design/09-Streaming.md) |
| 10 | Token Savers | 9 | [10-…](design/10-Token-Savers.md) |
| 11 | Skills, Session & Auto Plan | 32 | [11-…](design/11-Skills-Session-and-Auto-Plan.md) |
| 12 | Health, Circuit Breaker & Probe | 10 | [12-…](design/12-Health-Circuit-Breaker-and-Probe.md) |
| 13 | Media Endpoints | 33 | [13-…](design/13-Media-Endpoints.md) |
| 14 | Outbound Network | 12 | [14-…](design/14-Outbound-Network.md) |
| 15 | Storage | 12 | [15-…](design/15-Storage.md) |
| 16 | Usage, Pricing & Quota | 41 | [16-…](design/16-Usage-Pricing-and-Quota.md) |
| 17 | Configuration | 6 | [17-…](design/17-Configuration.md) |
| 18 | Web Dashboard | 24 | [18-…](design/18-Web-Dashboard.md) |
| 19 | Go Interfaces | 0 | [19-…](design/19-Go-Interfaces.md) |
| 20 | Cache | 5 | [20-…](design/20-Cache.md) |
| 21 | API Key Limits | 4 | [21-…](design/21-API-Key-Limits.md) |
| 22 | Deferred & Out of Scope | 1 | [22-…](design/22-Deferred-and-Out-of-Scope.md) |
| 23 | CLI | 2 | [23-…](design/23-CLI.md) |
| **Total** | | **415** | |

## Kinds

Each row is counted once, by the marker in its Why column.

| Kind | Meaning | Rows |
|---|---|---|
| Improvement | Differs from the reference for a stated reason | 346 |
| New Feature | Something the reference does not have | 4 |
| Trade-Off | A deliberate cost | 17 |
| Scope Cut | A reference feature not adopted | 24 |
| Planned | Resolved by a named later change | 3 |
| Temporary | Stopgap until a named later change | 5 |
| Retired | A framework choice or a rule that no longer applies, or a behavior now the reference's own; the identifier stays citable | 16 |
| Built As Planned | Was only a schedule change and is now done | 0 |
| **Total** | | **415** |

Each row names its own identifier; the design doc's `Divergences` table is the list.

How a row is counted:
- One row is one entry, even when its # cell lists several identifiers; that is how a rule and its mechanical companion stay together.
- The marker in the row decides its kind. A row that is both a scope cut and marked planned counts as a scope cut.
- 16 identifiers are used by two documents for different rows; the identifiers stay as they are because other docs cite them. They are listed below so no count silently merges two rows.

## Identifier collisions

| Identifier | Documents |
|---|---|
| 350 | 02, 04 |
| 351 | 01, 02 |
| 401, 403, 404, 405, 406, 407, 408, 409, 410 | 11, 18 |
| 402 | 04, 11, 18 |
| 425 | 06, 18 |
| 426, 427 | 02, 18 |
| 428 | 10, 18 |

A divergence in a `done` design document is built and tested; in a `todo` one it is a decision taken, not yet code.
