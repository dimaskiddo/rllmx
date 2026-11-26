# Router-LLM - Roadmap

What Router-LLM does today and what is planned. One line per area; design detail lives in [`docs/design/`](docs/design/).

Status words: **built** (implemented and tested), **partial** (some behavior missing), **planned** (not started).

## Server and access

| Area | Status |
|---|---|
| HTTP server, routing, guard, CORS, deadlines, body limits | built |
| Configuration, logging with redaction, maintenance jobs | built |
| Password login, dashboard sessions, API keys | built |
| CAPTCHA on the password form: Cloudflare Turnstile or Google reCAPTCHA v2, remote callers only, local callers exempt | built |
| OIDC and SAML single sign-on | built |
| API key management API | built |
| Per-key model allow-lists | built |
| Per-key request, token and cost budgets | built |
| Health, circuit breaker and model probes | built |

## Storage and usage

| Area | Status |
|---|---|
| SQLite storage, migration, credential sealing | built |
| PostgreSQL storage | built (the `make test-pg` run against a server is still pending) |
| Import from an existing 9router database (config and usage history) | built |
| Usage rows, pricing and cost | built |
| Usage dashboard routes (stats, chart, history, logs, providers, live feed), per-connection quota readings with the two refill routes, and the client connection list | built |
| OpenMetrics `GET /metrics` (requests, tokens, cost, latency, in-flight, key and account info) behind a bearer token or Basic pair, with a Prometheus/Grafana guide and a starter dashboard | built |

## Requests

| Area | Status |
|---|---|
| Pipeline, client formats (OpenAI, Claude, Gemini, Responses, Ollama) | built |
| Model resolution, aliases and provider prefixes | built |
| Account selection, per-model locks, cooldown and backoff | built |
| Format translation, streaming, usage and finish reasons | built |
| Executors for the shipped providers | built |
| Combos and routing strategies (fallback, round-robin, smart, fusion) | built |
| Vision and audio adapter: a per-capability fallback pool tried when no target model can read the turn | built |
| Response cache | built |
| Token savers (tool-output compaction, Headroom sidecar, built-in and custom prompts, watermark remover with an invisible-character strip) | built |
| Skills registry, local and catalog routers, catalog sync (one button for the official ECC catalog, with a count), the memory skill reading a local agent's notes, and one switch that stops every skill | built |
| Skills Studio, declarative rule hooks, config fields and the session cache | built |
| Auto plan: the heuristic and smart classifier, the planning combo, the plan turn and the code-combo switch | built |

## Accounts

| Area | Status |
|---|---|
| OAuth logins (device and code flows), token refresh, bulk import | built |
| Provider checks and probes | built |
| Live model catalog per account | built |
| models.dev catalog sync and the capability overlay (`/api/models/catalog-sync`, `CATALOG_SYNC`) | built |

## Media

| Area | Status |
|---|---|
| Embeddings | built |
| Image generation | built |
| Speech, transcription and voice lists | built |
| Video generation | built |
| Web search and page fetch | built |
| Compression (gzip, Brotli), security headers, HTTP/2 on the plain listener | built |
| Web image and video providers | built |

## Dashboard

| Area | Status |
|---|---|
| Login and callback page | built |
| Dashboard shell, router, theme and locale switch, English and Indonesian, a sidebar that is a drawer on a phone and a remembered rail on a desktop | built |
| Endpoint and API keys page (base URL, key guard, keys, access list, budgets with their spend, response cache) | built |
| Dashboard roles: an admin, and standard users who see only their assigned keys and those keys' usage; the Users page and the key owner select | built |
| Dashboard part 2: combos with auto-suggest, the vision/audio adapter and their smallest context and output limits, usage with charts and a live topology, quota, console log, settings and single sign-on, pricing, skills Studio, auto plan, token savers | built |
| About page: version and commit, an on-demand check of this repository's GitHub releases, a verified self-update (`rllmx artisan upgrade` or the admin action), credits to 9Router and 9Router-Extended, and the MIT license | built |
| REA Allow: a built-in skill that replaces the client's security-policy paragraphs, off by default and behind a warning | built |
| Quota readers for MiniMax Code, Xiaomi MiMo and Qoder; a keyset-paged request log; per-provider reset credits and expiry | built |
| Cookie and deepseek-web provider probes; codex-token, zed, kiro-refresh and kiro-external-idp importers | built |
| Suggested-model and kilo free-model routes | built |
| Skills cards are kill switches: each turns its group off and on without touching a row's own switch; the catalog router has its own switch | built |
| Providers that need no credential: opencode is served through a virtual account, so it needs no key and no login | built |
| Quota: reset credits name their grant, both providers list their credits and expiry, and an auto-ping runner warms a reset window | built |
| Maintenance: optimize, checkpoint, analyse and an incremental vacuum of the configuration file, plus the usage store's own jobs | built |
| Dashboard sign-in: `AUTH_ADMIN_USERNAME` (default `admin`) is the administrator's name, required at sign-in and reserved from every user | built |
| Single sign-on roles: `AUTH_SSO_ADMIN` names the admins, any other account the provider authenticates is provisioned as a standard user (first password set from that session), `AUTH_MAX_USERS` caps the list; an address taken from an editable claim (`preferred_username`) never signs in as a standard user | built |
| Web stack (Alpine.js, Tabler CSS, Lucide, Chart.js, Sortable.js: all vendored and pinned) and the `/static` route | built |
| Dashboard management API (settings, keys, models and aliases, custom and disabled models, availability, pricing, combos and presets, console log) | built |
| Usage API (stats, chart, history, logs, providers, active providers, live feed) | built |
| Quota API (per-connection readings, `providers/client`, the two refill routes) | built |
| Usage and quota pages: period-bounded, newest-first logs, provider names in the chart and the topology | built |
| Provider pages (list, detail, every login flow, custom endpoints, models, header overrides) | built |
| Outbound pacing per connection, so a burst is queued instead of reaching a provider as an anomaly | built |
| Dashboard review so far: sign-in and reorder fixes, polling and secret handling, contrast and accessibility, provider aliases and icons, tooltips and list rhythm, red confirmed deletes | built |
| Provider header overrides pre-filled from the provider's own headers, saving only what changed | built |
| Live feeds send each frame at once, with one status line per response | built |
| A key is shown masked and revealed on request; the dashboard session lifetime is `AUTH_SESSION_TTL` | built |
| A client tool's "how much have I used" is answered from the key's budget, and a key with a budget carries rate-limit headers | built |
| Every request logs its route, its savers and skills, and its close; a cancelled read is not logged as a fault | built |
| Console log as a live terminal (level colour, filter, copy, follow the tail) | built |
| Usage breakdown groups an API key into one row; compact chart axes | built |
| Provider and model catalog sync with 9router 0.5.123 (Kiro, Antigravity, GPT 6.2, Kimi K3.1 and others) | built |
| Kiro's Responses transport (`UPSTREAM_KIRO_CREATE_RESPONSE`), Gemini 4 Hydrogen long decode, a refused Antigravity prompt sent once more with its system text in the first turn | built |
| OpenDesign Go, DeepRelay and QwenCloud providers; Synthetic, DeepRelay and OpenDesign Go usage readers; a per-lane thinking shape | built |

---

Status is updated with each release. Design detail lives in [`docs/design/`](docs/design/); the pinned reference version is in [`docs/PARITY.md`](docs/PARITY.md).
