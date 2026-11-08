# PRD 06 — OAuth & Token Refresh (`app/oauth/`)

**Status:** partial · **Steps:** 1.9a, 1.9b, 1.9c

Step 1.9 brings the framework and the two seed providers (`claude`, `codex`); the rest add a data file and post-login hooks in 1.10. It is built in three parts: **1.9a** the login (sessions, token client, routes, callback page, listener; built), **1.9b** the refresh (proactive, reactive, background, the expired mark, the connection test; built, and a login's token now goes as a bearer token), **1.9c** the headers a login's token carries, the client tool and the Claude disguise (built).

### Connect Flows

A provider's flow is data (`oauth.flow` in its file), run by one engine per kind.

`app/oauth` holds the `code-pkce` flow since 1.9a (claude and codex). Every other flow below is the design step 1.10 implements.

| Flow | Providers (examples) | Step |
|---|---|---|
| `code-pkce`: authorization code with PKCE | claude, codex, gitlab, xai | 1.9 for claude and codex; 1.10 for the rest |
| `code`: authorization code | gemini-cli, antigravity, iflow, cline, trae, windsurf, zed | 1.10 |
| `device` | github (then a Copilot token), kiro, kimi, kilocode, qoder, glm, grok-cli, nous, codebuddy, minimax-code, freebuff | 1.10 |
| `paste`: token or cookie paste | cursor, kimchi, iflow cookie, web-cookie providers, API keys | 1.10 |
| Bulk import | codex, grok-cli | 1.10 |

There is no browser launch, so the dashboard shows the URL to open. The only process the router ever starts is `git clone`, through the filter gate in `app/git` (`PRD 11`), for the skill update (Skills Studio); the gate does not exist yet.

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
| Session | `oauth.Sessions`, an in-process map (not the `oauth` cache group: `Begin` has to be one atomic step, which the cache facade cannot give, and the group is memory-only anyway): state, provider, redirect, verifier, fixed port and a binding (device code and interval join in 1.10). At most 1000 logins and 20 per binding; the oldest of a full binding is dropped. The state is 256 random bits. TTL 10 minutes; `Take` makes it single use. The binding is `sso.BindFor(secret, "oauth_bind", <auth_token cookie>)`, so another browser gets the same answer as an unknown state. A login finished by the fixed-port listener has no dashboard cookie to compare; the 256-bit state, which only the provider's redirect brings back, and the listener's port are the proof, and the dashboard still reads the outcome only with its own binding. After the end, a `done` record (connection id or a short error code, no secret) lives 2 minutes and is read once through `status` |
| Routes | All `ClassAlwaysProtected`, behind the CSRF guard. `GET /api/oauth/{provider}/authorize`, `POST /api/oauth/exchange` (`{state, input}`, `input` being a full URL with the code in the query or fragment, a bare code or `code#state`), `POST /api/oauth/callback` (what the `/callback` page posts), `GET /api/oauth/status?state=`, `GET /api/oauth/{provider}/device-code`, `POST /api/oauth/poll`. No route returns a token, a secret or a verifier |
| `/callback` page | `GET /callback`, public, static (`web/static/callback.html` and `web/static/js/callback.js`, following the dashboard layout in `WORKFLOWS.md`). It reads `code`, `state` and `error` from the query or fragment, removes them from the address, and posts them to the server. No `postMessage`, `BroadcastChannel` or storage |
| Listeners | One per fixed port, shared by every login waiting on it, bound to `OAUTH_CALLBACK_HOST` (`127.0.0.1`; `0.0.0.0` in a container). They serve only the callback path, only for a loopback `Host`, only for a state a pending login holds on that port, and finish that login server side. They stop when the last login ends (checked on every release and every 15 s, never within 5 s of the last use) and with the server (`rllmx.oauth` runner). A busy port gives `paste: true` |
| Token calls | `oauth.TokenClient`: JSON or form per the data, answers bounded to 1 MiB, no redirect followed (a moved endpoint would carry the refresh token elsewhere), 15 s per call, on the guarded dialer. Built in 1.9a for the code exchange; the refresh call joins in 1.9b. Failures are reduced to a class (built: `invalid_grant`, `transport`, `rejected`, `malformed`; `refresh_token_reused` is folded into `invalid_grant` and the device classes arrive in 1.10; the login adds the fixed codes `access_denied`, `invalid_input` and `storage`); provider text is never kept, logged or returned. The PKCE verifier (32 bytes from `crypto/rand`), the S256 challenge (`sha256` then base64url, both stdlib) and the authorize URL are our own code; `golang.org/x/oauth2` is not a dependency. Token calls are ours too, because Claude's endpoint takes JSON and a refresh needs the raw error body for classification |
| Client credentials | Configuration (`OAUTH_<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET`), never literals in Go code or provider data (`tests/oauth_clients_test.go` scans `app`, `bootstrap`, `config`, `routes`, `pkg` and `web` for the two public ids by hash, whole words only; `.env.example` carries them). An empty id disables that provider's login and refresh with a clear error before anything is sent. Secrets are redacted in logs by name and never returned by the API |
| Account | Codex reads the email, the ChatGPT account id and the plan from the id token (no signature check: the claims name the account and decide duplicates). A login that repeats an account updates its row by the repository's duplicate rules |

### Refresh

All four triggers use `oauth.Refresher.Refresh` (built in 1.9b). Per provider, the registry's `oauth.refresh` block gives the body encoding (`json` for Claude and Codex), the lead time and, for Codex, the age rule; its `oauth.headers` are the static headers sent on a login's token.

| Trigger | When |
|---|---|
| Proactive | Before each request, when the access token is within the provider's lead time (default 5 minutes; Claude 4 h, Codex 10 min, which also refreshes when the last refresh is older than 8 days). A failure other than a dead token lets the request go on with the old one |
| Reactive | On a 401 or 403 from the upstream: up to three tries (1 s and 2 s apart), the credentials are stored, the same account is called once more. If that fails too, the normal failure path runs on the new answer |
| Background | A loop in the `rllmx.oauth` runner (not a cron job: the schedule facade has no seconds-level first run), started once the data gate opens: first pass after 10 s, then every 5 minutes, one account at a time, for active accounts with a refresh token and a known expiry within the lead time or 30 minutes, whichever is longer. The pause between accounts is `OAUTH_REFRESH_DELAY` (default 1500 ms; `OAUTH_REFRESH_GOOGLE_DELAY` joins in 1.10 with the first provider that sets `slowRefresh`). `OAUTH_BACKGROUND_REFRESH=false` turns it off. A failure logs the provider and the class only |
| Test | `POST /api/providers/{id}/test` for a registry entry with an `oauth` block: refresh when due, then check the token exists and has not expired. A provider without one still answers `test not supported` and writes nothing |

**Safety.** Calls for the same provider and refresh token share one request through `app/cache.Load` on the `refresh` group (single-flight lives there, per AGENTS), with the result kept for 10 s in the memory-only `refresh` group, so 50 concurrent requests make one call. The new credentials are stored by `ConnectionsRepo.SwapCredentials`, one transaction that writes only while the stored refresh token is still the one used. If another request or process rotated it first, nothing is written and the stored row is used; single-use tokens (xAI, MiniMax, Codex) are never spent twice. `invalid_grant` (which includes a reused token) marks the connection `testStatus: expired` with the fixed message "needs to be reconnected", under the same condition (`ConnectionsRepo.MarkExpired`); account selection skips it and nothing calls the provider for it again. **Only a reconnect clears the mark** (the login writes `active`): the refresh token is dead, so neither a refresh nor a test can heal it, and a passing test never overwrites it. A dead account met mid-request is excluded without a lock, since the mark is the stronger state. A request that meets only dead accounts answers **503** "[provider/model] needs to be reconnected", error class `expired`, with no retry hint: a 401 would make the client discard a good router key.

**What a refused request does.** On a 401 or 403 from a login's token the pipeline asks for a renewal (up to three tries, one and two seconds apart, and no call at all when the stored access token is no longer the refused one), then sends the same body once more with the new token. A second refusal is an ordinary account failure. Any other status never triggers a renewal.

---

## Remaining work
**Step 1.9** (1.9a, 1.9b and 1.9c built), framework, claude and codex only. The reference builds the redirect as `http://localhost:<window.location.port or 443>/callback` in the browser, so behind HTTPS it becomes `http://localhost:443/callback`.

- [x] OAuth token check and renewal on the connection test route (registry entries with an `oauth` block; the rest answer "test not supported") (1.9b)
- [x] The server decides the redirect: `fixed` and `local` are `localhost`; forwarded headers never used, the browser never sends `redirect_uri` (D151). `public` (`APP_URL` + `/callback`) is 1.10, as no seed provider uses it (1.9a)
- [x] Client credentials only from `OAUTH_*`; no literal in Go code, provider data or web files; empty id disables login with a clear error; no route returns a secret or verifier. The refresh half is 1.9b (1.9a)
- [x] Server-side session (`oauth.Sessions`): state, provider, redirect, verifier, dashboard binding, 10-minute TTL, single use; state verified on every completion path (D150). Device code is 1.10 (1.9a)
- [x] Paste endpoint (`POST /api/oauth/exchange`) accepting a redirected URL (query or fragment), a bare code, and `code#state` (1.9a)
- [x] `/callback` page posts to the server with the state; no `postMessage` (D152) (1.9a)
- [x] Fixed-port listeners (codex 1455), shared per port, Host and state checked, stopped with the last login, bind `OAUTH_CALLBACK_HOST` (D153) (1.9a)
- [x] Token refresh: proactive, reactive on 401/403, background every 5 minutes; the expired mark (1.9b)
- [x] A login's token goes as `Authorization: Bearer` with the provider's static headers; an API key keeps its scheme (1.9b)
- [x] The computed `anthropic-beta` list, `x-claude-code-session-id`, the identity headers and URL suffix, the Codex headers, client tool detection and the Claude disguise (1.9c; the design is in PRD 7). The Codex request *body* (forced stream, `store:false`, instructions, allowlist, lite mode, `/compact`) is 1.10
- [ ] Step 1.10: device code, the remaining redirect policies, and the Google family, Zed, kiro `kiro://`, Windsurf, Trae and Mimo token shapes
- [x] Token refresh detail: one call per provider and refresh token through `app/cache.Load` with a 10s result cache; `invalid_grant` and reuse mark the connection (`testStatus: expired`) and stop retrying

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
| 157 | No OAuth block (or a pasted access token): still `test not supported`, writes nothing; OAuth connection: renew when due, then check a token is held, with three fixed messages (`needs to be reconnected`, `token refresh failed`, `no access token`) and no call to the provider | Unrun test never marks healthy account failed |
| 65 | 10 s, 3 redirects, no https to http, no proxy, 1 MiB; nine fixed codes | Hostile provider cannot hold server or write into login page |
| 152 | Page posts code and state to server; no `postMessage`, channel, storage; address replaced in history first; dashboard polls `status`, readable once, starting browser only | Another page cannot inject a code; one-time code out of browser storage |
| 155 | `testStatus: expired` with fixed message; selection skips it; only a reconnect clears it (a dead refresh token cannot be healed by a refresh or a test); mark written only while dead token still stored | Dead account stops costing a failed call per request; shows on dashboard |
| 156 | One refresh path for request, 401 retry, background pass, connection test: one call per provider and refresh token, 10 s result; credentials stored only while stored refresh token still the one used (compare and swap, one transaction), so two processes cannot both spend a single-use token | Refresh storm costs one call; rotating tokens spent once |
| 182 | One flight per account; token in bounded cache, stored with account | Burst makes one GitHub call, not one per request |
| 172 | Step ends login with fixed message; provider text never kept or shown | Provider cannot put its own text or links into the dashboard |

---

## Findings

**1.9a built: login only.** A Claude or Codex account can be connected by `GET /api/oauth/{provider}/authorize`, then the callback page, the fixed-port listener or a pasted address. Nothing refreshes a token yet (1.9b) and an OAuth token is still sent as an API key (1.9c), so a connected account does not serve requests until those land.

**The reference trusts the browser; we do not.** It sends the verifier and redirect through the browser, never compares the returned `state`, and lets three channels carry the code. Here the verifier never leaves the process, `Begin` is single use, and a completion needs the state and the binding.

**The listener cannot check the dashboard cookie.** It lives on another port, so a login it finishes is proven by the secret state and the port alone. Cookies do not isolate by port, so a `localhost` dashboard would send its cookie there, but a `127.0.0.1` one would not; relying on it would make the flow depend on how the dashboard was opened.

**An equivalent check was removed.** `routerPort` once re-checked that the request Host was loopback; `IsLocalRequest` already requires it, so the mutant that removed the second check could not fail any test and the check went.

**1.9b built: refresh.** A token is renewed before a request when due, again after a 401 or 403, in the background, and when the account is tested. A login's token is now sent as a bearer token with the provider's static headers (Claude: `anthropic-beta: oauth-2025-04-20`, `anthropic-version`); the computed beta list and the Codex headers are 1.9c.

**A dead refresh token is stored, not retried.** The reference never records `invalid_grant`, so it calls the provider after every cooldown for good. `MarkExpired` writes the mark under the same stored-token condition as the swap, and a test proves exactly one swap wins a race and that a mark never lands on a rotated token. A mark that lands first does not stop a swap of the same token, which only a provider answering both ways could cause. The mark is the stronger state, so a dead account met in a request gets no lock on top.

**Parallel requests make one call.** `cache.Load` collapses concurrent renewals of one refresh token and keeps the result for 10 s; the loader also re-reads the row first, so a token another process already rotated costs no call.

**Two gaps the mutants found.** A non-auth error on a login renewed nothing in the code but nothing proved it, and `Ensure` could have passed an expired account; both have tests now.

**Not verified without a live account:** that Anthropic accepts the single `oauth-2025-04-20` beta value alone, and that Codex takes a JSON refresh (the reference registry says form; the code it runs sends JSON).

`app/git` is still open.

**1.9c built: what a login's request looks like.** The provider's data decides every header: the identity set, the URL suffix, the way the beta list is computed, the session header (Claude Code's own, or a keyed hash for Codex) and the account header (`ChatGPT-Account-ID` from the first of `workspaceId`, `chatgptAccountId`, `accountId` that the account holds). The pipeline computes the values and the request headers it reads are an allow-list (`pipeline.PickHeaders`), so a client header cannot reach an upstream. A Codex request now carries the right headers but still the pre-1.10 body, so it will likely be refused until 1.10.

**The session sent to Codex is never the client's.** A keyed hash of the session and the account is sent, so one client cannot choose the value another client's conversation uses (PRD 11).
