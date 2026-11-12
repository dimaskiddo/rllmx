# Router-LLM - Reference Parity

How closely Router-LLM follows 9router. The reference is read-only and never copied (`AGENTS.md`, Clean-Room Rule). Update the matching row with the feature.

---

## 1. Pinned Reference

| Reference | Version | Commit | Commit date | Role |
|---|---|---|---|---|
| 9router | 0.5.116 | `1339107e` | 2026-11-08 | Behavior baseline |

The project also tracks a private extended fork built on the same upstream release. On disagreement the baseline wins, and only the fork's own additions are adopted.

---

## 2. Where Things Are

Status changes go in the document holding the row; pins and status values stay here.

| File | Holds |
|---|---|
| [`design/`](design/) | One design document per area: status, design, parity tables, findings |
| [`DIVERGENCES.md`](DIVERGENCES.md) | Index of divergence identifiers; rows live in each design document |

---

## 3. Status Values

| Status | Meaning |
|---|---|
| `todo` | Planned for v1, not started |
| `partial` | Started, some behavior missing |
| `done` | Implemented, tested, documented |
| `unverified` | Built and tested against fakes; a live account is the missing evidence |
| `diverged` | On purpose differently; see the design document's `Divergences` section |
| `deferred` | Not in v1, may come later |
| `out` | Excluded for good (needs a spawned process other than the filter-gated `git clone`, outside scope) |

---

## 4. History

A change to the pin in section 1 is recorded in the same change.
