# Router-LLM - Divergences from the References

Deliberate differences from 9router and 9router-extended. **The detail lives in each PRD's `Divergences` section**, beside the area it describes; this file is only the index.

Each row carries the number, what we do differently and why it is better, including the reference mechanism it replaces. Reference versions are pinned in `docs/PARITY.md`; framework choices are not divergences (`ARCHITECTURE.md`).

**Numbers never change** — other docs cite them. Next free number: **252**. Add a row to the PRD of the area, then update the count here.

| PRD | Area | Numbers | Link |
|---|---|---|---|
| 1 | HTTP Server & Routing | 10 | [01-…](prds/01-HTTP-Server-and-Routing.md) |
| 2 | Authentication & Access | 40 | [02-…](prds/02-Authentication-and-Access.md) |
| 3 | Request Pipeline | 6 | [03-…](prds/03-Request-Pipeline.md) |
| 4 | Model Resolution & Combos | 14 | [04-…](prds/04-Model-Resolution-and-Combos.md) |
| 5 | Account Selection, Error Rules & Backoff | 4 | [05-…](prds/05-Account-Selection-and-Backoff.md) |
| 6 | OAuth & Token Refresh | 25 | [06-…](prds/06-OAuth-and-Token-Refresh.md) |
| 7 | Format Translation | 28 | [07-…](prds/07-Format-Translation.md) |
| 8 | Executors | 30 | [08-…](prds/08-Executors.md) |
| 9 | Streaming | 13 | [09-…](prds/09-Streaming.md) |
| 10 | Token Savers | 2 | [10-…](prds/10-Token-Savers.md) |
| 11 | Skills, Session & Auto Plan | 7 | [11-…](prds/11-Skills-Session-and-Auto-Plan.md) |
| 12 | Health, Circuit Breaker & Probe | 2 | [12-…](prds/12-Health-Circuit-Breaker-and-Probe.md) |
| 13 | Media Endpoints | 3 | [13-…](prds/13-Media-Endpoints.md) |
| 14 | Outbound Network | 13 | [14-…](prds/14-Outbound-Network.md) |
| 15 | Storage | 19 | [15-…](prds/15-Storage.md) |
| 16 | Usage, Pricing & Quota | 14 | [16-…](prds/16-Usage-Pricing-and-Quota.md) |
| 17 | Configuration | 8 | [17-…](prds/17-Configuration.md) |
| 20 | Cache | 5 | [20-…](prds/20-Cache.md) |
| 21 | API Key Limits | 1 | [21-…](prds/21-API-Key-Limits.md) |
| 22 | Deferred & Out of Scope | 3 | [22-…](prds/22-Deferred-and-Out-of-Scope.md) |
| 23 | CLI | 4 | [23-…](prds/23-CLI.md) |
| **Total** | | **251** | |

## Kinds

Each row's Why column says which kind it is. Every number is counted once, by the marker in its row.

| Kind | Meaning | Count | Numbers |
|---|---|---|---|
| Improvement | Fixes a reference defect | 208 | Every row with no other marker |
| New feature | Something neither reference has | 3 | 24, 32, 148 |
| Trade-off | A deliberate cost | 5 | 33, 81, 91, 138, 141 |
| Scope cut | A reference feature not adopted | 17 | 3, 6, 9, 10, 11, 16, 48, 89, 105, 158, 159, 160, 161, 162, 202, 214, 250 |
| Planned | Names the step that resolves it | 5 | 7, 8, 25, 66, 102 |
| Temporary | Stopgap until a named step | 2 | 93, 104 |
| Retired | A framework choice or a rule that no longer applies, or a behavior now the reference's own; the number stays citable | 10 | 193, 196, 197, 198, 199, 201, 222, 236, 244, 245 |
| Built as planned | Was only a schedule change and is now done | 1 | 216 |
| **Total** | | **251** | |

How a row is counted:
- A row with no marker is an improvement. A row marked *partial* is one too, and so are 112-114 and 118, where only a sub-point waits for a later step.
- A row that is both a scope cut and marked planned (3, 11, 159, 160) counts as a scope cut.
- 25 is a new feature planned for 2.3, and counts as planned.
- Recount from the PRD rows when a row is added or its marker changes.

A divergence in a `done` PRD is built and tested; in a `todo` PRD it is a decision taken, not yet code.
