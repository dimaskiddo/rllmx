# Design 22 — Deferred & Out of Scope

**Status:** n/a

| Item | Status | Reason |
|---|---|---|
| PXPIPE | Deferred | Lazily installed external module; add later behind an interface |
| MITM, Cloudflare and Tailscale tunnels, relay deployers | Deferred | Interception and public exposure; outside the router's job for v1 |
| Outbound proxy, proxy pools, per-connection proxies | Out | The router dials direct; nothing is built, stored or read for them (divergence 214) |
| OTLP push, the Goravel `Telemetry` facade | Out | Metrics are our own OpenMetrics text at `GET /metrics`, pulled by the scraper; nothing is pushed, and no otel SDK becomes a direct dependency (`docs/METRICS.md`, doc 16) |
| Browser-automation web login, `playwright-core`, `wreq-js` | Deferred | `playwright-core` drives Chromium for gemini-web (every request), chatgpt-web's Turnstile fallback, muse-web, the web-login button and the extended fork's browser pool and Obscura. `wreq-js` imitates a TLS ClientHello: OpenCode Free, built here as the suite set on the guarded transport (D269), and grok-web, deferred. Both are reached by a run-time `npm install`, a spawned process; the browser-driven providers stay deferred. chatgpt-web's plain-HTTP image path, uc-web and veoaifree-web are built from their media models (D322) |
| MiniMax lease broker | Deferred | Reads another desktop app's data; revisit later |
| Legacy `db.json`/`usage.json` import | Out | Only 9router databases that already migrated are supported |
| Cloud sync | Out | Removed from the reference as well |
| Install commands stored in skills, autostart, the Hermes process manager and installer, devin-cli, headroom installer | Out | Spawn processes; only the `git clone` behind the `app/git` filter gate may |
| Self-update (`rllmx artisan upgrade`, the About page's admin action) | In, bounded | Downloads this repository's release for the platform, verifies the published checksum, swaps its own binary and re-executes it with a fixed argv; no shell, no child command, no process manager. A `dev` build refuses (D454) |
| Kiro's AWS-signed API key import and MiMo's key/session import | Deferred | Neither has a connection shape here: a Kiro API key needs AWS signing at every call, and the MiMo key is usually refused while the session path exists |
| Translator playground | Out | Removed at the operator's request (4.22); the console-log routes under `/api/translator` stay |
| System tray, TUI | Out | Not a server concern |
| The reference's "REA allow" saver (drops the system-prompt paragraphs that carry an assistant's refusal and dual-use guidance) | In, at the operator's explicit request | Built as the `rea-allow` skill, off by default, and switching it on asks for confirmation; the dialog and doc 11 record what it removes (D455) |

Package status lives in `ROADMAP.md`, feature status in the parity tables.

---

## Parity: Deferred and Out Routes

| Endpoint | Status |
|---|---|
| `/api/settings/proxy-test`, `/api/proxy-pools/*` (CRUD, test, Vercel, Netlify, Deno, Cloudflare deploys) | deferred |
| `/api/cli-tools/*` config writers | deferred |
| `/api/pxpipe/*`, `/api/tunnel/*` (tailscale), `/api/mcp/{plugin}/sse`, `/message`, `/api/providers/web-login` | deferred |
| `/api/headroom/{start,stop,restart,update,auto-setup}`, `/api/skills/install`, `/api/autostart`, `/api/plugins/hermes/*` | out |
| `/api/headroom/{status,detect-port,extras}`, `/api/headroom/proxy/*` | out (the reverse proxy to a spawned process; nothing is spawned, so nothing is proxied) |
| `/api/oauth/iflow/cookie` | out (iflow is not built, D279) |
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

