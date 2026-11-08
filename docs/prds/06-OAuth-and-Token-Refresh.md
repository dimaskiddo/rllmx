# PRD 06 — OAuth & Token Refresh (`app/oauth/`)

**Status:** todo · **Steps:** 1.9

Step 1.9 brings the framework and the two seed providers (`claude`, `codex`); the rest add a data file and post-login hooks in 1.10.

### Connect Flows

A provider's flow is data (`oauth.flow` in its file), run by one engine per kind.

`app/oauth` does not exist yet: every flow below is the design step 1.9 (framework, `claude`, `codex`) and 1.10 (the rest) implements.

| Flow | Providers (examples) | Step |
|---|---|---|
| `code-pkce`: authorization code with PKCE | claude, codex, gitlab, xai | 1.9 for claude and codex; 1.10 for the rest |
| `code`: authorization code | gemini-cli, antigravity, iflow, cline, trae, windsurf, zed | 1.10 |
| `device` | github (then a Copilot token), kiro, kimi, kilocode, qoder, glm, grok-cli, nous, codebuddy, minimax-code, freebuff | 1.10 |
| `paste`: token or cookie paste | cursor, kimchi, iflow cookie, web-cookie providers, API keys | 1.10 |
| Bulk import | codex, grok-cli | 1.10 |

There is no browser launch, so the dashboard shows the URL to open. The only process the router ever starts is `git clone`, through the filter gate in `app/git` (`PRD 11`), for the skill update (Skills Studio); neither the gate nor the OAuth package exists yet.

### Login Data and Steps (step 1.10 D)

Nothing provider specific is code that data cannot name. A provider file's `oauth` block picks from fixed tables, and a test keeps each table equal to the schema's allowed names.

| Data field | Meaning |
|---|---|
| `hook` | A step after the tokens arrive (`app/oauth/hooks.go`): `codex`, `github`, `gitlab`, `grokCli`, `iflow`, `muse`, `nous`, `xai`. A step returns the email, the name, extra `providerSpecificData` and stored fields. A fatal step (`iflow`, `muse`) ends the login with a fixed message and never repeats provider text; a best-effort step (`github`, `gitlab`, `grokCli`, `nous`) keeps what it has |
| `dialect` | A login whose calls do not follow RFC 8628 (`app/oauth/dialect.go`): device style `kilocode`, `codebuddy`, `minimax`, `glm`; code style `cline`, `iflow`. A dialect builds the start answer and the poll, never the connection: the engine stores it |
| `headers`, `deviceIdHeader`, `basicAuth`, `exchangeEchoState` | Extra headers on the device, exchange and refresh calls (the content type, accept and credential headers cannot be set), a per-account device id made at the start of a device login, stored in `providerSpecificData.deviceId` and sent again on every refresh, Basic client authentication, and the login state in the exchange body (Claude only) |
| `endpoints` | Addresses a step calls, by role. Each must be https |
| `refresh.fields`, `tokenHeader`, `bare`, `tokenField`, `camel` | Extra refresh fields, the refresh token also in a header, a body with only those fields, the name that carries the token, and a camelCase answer with a `code` that is 0 on success |
| `noClient` | A login with no OAuth client id (kilocode, codebuddy, glm, cline) |
| `appFromAccount` | The OAuth application and server address come with the login request (GitLab). A `POST /api/oauth/{provider}/authorize` body `{baseUrl, clientId, clientSecret}` is read only for such a provider and refused for any other. The address must be a bare https origin, the id and secret at most 256 characters without control characters or spaces. They live in the memory session, the secret is sealed with the account (`providerSpecificData.clientSecret`), and nothing returns them |
| `redirectHost` | `localhost` (default) or `127.0.0.1` for an application registered that way (xai) |

The Copilot token the `github` step mints is stored sealed (`providerSpecificData.copilotToken`), and the Muse and Nous steps store the key that chat requests use as the access token or API key. Meta's account token is not kept.

### Redirect Policy

The reference builds the redirect in the browser as `http://localhost:<window.location.port or 443>/callback`, so behind HTTPS it becomes `http://localhost:443/callback`, and the browser sends `redirect_uri` to the server. Here the **server decides** the redirect and the browser never sends one. A forwarded header or the request origin never builds a redirect.

A redirect must be one the provider's OAuth client registered. Claude and Codex log in with the public client ids of the Claude Code and Codex command line tools, which accept loopback redirects only, so their redirect is a `localhost` address even when the dashboard is on an HTTPS host. This is the link between the router, as OAuth client, and the provider. A Claude Code CLI that uses the router takes no part: it calls `/v1/*` with a router API key. Cloaking shapes later upstream requests and has no part in the login.

| Policy (`oauth.redirect`) | Redirect | Providers |
|---|---|---|
| `fixed` | `http://localhost:<fixedPort><callbackPath>`, served by a listener | codex (`1455/auth/callback`), xai (`56121/callback`) |
| `local` | `http://localhost:<router port>/callback`; the port is the request's own when the request is local, else `APP_PORT` | claude; the Google family and Zed later |
| `public` | `${APP_URL}/callback`; an error when it is unset and the request is remote | gitlab and others whose provider accepts it |
| none (device flow) | The dashboard shows the verification URL and code | github, kimi, kilocode, qoder and others |

When the browser that finishes a `fixed` or `local` login cannot reach `localhost` of the router (a remote dashboard), the start answer says `paste: true`: after consent the browser lands on a dead page and the user pastes the address from its address bar. The deployer does not register anything for the shipped client ids. Replacing a client id with an app of one's own means registering the redirect the start answer shows. Whether Claude also accepts its hosted code page is still unchecked.

| Item | Behavior |
|---|---|
| Session | `oauth.Sessions` over the `oauth` cache group, which `app/cache` pins to the memory store and never sends to Redis: state, provider, redirect, verifier, flow, fixed port, device code and interval, and a binding. The state is 256 random bits. TTL 10 minutes; `Take` makes it single use. The binding is an HMAC of the `auth_token` cookie under the session key, so another browser gets the same answer as an unknown state. After the end, a `done` record (connection id or a short error code, no secret) lives 2 minutes and is read once through `status` |
| Routes | All `ClassAlwaysProtected`, behind the CSRF guard. `GET /api/oauth/{provider}/authorize`, `POST /api/oauth/exchange` (`{state, input}`, `input` being a full URL with the code in the query or fragment, a bare code or `code#state`), `POST /api/oauth/callback` (what the `/callback` page posts), `GET /api/oauth/status?state=`, `GET /api/oauth/{provider}/device-code`, `POST /api/oauth/poll`. No route returns a token, a secret or a verifier |
| `/callback` page | `GET /callback`, public, static (`web/static/callback.html` and `web/static/js/callback.js`, following the dashboard layout in `WORKFLOWS.md`). It reads `code`, `state` and `error` from the query or fragment, removes them from the address, and posts them to the server. No `postMessage`, `BroadcastChannel` or storage |
| Listeners | One per fixed port, shared by every login waiting on it, bound to `OAUTH_CALLBACK_HOST` (`127.0.0.1`; `0.0.0.0` in a container). They serve only the callback path, only for a loopback `Host`, only for a state a pending login holds on that port. They stop when the last login ends. A busy port gives `paste: true` |
| Token calls | `oauth.tokenClient`: JSON or form per the data, answers bounded to 1 MiB, no redirect followed (a moved endpoint would carry the refresh token elsewhere). Failures are reduced to a class (`invalid_grant`, `refresh_token_reused`, `authorization_pending`, `slow_down`, `access_denied`, `expired_token`, `transport`, `rejected`, `malformed`); provider text is never kept, logged or returned. The PKCE verifier (32 bytes from `crypto/rand`), the S256 challenge (`sha256` then base64url, both stdlib) and the authorize URL are our own code; `golang.org/x/oauth2` is not a dependency. Token calls are ours too, because Claude's endpoint takes JSON and a refresh needs the raw error body for classification |
| Client credentials | Configuration (`OAUTH_<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET`), never literals in Go code or provider data (a test scans `app`, `bootstrap`, `config`, `routes`, `pkg` and `web` by hash). An empty id disables that provider's login and refresh with a clear error before anything is sent. Secrets are redacted in logs by name and never returned by the API |
| Account | Codex reads the email, the ChatGPT account id and the plan from the id token (no signature check: the claims name the account and decide duplicates). A login that repeats an account updates its row by the repository's duplicate rules |

### Refresh

All four triggers use `oauth.Refresher.Refresh`.

| Trigger | When |
|---|---|
| Proactive | Before each request, when the access token is within the provider's lead time (default 5 minutes; Claude 4 h, Codex 10 min, which also refreshes when the last refresh is older than 8 days). A failure other than a dead token lets the request go on with the old one |
| Reactive | On a 401 or 403 from the upstream: up to three tries (1 s and 2 s apart), the credentials are stored, the same account is called once more. If that fails too, the normal failure path runs on the new answer |
| Background | Job `oauth-refresh`, every 5 minutes (first run after 10 s), one account at a time, for tokens within the lead time or 30 minutes of expiry, whichever is longer. The pause between accounts is `OAUTH_REFRESH_DELAY` (`OAUTH_REFRESH_GOOGLE_DELAY` for providers that set `slowRefresh`). `OAUTH_BACKGROUND_REFRESH=false` turns it off |
| Test | `POST /api/providers/{id}/test` for a registry entry with an `oauth` block: refresh when due, then check the token exists and has not expired. A provider without one still answers `test not supported` and writes nothing |

**Safety.** Calls for the same provider and refresh token share one request through `app/cache.Load` on the `refresh` group (single-flight lives there, per AGENTS), with the result kept for 10 s in the memory-only `refresh` group, so 50 concurrent requests make one call. The new credentials are stored by `ConnectionsRepo.SwapCredentials`, one transaction that writes only while the stored refresh token is still the one used. If another request or process rotated it first, nothing is written and the stored row is used; single-use tokens (xAI, MiniMax, Codex) are never spent twice. `invalid_grant` and `refresh_token_reused` mark the connection `testStatus: expired` with a fixed message, under the same condition; account selection skips it, and a good refresh, a reconnect or a good test clears the mark. A request that meets only dead accounts answers 401 "needs to be reconnected".

---

## Remaining work
**Step 1.9**, framework, claude and codex only. The reference builds the redirect as `http://localhost:<window.location.port or 443>/callback` in the browser, so behind HTTPS it becomes `http://localhost:443/callback`.

- [ ] OAuth token check and refresh-on-test for the connection test route (registry entries with an `oauth` block; the rest answer "test not supported")
- [ ] The server decides the redirect: `fixed` and `local` are `localhost`, `public` is `APP_URL` + `/callback`; forwarded headers never used, the browser never sends `redirect_uri` (D151)
- [ ] Client credentials only from `OAUTH_*`; no literal in Go code, provider data or web files; empty id disables login and refresh with a clear error; no route returns a secret or verifier
- [ ] Server-side session (`oauth.Sessions`): state, provider, redirect, verifier, device code, dashboard binding, 10-minute TTL, single use; state verified on every completion path (D150)
- [ ] Paste endpoint (`POST /api/oauth/exchange`) accepting a redirected URL (query or fragment), a bare code, and `code#state`
- [ ] `/callback` page posts to the server with the state; no `postMessage` (D152)
- [ ] Fixed-port listeners (codex 1455), shared per port, Host and state checked, stopped with the last login, bind `OAUTH_CALLBACK_HOST` (D153)
- [ ] Step 1.10: device code, the remaining redirect policies, and the Google family, Zed, kiro `kiro://`, Windsurf, Trae and Mimo token shapes
- [ ] Token refresh: proactive, reactive on 401/403, background every 5 minutes; one call per provider and refresh token through `app/cache.Load` with a 10s result cache; `invalid_grant` and reuse mark the connection (`testStatus: expired`) and stop retrying

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 20, 21, 34 | Redirect per provider from `APP_URL`; one single-use server session per login; ids and secrets as `OAUTH_*`. **(partial)** variables, cache policy exist; flows in 1.9 | Public HTTPS logins cannot redirect to localhost; state always checked; credentials rotate without rebuild |
| 150 | Server holds one session per login (state, provider, redirect, verifier, device code), memory, 10 min; browser sends provider, then state + code; state checked on every completion path; single use, bound to starting dashboard session; unknown, expired, replayed, foreign state: one generic answer | Forged or replayed callback cannot finish a login or attach an attacker's account; verifier never in URL or response |
| 151 | Per provider: fixed loopback (Codex 1455), loopback on router's own port (Claude), or `APP_URL` + `/callback`; remote browser on loopback-only provider pastes landing address; forwarded headers, request origin never build redirect | HTTPS-host login never produces wrong `localhost` redirect; dashboard says up front when paste needed |
| 153 | One listener per fixed port, shared by every waiting login; callback path only, loopback `Host` only, known state only; stops when last login ends; busy port falls back to paste; bind address `OAUTH_CALLBACK_HOST` | Concurrent Codex logins no longer collide; rebinding page cannot reach listener |
| 154 | Device code stays on server; browser polls router, which waits out provider interval, widens on `slow_down`, ends login on denial or expiry | Router cannot be made to flood a provider; code never exposed |
| 157 | No OAuth block: still `test not supported`, writes nothing; OAuth connection: token check with refresh-on-test, fixed message | Unrun test never marks healthy account failed |
| 65 | 10 s, 3 redirects, no https to http, no proxy, 1 MiB; nine fixed codes | Hostile provider cannot hold server or write into login page |
| 152 | Page posts code and state to server; no `postMessage`, channel, storage; address replaced in history first; dashboard polls `status`, readable once, starting browser only | Another page cannot inject a code; one-time code out of browser storage |
| 155 | `testStatus: expired` with fixed message; selection skips it; good refresh, reconnect, test clears it; mark written only while dead token still stored | Dead account stops costing a failed call per request; shows on dashboard |
| 156 | One refresh path for request, 401 retry, background pass, connection test: one call per provider and refresh token, 10 s result; credentials stored only while stored refresh token still the one used (compare and swap, one transaction), so two processes cannot both spend a single-use token | Refresh storm costs one call; rotating tokens spent once |
| 182 | One flight per account; token in bounded cache, stored with account | Burst makes one GitHub call, not one per request |
| 172 | Step ends login with fixed message; provider text never kept or shown | Provider cannot put its own text or links into the dashboard |

---

## Findings

Nothing built. `app/oauth`, `app/git` and the OAuth client credentials land in 1.9; until then no login, callback or refresh runs.
