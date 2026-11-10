# PRD 01 — HTTP Server & Routing (`app/http/`, `routes/`)

**Status:** partial · **Steps:** 1.0, 1.5

One listener on `APP_HOST` (default `127.0.0.1`; the container sets `0.0.0.0`) and `APP_PORT` (`20128`), through a provider runner around `Route`. Route tables live in `routes/`; `app/http` holds the guard, the URL rewrite, the locality check, the trusted-proxy view and the server runner; `app/http/middleware` holds the global stack; and `app/http/controllers` holds `HealthController`, `VersionController` and `InitController`. Client API controllers (`controllers/v1`), the dashboard JSON controllers (`app/http/controllers`) and the embedded `web/` do not exist yet; they arrive with 1.8, 2.6 and Phase 4.

### Stack

| Part | Setting |
|---|---|
| Router | Gin v1.12.0 through `goravel/gin`, pinned, built `-tags nomsgpack` (drops MessagePack: 14.1 MB to 7.2 MB on a hello program). Release mode, driver defaults; the engine is private, so we neither configure nor fork it. Selected by `http.default` in `config/http.go` |
| Trusted proxies | `SERVER_TRUSTED_PROXIES`, empty meaning loopback only. `app/http/trust.go` reads it: `ClientAddr` is the caller address for logs and the login limiter, `SecureRequest` decides HSTS and the dashboard cookie's `Secure` flag. `X-Forwarded-For` and `X-Forwarded-Proto` count only from a peer on that list. Gin's `Request().Ip()` trusts any `X-Forwarded-For` and is never an access decision |
| Server | A runner (`rllmx:http`) owns a stdlib `http.Server` whose handler wraps `Route().ServeHTTP` with the URL rewrite: `ReadHeaderTimeout` (`HTTP_SERVER_READ_HEADER_TIMEOUT`, 10 s), `IdleTimeout` (`HTTP_SERVER_IDLE_TIMEOUT`, 120 s), headers capped at 64 KiB (`http.drivers.gin.header_limit`), **no** `ReadTimeout` or `WriteTimeout` — a stream runs for minutes. `APP_HOST`/`APP_PORT`; TLS when the four `http.tls.*` keys are set. `http.request_timeout` is `0`, because the framework's timeout middleware buffers and cuts a stream. `app.disabled_runners` lists `goravel:http`, which builds its server without these settings |
| Unmatched requests | `Route().Fallback` answers all of them: an unknown `/api` path is 401 anonymous, 404 authenticated; a known path under another method is 405 (no `Allow` anonymous); an unknown client path is a 404 in the client's format |

### Middleware Order

Outermost first: path check and rewrite, recover, request id, access log, security headers, CORS, deadlines, body limit, cross-site guard, access guard (PRD 2), handler. The first stage is an `http.Handler` around the engine (`Route().ServeHTTP`), because Gin routes before its middleware. The rest are global middleware in `bootstrap/app.go` (`WithMiddleware`) and the guarded route group in `routes/`; the framework's own timeout and CORS middleware are replaced by ours.

| Middleware | Behavior |
|---|---|
| `SecurityHeaders` | `X-Content-Type-Options: nosniff` everywhere; `Cache-Control: no-store` on `/api`; `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'` and `Referrer-Policy: no-referrer` on everything that is neither `/api` nor the client API; `Strict-Transport-Security: max-age=31536000` only when `Trust.SecureRequest` says the caller arrived over TLS or a trusted proxy sent `X-Forwarded-Proto: https`. Set before the handler, so a denial carries them too |
| `CORS` | Fixed policy, no config block: `/v1` and `/v1beta` get `*` origin and the expose list, `/api` gets nothing, credentials are never allowed. An `OPTIONS` naming `Access-Control-Request-Method` is answered 204 here; any other `OPTIONS` goes on to the guard |
| `Deadlines` | Through `http.NewResponseController(stream.WriterOf(ctx))`, which is the only way past the driver's `*gin.BodyWriter`. `/api` read 30 s and write 60 s; the client API a 10 min read deadline when it carries a body, no write deadline. A writer that cannot take one is tolerated |
| `BodyLimit` | `SERVER_MAX_BODY_MB` for the client API, 16 MiB elsewhere. A declared length over the cap is refused 413 in the path's error format; a body with no declared length stops at the cap on the first oversized read |
| `SameOrigin` | A `POST`/`PUT`/`PATCH`/`DELETE` under `/api` needs `Sec-Fetch-Site` `same-origin` or `none`, or `Origin` equal to `Host`, or neither header. `same-site` is refused. One exact exemption, the SAML ACS (PRD 2) |


### Routes

| Path | Purpose |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat. A body with `input[]` is still OpenAI |
| `POST /v1/messages`, `/v1/messages/count_tokens` | Anthropic Messages; the count is a local estimate |
| `POST /v1/responses`, `/v1/responses/compact` | OpenAI Responses and its compaction variant |
| `POST /v1beta/models/{model}:generateContent`, `:streamGenerateContent` | Gemini. `{model}` is one or two `/`-separated segments, each matching `^[A-Za-z0-9_.-]+$` and free of `..`, split at the last `:`. Any other action is 404 (D224) |
| `GET /v1/models`, `/v1/models/info?id=`, `/v1/models/{provider}/{model}`, `GET /v1beta/models` | The models the caller can call: connections, custom models and alias targets, minus disabled ones; a restricted key sees its own list. `/v1/models/*path` is a catch-all because the id holds a slash (D225, D226) |
| `POST /v1/api/chat` | Ollama chat |
| `POST /v1/embeddings`, `/v1/images/generations`, `/v1/audio/*`, `/v1/videos/*`, `/v1/search`, `/v1/web/fetch` | Media (PRD 13) |
| `POST /v1/systemone` | Smart router entry |
| `/api/*` | Dashboard JSON API (PRD 18) |
| `GET /api/health` | Unauthenticated liveness |
| `GET /api/init` | `{"initialized":true,"needsSetup":<no connection configured>}`. An unreadable count answers `needsSetup:false`, so a broken store never becomes a setup prompt |
| `GET /api/version` | `{"currentVersion","commit","latestVersion":null,"hasUpdate":false}`. No registry lookup: the updater is out (PRD 22) |
| `/`, `/dashboard/*`, `/login`, `/setup` | Static dashboard |

### Path Rules & Rewrites

Gin matches `URL.Path` without cleaning it, so the first stage refuses (400, in the endpoint's error format) any non-canonical path: dot segments, `//`, a trailing slash, a backslash, control characters, or an encoded `/`, `.`, `\` or NUL in the raw form. Rewrites match whole segments (`/codexfoo` is not `/codex`), run once and clear `RawPath`, so the guard and the router read the same path.

| Rewrite | Why |
|---|---|
| `/v1/v1/*` to `/v1/*` | SDKs append `/v1/...` to a base URL that already ends in `/v1` |
| `/codex/*`, `/responses` to `/v1/responses` | Codex client paths |
| `/systemone` to `/v1/systemone` | Short alias |

Gin cannot express `{model}:generateContent`, so `POST /v1beta/models/*path` is a catch-all route in `routes/` split at the last `:`. The five chat routes share `ChatController.Serve`; the format comes from the path alone (D190). `count_tokens` is answered by `ChatController.CountTokens` with no pipeline.

### Limits & Deadlines

| Item | Behavior |
|---|---|
| Client body | `http.MaxBytesReader`, `SERVER_MAX_BODY_MB` (128; media uploads need it). `/api` bodies: 16 MiB |
| Oversize | Declared length over the cap is refused before the handler runs; a chunked body stops at the cap. 413, a read timeout is 408, both in the endpoint's error format |
| Upstream error body | Read up to 1 MiB |
| `/api` deadlines | Read 30 s, write 60 s |
| Client API deadlines | Read 10 minutes, no server write deadline |
| Stream write | The SSE and NDJSON writers set their own deadline before each write: a client that stopped reading is cut after 60 s, a quiet stream is untouched |

Deadlines are set once, at the start of the request, through `http.ResponseController` over `stream.WriterOf`, which is the only writer that carries one. A handler never sets a read deadline after reading the body: net/http's background read would fail and cancel the request context and the upstream call with it.

### Streams

`app/llm/stream/sse.go` and `app/llm/stream/ndjson.go` are the only stream writers: `NewSSE` for every client, `NewNDJSON` for an Ollama client (divergence 219). They share one core (`sink`), so the rules below hold for both.
- It builds its `ResponseController` on the innermost writer. The framework's `ctx.Response().Writer()` is a `*gin.BodyWriter` that has no `Unwrap`, hides Gin's flush error and copies every written byte into an in-memory buffer; `StreamWriter` wraps the same writer. A controller built on either never reports a departed client, and the buffer grows with the stream (a test fails when the unwrapping is removed). Stream handlers take the Gin context from `Context.Instance()` (the Route has no `Instance()`), write to `BodyWriter.ResponseWriter` (Gin's own writer, which has `Unwrap`), and never call `ctx.Response().Stream(...)`.
- Each event is flushed. The first error is remembered and fails every later call, so a caller stops spending upstream tokens.
- A panic after the response started re-panics `http.ErrAbortHandler`, so the client sees a broken stream, not a complete-looking one.

### CORS

`/v1` and `/v1beta` send `Access-Control-Allow-Origin: *` and `Access-Control-Expose-Headers: X-Request-Id, Retry-After`, also on errors. `/api/*` sends no CORS headers.

| Item | Behavior |
|---|---|
| Preflight | An `OPTIONS` that names `Access-Control-Request-Method` is answered 204 **before** the guard (browsers send no credentials with it). Any other `OPTIONS` goes through the guard |
| Allow-Headers | Echoes the requested names after a character check (`*` does not cover `Authorization`); a list with any other character is replaced by the fixed default list |
| Max-Age | 86400. Credentials are never allowed |

### Cross-Site Guard

A `POST`, `PUT`, `PATCH` or `DELETE` under `/api` is refused 403 unless `Sec-Fetch-Site` is `same-origin` or `none`; or that header is absent and `Origin` equals `Host`; or neither header is present, which is a script rather than a browser. `same-site` is refused: another port of this machine is same-site, not same-origin, and gets the cookie. A repeated header is never trusted. The client API is not covered, because a browser app calls it cross-origin by design. The exemption list is exact paths and holds only `/api/auth/saml/acs` (PRD 2).


### Shutdown

```mermaid
sequenceDiagram
    participant OS as Signal
    participant App as Goravel application
    participant Srv as http.Server
    participant Bg as Schedule facade
    participant W as Usage writer
    participant DB as Stores, cache, lock, log

    OS->>App: SIGINT or SIGTERM (application context cancelled)
    App->>Srv: http.Server.Shutdown, stop accepting, grace HTTP_SERVER_SHUTDOWN_GRACE (30s)
    App->>Srv: Close remaining, wait up to 5s for handlers
    App->>Bg: stop (its jobs write to the stores)
    App->>W: close, flush queued rows
    App->>DB: close analytics, main, cache, release lock, close log
    Note over App: Shutdown returns only after the last runner (shutdown priority orders the groups)
```

A second signal exits at once with code 1. Each stage after the server is a provider runner with a shutdown priority, which fixes the order above. Divergence 30 lists what the reference does instead.

### No Remote Stop

There is no `/api/shutdown` and no update route. Anonymous callers get 401 for any unknown `/api` path, so the absence is not an oracle.

### Vulnerability Scan (govulncheck)

The toolchain is Go 1.26.10 and `go.mod` says `go 1.26.0` (the `x/net` fix needs it). `govulncheck ./...` found 13 issues in modules the framework pulls in. Ten are fixed by `x/net` v0.60.0, `x/text` v0.42.0, `quic-go` v0.59.1, `pgx` v5.9.2 and `grpc` v1.83.1. Three remain, all OpenTelemetry (`sdk/log`, `otlploggrpc`, and `otlptrace*` with `sdk`): their fixes need `otel/log` v0.21.0, which dropped its own `Value` and `KeyValue` types, so Goravel v1.18.0's telemetry package no longer compiles against it. They stay open until a Goravel release supports it; the router sets up no OpenTelemetry exporter, so the reported traces are in code it does not start. A step is not done while a called vulnerability remains that can be fixed (`AGENTS.md`, Security).

### Framework Choices

How the program is built on Goravel. None of this changes behavior toward a client, so none is a divergence (`DIVERGENCES.md` compares behavior only). Each row names its cost.

| Area | Choice | Cost or risk |
|---|---|---|
| HTTP server | A runner owns the `http.Server`, because the driver builds one with no header or idle timeout and no key sets them; `goravel:http` disabled | We own listen, TLS, shutdown and the second-signal exit. `Route().Run` is unused |
| Gin engine | Driver defaults: `RedirectTrailingSlash` on, `HandleMethodNotAllowed` off, every proxy trusted. v1.18.0 exposes no engine accessor, and the engine is rebuilt whenever global middleware or the recover handler is set | The rewrite wrapper refuses non-canonical paths first, `Fallback` answers 401/404/405, and the guard reads only `Request().Origin().RemoteAddr`. A driver release exposing the engine would allow `HandleMethodNotAllowed` and a trusted-proxy list; a wrong-method test catches the change |
| Debug logger | `APP_DEBUG=true` makes the driver add Gin's logger, which writes the query string | `APP_DEBUG` stays `false` outside local development; the access log (path only) is ours |
| Runner order | Every runner starts at once; only shutdown is ordered. `rllmx:data` takes the lock, opens the stores and the writer, then marks data ready. `rllmx:http` opens its socket first (a busy port is the first error) and waits for that mark. Shutdown priority: HTTP -100, schedule and the framework at 0, data +100 | The socket accepts connections slightly before the server answers; `rllmx artisan health` times out during a long migration |
| Scheduler | `Schedule` with `SkipIfStillRunning`; the framework always recovers a job panic. Start offsets and the clock are passed per job; a job gets no context and watches the application context | No jitter or clock injection; shutdown waits for a running job with no limit, so jobs stay short |
| Database layer | `DB`'s manual interface (`Select`, `Insert`, `Update`, `Delete`, `Statement`, `Transaction`) with `goravel/sqlite` and `goravel/postgres`; `Schema` for checks, exact DDL and migrations. No ORM model; rows are structs with `db` tags and string timestamps. Sealing, `kv` scopes, unknown JSON keys, NUL stripping, timestamps and `Rebind` are thin wrappers | The drivers are GORM-based, so `gorm.io` stays in `go list -deps` (a test keeps it out of our imports). One pool limit set serves every connection, `Connection(name)` caches in an unlocked map (stores are built once, in order), and a failed connection build panics |
| Default connection | `database.default` is the writer, and Goravel boots `DB` for every command, pinging it read-write. `config.Boot` therefore creates `DATA_DIR/db` (`0700`) and an empty `data.sqlite` (`0600`) when the connection is inside `DATA_DIR`, so the ping neither warns nor leaves a `0644` file | Any command, even `version` and `health`, can leave an empty database file. A `DB_DATABASE` elsewhere (a 9router file) is never created |
| Framework commands | Two gates over one list in `bootstrap/commands_filter.go`. `WithCommandsFilter` covers only the foundation and console batches; v1.18.0's database provider registers `migrate*`, `db:wipe`, `db:seed`, `db:show`, `db:table` and the `make:*` generators with a bare `artisan.Register`, and `Artisan` cannot unregister. `providers.CommandsServiceProvider` wraps the bound `Artisan` and filters every `Register` through `FilterCommandsByAllowlist` | The wrapper needs the framework to finish every `Register` before any `Boot`, which `Build` does; a release registering from `Register` would slip past. The exit is a version bump — `master` already routes through the filtered `app.Commands(...)`. The generators are listed one by one, not as `make:*`: the four ORM ones (`make:model`, `make:observer`, `make:seeder`, `make:factory`) are left out, and a generator a later release adds stays dropped until it is listed |
| Cache | The `Cache` facade with memory and Redis stores; `app/platform/cache` adds keys, a generation counter for purges and fail-open | The memory store has no size cap and returns values without copying, and `Remember` does not collapse concurrent loads; `app/platform/cache.Load` adds single-flight |
| Logging | The `Log` facade and `config/logging.go` (channel, path, level, formatter, retention). A `custom` channel redacts an entry before the file channel writes it. Neither the Console Log ring nor its read route exists: both come with 2.6 | The file channels have no size-based rotation or compression (`daily` keeps `days` files). Redaction is proven by a test on the channel |

---

## Parity: Server, Guard and Routes

| Item | Package | Status | Step |
|---|---|---|---|
| Request guard (ref `src/dashboardGuard.js`: public, protected, always protected, local only) | `app/http` (policy, locality, route table, fallback), `app/http/middleware`, `routes/` | done (1.0 class table, locality and fallback; 1.6a sessions and keys; D41-47, D49) | 1.0, 1.6 |
| Graceful shutdown, server timeouts, SSE writer, start (`rllmx`), `rllmx artisan health` (ref `custom-server.js`) | `app/http` (server runner), `app/providers`, `app/llm/stream`, `app/console/commands` | partial (1.0; the store-closing runners are `app/store/data`'s, which are built, but the sequence is only proven once the server is exercised live; D30, D48) | 1.0 |
| `/api/health` | `app/http/controllers` | need user validation (1.0) | 1.0 |
| `/api/init`, `/api/version` | `app/http/controllers` | done (1.5). `version` answers the build and never contacts a package registry | 1.5 |
| `/api/version/update`, `/api/version/shutdown`, `/api/shutdown` | none | out (no route; signal stop) | 1.5 |

---

## Remaining work

Steps 1.0 and 1.5 are built. The PRD stays `partial` because three rows above are paid by later steps:

- [x] The request guard runs on a real authenticator, with sessions, API keys, OIDC and SAML (1.6a to 1.6c)
- [x] The data runner closes the stores, the writer and the cache in shutdown-priority order (1.4); what is unproven is the ordering under a live server, which no unit test can show
- [ ] `/v1`, `/v1beta` and `/api/*` client and dashboard routes, and the pages — **1.8**, **2.6**, Phase 4

Nothing in the listener, the middleware order, the path rules, CORS, the body cap, the deadlines or the two base routes is outstanding.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 30 | Header and idle timeouts (`HTTP_SERVER_READ_HEADER_TIMEOUT`, `HTTP_SERVER_IDLE_TIMEOUT`), no write timeout on streams, bounded graceful drain (`HTTP_SERVER_SHUTDOWN_GRACE`) (reference: `server.js`) | Slow clients, exits stop hurting |
| 196 | Retired: scheduling uses the `Schedule` facade, a framework choice, not client behavior | Recorded so the number stays citable (framework choice) |
| 197 | Retired: the HTTP server, its timeouts and the Gin options come from the framework | Recorded so the number stays citable (framework choice) |
| 46, 47 | Rewrite first, non-canonical paths refused; preflight echoes requested names after character check | One path for guard and router; browsers can send `Authorization` |
| 224 | The Gemini model is one or two plain segments (`gemini-2.5-pro` or `provider/model`) split at the last colon; `countTokens`, `embedContent` and any other action are 404; the whole Gemini body is read, not only text parts. The reference runs every action as generate with the suffix left in the model name and keeps text parts only | A crafted segment cannot change the upstream path, a wrong action fails loudly, tools, images and function calls survive |
| 203 | Security headers on every response: `nosniff` everywhere, `no-store` on `/api`, CSP and `Referrer-Policy` on pages, HSTS once TLS reached this process or a trusted proxy said it did | The reference sets none; a dashboard behind a reverse proxy, or loaded over a LAN address, would otherwise be framable and cacheable |
| 204 | `Access-Control-Expose-Headers: X-Request-Id, Retry-After, X-Rllm-Cache` and `Max-Age: 86400` | A browser client can read the request id, the backoff delay and whether the response cache answered; a preflight is not repeated on every call. Reference exposes nothing and caches no preflight |
| 205 | One body cap before the handler, 413 in the endpoint's error format, and a streaming stop for a body with no declared length | Reference bounds only the Next rewrite buffer, so a direct route has no cap at all |
| 206 | `/api/version` answers the build and never contacts a package registry | The reference's public route calls npm on every cold start and needs an outbound request to describe itself |

---

## Findings

The driver's engine is private in v1.18.0, so `HandleMethodNotAllowed` cannot be turned on and Gin has no 405; the `Fallback` handler answers it.
The driver's `http.Server` sets no header or idle timeout and no config key sets one, so a runner owns the server instead.
`ctx.Response().Writer()` is the driver's `*gin.BodyWriter`, which embeds the `gin.ResponseWriter` **interface** and so has no `Unwrap`; `http.NewResponseController` on it reports "not supported" and silently sets nothing. Every deadline must go through `stream.WriterOf`, which is the same unwrap the SSE writer uses. A test proves the deadline reaches the socket by writing a body larger than net/http's 4 KiB buffer after it.
The `/api` body cap is a constant (16 MiB) rather than a variable, because only `SERVER_MAX_BODY_MB` is configurable and it describes the client API. The dashboard's largest body is an imported settings file; 16 MiB covers it with room to spare.
`config/cors.go` was deleted: the policy is fixed (`*`, never credentials), so a block of six knobs read by nothing promised configuration the product does not have.
