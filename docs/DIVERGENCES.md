# Router-LLM — Divergences from the Reference

Deliberate differences from 9router. **The detail lives in each design doc's `Divergences` section**, beside the area it describes; this file is only the index.

Each row carries a stable identifier, what we do differently and why it is better, including the reference mechanism it replaces. The reference version is pinned in [`PARITY.md`](PARITY.md); framework choices are not divergences ([`ARCHITECTURE.md`](ARCHITECTURE.md)).

**Identifiers never change,** because other documents cite them. Add a row to the design document of the area, then update the count here.

| Design doc | Area | Identifiers | Link |
|---|---|---|---|
| 1 | HTTP Server & Routing | 15 | [01-…](design/01-HTTP-Server-and-Routing.md) |
| 2 | Authentication & Access | 53 | [02-…](design/02-Authentication-and-Access.md) |
| 3 | Request Pipeline | 7 | [03-…](design/03-Request-Pipeline.md) |
| 4 | Model Resolution & Combos | 24 | [04-…](design/04-Model-Resolution-and-Combos.md) |
| 5 | Account Selection, Error Rules & Backoff | 5 | [05-…](design/05-Account-Selection-and-Backoff.md) |
| 6 | OAuth & Token Refresh | 35 | [06-…](design/06-OAuth-and-Token-Refresh.md) |
| 7 | Format Translation | 36 | [07-…](design/07-Format-Translation.md) |
| 8 | Executors | 63 | [08-…](design/08-Executors.md) |
| 9 | Streaming | 14 | [09-…](design/09-Streaming.md) |
| 10 | Token Savers | 9 | [10-…](design/10-Token-Savers.md) |
| 11 | Skills, Session & Auto Plan | 32 | [11-…](design/11-Skills-Session-and-Auto-Plan.md) |
| 12 | Health, Circuit Breaker & Probe | 10 | [12-…](design/12-Health-Circuit-Breaker-and-Probe.md) |
| 13 | Media Endpoints | 33 | [13-…](design/13-Media-Endpoints.md) |
| 14 | Outbound Network | 15 | [14-…](design/14-Outbound-Network.md) |
| 15 | Storage | 19 | [15-…](design/15-Storage.md) |
| 16 | Usage, Pricing & Quota | 25 | [16-…](design/16-Usage-Pricing-and-Quota.md) |
| 17 | Configuration | 8 | [17-…](design/17-Configuration.md) |
| 18 | Web Dashboard | 20 | [18-…](design/18-Web-Dashboard.md) |
| 19 | Go Interfaces | 0 | [19-…](design/19-Go-Interfaces.md) |
| 20 | Cache | 5 | [20-…](design/20-Cache.md) |
| 21 | API Key Limits | 4 | [21-…](design/21-API-Key-Limits.md) |
| 22 | Deferred & Out of Scope | 3 | [22-…](design/22-Deferred-and-Out-of-Scope.md) |
| 23 | CLI | 4 | [23-…](design/23-CLI.md) |
| **Total** | | **460** | |

## Kinds

Each row's Why column says which kind it is. Every number is counted once, by the marker in its row.

| Kind | Meaning | Count | Numbers |
|---|---|---|---|
| Improvement | Differs from the reference for a stated reason | 370 | Every row with no other marker |
| New feature | Something the reference does not have | 9 | 24, 32, 148, 284, 310, 336, 25, 350, 351 |
| Trade-off | A deliberate cost | 21 | 33, 81, 91, 138, 141, 286, 287, 289, 290, 317, 332, 334, 358, 360, 383, 385, 387, 400, 410, 415 |
| Scope cut | A reference feature not adopted | 37 | 3, 6, 9, 10, 11, 16, 48, 89, 105, 158, 159, 160, 161, 162, 202, 214, 250, 256, 260, 273, 278, 279, 320, 322, 346, 347, 359, 366, 372, 373, 379, 380, 384, 398, 399 |
| Planned | Resolved by a named later change | 4 | 25, 66, 102, 321 |
| Temporary | Stopgap until a named later change | 3 | 93, 104, 325 |
| Retired | A framework choice or a rule that no longer applies, or a behavior now the reference's own; the identifier stays citable | 15 | 193, 196, 197, 198, 199, 201, 222, 236, 244, 245, 277, 325, 337, 396, 426 |
| Built as planned | Was only a schedule change and is now done | 1 | 216 |
| **Total** | | **460** | |

How a row is counted:
- A row with no marker is an improvement. A row marked *partial* is one too, and so are 112-114 and 118, where only a sub-point waits for later work.
- A row that is both a scope cut and marked planned (3, 11) counts as a scope cut.
- 25 is a new feature planned, and counts as planned.
- Recount from the design doc rows when a row is added or its marker changes.

A divergence in a `done` design document is built and tested; in a `todo` one it is a decision taken, not yet code.
