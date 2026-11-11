# Router-LLM - Architecture

LLM gateway in Go on Goravel Lite v1.18.0. Clients talk to one endpoint in the OpenAI, Anthropic, Gemini or Responses format; Router-LLM picks an account for the requested model, translates the request to the provider's format, streams the answer back in the client's format and records usage. Single static binary; when it lands, the only process it starts is `git`, for the skill update. **Config defaults:** see `.env.example`, never guess values.

This file holds what cuts across every area: the module map, the decisions behind the whole design, the approved modules, and the terminal commands. **Per-area design and status live in `docs/prds/`** — read only the PRD your task touches. `AGENTS.md` holds scope, build and security constraints and the compatibility contract; `TASKS.md` says what is built.

---

```mermaid
graph LR
    subgraph cmd["app/console/commands/"]
        Main["db:*, keys:*, health, version"]
    end

    subgraph internal["app/"]
        App["providers"]
        GW["gateway/ pipeline catalog conversation prefetch account"]
        LLM["llm/ upstream routing translate executor oauth stream quota zed"]
        ACC["access/ auth identity oidc saml sso"]
        ST["store/ repository data importer analytics sealer dialect document lock"]
        PROT["protocol/ sigv4 eventstream cosy keccak"]
        PLAT["platform/ cache network settings file text ttl redactor failure"]
        API["http, routes/"]
    end

    Main --> App
    App --> API
    App --> PLAT
    App --> ST
    API --> ACC
    API --> GW
    GW --> LLM
    LLM --> ST
    LLM --> PROT
    LLM --> PLAT
    ACC --> ST
    ST --> PROT
    ST --> PLAT
    PROT --> PLAT
```

Every node is built. `providers` boots the framework, `http` serves the guarded routes, `store/data` owns the stores and the maintenance jobs, `platform/cache` sits over the `Cache` facade, `store/analytics` holds the usage store, `llm/stream` holds the SSE and NDJSON writers, the upstream readers, the pump with its timers, the usage tracker and the client buffer, and `platform/failure` the error shapes; `llm/translate` holds the typed hub and the concerns around it, with each client wire format in its own package (`translate/openai`, `claude`, `gemini`, `responses`, `ollama`) beside the upstream-only `kiro`, `commandcode` and `cursor`. `llm/executor` sends one request to one account and returns the answer unread, with the provider families in sub-packages and `executor/mux` building the table; `gateway/conversation` names the session a request belongs to, and `gateway/pipeline` walks a request through parse, resolve, translate, the account loop and the usage row.

Packages not drawn in the graph: `failure` (error bodies per client format), `sealer` (sealed values), `file` (atomic writes and the capped read), `text` (control characters, rune-safe cuts, markup stripping), `ttl` (a cached read that takes no lock to serve and shares one load), `lock` (the data-directory lock), `identity` (caller and key status), `importer` (legacy seeding), `settings` (typed config), `redactor` (log scrubbing), `upstream` (provider registry and price table), `routing` (model name to provider, model or combo) `account` (account selection, error rules, backoff, locks) and `translate` (the typed hub and the format codecs), all three called by `pipeline` since 1.8c; `prefetch` (remote image URLs to inline data for Gemini and Ollama targets, on a guarded client, called by `pipeline` since 1.8d); the chat controller serves `POST /v1/chat/completions` and gets the pipeline from `providers.PipelineSource`, which builds it on the first request because the stores open after the routes are declared; the registry and the capability tables are loaded at start. The `response` cache group holds non-stream answers for the opt-in response cache; `pipeline` looks it up after the model is resolved and `cache.Budget` bounds it in process. Shared test helpers live in `tests/`: the root package (connections, the fake upstream, route, config, log and cache doubles, `Settings`, `TimeoutError`, `Box`, `Engine`) may import only packages whose own tests do not import it back, and `tests/imports_test.go` fails when it does; a helper that needs `repository` or `analytics` lives in a sub-package (`tests/store` builds a `repository.Store`, `tests/usage` collects usage rows).

`catalog` (the models a caller can call, for `GET /v1/models` and its relatives; it reads the stores, asks a user node for its own list through the checker's guarded client, and never writes) is called by the models controller, which gets it from `providers.CatalogSource` on the first request, like the pipeline.

`oauth` (account logins: the authorize address, the in-process login store, the token client, the paste parser and the fixed-port callback listener; no HTTP types) is called by the OAuth controller and reuses the guarded dialer, the PKCE helpers of `oidc` and the binding of `sso`; `web` holds the embedded static files (only the callback page until Phase 4). `oauth` also renews tokens (`Refresher`: before a request, after a refusal, in a background loop run by the `rllmx.oauth` runner, and for the connection test); `pipeline` asks it through a small interface and sends a login's token as a bearer token. The headers a login's token carries are computed in `pipeline` from the provider's data, and `translate.Cloak` disguises a Claude request from another client (1.9c).

`combo`, `media`, `saver`, `skill`, `plan` and the health probe do not exist yet; each arrives with its step (`TASKS.md`).

---

## Key Design Decisions

1. **Pure Go, no CGO.** Static binary, simple cross-compile, Windows included.
2. **Behavioral parity, independent code.** The references define behavior; the code is written fresh and tracked in `docs/PARITY.md`.
3. **Drop-in database.** Same schema and stored formats for the configuration tables; additive changes only; unknown JSON fields preserved.
4. **OpenAI hub translation.** One shape to translate through; every codec goes to or from the hub only, never direct.
5. **Registry-driven providers.** A provider is a data file; code exists only for real protocol differences.
6. **Classification is data.** Error rules are a table that locks, backoff and combo advancement all read.
7. **Locks are JSON fields in the shared row**, as in the reference, so a migrated database keeps its cooldowns.
8. **Single-flight token refresh.** One in-flight refresh per provider and token protects rotating tokens.
9. **Fail open for enhancements, fail closed for access.** Savers, skills and plan injection never fail a request; key checks, SSRF guards and local-only routes always do.
10. **Declarative skill logic.** Rules instead of scripts: no code execution, bounded regex, header denylist, access re-checked.
11. **The socket peer decides, and the dialer proves the address.** Forwarding headers count only from a trusted proxy peer and never grant access; `app/http/trust.go` is the one reader of `SERVER_TRUSTED_PROXIES`, deciding HSTS, the login limiter's key and the dashboard cookie's `Secure` flag. Outbound, `network.NewGuard` resolves a name itself, classifies the addresses and dials the one it checked, so a check and a dial cannot disagree. Link-local and the cloud metadata addresses are refused even when listed.
12. **Read only what was verified.** The SAML verifier returns the assertion it proved, and every claim is read from that value rather than from the document it arrived in. The same rule guards the signature-wrapping attack that XML-DSig is famous for.
13. **Plain-web dashboard.** Embedded static files and the reference's API contract.
14. **Env-only configuration.** Process environment and `.env`; runtime behavior in the settings row.
15. **SQLite and PostgreSQL** through the Goravel `DB` and `Schema` facades (`goravel/sqlite` and `goravel/postgres`), one test suite on both. No `database/sql` handle, pool, `sqlx` wrapper or migration runner of ours; `dialect.Rebind` is the only gap filler.
16. **`os/exec` behind one filter gate.** `app/git` does not exist yet; when it lands it is deny-by-default and the only code that builds an `exec.Cmd`. It starts `git` and nothing else, only as `clone`, with a fixed flag set, a validated ref, an `https` URL from the admin's list that passes the SSRF guard, a clone directory inside the data area, a minimal environment, a 60 s timeout and size caps. A test fails if any other non-test file imports `os/exec`. Anything else that needs a spawned process is out.
17. **Usage is metadata only.** One structured row per request in a separate store; no prompt, body or header is ever persisted.
18. **The cache never breaks a request.** Every cache failure is a miss; credentials stay in process memory.
19. **The server decides OAuth redirects.** The browser never supplies a `redirect_uri`, and every login has a single-use server-side session.
20. **The circuit breaker is used** (planned, step 2.5). Health data will affect routing instead of only appearing in the UI.

---

## Approved Modules

Every module here is CGO-free, on the `TASKS.md` Dependency gates table, and named at the site that uses it. Anything not listed is either the framework's own dependency or not approved.

| Module | Why it exists, and why not our code |
|---|---|
| `tidwall/gjson`, `tidwall/sjson` | Edit a JSON `data` column in place. A map round trip reorders keys, reformats numbers and drops unknown fields, which breaks the byte-for-byte round trip the compatibility contract requires. `PRD 15` |
| `gofrs/flock` | The data-directory lock. It is released when the process dies and works on Windows, which an advisory `flock` syscall does not. `PRD 15` |
| `subosito/gotenv` | Read key names, the size cap and the BOM check the framework's loader does not expose, so a misspelled variable is refused rather than ignored. `PRD 17` |
| `go-jose/go-jose/v4` | Parse an OIDC ID token and select its key from the provider's JWKS by `kid`, algorithm and use. JWS verification itself is unambiguous — the signed bytes are the raw string — but key selection is where a lenient check accepts a token signed by the provider's **encryption** key, and that is the piece worth a reviewed implementation. Every claim rule stays ours. `PRD 2` |
| `russellhaering/goxmldsig` | Verify a SAML signature. Canonical XML 1.0 has namespace-propagation and attribute-ordering rules that a hand-written implementation gets wrong in the *accepting* direction, which is a signature bypass rather than a rejection. The parsing, the claim checks and the reuse defence stay ours. `PRD 2` |
| `golang.org/x/sync`, `golang.org/x/term` | Framework requirements used directly: `singleflight` for one cache load, and the hidden terminal prompt. |
| `google.golang.org/protobuf` | Record and read the fields of Cursor's agent protocol, which is protobuf on the wire. The library is the framework's own requirement (through gRPC), and only its `encoding/protowire` field reader and writer are used: the messages, their numbers and their meaning stay ours, in `app/llm/executor/cursor`. A generated binding would need the vendor's `.proto` files, which the reference does not ship |
| `google/uuid` | Version 4 ids for connections, nodes and rows (`app/store/dialect.NewID`, the node id in the nodes controller). The framework already requires it, so it adds nothing to the build; the stored id format is the 9router one, a lower-case v4 UUID. |

The session cookie's HS256 is our own `crypto/hmac`, not a JWT module.

---

## Go Conventions

`AGENTS.md` states each rule in one line. This section holds the reason and the shape, so a rule is written once.

**Context.** Pass `ctx` first. A request path derives from the request context, so a client that leaves cancels the work it started, including the upstream call. `context.Background()` is for start-up and shutdown only. A runner may keep its lifetime context in a field (`analytics.Writer`, the OAuth runner); a handler or a pipeline step never does.

**Goroutines.** One owner starts it, one `ctx` stops it, and a test proves it exits. Bounded fan-out uses `errgroup.Group` with `SetLimit`. The two goroutines on the hot path are the stream reader (`app/llm/stream/pull.go`) and the Codex peek (`app/llm/executor/peek.go`); each reads one body and ends when the body closes or the context is cancelled.

**Timers.** `time.After` allocates a timer that lives until it fires, so inside a loop use `time.NewTimer` and `Stop` it. A single bounded wait (a retry delay, a start-up offset) may use `time.After`.

**Locks.** Hold one only around memory, never around a network call, a channel send or a callback. State that is read on every request and written on a reload is built whole and swapped with `atomic.Pointer`, so readers take no lock. `app/platform/ttl` is that pattern for a cached read: `ttl.Value[T]` serves a fresh value without a lock, shares one load between callers (a caller that leaves does not fail the others), and never keeps a failed load; the routing snapshot, the account settings, the auth settings and the request-path settings read use it.

**Memory.** The cheapest allocation is the one not made. In order: stream instead of buffering; bound anything that grows from input; preallocate when the size is known; reuse a hot buffer with `sync.Pool`, resetting it on Get and dropping an oversized one on Put; never pool a secret. A body from outside is read with `file.ReadCapped`, which reads one byte past the cap to tell "exactly at it" from "over it", so no read site restates that rule. A stream frame is built in a scratch buffer the writer keeps (dropped when one frame made it large) and an event is read once, with `gjson.GetBytes`, which scans in place where `ParseBytes` copies the event. Bodies are read and patched with `gjson`/`sjson` because a `map[string]any` round trip copies the document, reorders keys and loses number precision. Compile a regexp once. Reuse the `http.Client` and `Transport` so connections stay alive, and always drain and close a response body.

**Security.** Validate at a trust boundary: client input, an upstream answer, a data file. Size, count, depth and charset are checked there and nowhere deeper. Randomness is `crypto/rand`; secret comparison is constant time; nothing secret reaches a log. `govulncheck ./...` is run when a dependency changes and before a step is called done.

**Errors.** Wrap with `%w` and a lower-case message that names the operation. A sentinel error is part of a package's contract and is tested with `errors.Is`. `panic` is for a mistake in start-up wiring (a duplicate route, a missing binding), never for input.

**Interfaces.** Define one where it is used, with the methods that caller needs. Do not add one for a single implementation, except to give a test a fake for a facade (`cache.Driver`, `log.Log`, `db.DB`).

**Layout.** `app/` holds six domain folders, each holding packages: `platform` (infrastructure every other domain uses), `protocol` (wire-protocol code that imports nothing in `app/`), `store` (persistence and its formats), `access` (who may call in), `llm` (everything about talking to a model provider), `gateway` (request handling that joins them). Imports run down only, in that order, and a test fails an upward edge. A package that outgrows about 25 files into a shared framework plus independent provider families splits into sub-packages (`translate/openai`, `executor/qoder`); a sub-package imports its parent's framework and never a sibling, and one wiring package per domain (`executor/mux`) builds the table of providers by explicit construction. A format package is imported by the pipeline that registers it and by tests, never by another format. `oauth` keeps its provider steps in one table injected through `Dialects` rather than in sub-packages, because every step calls the engine it sits in. Goravel owns `app/http`, `app/console`, `app/providers`, `app/facades`, `app/models`, `bootstrap`, `config`, `routes`, `database`, `tests`, `storage` and `resources`; none is reused for another purpose. A package that is importable by other programs lives in `pkg/` only if it imports no `app/` package, takes no product types in its API, and has a plausible outside consumer. A package that encodes a persisted or wire format documents it beside the code, and a change to it is breaking.

**Commands.** A new Artisan command goes on `bootstrap.CommandsAllowlist` or it does not exist; the destructive framework commands stay unreachable behind that list and the `Artisan` wrapper in `app/providers.CommandsServiceProvider` (`PRD 15`, `PRD 23`).

**Facades.** Product code may use `Route`, `Http` (Gin), `Schedule`, `Cache`, `Log`, `DB`, `Schema`, `Hash`, `App`, `Artisan` and `Config`. `Orm`, `RateLimiter`, `Session`, `Storage`, `Validation` and `View` are installed only as another facade's dependency. `Auth`, `Crypt` (the sealed format is ours), `Queue`, `Mail`, `Event` and `Telemetry` are not installed, and `Process` is unused. Install a facade only for a use listed here.

---

## Terminal Commands

Artisan commands in `app/console/commands`, run against the local store. Built today: `db:backup`, `db:check`, `db:vacuum`, `db:import-config`, `db:import-analytics`, `db:export-plain`, `keys:create`, `keys:list`, `keys:revoke`, `password:reset`, `health`, `version`. `PRD 23` tracks the surface.

| Rule | Detail |
|---|---|
| Store | The read-only connection (`sqlite_ro`, `mode=ro`; `sqlite_backup` for `db:backup`) for the command, or the writer connection for the two that change data (`db:vacuum`, `db:import-config`). A command never creates a table and never runs a migration. `db:import-analytics` is the one that refuses up front: `Store.Verify` fails with `ErrNeedsMigration` when the plan is not empty or plain secrets are still unsealed, so the import never runs against a database the server has not opened yet. `db:check` reports the same plan problems instead of refusing. `db:backup` and `db:vacuum` look at no schema at all. A command can still leave an empty `data.sqlite` under `DATA_DIR` behind, because Goravel's boot ping opens the default connection read-write (`PRD 1`, Framework Choices) |
| Terminal | Refuse without one. Passwords come from the console context's `Secret` prompt only, never flags or environment, never echoed or logged |
| Verification | Dashboard password against the bcrypt hash, or `AUTH_INITIAL_PASSWORD` while no hash exists. Three attempts per run, 2 s pause after a wrong one, an empty answer never passes |
| `keys:create [--name]` | Prints the key once (name up to 64 characters, no control characters). Needs the `machine-id` file the server wrote |
| `keys:list` | Id, name, masked key, checksum class (`ok`, `mismatch`, `legacy`), `active` or `revoked` |
| `keys:revoke <id>` | Deactivates; a running server stops accepting it within 10 s |
| `password:reset` | Current password, then the new one twice, same rules as the dashboard. Stores the hash and bumps the epoch |
| `db:backup\|check\|vacuum\|import-analytics\|import-config` | Ask no password (they never reveal or change credentials). Confirmations: `docs/WORKFLOWS.md` |
| `db:export-plain` | Asks the dashboard password, then y/N unless `--yes`, then writes the copy under the backups folder. SQLite only |

The dashboard limiter lives in server memory and is not shared with the terminal; anyone who can run the command can already read the files.

---
