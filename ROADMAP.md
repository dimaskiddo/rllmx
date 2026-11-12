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
| API key management API, per-key model allow-lists and budgets | planned |
| Health, circuit breaker and model probes | partial |

## Storage and usage

| Area | Status |
|---|---|
| SQLite storage, migration, credential sealing | built |
| PostgreSQL storage | built |
| Import from an existing 9router database (config and usage history) | built |
| Usage rows, pricing and cost | built |
| Usage dashboard and quota views | planned |

## Requests

| Area | Status |
|---|---|
| Pipeline, client formats (OpenAI, Claude, Gemini, Responses, Ollama) | built |
| Model resolution, aliases and provider prefixes | built |
| Account selection, per-model locks, cooldown and backoff | built |
| Format translation, streaming, usage and finish reasons | built |
| Executors for the shipped providers | built |
| Combos and routing strategies | planned |
| Response cache | built |
| Token savers and skills | planned |

## Accounts

| Area | Status |
|---|---|
| OAuth logins (device and code flows), token refresh, bulk import | built |
| Provider checks and probes | built |
| Live model catalog per account | built |

## Media

| Area | Status |
|---|---|
| Embeddings | built |
| Image generation | built |
| Speech, transcription and voice lists | built |
| Video generation | planned |
| Web search and page fetch | planned |

## Dashboard

| Area | Status |
|---|---|
| Login and callback page | built |
| Provider, key and usage pages | planned |

---

Status is updated with each release. Design detail lives in [`docs/design/`](docs/design/); the pinned reference version is in [`docs/PARITY.md`](docs/PARITY.md).
