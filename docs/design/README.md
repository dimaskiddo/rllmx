# Router-LLM — Design Documents

One file per area. Each carries its status, the design, its parity table against the reference, its deliberate differences (**Divergences**), and a **Findings** section for what was learned while building it.

**Read only the design doc your task touches.** That is the point of the split: a storage task reads `15-Storage.md` and nothing else.

| # | Area | Status |
|---|---|---|
| 01 | [HTTP Server & Routing](01-HTTP-Server-and-Routing.md) | partial |
| 02 | [Authentication & Access](02-Authentication-and-Access.md) | partial |
| 03 | [Request Pipeline](03-Request-Pipeline.md) | partial |
| 04 | [Model Resolution & Combos](04-Model-Resolution-and-Combos.md) | partial |
| 05 | [Account Selection, Error Rules & Backoff](05-Account-Selection-and-Backoff.md) | partial |
| 06 | [OAuth & Token Refresh](06-OAuth-and-Token-Refresh.md) | partial |
| 07 | [Format Translation](07-Format-Translation.md) | partial |
| 08 | [Executors](08-Executors.md) | partial |
| 09 | [Streaming](09-Streaming.md) | partial |
| 10 | [Token Savers](10-Token-Savers.md) | todo |
| 11 | [Skills, Session & Auto Plan](11-Skills-Session-and-Auto-Plan.md) | todo |
| 12 | [Health, Circuit Breaker & Probe](12-Health-Circuit-Breaker-and-Probe.md) | partial |
| 13 | [Media Endpoints](13-Media-Endpoints.md) | done |
| 14 | [Outbound Network](14-Outbound-Network.md) | partial |
| 15 | [Storage](15-Storage.md) | partial |
| 16 | [Usage, Pricing & Quota](16-Usage-Pricing-and-Quota.md) | partial |
| 17 | [Configuration](17-Configuration.md) | partial |
| 18 | [Web Dashboard](18-Web-Dashboard.md) | partial |
| 19 | [Go Interfaces](19-Go-Interfaces.md) | partial |
| 20 | [Cache](20-Cache.md) | partial |
| 21 | [API Key Limits](21-API-Key-Limits.md) | done |
| 22 | [Deferred & Out of Scope](22-Deferred-and-Out-of-Scope.md) | n/a |
| 23 | [CLI](23-CLI.md) | done |

**Status values** (same as [`../PARITY.md`](../PARITY.md)): `done` built and tested · `unverified` built, a live account is the missing evidence · `partial` some behavior missing · `todo` not started · `n/a` policy, not a build target.

## Cross-cutting

| File | Holds |
|---|---|
| `../AGENTS.md` | Scope, Go rules (concurrency, memory, security, clean code), Goravel and build constraints, the compatibility contract |
| `../ARCHITECTURE.md` | Module map, key design decisions, approved modules, terminal commands |
| `../WORKFLOWS.md` | Startup order and operator procedures |
| `../PARITY.md` | Pinned reference version, status vocabulary |
| `../DIVERGENCES.md` | Index of divergence identifiers; the rows live in each design doc |

## Conventions

- A design doc's **Findings** section is written when its status reaches `done`. It records what was discovered while building: framework behavior that forced a design, a bug already fixed, an approach abandoned. An open defect, an unverified claim or a carried risk belongs in the private working record, not in Findings. Cite a divergence number when the finding became a divergence; the row goes in that design doc's `Divergences` section and the count in `DIVERGENCES.md` is updated.
- A finding that changes behavior toward a client is a row in that design doc's `Divergences` section, indexed in `DIVERGENCES.md`, not a note in Findings.
- Section numbers are stable; other docs cite them as `design doc 15` or `design/15-Storage.md`.
