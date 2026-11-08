# Router-LLM - PRDs

One file per area. Each carries its status, the steps that deliver it, the design, and a **Findings** section recording what was learned while building it.

**Read only the PRD your task touches.** That is the point of the split: a storage task reads `15-Storage.md` and nothing else.

| # | Area | Status | Steps |
|---|---|---|---|
| 01 | [HTTP Server & Routing](01-HTTP-Server-and-Routing.md) | partial | 1.0, 1.5 |
| 02 | [Authentication & Access](02-Authentication-and-Access.md) | partial | 1.6 |
| 03 | [Request Pipeline](03-Request-Pipeline.md) | todo | 1.8 |
| 04 | [Model Resolution & Combos](04-Model-Resolution-and-Combos.md) | partial | 1.7, 2.4 |
| 05 | [Account Selection, Error Rules & Backoff](05-Account-Selection-and-Backoff.md) | partial | 1.7 |
| 06 | [OAuth & Token Refresh](06-OAuth-and-Token-Refresh.md) | todo | 1.9 |
| 07 | [Format Translation](07-Format-Translation.md) | partial | 1.8, 1.10 |
| 08 | [Executors](08-Executors.md) | todo | 1.10 |
| 09 | [Streaming](09-Streaming.md) | partial | 1.0, 1.8, 2.4 |
| 10 | [Token Savers](10-Token-Savers.md) | todo | 3.1 |
| 11 | [Skills, Session & Auto Plan](11-Skills-Session-and-Auto-Plan.md) | todo | 3.2 to 3.4 |
| 12 | [Health, Circuit Breaker & Probe](12-Health-Circuit-Breaker-and-Probe.md) | todo | 2.5 |
| 13 | [Media Endpoints](13-Media-Endpoints.md) | todo | 1.11 |
| 14 | [Outbound Network](14-Outbound-Network.md) | partial | 1.5, 1.10 |
| 15 | [Storage](15-Storage.md) | partial | 1.4, 1.7, 1.8 |
| 16 | [Usage, Pricing & Quota](16-Usage-Pricing-and-Quota.md) | partial | 1.4, 1.8, 2.3 |
| 17 | [Configuration](17-Configuration.md) | partial | 1.0, 1.3, 1.5, 1.6, 1.8 |
| 18 | [Web Dashboard](18-Web-Dashboard.md) | todo | Phase 4 |
| 19 | [Go Interfaces](19-Go-Interfaces.md) | partial | 1.0 to 1.10 |
| 20 | [Cache](20-Cache.md) | partial | 1.3, 1.8f, 1.8g |
| 21 | [API Key Limits](21-API-Key-Limits.md) | todo | 2.2, 2.3 |
| 22 | [Deferred & Out of Scope](22-Deferred-and-Out-of-Scope.md) | n/a | — |
| 23 | [CLI](23-CLI.md) | done | 1.0, 1.4, 1.6 |

**Status values** (same as `docs/PARITY.md`): `done` built and tested · `need user validation` built, the user's run is the missing evidence · `partial` some behaviour missing · `todo` not started · `n/a` policy, not a build target.

## Cross-cutting

| File | Holds |
|---|---|
| `../AGENTS.md` | Scope, rules, build and security constraints, the compatibility contract |
| `TASKS.md` | Current step, open items, what you must decide |
| `ARCHITECTURE.md` | Module map, key design decisions, terminal commands |
| `WORKFLOWS.md` | Startup order, operator procedures, the Parity Sync playbook |
| `PARITY.md` | Pinned reference versions, status vocabulary, sync log |
| `DIVERGENCES.md` | Index of divergence numbers; the rows live in each PRD |

## Conventions

- A PRD's **Findings** section is written when its status reaches `done`. It records what was discovered while building: framework behaviour that forced a design, a bug fixed, an approach abandoned. Cite a divergence number when the finding became a divergence; the row goes in that PRD's `Divergences` section and the count in `DIVERGENCES.md` is updated.
- A finding that changes behaviour toward a client is a row in that PRD's `Divergences` section, indexed in `DIVERGENCES.md`, not a note in Findings.
- Section numbers are stable; other docs cite them as `PRD 15` or `prds/15-Storage.md`.
