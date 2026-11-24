# Router-LLM — Roadmap

What Router-LLM does today and what is planned. One line per area; design detail lives in [`docs/design/`](docs/design/).

Status words: **built** (implemented and tested), **partial** (some behavior missing), **planned** (not started).

## Server and access

| Area | Status |
|---|---|
| HTTP server, routing, guard, CORS, deadlines, body limits | built |
| Configuration, logging with redaction, maintenance jobs | built |
| Password login, dashboard sessions, API keys | built |
| OIDC and SAML single sign-on | built |
| API key management API | built |
| Per-key model allow-lists | built |
| Per-key request, token and cost budgets | built |
| Health, circuit breaker and model probes | built |

## Storage and usage

| Area | Status |
|---|---|
| SQLite storage, migration, credential sealing | built |
| PostgreSQL storage | built |
| Import from an existing 9router database (config and usage history) | built |
| Usage rows, pricing and cost | built |
| Usage dashboard routes (stats, chart, history, logs, providers, live feed), per-connection quota readings with the two refill routes, and the client connection list | built (routes); pages planned |

## Requests

| Area | Status |
|---|---|
| Pipeline, client formats (OpenAI, Claude, Gemini, Responses, Ollama) | built |
| Model resolution, aliases and provider prefixes | built |
| Account selection, per-model locks, cooldown and backoff | built |
| Format translation, streaming, usage and finish reasons | built |
| Executors for the shipped providers | built |
| Combos and routing strategies (fallback, round-robin, smart, fusion) | built |
| Response cache | built |
| Token savers (tool-output compaction, Headroom sidecar, built-in and custom prompts) | built |
| Skills registry, local and catalog routers, catalog sync | built |
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
| Web image and video providers | built for chatgpt-web, uc-web and veoaifree-web; grok-web, gemini-web and muse-web deferred |

## Dashboard

| Area | Status |
|---|---|
| Login and callback page | built |
| Dashboard shell, router, theme and locale switch, English and Indonesian | built |
| Endpoint and API keys page (base URL, key guard, keys, access list, budgets, response cache) | built |
| Dashboard roles: an admin, and standard users who see only their assigned keys and those keys' usage; the users card and the key owner select | built |
| Dashboard part 2: combos with auto-suggest, usage with charts and a live topology, quota, console log, settings and single sign-on, pricing, skills Studio, auto plan, token savers | built |
| Single sign-on roles: `AUTH_SSO_ADMIN` names the admins, any other account the provider authenticates is provisioned as a standard user (first password set from that session), `AUTH_MAX_USERS` caps the list; an address taken from an editable claim (`preferred_username`) never signs in as a standard user | built |
| Web stack (Alpine.js, Tabler CSS, Lucide, Chart.js, Sortable.js: all vendored and pinned) and the `/static` route | built |
| Dashboard management API (settings, keys, models and aliases, custom and disabled models, availability, pricing, combos and presets, console log) | built |
| Usage API (stats, chart, history, logs, providers, active providers, live feed) | built |
| Quota API (per-connection readings, `providers/client`, the two refill routes) | built; quota auto-ping planned |
| Usage and quota pages | built |
| Provider pages (list, detail, every login flow, custom endpoints, models, header overrides) | built |
| Outbound pacing per connection, so a burst is queued instead of reaching a provider as an anomaly | built |
| Dashboard review so far: sign-in and reorder fixes, polling and secret handling, contrast and accessibility, provider aliases and icons, tooltips and list rhythm, red confirmed deletes | built |

---

Status is updated with each release. Design detail lives in [`docs/design/`](docs/design/); the pinned reference version is in [`docs/PARITY.md`](docs/PARITY.md).
