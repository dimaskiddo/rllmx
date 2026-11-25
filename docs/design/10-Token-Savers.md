# Design 10 — Token Savers (`app/gateway/saver`)

**Status:** done

The package splits by saver: `rtk` holds the filters and their detector, `headroom` the sidecar client, `caveman` and `ponytail` their own levels and topic, and `prompt` the shared scorer and injector. `saver` itself wires them and holds `Options`, `Stats` and `Apply`.

The savers run on the hub request inside the pipeline's build step, after `translate.Prepare` and the image prefetch, before the codec writes the body. Every format therefore gets them with no per-format code. The body is built once per destination, so an account retry reuses the compacted request.

Every step fails open: a missing setting, a filter that cannot shorten the text, a sidecar that does not answer or an unknown level leaves the request exactly as it was. A request with the header `x-9router-token-saver: off` runs no saver at all.

| Saver | Setting | Behavior |
|---|---|---|
| RTK | `rtkEnabled` (on) | Compacts the text of successful tool results in place: git diff/log/status, grep, find, ls, tree, search lists, build output, line-numbered dumps, duplicate logs, long unstructured blobs |
| Headroom | `headroomEnabled`, `headroomUrl`, `headroomCompressUserMessages`, `headroomTimeoutMs` | Sends tool output, and user text when allowed, to the operator's compression sidecar at `<url>/v1/compress` |
| Caveman | `cavemanEnabled`, `cavemanLevel`, `cavemanRoutingMode` | Appends a terse-answer instruction to the system prompt; the card carries the reference's own name and description |
| Ponytail | `ponytailEnabled`, `ponytailLevel`, `ponytailRoutingMode` | Appends a minimal-code instruction to the system prompt; the card carries the reference's own name and description |
| Custom prompt | `customPromptEnabled`, `customPrompt` | Appends the operator's own text, independent of the two above |
| PXPIPE | `pxpipe*` | Deferred |
| Sliding-window trim | `tokenSaverEnabled`, `tokenSaverBudget` | Stored and imported, no behavior (D9) |

The order is RTK, Headroom, caveman, ponytail, custom prompt. Each prompt is appended to the first system message as its own `"\n\n"`-delimited segment, or the message is created at the top. An existing segment is never added twice, so a rebuilt body cannot double it.

**Activation modes.** `cavemanRoutingMode` and `ponytailRoutingMode` take `always` or `smart`. The defaults are the extended fork's: caveman `always`, ponytail `smart`.

In `smart` mode the latest user text is scored against that saver's own triggers, keywords and description. The instruction is injected only above a threshold: 0.35 for caveman, 0.30 for ponytail. Greetings and bare arithmetic never match, and the scorer reads at most 4 KiB and 400 tokens. A saver that was on but did not match, and RTK with no tool text to compact, are named in the request's feature line as `CAVEMAN skipped`, `PONYTAIL skipped` and `RTK skipped`, so an absent saver is not mistaken for a disabled one; the usage row still lists only the savers that acted.

**RTK details.** A block under 500 bytes or over 10 MiB is passed through, and the detector reads only its first 1 KiB. The most specific shape wins, and a filter's output is accepted only when it is non-empty and strictly shorter than its input. A tool result the client marked as failed is never touched.

Every cap is a package constant: 100 lines per diff hunk and 500 for a whole diff, 200 log lines, 10 matches per file, 10 names per directory and 20 directories, 10 files per status block, 200 tree lines, and 120 head with 60 tail lines for a truncated block.

**Headroom details.** The client posts `{messages, model}` to `<url>/v1/compress`, under a timeout from 100 ms to 60 s (default 3 s), and reads at most 8 MiB. The projection sends every successful tool result, and user text when `headroomCompressUserMessages` is on. The system prompt and any message with more than one part are never sent.

An answer is applied only when the row count, roles and tool call ids all match, and only for a row whose text is non-empty and shorter. One mismatch discards the whole answer.

The sidecar address is the settings row's `headroomUrl`, which is empty until the operator sets it, then the `SAVER_HEADROOM_URL` configuration value, then `http://localhost:8787`. Each distinct host gets a client built through the outbound guard with that host added to the allow list, so a local sidecar needs no `UPSTREAM_ALLOW_PRIVATE` entry.

**Dashboard routes.** `GET /api/headroom/status` answers the configured address and whether it serves `/health` within 1.5 s. `/api/headroom/proxy/*path` forwards GET, POST, PUT, PATCH and DELETE to the sidecar. Both need a dashboard session.

The proxy strips hop-by-hop headers, does not forward `Host`, follows no redirect, and caps the body at 8 MiB. `Cookie`, `Authorization` and `Set-Cookie` never cross it in either direction, the answer is sent under `Content-Security-Policy: sandbox` and `nosniff`, and any user info is stripped from an address before it is shown.

**Response cache.** The key carries a saver fingerprint: the enabled switches, the levels, both activation modes, the user-message flag, and a hash of the custom prompt and the sidecar address. An answer produced with one configuration is never served for another.

---

## Dashboard

`/dashboard/token-saver` writes `rtkEnabled`, `headroomEnabled`, `headroomUrl`, `headroomCompressUserMessages`, `headroomTimeoutMs`, the four `caveman*` and `ponytail*` keys, and `customPromptEnabled` with `customPrompt`. The cards stack in one column, because each body appears and hides and a two-column grid leaves a short card beside a tall one. The level buttons offer lite, full and ultra; a stored `wenyan*` level shows as ultra outside a Chinese locale, and the sidecar's process controls are not offered here (D398).

## Parity: Token Savers

| Feature | Reference | Package | Status |
|---|---|---|---|
| RTK filters (12) | `open-sse/rtk/filters/*` | `saver/rtk` | done |
| Detection and the smaller-only rule | `open-sse/rtk/autodetect.js`, `applyFilter.js` | `saver/rtk` | done |
| The two built-in prompt styles, level and activation mode | the reference's `rtk` prompt files, the extended fork's skill manifests | `saver/caveman`, `saver/ponytail`, `saver/prompt` | done |
| Watermark Remover: the skill's prompt (smart or always) and the strip of invisible characters from every answer | `skills/watermarks-remover`, none for the strip | `app/gateway/skill`, `app/platform/text`, `app/gateway/pipeline` | done (the prompt is the extended fork's own text; the strip is D428) |
| Custom prompt | `open-sse/rtk/customPrompt.js` | `saver`, `saver/prompt` | done |
| Headroom client | `open-sse/rtk/headroom.js` | `saver/headroom` | done |
| `/api/headroom/status`, `/api/headroom/proxy` | `src/app/api/headroom/{status,proxy}` | `app/http/controllers` | done |
| Sidecar process management (`start`, `stop`, `restart`, `detect-port`, `extras`, `auto-setup`, `update`) | `src/app/api/headroom/*`, `src/lib/headroom/process.js` | none | out (D398) |
| Dashboard HTML rewrite inside the proxy | `headroom/proxy/[...path]/route.js` | none | out (D399) |
| `x-9router-token-saver: off` bypass header | `open-sse/handlers/chatCore.js` | `app/gateway/pipeline` | done |
| Sliding-window trim (dead code in the extended fork) | extended `open-sse/rtk/trim.js` | none | out (D9) |
| PXPIPE | `open-sse/rtk/pxpipe.js` | none | deferred |

---

## Remaining work
**Step.** None. A live Headroom sidecar was never called here; the tests stand a fake one in its place, so a real deployment is the missing evidence.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 134 | Kept as entered (spaces trimmed) | Providers with trailing slash validate |
| 9 | Dropped; `tokenSaverEnabled`, `tokenSaverBudget` kept and imported with no behavior | Dead in the reference (scope cut) |
| 395 | `cavemanRoutingMode` and `ponytailRoutingMode` are honored | The extended fork ships the option in its manifests but injects from the enable flags alone, so its "Smart" label does nothing |
| 428 | Watermark Remover also strips the invisible characters from a delivered answer, raw UTF-8 and `\uXXXX` escapes, streaming included | The reference only asks the model not to emit them; asking is not a guarantee, and a client that copies the answer carries the mark either way |
| 396 | Retired: the level texts are the reference's own, kept verbatim | The maintainer directed using the reference texts directly; each package names its source, and an edit tracks the reference |
| 397 | Headroom is sent tool output and, when allowed, user text; the system prompt is never sent | A compression proxy that rewrites the operator's instructions can change behavior in ways no setting explains |
| 398 | No sidecar install, start, stop, restart, port detection, extras or update | Process management is out of scope (`design doc 22`); the operator runs the sidecar |
| 399 | The proxy does not rewrite the sidecar's dashboard HTML | The sidecar's own UI is not part of v1, and rewriting a third party's HTML invites breakage |
| 418 | The proxy never forwards the dashboard session or an `Authorization` header, drops `Set-Cookie`, and sandboxes what the sidecar sends | A sidecar page is served on the dashboard's origin: a process that took the port could have read the session and run script as the administrator |

---

## Findings

| Finding | Decision |
|---|---|
| `tools/mutant.sh` marks the case-insensitive bypass, inject-once, the failed-tool-result skip, the minimum size, the UTF-8 head cut, the cache fingerprint, the sidecar row identity, the no-grow rule, the proxy credential rule and the loopback rule CAUGHT | pinned by tests |
| Three designed mutants are equivalent and stay MISSED: the arithmetic skip (an all-digit query tokenizes to nothing), the trigger boost (a trigger's own words are already in the document vector) and the segment separator (every built-in prompt is one line) | accepted, no behavior to pin |
| The extended fork's `routing_mode` is honored here and ignored there (D395) | fixed |
| The settings row seeded `headroomUrl` with the localhost default, so `SAVER_HEADROOM_URL` was never read on a fresh install | the seed is empty |
| The response key left out whether caveman and ponytail were on, so toggling one served the old style for the cache lifetime | both switches are in the key |
| The scorer was a copy of the skill scorer (D400) | one package, `app/platform/relevance`; the 24 smart verdicts are pinned |
| The caveman and ponytail level texts are the reference's own, with their source named in each package (D396 retired) | kept verbatim; an edit tracks the reference |
