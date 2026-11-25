# Router-LLM - Environment Variables

Every variable this program reads, in the order [`.env.example`](../.env.example) lists them. The process environment overrides `.env`; `.env` overrides the default in `config/`. Only `APP_KEY`, `DB_ENCRYPTION_SECRET` and a dashboard password must be set.

A value that contains a dollar sign must be wrapped in single quotes, because `$NAME` in an unquoted or double-quoted value is expanded. Never commit `.env`.

---

## Application

| Variable | Default | Detail |
|---|---|---|
| `APP_KEY` | none, **required** | Exactly 32 characters, made with `./artisan key:generate`. The framework stops at start without it. Keep it secret and keep it the same across restarts. It is the framework's key, not the key that seals provider credentials in the database. |
| `APP_NAME` | `Router-LLM` | Shown by `artisan about`. |
| `APP_ENV` | `production` | `local`, `staging` or `production`. The name is written in front of every log line. |
| `APP_DEBUG` | `false` | `true` shows more detail in the framework's own messages. Keep it `false` on any server people can reach. |

## Network

| Variable | Default | Detail |
|---|---|---|
| `APP_HOST` | `127.0.0.1` | The address the server listens on. `127.0.0.1` accepts this machine only. Use `0.0.0.0` in a container, or to accept other machines, and put an API key in front of every client. |
| `APP_PORT` | `20128` | The port. It is the port 9router uses, so clients keep their base URL. |
| `APP_URL` | empty | The address people reach the gateway at, for example `https://gw.example.com`. Set it when you sign in through OIDC or SAML from another machine; it also builds the SAML entity id. |
| `HTTP_SERVER_READ_HEADER_TIMEOUT` | `10s` | How long a client may take to send its request headers. 1s to 5m. |
| `HTTP_SERVER_IDLE_TIMEOUT` | `120s` | How long an idle keep-alive connection is kept. 1s to 1h. |
| `HTTP_SERVER_SHUTDOWN_GRACE` | `30s` | How long a shutdown lets running requests finish; a second signal exits at once. 1s to 10m. There is no whole-request timeout, because a stream runs for minutes. |
| `HTTP_COMPRESSION` | `true` | Compresses an answer worth it with Brotli or gzip, as the client asks. A stream, audio, video and an image are never compressed, and a response that already carries an encoding is left alone, so a proxy that compresses is fine. |
| `HTTP_H2C` | `false` | `true` lets the plain, non-TLS listener speak HTTP/2, for a reverse proxy that multiplexes streams to the router. With TLS configured HTTP/2 is already on. |
| `SERVER_MAX_BODY_MB` | `128` | The largest client API request body. |
| `SERVER_TRUST_LOOPBACK` | `true` | `true` treats a request from this machine as local. Set `false` behind a tunnel or reverse proxy on this same machine (cloudflared, ngrok, `tailscale serve`, nginx), because its traffic arrives from 127.0.0.1. |
| `SERVER_TRUSTED_PROXIES` | empty (loopback only) | Addresses or CIDR ranges, comma separated, whose `X-Forwarded-For` and `X-Forwarded-Proto` are believed. |

## Auth

| Variable | Default | Detail |
|---|---|---|
| `AUTH_ADMIN_USERNAME` | `admin` | The username the administrator signs in with. Any other name is a standard user. |
| `AUTH_INITIAL_PASSWORD` | empty | The dashboard password accepted while none is stored. Empty uses the built-in default, which is public: a remote login on it is refused until you change it. |
| `AUTH_API_KEY_SECRET` | empty | The checksum secret of an API key. Copy the 9router `API_KEY_SECRET` value, or keys migrated from it stop reading as "ok" in `keys:list`. |
| `AUTH_MACHINE_ID_SALT` | empty | Salt of the machine id inside every new key. Same rule as `AUTH_API_KEY_SECRET`. |
| `AUTH_COOKIE_SECURE` | `false` | `true` forces the Secure flag on the dashboard cookie. Leave it `false` when TLS ends at this process or at a proxy listed in `SERVER_TRUSTED_PROXIES`. |
| `AUTH_REFUSE_DEFAULT_PASSWORD` | `false` | `true` refuses to start while the dashboard password is still a public default. |
| `AUTH_SESSION_TTL` | `24h` | How long a dashboard session lasts before the next sign-in, as a duration such as `12h` or `90m`. 5m to 720h. A shorter value applies to new sessions. |

## Auth: single sign-on

| Variable | Default | Detail |
|---|---|---|
| `AUTH_SSO_ADMIN` | empty | Who signs in as the administrator through the configured identity provider: exact email addresses or `@domain`, comma separated, no wildcards. **Every other account the provider authenticates is admitted as a standard user**, so on a public provider the dashboard is as open as that provider's own registration. Empty gives nobody admin through SSO. An OIDC token without an `email` claim may fall back to `preferred_username`; such an address must already belong to a user, because the claim is editable. |
| `AUTH_MAX_USERS` | `200` | The most standard users the list may hold, whether an admin created them or single sign-on provisioned them. 1 to 100000. |

Both modes are built, and password login keeps working unless SSO-only mode is chosen **and** that SSO is fully set up.

- **OIDC:** issuer URL, client id and client secret in the settings row; register `https://<your address>/api/auth/oidc/callback` at the provider and set `APP_URL`.
- **SAML:** sign-on URL, the IdP's signing certificate and the issuer from its responses; register `https://<your address>/api/auth/saml/acs`, and offer `https://<your address>/api/auth/saml/metadata` as its metadata. Its entity id is that same address unless `samlIssuer` overrides it; a 9router import carries `urn:9router:sp` for you.
- **Sessions:** a password change, a narrowed allow list or switching the mode off ends a provider session at the next request.

## Outbound network

| Variable | Default | Detail |
|---|---|---|
| `UPSTREAM_CONNECT_TIMEOUT` | `60s` | How long one outbound connection may take to establish and to start answering. 1s to 10m. |
| `UPSTREAM_ALLOW_PRIVATE` | empty (public only) | Addresses, CIDR ranges or host names this program may reach when they are not public: loopback, the private ranges, carrier-grade NAT. Needed for a provider or identity provider on your own network, for example `10.0.0.0/8,idp.internal`. Link-local and the cloud metadata addresses are refused even when listed here. |
| `UPSTREAM_TLS_CURVE` | `x25519` | The TLS key-exchange group outbound calls offer. `x25519` offers only that group, which changes the ClientHello enough for a network that resets the standard offer; `default` offers the standard list. |
| `UPSTREAM_RATE_LIMIT_RPM` | `60` | Outbound calls a minute one connection may make. Requests beyond this queue until a slot frees; it is per connection, so a second account of the same provider adds its own allowance. `0` turns pacing off. |
| `UPSTREAM_RATE_LIMIT_BURST` | `10` | Calls that may leave at once before the rate applies. At least 1. |
| `UPSTREAM_RATE_LIMIT_MAX_WAIT` | `30s` | How long one call may queue before it is refused with a 503 instead. `0` waits without a bound. |
| `UPSTREAM_CLAUDE_CLI_VERSION` | empty | The `claude-cli/x.y.z` version the Claude fingerprint advertises. Empty reads the latest published `@anthropic-ai/claude-code` from npm every six hours, never below this build's floor; a value pins it and turns the lookup off. |
| `UPSTREAM_KIRO_CREATE_RESPONSE` | `auto` | When a Kiro request goes to Kiro's Responses transport. `auto` tries it only after the rate-limit answer the IDE transport gives accounts enrolled in it; `always` uses it for every request; `off` never does. |
| `UPSTREAM_MINIMAX_SIGN_SECRET` | empty | The key MiniMax Code signs its account calls with. It is configuration, never a literal; empty leaves that quota reading unavailable. |

The permission check runs at connect time on the address actually used, redirects included. A `http://localhost:11434/v1` node or a LAN host needs an entry (`localhost`, `192.168.1.0/24`), as does `ollama-local` on its default port. A search instance and the Headroom sidecar are permitted for their own calls, so a local one needs no entry.

## Media

| Variable | Default | Detail |
|---|---|---|
| `MEDIA_VIDEO_TIMEOUT` | `120s` | How long one video create or poll may take to answer. It bounds the HTTP round trip, not the job's own rendering time. 5s to 10m. |
| `MEDIA_SEARXNG_URL` | empty | The SearXNG instance `/v1/search` sends to, for example `http://localhost:8888/search`. Empty serves no SearXNG. |

## Token savers

| Variable | Default | Detail |
|---|---|---|
| `SAVER_HEADROOM_URL` | empty | The Headroom compression sidecar the router calls when the headroom saver is on, for example `http://localhost:8787`. It is only a default: the settings row's `headroomUrl` wins. The router never installs or starts it. |

## Agent memory

| Variable | Default | Detail |
|---|---|---|
| `HERMES_HOME` | empty | The Hermes Agent directory whose `memories/` folder the memory skill injects. Empty reads nothing. |

## OAuth logins

| Variable | Default | Detail |
|---|---|---|
| `OAUTH_BACKGROUND_REFRESH` | `true` | Renews Claude and Codex tokens in the background (a first pass 10s after start, then every 5 minutes), so a token is fresh before a request needs it. A request still renews a token that is due, and renews and retries once after the provider refuses one. |
| `OAUTH_REFRESH_DELAY` | `1500ms` | The pause between two accounts in a background pass. 0s to 1m. |
| `OAUTH_REFRESH_GOOGLE_DELAY` | `12s` | The longer pause after an account of a provider Google rate-limits renewals for (Gemini CLI, Antigravity), plus up to 4s of jitter. 0s to 1m. |

## Model catalog

| Variable | Default | Detail |
|---|---|---|
| `CATALOG_SYNC` | `true` | Refreshes the model catalog from models.dev (a first run a minute after start, then every 24 hours; a failed run retries in 30 minutes). It only adds capabilities and corrects a model's window where the gateway's published number differs from the built-in tables. `false` stops the timer; a catalog already synced into `DATA_DIR` is still loaded. `POST /api/models/catalog-sync` syncs on demand either way. |

## OAuth logins: client applications

| Variable | Default | Detail |
|---|---|---|
| `OAUTH_CALLBACK_HOST` | `127.0.0.1` | The address the Codex login listener (port 1455) binds to. `127.0.0.1` accepts this machine only; use `0.0.0.0` in a container. |
| `OAUTH_CLIENT_ID_CLAUDE`, `OAUTH_CLIENT_ID_CODEX` | the tools' published values | Client ids of the Claude Code command line tool and Codex, which their logins use. Both are public ids; empty switches that login off. A login only works on the redirect the tool registered, so replace an id only with an app of your own and register the redirect the dashboard shows. |
| `OAUTH_CLIENT_ID_GITHUB` | the plugin's published value | Client id of the GitHub Copilot editor plugin, for the GitHub device login. A public id; empty switches the login off. |
| `OAUTH_CLIENT_ID_KIMI`, `OAUTH_CLIENT_ID_MUSE`, `OAUTH_CLIENT_ID_NOUS`, `OAUTH_CLIENT_ID_XAI` | the tools' published values | Client ids of the Kimi, Meta Muse, Nous and xAI command line tools, for their device logins. All are public ids; empty switches that login off. The xAI id also serves the Grok CLI login. |
| `OAUTH_CLIENT_ID_MINIMAX_CODE` | the tool's published value | Client id of MiniMax Code, for both its sites (China and global). A public id. |
| `OAUTH_CLIENT_ID_GOOGLE`, `OAUTH_CLIENT_SECRET_GOOGLE`, `OAUTH_CLIENT_ID_ANTIGRAVITY`, `OAUTH_CLIENT_SECRET_ANTIGRAVITY` | the tools' published installed-app values | Id and secret of the Gemini CLI client and of the Antigravity client. Both are public installed-app values these tools publish, so the secret protects nothing; empty switches that login off. |

The values in `.env.example` are the vendors' published installed-app values, not this project's secrets. They are public by design and appear in every release. Replace any of them with your own app when you register one.

## Streams

| Variable | Default | Detail |
|---|---|---|
| `STREAM_FIRST_CHUNK_TIMEOUT` | `200s` | How long to wait for the first event of a streamed answer, which is the time a model needs to read a long prompt. 5s to 1h. |
| `STREAM_STALL_TIMEOUT` | `360s` | How long a stream may stay silent before it is cut and the client gets an error. 5s to 1h. |
| `STREAM_USAGE_WAIT` | `3s` | How long a finished turn waits for the usage report some upstreams send after it. 100ms to 30s. |

## Data

| Variable | Default | Detail |
|---|---|---|
| `DATA_DIR` | `storage/app` | The folder for everything the router keeps: the databases (`db/`), backups, the dashboard signing secret (`jwt-secret`), the machine id (`machine-id`) and skills (`skills/`). Created with mode 0700, files 0600. Keep it inside `storage/`, because the logs are in `storage/logs` and the container mounts `storage/` as its one volume. Use an absolute path if you set it. |
| `JWT_SECRET` | generated into `DATA_DIR/jwt-secret` | Signs the dashboard login cookie. This is the name 9router uses: copy its value to keep existing sessions valid. Empty means a random one is created once and stored in `DATA_DIR/jwt-secret`. |

## Logs

All logging goes through the framework's log channel, and an entry is cleaned of secrets before it is written. Nothing is written to a system temp folder.

| Variable | Default | Detail |
|---|---|---|
| `LOG_CHANNEL` | `single` | Where the log goes. `single`: one file, `storage/logs/rllmx.log`, never rotated. `daily`: one file per day, `rllmx-YYYY-MM-DD.log`, the last 7 kept. `stack`: the daily channel, declared in `config/logging.go`. The file name, the text or json format and the retention are set in `config/logging.go`, not here. |
| `LOG_OUTPUT` | `both` (`.env.example` ships `stdout`) | Where an entry goes. `file`: the log file only. `stdout`: the console only, for a terminal or a container's log collector, and no file is opened. `both`: both. |
| `LOG_LEVEL` | `info` | The least important entry written: `debug`, `info`, `warning`, `error`, `fatal` or `panic`. `debug` can show request details, so do not leave it on in production. |

## Database

Two data sets are kept: the configuration tables (the 9router layout) and the usage rows (analytics, this build's own layout).

| Variable | Default | Detail |
|---|---|---|
| `DB_CONNECTION` | `sqlite` | `sqlite`: the same SQLite file format 9router uses, so an existing 9router database opens as it is; nothing else needs setting. `postgres`: a PostgreSQL server; needs `DB_HOST`, `DB_USERNAME` and `DB_PASSWORD`. |
| `DB_ENCRYPTION_SECRET` | none, **required** | Seals every stored credential (client keys, provider tokens, the SSO secret) with AES-256-GCM. Exactly 64 hex characters, made with `openssl rand -hex 32`. Required to start the server and to run any `db:` command that reads a credential; `version` and `health` work without it. Keep a copy: a lost secret cannot be recovered. The first start on a plain 9router database makes a full backup before it seals anything. |
| `ANALYTICS_RETENTION_DAYS` | `90` | How long per-request usage rows are kept (1 to 36500). The daily totals are kept for good. |
| `DB_DATABASE` | empty | The configuration database. SQLite: path of the file; empty means `DATA_DIR/db/data.sqlite`. Point it at a 9router `data.sqlite` to reuse that install, and never open the same file with 9router and Router-LLM at once. PostgreSQL: the database name. |
| `DB_DATABASE_ANALYTICS` | empty | The usage database. SQLite: path of the file; empty means `DATA_DIR/db/analytics.sqlite`. PostgreSQL: the database name; empty means the same database as `DB_DATABASE`, in the schema `rllmx_analytics`. |
| `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD` | empty, `5432`, empty, empty | PostgreSQL only, ignored with SQLite. |
| `DB_SCHEMA` | `public` | Schema of the configuration tables. The usage tables always use `rllmx_analytics`. |
| `DB_SSLMODE` | `require` | `disable`, `allow`, `prefer`, `require`, `verify-ca` or `verify-full`. Use `require` or stronger on any network you do not control. |

## Cache

The default store is the in-process memory store: nothing to set, nothing to run, and nothing shared between instances. Cached values are lost on restart. It is the right choice for one instance.

| Variable | Default | Detail |
|---|---|---|
| `REDIS_HOST` | empty | Empty means memory only. Otherwise the host name or address, and the groups that may be shared (settings, key lookups, model state, response cache, tool call signatures) also use Redis or Valkey. Credentials, login sessions and refreshed tokens always stay in memory, whatever is set here. A Redis that is down is a cache miss, never a failed request. |
| `REDIS_PORT` | `6379` | The Redis or Valkey port. |
| `REDIS_PASSWORD` | empty | Empty when the server needs none. |
| `REDIS_DB` | `0` | Database number. Not used by Valkey clusters. |
| `CACHE_RESPONSE_MAX_ENTRIES` | `1000` | Where the in-process response cache stops storing (1 to 100000). With `REDIS_HOST` set, the server's own memory limit applies instead and this is ignored. |
| `CACHE_RESPONSE_MAX_MB` | `64` | The in-process response cache's size cap (1 to 4096), under the same rule. |

The response cache and its semantic layer are runtime settings, switched in the dashboard; these two limits only bound the store. Routing strategies and token savers are runtime settings as well.
