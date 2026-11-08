# Router-LLM - Divergences from the References

Deliberate differences from 9router and 9router-extended. **The detail lives in each PRD's `Divergences` section**, beside the area it describes; this file is only the index.

Each row carries the number, what we do differently and why it is better, including the reference mechanism it replaces. Reference versions are pinned in `docs/PARITY.md`; framework choices are not divergences (`ARCHITECTURE.md`).

**Numbers never change** — other docs cite them. Next free number: **227**. Add a row to the PRD of the area, then update the count here.

| PRD | Area | Numbers | Link |
|---|---|---|---|
| 1 | HTTP Server & Routing | 10 | [01-…](prds/01-HTTP-Server-and-Routing.md) |
| 2 | Authentication & Access | 38 | [02-…](prds/02-Authentication-and-Access.md) |
| 3 | Request Pipeline | 6 | [03-…](prds/03-Request-Pipeline.md) |
| 4 | Model Resolution & Combos | 14 | [04-…](prds/04-Model-Resolution-and-Combos.md) |
| 5 | Account Selection, Error Rules & Backoff | 4 | [05-…](prds/05-Account-Selection-and-Backoff.md) |
| 6 | OAuth & Token Refresh | 14 | [06-…](prds/06-OAuth-and-Token-Refresh.md) |
| 7 | Format Translation | 27 | [07-…](prds/07-Format-Translation.md) |
| 8 | Executors | 21 | [08-…](prds/08-Executors.md) |
| 9 | Streaming | 12 | [09-…](prds/09-Streaming.md) |
| 10 | Token Savers | 2 | [10-…](prds/10-Token-Savers.md) |
| 11 | Skills, Session & Auto Plan | 7 | [11-…](prds/11-Skills-Session-and-Auto-Plan.md) |
| 12 | Health, Circuit Breaker & Probe | 2 | [12-…](prds/12-Health-Circuit-Breaker-and-Probe.md) |
| 13 | Media Endpoints | 3 | [13-…](prds/13-Media-Endpoints.md) |
| 14 | Outbound Network | 12 | [14-…](prds/14-Outbound-Network.md) |
| 15 | Storage | 19 | [15-…](prds/15-Storage.md) |
| 16 | Usage, Pricing & Quota | 14 | [16-…](prds/16-Usage-Pricing-and-Quota.md) |
| 17 | Configuration | 8 | [17-…](prds/17-Configuration.md) |
| 20 | Cache | 5 | [20-…](prds/20-Cache.md) |
| 21 | API Key Limits | 1 | [21-…](prds/21-API-Key-Limits.md) |
| 22 | Deferred & Out of Scope | 3 | [22-…](prds/22-Deferred-and-Out-of-Scope.md) |
| 23 | CLI | 4 | [23-…](prds/23-CLI.md) |
| **Total** | | **226** | |

**Kinds**, as each row's Why column says: *improvement* fixes a reference defect. *trade-off* is a deliberate cost. *scope cut* is a reference feature not adopted. *planned* and *temporary* name the step that resolves them. Numbers 196-199 and 201 are retired: the framework choice they recorded stays citable.

**Tally:** 226 numbers — 192 improvements, 5 trade-offs, 16 scope cuts, 4 planned, 3 temporary, 5 retired.

A divergence in a `done` PRD is built and tested; in a `todo` PRD it is a decision taken, not yet code.
