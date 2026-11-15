# Design 18 — Web Dashboard (`web/`)

**Status:** todo

`web/` holds only the OAuth callback page: `web/embed.go` (`web.Static`, `go:embed static`), `static/callback.html` and `static/js/callback.js`, served by `GET /callback` and `GET /static/js/callback.js` (both public; no inline script, because the page CSP is `default-src 'self'`). The rest of the dashboard is planned in the same folder.

Static files embedded with `go:embed`: one HTML shell per page, hand-written CSS with variables, ES modules, no framework, no bundler, no runtime CDN. A small `ui.js` provides a text-safe element builder, a fetch wrapper, a router, toasts and modals. Pages call the same `/api/*` paths and JSON shapes as the reference dashboard. The page table is below.

| Area | Pages |
|---|---|
| Home | home |
| Providers | list, new, detail (connections, models, overrides, test) |
| Routing | combos (presets, suggestions), endpoint (base URL, API keys, token saver) |
| Media | per-kind provider pages, combos |
| Monitoring | usage, quota, console log |
| Network | token saver |
| Tools | translator playground (shown when the planned `TRANSLATOR_ENABLED` is true), basic chat, skills (Studio) |
| Account | profile, login, setup wizard |
| Settings | pricing, general |
| Extended | routing health, auto plan, skills |

| Rule | Detail |
|---|---|
| i18n | `web/static/i18n/en.json` and `id.json`, key-based; the locale cookie is set by `/api/locale`. A new locale is a new JSON file |
| Escaping | Dynamic text uses `textContent` or escaped attributes, never `innerHTML` from data |
| Secrets | The API masks them and the dashboard never receives raw tokens. The one exception is a deliberate reveal: a signed-in operator asks for one key by id and the answer is logged (D330) |

### Provider Routes (`app/http/controllers`)

All always-protected: a session is needed even when `requireLogin` is off, and cross-site writes are refused.

| Routes | Purpose |
|---|---|
| `GET/POST /api/providers`, `GET/PUT/DELETE /api/providers/{id}` | Connections |
| `GET/PUT /api/providers/{id}/overrides` | Header overrides (design doc 8) |
| `POST /api/providers/{id}/test`, `POST /api/providers/validate`, `GET /api/providers/{id}/models` | Key checks and model lists (design doc 14) |
| `GET/POST /api/provider-nodes`, `PUT/DELETE /api/provider-nodes/{id}`, `POST /api/provider-nodes/validate` | Custom nodes |

| Rule | Detail |
|---|---|
| Output | Everything returned about a connection or node goes through an allow list (`ConnectionView`, `NodeView`). A new stored field stays hidden until listed. A name holding 32 or more key characters shows its first eight plus `***`. A base URL shows without credentials, query or fragment |
| fal key | A fal key is judged by `GET https://api.fal.ai/v1/models?limit=1` with `Authorization: Key …`: the queue host answers a bare 404 before it reads a key, so only the model catalog tells a bad key from a good one |
| Input | Writes accept only listed fields. `modelLock_*`, `backoffLevel`, tokens and the proxy family cannot be written |
| Creating | Only for a registered provider (API-key, free, cookie or `apikey` mode) or an existing node. A provider whose data file has not landed cannot get a new connection from the dashboard; its existing connections are still listed, selected and locked. `design doc 8` lists the ids and their status (all `todo` today) |
| Node connection | Always takes `prefix`, `apiType`, `baseUrl` and `nodeName` from the node |
| Node writes | Stricter than the reference: prefix matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, is not a built-in id or alias, and is unique; base URL is http or https with a host, no credentials, query or fragment, at most 2048 bytes. Create, update (with its connections) and delete (with its connections) are each one transaction. Rows that break these rules still read and resolve |

---

## Parity: Dashboard Pages

Plain HTML, ES modules, CSS; same flows and `/api/*` contract (design doc 18).

| Pages | Status |
|---|---|
| home, providers (list, new, detail), combos, endpoint (base URL, keys, token saver), token saver, login and setup, i18n `en`, `id` | todo |
| usage, quota, console log, media providers (per kind, combos), profile, translator playground, basic chat, pricing settings, skills Studio (extended), routing health, auto plan (extended) | todo |
| cli-tools, mitm, pxpipe, other i18n locales | deferred |
| proxy pages and the per-connection proxy fields | out |
| landing | out |

---

## Remaining work
**Only the callback page is built;** the rest of `web/` is not.

- [ ] 4.0: written stack-spike decision in `ARCHITECTURE.md` before any framework enters `web/`
- [ ] 4.1: shell, routing, i18n (`web/static/i18n/{en,id}.json`), login, keys, connections
- [ ] 4.3: combos, usage charts, logs, settings, skills studio
- [ ] Management API behind the pages: `/api/settings`, `/api/keys/*`, `/api/usage/*`, catalog sync, `/api/locale`, `/api/tags`, and the Console Log ring's read route
- **Done when:** no `innerHTML` from data, every dynamic string escaped.

---

## Findings

The embedded `web/` folder holds the login and callback pages today; the rest is listed under Remaining work.

