# PRD 22 — Deferred & Out of Scope

**Status:** n/a · **Steps:** —

| Item | Status | Reason |
|---|---|---|
| PXPIPE | Deferred | Lazily installed external module; add later behind an interface |
| MITM, Cloudflare and Tailscale tunnels, relay deployers | Deferred | Interception and public exposure; outside the router's job for v1 |
| Outbound proxy, proxy pools, per-connection proxies | Out | The router dials direct; nothing is built, stored or read for them (divergence 214) |
| Browser-automation web login, TLS-fingerprint clients | Deferred | Needs a browser or non-stdlib TLS stack; cookie paste covers the simple providers |
| MiniMax lease broker | Deferred | Reads another desktop app's data; revisit later |
| Legacy `db.json`/`usage.json` import | Out | Only 9router databases that already migrated are supported |
| Cloud sync | Out | Removed from the reference as well |
| Install commands stored in skills, self-updater, autostart, Hermes manager, devin-cli, headroom installer | Out | Spawn processes; only the `git clone` behind the `app/git` filter gate may |
| System tray, TUI | Out | Not a server concern |

Package status lives in `TASKS.md`, feature status in the parity files.

---

## Parity: Deferred and Out Routes

| Endpoint | Status |
|---|---|
| `/api/settings/proxy-test`, `/api/proxy-pools/*` (CRUD, test, Vercel, Netlify, Deno, Cloudflare deploys) | deferred |
| `/api/cli-tools/*` config writers | deferred |
| `/api/pxpipe/*`, `/api/tunnel/*` (tailscale), `/api/mcp/{plugin}/sse`, `/message`, `/api/providers/web-login` | deferred |
| `/api/headroom/{start,stop,restart,update,auto-setup}`, `/api/skills/install`, `/api/autostart`, `/api/plugins/hermes/*` | out |
| `/api/tunnel/update`, `/api/pxpipe/update`, `/api/plugins/update-check` | out |

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 16, 48, 89 | Not provided; no outbound call, `latestVersion` null; proxy fields ignored, untouched | Out of scope; no proxy feature (scope cut) |

---

## Findings

A policy list, not a build target.
