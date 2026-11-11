# PRD 17 — Configuration (`config/`, `app/platform/settings/`)

**Status:** partial · **Steps:** 1.0, 1.3, 1.5, 1.6, 1.8

The settings and variable tables below are parity data: the keys the store reads and writes back. A key whose *behaviour* belongs to a later step is marked there, and that step's PRD owns it.

Environment variables and an optional `.env` in the working directory, read by the framework's config loader through `config/*.go`. The process environment wins. Every variable has a default in the `config.Env(name, default)` call.

### Loading

```mermaid
flowchart LR
    A[".env checked by config.Boot (size cap, BOM, unknown names), then read by the framework"] --> B[Process environment layered on top]
    B --> C["config/*.go: config.Env(name, default) per key"]
    C --> D["app/platform/settings builds one typed struct, validation, fixed error text"]
```

| Rule | Detail |
|---|---|
| Empty value | Viper treats set-but-empty as unset, so the default applies — process environment and `.env` alike |
| Errors | Never quote a value: `APP_PORT: invalid value`. Our checks (unknown name, size cap, range) exit non-zero naming the variable. A `.env` syntax error or a missing or short `APP_KEY` is reported by the framework, in its words |
| Unknown names | A `.env` entry in an owned group (`SERVER_`, `HTTP_`, `AUTH_`, `DB_`, `ANALYTICS_`, `CACHE_`, `LOG_`, `STREAM_`, `UPSTREAM_`, `OAUTH_`, `MEDIA_`, `SAVER_`, `CATALOG_`, `TRANSLATOR_`, `PROVIDER_`) that the code does not read fails startup, as does a lower-case spelling of a real name. The process environment is never scanned. `TEST_*` and Windows `=C:` are ignored |
| Data dir | Accepts `~` and a relative path; made absolute |
| Secrets | No secret reaches the `Log` facade; what slips through a URL, header or error text is scrubbed by `app/platform/redactor`, in front of every file channel. There is no `Secret` string type |
| Expansion | `$NAME` and `${NAME}` in unquoted and double-quoted values, from names defined earlier in the same file (unknown becomes empty; the process environment is not consulted). A value with `$` must be single-quoted; single-quoted values are literal |
| `.env.example` | Lists exactly what the code reads. A test checks both directions, that each name matches `^[A-Z]+(_[A-Z0-9]+)+$` and that none collides after case folding. A variable is added by the step that reads it |
| Raw key names | The framework exposes no key list, so `config.Boot` reads `support.EnvFilePath` with `subosito/gotenv`, the parser the loader already uses. It supplies key names, the size cap and the BOM check only |

### Naming

- Goravel-owned names are used as the framework defines them: `APP_NAME`, `APP_ENV`, `APP_KEY`, `APP_DEBUG`, `APP_URL`, `APP_HOST`, `APP_PORT`, `JWT_SECRET`. `APP_KEY` is 32 characters (`./artisan key:generate`), is required by the framework at boot, and is not the database sealing secret.
- Every other variable has the form `<GROUP>_<NAME>`, upper case, with no product prefix. Groups: `SERVER` (guard and limit settings), `HTTP` (the listener's `http.server.*` keys, the same tree as Goravel's `http.*`), `AUTH`, `DB`, `ANALYTICS`, `CACHE`, `LOG`, `STREAM`, `UPSTREAM`, `OAUTH`, `MEDIA`, `SAVER`, `CATALOG`, `TRANSLATOR`, `PROVIDER`. `DATA_DIR` stands alone.
- Booleans are `true` or `false`, and the name says what `true` does (`OAUTH_BACKGROUND_REFRESH=true`, never `DISABLE_*`).
- Durations are Go durations (`200s`, `5m`), so no unit in the name. Sizes and counts carry the unit (`_MB`, `_DAYS`).
- Secrets and URLs end in `_SECRET`, `_PASSWORD`, `_URL`, so the log redaction handler recognizes them. There is no config printer command; print the names, never the values.
- 9router names that no longer exist are not read. `AUTH_API_KEY_SECRET` and `AUTH_MACHINE_ID_SALT` must hold the values 9router used (`API_KEY_SECRET`, `MACHINE_ID_SALT`) to keep imported keys valid, and `JWT_SECRET` keeps the 9router name and value. The mapping is in `this PRD, Environment Variables`.

### Variables

The **Step** column names the `TASKS.md` step that adds the group. No code reads rows marked *planned* yet, so setting one in `.env` fails startup with `unknown variable` until its step lands. Only `.env` entries in these groups are checked; the process environment is never scanned.

| Group | Variables (default) | Step |
|---|---|---|
| Server | `APP_PORT` (20128), `APP_HOST` (127.0.0.1), `HTTP_SERVER_READ_HEADER_TIMEOUT` (10s, 1s to 5m), `HTTP_SERVER_IDLE_TIMEOUT` (120s, 1s to 1h), `HTTP_SERVER_SHUTDOWN_GRACE` (30s, 1s to 10m; a second signal exits at once), `SERVER_MAX_BODY_MB` (128), `SERVER_TRUST_LOOPBACK` (true), `SERVER_TRUSTED_PROXIES` (empty: loopback only; addresses or CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` are believed), `APP_URL` (empty; `http(s)://host[:port]`, no userinfo, path or query, http only for loopback; base for OAuth, OIDC and SAML callbacks) | 1.3, 1.5, 1.6 |
| Data | `DATA_DIR` (`storage/app`) | 1.3 |
| Auth | `JWT_SECRET` (generated into `jwt-secret`), `AUTH_INITIAL_PASSWORD`, `AUTH_API_KEY_SECRET`, `AUTH_MACHINE_ID_SALT`, `AUTH_COOKIE_SECURE` (false), `AUTH_REFUSE_DEFAULT_PASSWORD` (false), `AUTH_SSO_ALLOW` (empty: **any account the provider accepts**; exact addresses or `@domain`) | 1.3, 1.6 |
| Database | `DB_CONNECTION` (`sqlite` by default, or `postgres`), `DB_DATABASE` (the SQLite file, empty: `DATA_DIR/db/data.sqlite`; the PostgreSQL database), `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD`, `DB_SCHEMA` (`public`), `DB_SSLMODE` (`require`; PostgreSQL only), `DB_ENCRYPTION_SECRET` (required) | 1.3 |
| Analytics | `DB_DATABASE_ANALYTICS` (empty: `DATA_DIR/db/analytics.sqlite`, or the schema `rllmx_analytics` on PostgreSQL), `ANALYTICS_RETENTION_DAYS` (90) | 1.3 |
| Cache | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB` (empty `REDIS_HOST`: the memory store only) | 1.3 |
| Response cache | `CACHE_RESPONSE_MAX_ENTRIES` (1000, 1 to 100000), `CACHE_RESPONSE_MAX_MB` (64, 1 to 4096): the in-process bound; ignored when `REDIS_HOST` is set | 1.8g |
| Logging | `LOG_CHANNEL` (`single`; `daily` or `stack` also), `LOG_LEVEL` (info). The file, the formatter (`text` or `json`) and the retention days are keys of the channel in `config/logging.go` (`storage/logs/rllmx.log`), not variables. No `print` flag: the framework drops console output on a custom driver, and nothing is copied to the console | 1.3 |
| Streams | `STREAM_STALL_TIMEOUT` (360s, 5s to 1h), `STREAM_FIRST_CHUNK_TIMEOUT` (200s, 5s to 1h), `STREAM_USAGE_WAIT` (3s, 100ms to 30s; how long a finished turn waits for a late usage report), all read in 1.8b; `STREAM_EMPTY_PEEK_DEADLINE` (45s, 1s to 10m) (planned, 2.4) | 1.8b, 2.4 |
| Upstream | `UPSTREAM_CONNECT_TIMEOUT` (60s, 1s to 10m), `UPSTREAM_ALLOW_PRIVATE` (empty: public targets only; host names, addresses or CIDRs; link-local and the cloud metadata addresses are refused even when listed), `UPSTREAM_TLS_CURVE` (`x25519` offers only that group, `default` offers the standard list; anything else fails the start) | 1.6b, Parity Sync |
| OAuth login | `OAUTH_CALLBACK_HOST` (127.0.0.1): an IP address, the bind address of the fixed-port login listener (built in 1.9a) | 1.9a |
| OAuth refresh | `OAUTH_BACKGROUND_REFRESH` (true) and `OAUTH_REFRESH_DELAY` (1500ms, 0s to 1m, pause between accounts), built in 1.9b. `OAUTH_REFRESH_GOOGLE_DELAY` (12s, 0s to 1m, the pause after an account of a provider that sets `slowRefresh`, plus 0 to 4s of jitter), built in 1.10g | 1.9b, 1.10g |
| Media | `MEDIA_VIDEO_TIMEOUT`, `MEDIA_SEARXNG_URL` (empty) (planned) | 1.11 |
| Token savers | `SAVER_HEADROOM_URL` (`http://localhost:8787`) (planned) | 3.1 |
| Catalog | `CATALOG_SYNC` (true; `false` turns sync off) (planned) | 2.6 |
| Translator | `TRANSLATOR_ENABLED` (false) (planned) | 2.6 |
| OAuth clients | `OAUTH_CLIENT_ID_<PROVIDER>` for antigravity, claude, codex, github, google, kimi, kimi_coding, minimax_code, muse, nous, trae, windsurf, xai; `OAUTH_CLIENT_SECRET_<PROVIDER>` only for antigravity, google, trae. Default empty, which disables that provider's login; `.env.example` carries working values. `google` serves Gemini and Gemini CLI, `minimax_code` both MiniMax Code regions, `xai` xAI and Grok CLI. Built in 1.9a: `OAUTH_CLIENT_ID_CLAUDE` and `OAUTH_CLIENT_ID_CODEX` (at most 256 characters, no spaces or control characters); 1.10c adds `OAUTH_CLIENT_ID_GITHUB`; 1.10g adds `OAUTH_CLIENT_ID_GOOGLE`, `OAUTH_CLIENT_SECRET_GOOGLE` (Gemini CLI), `OAUTH_CLIENT_ID_ANTIGRAVITY` and `OAUTH_CLIENT_SECRET_ANTIGRAVITY`; 1.10f adds `OAUTH_CLIENT_ID_MINIMAX_CODE` (both MiniMax Code sites; CodeBuddy and GLM need none); 1.10d adds `OAUTH_CLIENT_ID_KIMI`, `OAUTH_CLIENT_ID_MUSE`, `OAUTH_CLIENT_ID_NOUS` and `OAUTH_CLIENT_ID_XAI` (the Grok CLI login reads the xAI one; Kilo Code needs none). iflow is not built (D279), so it has no variable | 1.9a, 1.10 |
| Provider overrides | `PROVIDER_AZURE_ENDPOINT`, `..._AZURE_DEPLOYMENT`, `..._AZURE_API_VERSION`, `..._AZURE_ORGANIZATION`, `..._MIMO_PLATFORM_URL`, `..._COMMANDCODE_BASE_URL` (planned) | 1.10 |

### Not Read

- `REQUIRE_API_KEY` appears in the reference's example file but no code reads it. The `requireApiKey` setting is the control.
- `ENABLE_REQUEST_LOGS` and `OBSERVABILITY_*` have no replacement: usage is always recorded and holds metadata only (PRD 16).
- Dropped with their feature: `NODE_ENV`, `NEXT_*`, cloud URLs, tunnel, MITM, proxy, updater, browser-pool and Devin variables. The `HTTP_PROXY` family is ignored.

### Settings Row

Everything the dashboard edits lives in `settings.data` with the reference's defaults. Keys this version does not use are kept. The defaults list is in `this PRD, Settings Keys`.

---

## Parity: Settings Keys

Defaults from the reference `DEFAULT_SETTINGS`; every key read and written back. **honored**: acting step built; **stored**: round trip only.

The status column is the v1 target, not the build state: behavior steps land after 1.4 (login and keys 1.6, providers and routing 1.7, usage and response cache 1.8, combos and skills later). Until then the store reads and writes the key back; nothing acts on it. Steps in `TASKS.md`.

| Keys | Status |
|---|---|
| `requireLogin`, `requireApiKey`, `authMode`, `ssoType`, `oidc*`, `saml*`, `password` | honored |
| `stickyRoundRobinLimit`, `providerStrategies`, `fallbackStrategy` | honored |
| `comboStrategy`, `comboStickyRoundRobinLimit`, `comboStrategies`, `capacityAdapter` | honored |
| `providerThinking`, `quotaVisibility` | honored |
| `providerOverrides` | honored: read and written by the override routes since 1.7c; applied to an upstream call by the executors (1.10) |
| `rtkEnabled`, `headroomEnabled`, `headroomUrl`, `headroomCompressUserMessages`, `headroomTimeoutMs`, `cavemanEnabled`, `cavemanLevel`, `ponytailEnabled`, `ponytailLevel`, `customPromptEnabled`, `customPrompt` | honored |
| `claudeAutoPing`, `codexAutoPing` | honored |
| `responseCache` | honored since 1.8g (`{enabled, ttlSeconds, maxEntryKb}`, default off; D24, D148); written by the settings API (2.6) |
| `ccFilterNaming` | honored (answers the Claude CLI topic-naming probe locally; absent = off; dashboard toggle with the settings API) |
| `autoPlan*`, `<skillId>Enabled`, `<skillId>RoutingMode`, `ecc_auto_skill_router*` (extended) | honored |
| `enableObservability`, `observabilityMaxRecords`, `observabilityBatchSize`, `observabilityFlushIntervalMs`, `observabilityMaxJsonSize` | stored (usage always recorded, metadata only) |
| `outboundProxyEnabled`, `outboundProxyUrl`, `outboundNoProxy` | out (no default, never read; a value in an imported row is kept as stored and skipped by `db:import-config`) |
| `headroomAutoStart`, `headroomCodeAware`, `headroomKompress`, `hermesServiceAutoStart` (extended), `pxpipe*` | stored |
| `tunnelEnabled`, `tunnelUrl`, `tunnelProvider`, `tailscaleEnabled`, `tailscaleUrl`, `tunnelDashboardAccess`, `mitmEnabled`, `mitmRouterBaseUrl`, `dnsToolEnabled`, `cloudEnabled` | stored |
| `tokenSaverEnabled`, `tokenSaverBudget` (extended) | stored and imported, no behavior (trim dropped, D9) |

---

## Parity: Environment Variables
### 1. Renamed

| 9router variable | Router-LLM variable | Note |
|---|---|---|
| `PORT` | `APP_PORT` | Goravel name |
| `HOSTNAME` | `APP_HOST` | Goravel name |
| `DATA_DIR` | `DATA_DIR` | as is; default `storage/app`, not `~/.9router` |
| `JWT_SECRET` | `JWT_SECRET` | as is (also the Goravel name); same value keeps existing sessions valid |
| `INITIAL_PASSWORD` | `AUTH_INITIAL_PASSWORD` | |
| `API_KEY_SECRET` | `AUTH_API_KEY_SECRET` | must equal the 9router value for existing keys to validate |
| `MACHINE_ID_SALT` | `AUTH_MACHINE_ID_SALT` | must equal the 9router value |
| `AUTH_COOKIE_SECURE` | `AUTH_COOKIE_SECURE` | as is |
| `STRICT_PRODUCTION_GUARD` (extended) | `AUTH_REFUSE_DEFAULT_PASSWORD` | name says what `true` does |
| `LOG_LEVEL` | `LOG_LEVEL` | as is |
| `ENABLE_TRANSLATOR` | `TRANSLATOR_ENABLED` | planned, step 2.6 |
| `STREAM_STALL_TIMEOUT_MS` | `STREAM_STALL_TIMEOUT` | Go duration (`360s`), not milliseconds |
| `STREAM_FIRST_CHUNK_TIMEOUT_MS` | `STREAM_FIRST_CHUNK_TIMEOUT` | duration |
| `EMPTY_PEEK_DEADLINE_MS` | `STREAM_EMPTY_PEEK_DEADLINE` | duration |
| `FETCH_CONNECT_TIMEOUT_MS` | `UPSTREAM_CONNECT_TIMEOUT` | duration |
| `NINEROUTER_TLS_ECDH_CURVE` | `UPSTREAM_TLS_CURVE` | same intent; this program's own name and only two values |
| `FETCH_KEEPALIVE_TIMEOUT_MS` | — | not read: the Go pool's idle timeout is fixed and a dropped socket costs one replay |
| `FETCH_FRESH_RETRY` | — | not read: the replay has no kill switch |
| `VIDEO_FETCH_TIMEOUT_MS` | `MEDIA_VIDEO_TIMEOUT` | duration; planned, step 1.11 |
| `DISABLE_BACKGROUND_TOKEN_REFRESH` | `OAUTH_BACKGROUND_REFRESH` | polarity inverted: `true` enables, default `true` |
| `BG_REFRESH_DELAY_MS` | `OAUTH_REFRESH_DELAY` | duration; pause between two accounts in a background pass (not a start delay; the first pass always waits 10 s) |
| `BG_REFRESH_GOOGLE_DELAY_MS` | `OAUTH_REFRESH_GOOGLE_DELAY` | duration |
| (browser-built redirect) | `OAUTH_CALLBACK_HOST` | new: fixed-port login listener bind address (default `127.0.0.1`; `0.0.0.0` in a container) |
| `SEARXNG_URL` | `MEDIA_SEARXNG_URL` | planned, step 1.11 |
| `HEADROOM_URL` | `SAVER_HEADROOM_URL` | planned, step 3.1 |
| `MODEL_CATALOG_SYNC` (`off` disables) | `CATALOG_SYNC` | boolean: `false` disables; planned, step 2.6 |
| `AZURE_ENDPOINT`, `AZURE_DEPLOYMENT`, `AZURE_API_VERSION`, `AZURE_ORGANIZATION` | `PROVIDER_AZURE_*` | planned, step 1.10 |
| `KIMI_OAUTH_CLIENT_ID`, `KIMI_CODING_OAUTH_CLIENT_ID` | `OAUTH_CLIENT_ID_KIMI`, `OAUTH_CLIENT_ID_KIMI_CODING` | |
| `MIMO_PLATFORM_URL`, `COMMAND_CODE_API_BASE_URL` | `PROVIDER_MIMO_PLATFORM_URL`, `PROVIDER_COMMANDCODE_BASE_URL` | planned, step 1.10 |
| OAuth client ids and secrets hardcoded in `open-sse/providers/registry/*.js`, `shared.js`, `services/nous.js` | `OAUTH_CLIENT_ID_<PROVIDER>` and `OAUTH_CLIENT_SECRET_<PROVIDER>` (18 variables, listed in `.env.example`) | diverged: configuration, not code |

---
### 2. Not Read

| 9router variable | Status |
|---|---|
| `ENABLE_REQUEST_LOGS`, `OBSERVABILITY_*` | out (usage always recorded, metadata only) |
| `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` (and lowercase) | deferred (transports built with no proxy) |
| `NINE_ROUTER_PASSWORD`, `NINE_ROUTER_API_KEY` | out (no remote CLI mode) |
| `STRICT_SSL` | out (TLS always verified) |
| `REQUIRE_API_KEY` | out (the reference never reads it) |
| `NODE_ENV`, `NEXT_*`, `BASE_URL`, `CLOUD_URL`, `INSTANCE_NAME` | out |
| `TUNNEL_*`, `CLOUDFLARED_PROTOCOL`, `MITM_*`, `DEBUG_MITM`, `UPDATER_*`, `TRAY_MODE`, `NINEROUTER_*`, `NINE_ROUTER_PROXY_*` internals, `DEVIN_*`, `CLI_DEVIN_*`, `OBSCURA_*`, `NINEROUTER_BROWSER_POOL`, `MINIMAX_DESKTOP_DATA_DIR`, `APP_BUILD_MD5` | out |

---
### 3. New Variables

No reference counterpart: `HTTP_SERVER_READ_HEADER_TIMEOUT`, `HTTP_SERVER_IDLE_TIMEOUT`, `HTTP_SERVER_SHUTDOWN_GRACE`, `SERVER_MAX_BODY_MB`, `SERVER_TRUST_LOOPBACK`, `SERVER_TRUSTED_PROXIES`, `AUTH_SSO_ALLOW`, `UPSTREAM_ALLOW_PRIVATE`, `DB_CONNECTION`, `DB_DATABASE`, `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD`, `DB_SCHEMA`, `DB_ENCRYPTION_SECRET`, `LOG_CHANNEL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB`, `DB_DATABASE_ANALYTICS`, `DB_SSLMODE`, `ANALYTICS_RETENTION_DAYS`. Framework-owned `APP_KEY` (32 characters) and `APP_URL` (public gateway address, for OIDC and SAML) as Goravel defines them; `APP_NAME`, `APP_ENV`, `APP_DEBUG` too, no product meaning.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 26 | Goravel config loader via `config/*.go`; `config.Boot` checks size cap, BOM, unknown names first; never quotes a value; `$` needs single quotes | Predictable format |
| 194 | Goravel-owned names as framework defines them (`APP_*`, `JWT_SECRET`); every other variable `<GROUP>_<NAME>`, no prefix (`AUTH_API_KEY_SECRET`, `AUTH_MACHINE_ID_SALT`, `HTTP_SERVER_*`). `JWT_SECRET` keeps its name; values of `API_KEY_SECRET`, `MACHINE_ID_SALT` copy over unchanged | One naming contract, group-owned unknown-name check, framework names. Risk: shared host may carry another program's `DB_*`/`LOG_*`, so only `.env` entries scanned |
| 201 | Retired: logging is the `Log` facade with its redacting channel | Recorded so the number stays citable (framework choice) |
| 6, 10, 105 | Not read, implemented, adopted; settings keys kept and imported | Dead or unsafe (scope cut) |
| 28 | All log output through one redactor. **(partial)** the redacting channel is built and tested; the Console Log ring and its read route are not, and arrive with 2.6 | Errors, headers can carry tokens |
| 33 | All `<GROUP>_<NAME>`, Go durations; old names not read | One contract; operators rename (PRD 17) (trade-off) |

---

## Findings

Viper treats a set-but-empty value as unset, so a default applies where an operator may expect an empty string.
The framework exposes no key list for `.env`, so `config.Boot` reads the file with `subosito/gotenv` for names, the size cap and the BOM check.
