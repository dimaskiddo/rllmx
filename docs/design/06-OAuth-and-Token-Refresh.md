# Design 06 — OAuth & Token Refresh (`app/llm/oauth/`)

**Status:** done

The OAuth engine brings the framework and two seed providers (`claude`, `codex`); the rest add a data file and post-login hooks. Three parts, all built:

| Part | What it holds |
|---|---|
| Login | Sessions, the token client, the routes, the callback page and the listener |
| Refresh | Proactive, reactive and background renewal, the expired mark, the connection test; a login's token is sent as a bearer token |
| Headers | What a login's token carries, the client tool and the client-compatible request shape |

### Connect Flows

A provider's flow is data (`oauth.flow` in its file), run by one engine per kind.

`app/llm/oauth` holds the `code-pkce` flow (claude and codex), the RFC 8628 `device` flow (github), the `code` flow without PKCE (cline, clinepass) and the `code-pkce` logins xai and gitlab. The remaining flows below are built as well.

The dashboard reads a login's shape from `oauth.Service.Offer`: the flow, the paste and bulk flags, the import fields, the app flags and the variants a code dialect accepts (`CodeDialect.Variants`). It names no URL, scope or client id, and `Enabled` follows `NewFlow` exactly.

| Flow | Providers (examples) |
|---|---|
| `code-pkce`: authorization code with PKCE | claude, codex, gitlab, xai |
| `code`: authorization code, no PKCE | cline, clinepass (built); trae, windsurf, zed |
| `code-pkce` with `noPkce` and a client secret | gemini-cli, antigravity (built) |
| `device` | github (then a Copilot token) |
| `device`, with a hook or headers | kimi, muse, nous, kilocode, grok-cli |
| `device`, own dialect | codebuddy-cn, codebuddy-intl, minimax-code, minimax-code-global, glm (ZCode) |
| `device`, own dialect (AWS SSO OIDC: a registered client, then a device code) | kiro |
| `code-pkce`, own dialect and a pasted custom-scheme redirect | kiro-social (Google or GitHub through AWS Cognito) |
| `device`, own dialect | qoder, freebuff |
| `paste`: token or cookie paste | cursor, kimchi, web-cookie providers (one shared parser in `app/llm/upstream`, token or cookie, one credential per row, rotating through PUT), API keys |
| Bulk import | codex, grok-cli |

A code login always offers the paste field beside the address it opens. When the browser's callback cannot reach this gateway (no listener on a fixed port, a dead `localhost` page, a remote browser), the reader copies the address the browser landed on and `POST /api/oauth/exchange` finishes the same login from it; the field is no longer hidden when the start answer says `paste: false`. A `device` flow has no authorize URL, so `Service.Start` refuses it with `ErrUnsupported` and the dashboard's retry asks for the device code again. There is no browser launch, so the dashboard shows the URL to open. The only process the router ever starts is `git clone`, through the filter gate in `app/git` (`design doc 11`), for the skill update (Skills Studio); the gate is `app/git.Gate`.

### Device Login

A device provider's `oauth` block has `flow: device`, `deviceUrl` and `tokenUrl` (both https), an optional `scope` (a device start that names none sends none, which Meta requires), and no authorize address, redirect, exchange or parameters; a refresh block is optional (a provider that never renews its token has none).

| Item | Behavior |
|---|---|
| Start | `GET /api/oauth/{provider}/device-code` posts the client id and, only when the data names one, the scope to `deviceUrl` as a form and answers `{state, userCode, verificationUri, expiresIn, interval}`. A provider that refuses the start answers 502 with a fixed message of its own; a transport failure answers 502 and an internal one 503. The device code is kept in the server-side session and never returned. The answer must hold a device code (at most 1 KiB), a user code of at most 64 characters, and a plain https page without credentials; the interval is 1 to 60 s (default 5 s), the lifetime up to 30 min (default 15 min) |
| Poll | `POST /api/oauth/poll` with `{state}`. The session is bound to the dashboard session that started it, like the redirect flows. The router asks the provider at most once per interval whatever the browser sends, and two simultaneous polls make one call (`Sessions.Poll` hands the login to one caller and the rest read `pending`). `authorization_pending` and `slow_down` come back in a 200 body or an error status, so the body is read first; `slow_down` adds 5 s to the interval, capped at 30 s. A finished login answers once, then `unknown` |
| End | `expired_token`, `access_denied`, any other provider error or a body with no access token end the login with a fixed word (`expired_token`, `access_denied`, `rejected`, `transport`); provider text is never kept |
| Hook | After the tokens arrive, the `hook` named in the data runs from the table in `app/llm/oauth/dialects` and returns the account's email and provider data. A hook never fails a login: what it cannot learn it leaves out. A test keeps the table equal to `upstream.HookNames` |

### Copilot Token

A GitHub login token cannot call the Copilot API. The data's `mint: copilot` and `endpoints.copilotToken` make the login's token a source for a second credential, and that credential is the one a request carries.

| Item | Behavior |
|---|---|
| Mint | `GET` the endpoint with `Authorization: token <login token>` and the data's `user-agent`, `editor-version`, `editor-plugin-version` and `x-github-api-version` headers (the chat-only headers are not sent). Only `token` and `expires_at` are read; the token is at most 4 KiB with no control character |
| Keeping | `oauth.Minter` serves the stored token while more than five minutes of it remain, else mints. One mint is in flight per account (`cache.Load` on the memory-only `connections` group, 10 s) however many requests ask. The token is also written sealed to `providerSpecificData.copilotToken` with its expiry, so a restart does not mint for every account at once |
| Refusal | A 401 or 403 from the chat endpoint asks for one new token (`Minter.Renew`, which hands back a token another request already replaced without a call) and sends the same body once more. A refused token is never sent twice |
| Failure | A mint that fails is the account's failure with a fixed text (`transport` gives 502, any other 401 "no credentials"); nothing the provider said, and no token, is logged, stored in an error or returned |
| GitHub profile | `GET` the `user` endpoint with the login token. Login name, display name, email and numeric id are stored (short text only, no control characters). The email is often hidden, so a login with a `githubUserId` is the same account as an earlier row with that id (divergence 235) |

### Login Data and Steps

The engine holds the types, the token client, the sessions, the listener and the shared steps. Each provider's steps live in `app/llm/oauth/<provider>`, and `app/llm/oauth/dialects` builds the `Dialects` table they are looked up in.

Nothing provider specific is code that data cannot name: a provider file's `oauth` block picks from fixed tables, and a test keeps each table equal to the schema's allowed names.

| Group | Built |
|---|---|
| Device login | `flow: device`, `deviceUrl`, `hook: github`, `mint`, `endpoints`, `callHeaders` with the device id, `params`, `endpoints.verify`, `dialect: kilocode`, `noClient`, `refresh.tokenHeader`, `refresh.hook` |
| Hooks | `muse`, `nous`, `kilocode`, `grokCli` |
| Device dialects | `codebuddy`, `minimax`, `zcode`, a dialect renewal, `refresh.fields`, device state in the server session |
| Code flow | `flow: code`, `redirect: public`, `redirectHost`, `appFromAccount`, `pat`, `refresh.url`, the code dialect `cline`, authorize-parameter placeholders, the hooks `xai` and `gitlab` |

Rows marked *later* in the tables below are still design.

| Data field | Meaning |
|---|---|
| `hook` | A step after the tokens arrive (`app/llm/oauth/<provider>`, from the table in `app/llm/oauth/dialects`). Built: `codex`, `github`, `muse`, `nous`, `kilocode`, `grokCli`, `xai`, `gitlab`. iflow is not built (D279). A step returns the email, extra `providerSpecificData` and what it changes about the stored credential: the access token (`Access`), the key a request carries (`APIKey`), whether the login can be renewed (`NoRefresh`) and an earlier expiry (`ExpiresAt`). A step returns an error to end the login with a fixed word and never repeats provider text. Fatal: `muse` (no key, no login). Best effort: `github`, `nous`, `kilocode`, `grokCli`, `xai` (id token claims, no call), `gitlab` (`GET` the user endpoint of the account's server) |
| `dialect` | A device login whose start and poll do not follow RFC 8628 (`app/llm/oauth/<provider>`, its `DeviceDialect` in `app/llm/oauth/device_dialect.go`), or a `code` login whose exchange and renewal are not plain token calls (`app/llm/oauth/<provider>`, its `CodeDialect` in `app/llm/oauth/code_dialect.go`). Built: `kilocode`, `codebuddy`, `minimax`, `zcode`, `kiro` (device), `cline` (code), `kiro-social` (code-pkce), `kimchi` (code). iflow is not built (D279). `codebuddy` and `zcode` have no client id and no PKCE; `minimax` sends a PKCE challenge, and when the start answer has no device code the user code is polled instead. `codebuddy` also owns its renewal (the refresh token in `X-Refresh-Token`, an empty JSON body, a camelCase answer; only a 401 means the sign-in is gone). `zcode` trades the Z.ai token for a coding-plan key in the poll (business login, personal organisation and project, the key named `zcode-api-key`, made when missing, and its secret half) and stores only that key. A dialect builds the tokens, never the connection: the engine stores it. A dialect is valid only for its own flow |
| `callHeaders` | Headers on the router's own calls while logging in and renewing: the device start, the poll, the renewal and a hook. A name cannot be a credential or framing header (the same filter as `headers`); a value may hold `{deviceId}` and no other brace. When any value holds it, the start makes one id, keeps it in the server-side login, sends it, and stores it in `providerSpecificData.deviceId`, from where every renewal sends it again. An account without an id leaves that header out. `headers` stays the chat-only set |
| `params` | Extra fields, in the data's order. A device login adds them to the start form after `scope` and `client_id`; a `code-pkce` login adds them to the authorize address after the standard parameters; a `code` login sends **only** these. A `code-pkce` or device login cannot use the login's own names (`state`, `client_id` and so on); a `code` login has none, so it may. A value may hold `{nonce}` (16 random bytes in hex, new for each login), and a `code` login also `{redirect}` and `{state}`; any other brace is refused |
| `basicAuth`, `exchangeEchoState` | Later: Basic client authentication; the login state in the exchange body (Claude only) |
| `endpoints` | Addresses a step calls, by role: `copilotToken`, `user`, `key` (Meta), `agentKey` (Nous), `profile` (Kilo), `verify` (the page to open when the start answer names none, with `?user_code=` added when the answer has a plain code). Each must be https |
| `refresh.tokenHeader`, `refresh.hook` | Built: a header that also carries the refresh token (Nous), and a step run after a renewal from a table of its own (`app/llm/oauth/nous`; Nous mints a new agent key). A step that fails keeps the key the account holds. Built: `refresh.fields`, extra fields of the standard renewal call (MiniMax Code: `scope`, `audience`), at most four, never the names the call sets itself. `bare`, `tokenField` and `camel` are not needed: the one provider that wanted them (CodeBuddy) renews in its dialect |
| `noPkce`, `slowRefresh` | Built. `noPkce` on a `code-pkce` login sends no code challenge and no verifier, for a client registered without them that proves itself with a secret (Google). The secret comes from configuration (`OAUTH_CLIENT_SECRET_GOOGLE`, `OAUTH_CLIENT_SECRET_ANTIGRAVITY`, at most 256 characters, no spaces), is sent on the code exchange and every renewal, and is never logged or returned. `slowRefresh` makes the background pass wait `OAUTH_REFRESH_GOOGLE_DELAY` (12 s, 0 to 1 m) plus 0 to 4 s of random jitter after that account, instead of the common gap |
| `callHeaders` on `code-pkce` | Built. The same filter as on the device flow; the Google hooks read them |
| `noClient` | Built: a login with no OAuth client id (kilocode, cline, clinepass), so no `OAUTH_*` variable exists for it. Valid for the device and `code` flows (also codebuddy and glm) |
| `appFromAccount` | Built (gitlab). The OAuth application and server address come with the login request. A `POST /api/oauth/{provider}/authorize` body `{baseUrl, clientId, clientSecret}` is read only for such a provider and refused for any other, and the `GET` form is refused for it (answer 400). The address must be a bare https origin (`upstream.BareOrigin`: host and port, no path, query, userinfo, fragment or trailing slash), the id and secret at most 256 characters without control characters or spaces; the secret is optional. They live in the memory session (`Session.App`); on success `baseUrl`, `clientId`, `authKind: oauth` and the secret (`clientSecret`, already a sealed path) are stored in the account's provider data. `Flow.WithApp` moves `authorizeUrl`, `tokenUrl`, `refresh.url` and every endpoint onto that origin by their path. The refresher builds its flow from the stored application, so such an account renews with its own id and secret (divergence 246). Exchange and renewal send `client_secret` only when one is held. Nothing returns the secret or puts it in an address |
| `import` | Built. `POST /api/oauth/{provider}/import` with `{accessToken, machineId}` for a provider whose `oauth.fields` names that value, and a `flow` of `import`: the provider hands over its token in its own client, so nothing is called and nothing is renewed. The token is held to a credential shape and a minimum length, the JWT payload names the account and its expiry (a token that is not a JWT carries no expiry; D281), and the extra value is stored in provider data. An import login has no authorize address, redirect, exchange, dialect or renewal, and a data file that sets one fails the load. The answer is the account id only |
| `pat` | Built (gitlab). `POST /api/oauth/{provider}/pat` with `{token, baseUrl}`, for a provider whose data sets it: the token is checked against the user endpoint of the named server, presented as a bearer token (the way chat presents it), then stored with no refresh token and no expiry (`authKind: personal_access_token`). The answer is the account id only |
| `redirectHost` | Built. `localhost` (default) or `127.0.0.1`, only on the `fixed` redirect, for a client registered that way (xai) |
| `refresh.url` | Built. The renewal address when it is not `tokenUrl` (cline), https |

The Copilot token the `github` step mints is stored sealed (`providerSpecificData.copilotToken`), and the Muse and Nous steps store the key that chat requests use as the access token or API key. Meta's account token is not kept.

### Redirect Policy

The reference builds the redirect in the browser as `http://localhost:<window.location.port or 443>/callback`, so behind HTTPS it becomes `http://localhost:443/callback`, and the browser sends `redirect_uri` to the server. Here the **server decides** the redirect and the browser never sends one. A forwarded header or the request origin never builds a redirect.

A redirect must be one the provider's OAuth client registered.

- Claude and Codex log in with the public client ids of their command line tools, which accept loopback redirects only. Their redirect is therefore a `localhost` address even when the dashboard is on HTTPS.
- That is the link between the router, as OAuth client, and the provider.
- A Claude Code CLI using the router takes no part: it calls `/v1/*` with a router API key. Request shaping applies to later upstream requests, never to the login.

| Policy (`oauth.redirect`) | Redirect | Providers |
|---|---|---|
| `fixed` | `http://<redirectHost>:<fixedPort><callbackPath>` (`localhost` unless the data names `127.0.0.1`), served by a listener | codex (`localhost:1455/auth/callback`), xai (`127.0.0.1:56121/callback`) |
| `local` | `http://localhost:<router port>/callback`; the port is the request's own when the request is local, else `APP_PORT` | claude, gemini-cli, antigravity; Zed later |
| `public` | `<public base>/callback`, the base being `APP_URL` or, for a local request with no `APP_URL`, the request's own loopback host (`sso.PublicBase`); an error (400, "set APP_URL") when neither exists. `paste` is false: the router's own callback page receives it. The dashboard must be opened at the `APP_URL` origin or its cookie does not reach `/callback` | gitlab, cline, clinepass |
| none (device flow) | The dashboard shows the verification URL and code | github, kimi, kilocode, qoder and others |

When a remote dashboard's browser cannot reach `localhost`, the start answer says `paste: true`: after consent the browser lands on a dead page and the user pastes the address from its bar.

- Nothing is registered for the shipped client ids.
- Your own client id means registering the redirect the start answer shows.

| Item | Behavior |
|---|---|
| Session | `oauth.Sessions`, an in-process map (not the `oauth` cache group: `Begin` has to be one atomic step, which the cache facade cannot give, and the group is memory-only anyway): state, provider, redirect, verifier, fixed port and a binding. At most 1000 logins and 20 per binding; the oldest of a full binding is dropped. The state is 256 random bits. TTL 10 minutes; `Take` makes it single use. The binding is `sso.BindFor(secret, "oauth_bind", <auth_token cookie>)`, so another browser gets the same answer as an unknown state. A login finished by the fixed-port listener has no dashboard cookie to compare; the 256-bit state, which only the provider's redirect brings back, and the listener's port are the proof, and the dashboard still reads the outcome only with its own binding. After the end, a `done` record (connection id or a short error code, no secret) lives 2 minutes and is read once through `status` |
| Routes | All `ClassAlwaysProtected`, behind the CSRF guard. `GET /api/oauth/{provider}/authorize`, `POST /api/oauth/{provider}/authorize` (an application with the request), `POST /api/oauth/{provider}/pat`, `POST /api/oauth/{provider}/import`, `POST /api/oauth/{provider}/bulk-import` (an array, one object or `{accounts:[...]}` of tokens, one result per account, for a data file that sets `oauth.bulk`); the reference's `POST /api/oauth/codex/import-token` is the same single-token operation and is served here by `bulk-import` with one object, `POST /api/oauth/exchange` (`{state, input}`, `input` being a full URL with the code in the query or fragment, a bare code or `code#state`), `POST /api/oauth/callback` (what the `/callback` page posts), `GET /api/oauth/status?state=`, `GET /api/oauth/{provider}/device-code`, `POST /api/oauth/poll`. No route returns a token, a secret or a verifier |
| `/callback` page | `GET /callback`, public, static (`web/static/callback.html` and `web/static/js/callback.js`, following the dashboard layout in `WORKFLOWS.md`). It reads `code`, `state` and `error` from the query or fragment, removes them from the address, and posts them to the server. No `postMessage`, `BroadcastChannel` or storage |
| Listeners | One per fixed port, shared by every login waiting on it, bound to `OAUTH_CALLBACK_HOST` (`127.0.0.1`; `0.0.0.0` in a container). They serve only the callback path, only for a loopback `Host`, only for a state a pending login holds on that port, and finish that login server side. They stop when the last login ends (checked on every release and every 15 s, never within 5 s of the last use) and with the server (`rllmx.oauth` runner). A busy port gives `paste: true` |
| Token calls | `oauth.TokenClient`: JSON or form per the data, answers bounded to 1 MiB, no redirect followed (a moved endpoint would carry the refresh token elsewhere), 15 s per call, on the guarded dialer. Built for the code exchange and the refresh call. Failures are reduced to a class (built: `invalid_grant`, `transport`, `rejected`, `malformed`; `refresh_token_reused` is folded into `invalid_grant` and the device classes arrive; the login adds the fixed codes `access_denied`, `invalid_input` and `storage`); provider text is never kept, logged or returned. The PKCE verifier (32 bytes from `crypto/rand`), the S256 challenge (`sha256` then base64url, both stdlib) and the authorize URL are our own code; `golang.org/x/oauth2` is not a dependency. Token calls are ours too, because Claude's endpoint takes JSON and a refresh needs the raw error body for classification |
| Client credentials | Configuration (`OAUTH_CLIENT_ID_<PROVIDER>`, `OAUTH_CLIENT_SECRET_<PROVIDER>`), never literals in Go code or provider data (`tests/oauth_clients_test.go` scans `app`, `bootstrap`, `config`, `routes`, `pkg` and `web` for the two public ids by hash, whole words only; `.env.example` carries them). An empty id disables that provider's login and refresh with a clear error before anything is sent. Secrets are redacted in logs by name and never returned by the API |
| Account | Codex reads the email, the ChatGPT account id and the plan from the id token (no signature check: the claims name the account and decide duplicates). A login that repeats an account updates its row by the repository's duplicate rules |

### Refresh

All four triggers use `oauth.Refresher.Refresh`. Per provider, the registry's `oauth.refresh` block gives the body encoding (`json` for Claude and Codex), the lead time and, for Codex, the age rule; its `oauth.headers` are the static headers sent on a login's token.

| Trigger | When |
|---|---|
| Proactive | Before each request, when the access token is within the provider's lead time (default 5 minutes; Claude 4 h, Codex 10 min, which also refreshes when the last refresh is older than 8 days). A failure other than a dead token lets the request go on with the old one |
| Reactive | On a 401 or 403 from the upstream: up to three tries (1 s and 2 s apart), the credentials are stored, the same account is called once more. If that fails too, the normal failure path runs on the new answer |
| Background | A loop in the `rllmx.oauth` runner (not a cron job: the schedule facade has no seconds-level first run), started once the data gate opens: first pass after 10 s, then every 5 minutes, one account at a time, for active accounts with a refresh token and a known expiry within the lead time or 30 minutes, whichever is longer. The pause between accounts is `OAUTH_REFRESH_DELAY` (default 1500 ms; `OAUTH_REFRESH_GOOGLE_DELAY` read for the providers that set `slowRefresh`). `OAUTH_BACKGROUND_REFRESH=false` turns it off. A failure logs the provider and the class only |
| Test | `POST /api/providers/{id}/test` for a registry entry with an `oauth` block: refresh when due, then check the token exists and has not expired. A provider without one still answers `test not supported` and writes nothing |

**Safety.**

- One call per provider and refresh token: `app/platform/cache.Load` collapses concurrent renewals and keeps the result 10 s in the memory-only `refresh` group, so 50 requests make one call.
- `ConnectionsRepo.SwapCredentials` writes only while the stored refresh token is still the one used. If something rotated it first, nothing is written. Single-use tokens (xAI, MiniMax, Codex) are never spent twice.
- `invalid_grant` marks `testStatus: expired` with the fixed message "needs to be reconnected". Selection skips the account and nothing calls the provider again.
- Three renewals in a row that the provider answers with anything but `invalid_grant` and a success (a Codex account answers `rejected`) mark the account expired the same way. The count is in memory, resets on a success, and a transport failure or a 5xx never adds to it, so an outage cannot retire a good account.
- Only a reconnect clears the mark: the login writes `active`. Neither a refresh nor a passing test can heal a dead token, and a passing test never overwrites the mark.
- A dead account met mid-request is excluded without a lock. Meeting only dead accounts answers **503** with error class `expired` and no retry hint, because a 401 would make the client discard a good router key.

**What a refused request does.**

- A 401 or 403 from a login's token asks for a renewal: up to three tries, one and two seconds apart, and no call at all when the stored token is no longer the refused one.
- The same body is sent once more with the new token; a second refusal is an ordinary account failure.
- Any other status never triggers a renewal.

---

## Remaining work

The framework, claude and codex are built. The reference builds the redirect as `http://localhost:<window.location.port or 443>/callback` in the browser, so behind HTTPS it becomes `http://localhost:443/callback`.

- [x] OAuth token check and renewal on the connection test route (registry entries with an `oauth` block; the rest answer "test not supported")
- [x] The server decides the redirect: `fixed` and `local` are `localhost`; forwarded headers never used, the browser never sends `redirect_uri` (D151). `public` (`APP_URL` + `/callback`) is, used by gitlab, cline and clinepass
- [x] Client credentials only from `OAUTH_*`; no literal in Go code, provider data or web files; empty id disables login with a clear error; no route returns a secret or verifier. The refresh half is 
- [x] Server-side session (`oauth.Sessions`): state, provider, redirect, verifier, dashboard binding, 10-minute TTL, single use; state verified on every completion path (D150). Device code is 
- [x] Paste endpoint (`POST /api/oauth/exchange`) accepting a redirected URL (query or fragment), a bare code, and `code#state`
- [x] `/callback` page posts to the server with the state; no `postMessage` (D152)
- [x] Fixed-port listeners (codex 1455), shared per port, Host and state checked, stopped with the last login, bind `OAUTH_CALLBACK_HOST` (D153)
- [x] Token refresh: proactive, reactive on 401/403, background every 5 minutes; the expired mark
- [x] A login's token goes as `Authorization: Bearer` with the provider's static headers; an API key keeps its scheme
- [x] The computed `anthropic-beta` list, `x-claude-code-session-id`, the identity headers and URL suffix, the Codex headers, client tool detection and the client-compatible request shape. The Codex request *body* (forced stream, `store:false`, instructions, allowlist, lite mode, `/compact`) is 
- [x]: the device flow (start, poll with the provider's interval, `slow_down`), the hook table, the GitHub login and its Copilot token (D154, D182, divergences 234 to 237)
- [x]: the device logins kimi, muse, nous, kilocode and the grok-cli login; `callHeaders` and the device id, device `params`, `endpoints.verify`, the `kilocode` dialect, `noClient`, `refresh.tokenHeader` and `refresh.hook`, hooks that can fail a login and replace the stored credential (divergences 238 to 243); the Kimi lanes follow the reference (244, retired)
- [x]: the code-flow logins xai (fixed `127.0.0.1:56121`), gitlab (own application per account, `public` redirect, token paste) and cline and clinepass (`code` flow, base64 code, `public` redirect); `redirectHost`, `appFromAccount`, `pat`, `refresh.url`, the `cline` code dialect, authorize placeholders, the hooks `xai` and `gitlab` (divergences 246 to 248)
- [x]: the logins codebuddy-cn, codebuddy-intl, minimax-code, minimax-code-global and glm (ZCode); the device dialects `codebuddy`, `minimax`, `zcode`, a dialect renewal, `refresh.fields`, device state (`Verifier`, `DeviceExtra`) kept in the session, the client id `OAUTH_CLIENT_ID_MINIMAX_CODE` (both MiniMax sites), `clampLife` and `clampInterval` no longer wrap on a huge value (divergences 249, 250)
- [x]: the Google logins gemini-cli and antigravity (`noPkce`, a client secret from `OAUTH_CLIENT_SECRET_*`, the hooks `google` and `antigravity`, `projectId` at login and on first use, Antigravity onboarding in the login), `slowRefresh` and `OAUTH_REFRESH_GOOGLE_DELAY`, Vertex tokens from a service account, user credentials or a raw key (divergences 252 to 258)
- [x] Kiro's Google/GitHub social login: `kiro-social`, a `code-pkce` login whose redirect is the provider's own `kiro://` scheme, finished by pasting the address back. `ParseInput` reads a custom scheme as well as http(s), the login is always paste, and the identity provider rides the start request as `variant` and is stored as `authMethod` for the renewal
- [x] Zed and Mimo token shapes (built: `app/llm/oauth/zed`, `app/llm/oauth/xiaomi`). Windsurf and Trae stay out (D158). Kiro's pasted refresh-token import stays a scope cut (D266), and CommandCode's `~/.commandcode/auth.json` auto-import stays out: it reads another program's file (D267)
- [x] Token refresh detail: one call per provider and refresh token through `app/platform/cache.Load` with a 10s result cache; `invalid_grant` and reuse mark the connection (`testStatus: expired`) and stop retrying

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 20, 21, 34 | Redirect per provider from `APP_URL`; one single-use server session per login; ids and secrets as `OAUTH_*`. **(built)** variables, cache policy and flows | Public HTTPS logins cannot redirect to localhost; state always checked; credentials rotate without rebuild |
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
| 235 | A GitHub login whose profile gives a numeric user id is the same account as an earlier row with that id, whatever the email; two users who share an email stay two rows. Without an id the email rule is unchanged | The reference matches on the email alone, and GitHub hides it for most users, so every login there made another row for the same person (improvement) |
| 238 | The Grok CLI login names the account from the id token's `email`, then its `preferred_username`, then the access token's, then the profile's address. A subject id is never taken for an address, and an address with a space or a control character is dropped. The id token is stored in the sealed top-level `idToken` | The reference falls back to the `sub` claim, which then goes upstream as `x-email`, and keeps the id token in provider data, where a renewal leaves it stale (improvement) |
| 239 | The Grok CLI token address is data (`https://auth.x.ai/oauth2/token`); nothing is discovered from `.well-known/openid-configuration` | One outbound call less, and no address taken from an answer; if xAI moves it, the data file is the fix (improvement) |
| 240 | The device id of a kimi login is made by the router, sent only on the provider's own calls and stored with the account; the host name and the product name are never sent (`X-Msh-Platform: rllmx`, `X-Msh-Device-Name: router`). The id and the device code never reach the browser | The reference sends the host name and its product name to a third party, and returns the device code to the page. Both `X-Msh-*` values are unverified (improvement) |
| 425 | A refresh the provider keeps rejecting is retried for good by the reference. Three consecutive rejections mark the account expired, and only a reconnect clears it | The 5-minute pass called the provider about a dead account forever and logged a warning each time; the dashboard showed nothing wrong. A transport failure or a 5xx never counts |
| 241 | The Meta access token is not stored: the minted key is the access token, and the account has no refresh token or expiry. A login with no key, an inactive plan or a payment demand ends with a fixed word | Nothing reads the Meta token again, so it is one less long-lived secret; the reference stores it under `oauthAccessToken` (improvement) |
| 242 | The Nous agent key is the stored `apiKey`, and the account's expiry is the earlier of the access token's and the key's. A renewal mints a new key and keeps the old one if that fails | The reference refreshes only when the access token nears its end, so a key can die first and requests fail until then (improvement) |
| 243 | A hook is an ordinary step that returns an error: it can end the login, replace the stored credential and cap the expiry. Every failure is a class, never provider text | The reference throws provider text from a hook into the dashboard and cannot say a login has no refresh token (improvement) |
| 246 | A GitLab account is renewed with the application it was connected with: the base address, id and secret are stored with it (the secret sealed) and the refresher rebuilds its flow from them. A pasted personal access token has no renewal | The reference resolves no refresh address for GitLab, so its OAuth tokens expire after two hours and are never renewed, and it does not store the secret a renewal would need (improvement) |
| 247 | GitLab chat goes to `{baseUrl}/api/v4/chat/completions`, the account's own server (a bare https origin, checked again when the request is sent) | The reference sends every GitLab request to gitlab.com whatever address the account holds, so a self-managed login could never chat (improvement) |
| 249 | The ZCode login stores only the coding-plan key and the account's name, id and email. The Z.ai OAuth token, the business token and the ZCode JWT are used in the poll and dropped. The poll token and the MiniMax mode flag stay in the server session | The reference keeps all three tokens in plain provider data where nothing reads them, and round-trips the poll token and the flag through the browser (improvement) |
| 250 | The MiniMax Code desktop "lease" login (borrowing the token of the desktop app over a local socket) is not built | It needs the desktop app, and 9router 0.5.116 alone has it (scope cut) |
| 252 | Antigravity onboarding runs inside the login, four tries three seconds apart, and the login succeeds either way. The reference starts a detached task of ten tries five seconds apart that nothing can cancel | A login has one owner and a bound, and shutdown cannot leave a task polling Google (improvement) |
| 253 | An account with no project that cannot be found fails with a fixed class and sends nothing. The reference makes up a project name such as `useful-spark-a1b2c` and sends the request with it | A made-up project is a request Google flags as abuse; a clear failure costs one reconnect (improvement) |
| 254 | A raw Vertex API key goes in the `x-goog-api-key` header, never in the address. The reference puts it in `?key=` | An address is logged and kept by proxies and history; a header is not (improvement) |
| 255 | The Gemini CLI `User-Agent` is `GeminiCLI/<version>/<model> (router; router; terminal)`: the operating system and architecture are fixed words. The reference sends the host's own | The host's details do not leave the machine (improvement, as 164, 176 and 248) |
| 256 | A client body in the Cloud Code envelope is not accepted; the client format is named by the endpoint. `antigravity-to-openai` is not built | Nothing this program serves speaks that envelope to clients (scope cut) |
| 257 | The Google client id and secret are `OAUTH_CLIENT_ID_*` and `OAUTH_CLIENT_SECRET_*` configuration, not literals in the data files. `.env.example` carries the published values | A deployer can replace them without a rebuild (improvement) |
| 258 | A Vertex service-account assertion is built from standard-library crypto and kept nowhere: the private key is read from the sealed API key for each mint and the token, not the key, is cached for its life minus five minutes | The signing code needs no dependency, and a cache holds a short-lived token instead of a long-lived key (improvement) |
| 260 | A Claude-named model on Antigravity is written through the Gemini body with the other models; the reference builds a Claude request first. The result differs in an unset `temperature` and `maxOutputTokens` (left unset, not 1 and 4096) and in a Claude Code system line (kept). **Unverified:** Antigravity may answer a request with that line or without the defaults differently | One mapping instead of two; the three differences are checked by a live account (scope cut, listed in the live checks) |
| 237 | The Copilot token is minted once per account for any number of requests, kept 10 s in the memory-only `connections` group and stored sealed in the account's provider data; the login token is never sent to the chat endpoint. A mint that fails gives a fixed account failure and no provider text | The reference mints in several places with and without de-duplication and logs the failure; one flight and a sealed copy cost one call per hour and survive a restart (improvement) |

---

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| The reference sends the verifier and redirect through the browser, never compares `state`, and lets three channels carry the code. | The verifier never leaves the process, `Begin` is single use, and a completion needs the state and the binding. |
| Cookies do not isolate by port, so a listener cannot check the dashboard cookie. | A login finished on another port is proven by the secret state and the port alone. |
| `IsLocalRequest` already requires a loopback Host, so a second check could not fail a mutant. | The redundant `routerPort` check was removed. |
| The reference never records `invalid_grant`, so it calls the provider for good. | `MarkExpired` marks under the same stored-token condition as the swap; one swap wins a race, and a mark never lands on a rotated token. |
| A mark that lands first must not stop a swap of the same token. | The mark is the stronger state, so a dead account met in a request gets no lock on top. |
| Twenty parallel requests must cost one renewal. | `cache.Load` collapses renewals of one refresh token for 10 s and re-reads the row first. |
| Two gaps the mutants found: a non-auth error renewed nothing and `Ensure` could pass an expired account. | Both have tests now. |
| Fuzzing found `clampLife` and `clampInterval` multiplied before capping, so a huge `expires_in` wrapped negative. | They cap first, which also fixes the Kimi, Nous and GitHub device logins. |
| A device login is one more kind of session, so the same binding, single use and read-once outcome apply. | The poll gate lives in the store, not the handler, so the browser cannot make the router poll faster than the provider allows. |
| GitHub answers a pending poll with a 200 body, and an ended login answers through the status read. | The body is read before the status, and the finished-login check sits in the gate. |
| Only the minted Copilot token may reach the chat endpoint. | The pipeline asks the minter, not the stored account; a hand-added key keeps its own scheme. |
| A client must not choose the session value another client's conversation uses. | A keyed hash of the session and the account is sent to Codex (design doc 11). |
| A GitLab account's secret must survive a renewal without configuration. | It stays in the memory session and is stored sealed, so the refresher rebuilds the flow from the account. |
| A stored server address can be altered after the login. | A bare https origin is checked twice: at login and again when a chat request is sent. |
| The `public` redirect needs the session cookie at the `APP_URL` origin. | A dashboard opened elsewhere finishes by pasting the landing address. |

