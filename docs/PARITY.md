# Router-LLM - Reference Parity

How closely Router-LLM follows its references. References read-only, never copied (`AGENTS.md`, Clean-Room Rule). Update matching rows with the feature; run the Parity Sync playbook in `docs/WORKFLOWS.md` when a reference moves.

---

## 1. Pinned References

| Reference | Path | Version | Commit | Commit date | Role |
|---|---|---|---|---|---|
| 9router | `references/9router` | 0.5.116 | `1339107e` | 2026-11-08 | Baseline behavior |
| 9router-extended | `references/9router-extended` | 0.5.116-extended | `8b06003a` | 2026-11-11 | Extended additions only |

`../rllm` is a second reference, like 9router: an earlier Go implementation written without a framework. Read it for behavior only — its code, package layout, stdlib choices and filenames are never carried over (Clean-Room Rule), and rllmx's shape follows `AGENTS.md`, not that repo. Behavior and `docs/DIVERGENCES.md`: judged against 9router and 9router-extended only.

9router-extended is based on the same upstream release as the baseline: on disagreement the baseline wins, and only the fork's own additions are adopted.

---

## 2. Where Things Are

Status changes go in the topic file holding the row; pins and status values stay here. The sync log and the unverified values are working records: see `TASKS.md`, Sync log and Unverified values.

| File | Holds |
|---|---|
| `prds/` | One PRD per area: status, steps, design, parity tables, findings |
| `DIVERGENCES.md` | Index of divergence numbers; rows live in each PRD |

---

## 3. Status Values

| Status | Meaning |
|---|---|
| `todo` | Planned for v1, not started |
| `partial` | Started, some behavior missing |
| `done` | Implemented, tested, documented |
| `need user validation` | Built and green on the agent's runs; a claim in it can only be settled by the user's run |
| `diverged` | On purpose differently; see the PRD's `Divergences` section |
| `deferred` | Not in v1, may come later |
| `out` | Excluded for good (needs a spawned process other than the filter-gated `git clone`, outside scope) |

---

## 4. History

Unverified reference values, the sync log (one table row per step) and every move of a pin are working records, kept apart from this file: see `TASKS.md`, Sync log. A change to a pin in section 1 is recorded there in the same change.
