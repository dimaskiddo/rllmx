# Design 10 — Token Savers (`app/saver/`)

**Status:** todo

The token saver does not exist. Everything below is the target.

All savers run after translation and **fail open**: an error leaves the body unchanged. The request header `x-9router-token-saver: off` bypasses all of them.

| Saver | Setting | Behavior |
|---|---|---|
| RTK | `rtkEnabled` (on) | In-process filters shrink bulky tool output (git diff/log/status, grep, ls, tree, find,.) |
| Two built-in prompt styles | `cavemanEnabled`/`cavemanLevel`, `ponytailEnabled`/`ponytailLevel` | Inject a built-in system prompt |
| Custom prompt | `customPromptEnabled`, `customPrompt` | Injects the user's own system prompt |
| Headroom | `headroomEnabled`, `headroomUrl`, `headroomTimeoutMs` | HTTP call to an external compression sidecar. The router never installs or starts it |
| PXPIPE | | Deferred |

---

## Parity: Token Savers

| Feature | Reference | Package | Status |
|---|---|---|---|
| RTK filters | `open-sse/rtk/filters/*` | `app/saver` | todo |
| The two built-in prompt styles | the reference's `rtk` prompt files | `app/saver` | todo |
| Custom prompt (baseline) | `open-sse/rtk/customPrompt.js` | `app/saver` | todo |
| Headroom client | `open-sse/rtk/headroom.js` | `app/saver` | todo |
| `x-9router-token-saver: off` bypass header | `open-sse/handlers/chatCore.js` | `app/saver` | todo |
| Sliding-window trim (dead code in the extended fork) | extended `open-sse/rtk/trim.js` | none | out (D9) |
| PXPIPE | `open-sse/rtk/pxpipe.js` | none | deferred |

---

## Remaining work
**Step.** The token saver does not exist.

- [ ] `app/saver` with the savers in this design doc and the parity table below, applied after translation, failing open

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 134 | Kept as entered (spaces trimmed) | Providers with trailing slash validate |
| 9 | Dropped; `tokenSaverEnabled`, `tokenSaverBudget` kept and imported with no behavior | Dead in the reference (scope cut) |

---

## Findings

None recorded.
