# Router-LLM - Agent Instructions

Router-LLM (short name RLLM, binary `rllmx`) is an LLM gateway in Go on the Goravel Lite framework: one endpoint that speaks the OpenAI, Anthropic and Gemini client APIs and routes to many upstream providers. It is a Go rewrite of 9router and the extended 9router fork, and opens an existing 9router database as a drop-in replacement.

**Scope:** an LLM request router. Reject MITM interception, tunnels, relay deployers, process managers, auto-installers, cloud sync, licensing and anything that shells out other than the git allowlist (Build). Open source (MIT).

**This file holds rules.** Behavior, formats and design live in `docs/prds/` — one PRD per area. Never restate a PRD rule here; cite it.

---

## Workflow Rules

1. Before every session and any plan, read this file, `TASKS.md` and `docs/PARITY.md`, then **only the PRD your task touches** (`docs/prds/README.md` indexes them). Never open every PRD. Plans cite the PRD they implement; on conflict, update it first, with user approval. When a PRD reaches `done`, write its **Findings** section.
2. Before hand-writing a provider, command, middleware, route file, config file or test, run `./artisan list | grep make:` and scaffold with the matching `make:*` command (`make:<name> --help` first).
3. Never rework items marked done in the task list unless told to. Update the task list right after finishing a task.
4. Never write the entire codebase in one response.

## Skills

Before any task, load once per session with the Skill tool, in this order: `caveman`, `ponytail`, `humanizer`, `code-humanizer`, `remove-ai-marks`, `safeguard-guardrails`, `karpathy-guidelines`, `security-audit`, `using-superpowers`, `goravel-development` (the project skill in `.agents/skills/goravel-development/`; read `CUSTOM.md` beside it when it exists).

- `using-superpowers` routes to per-task skills (brainstorming, TDD, debugging, review).
- `security-audit` runs in guidance mode; a full audit needs an explicit request.
- This file beats any skill.

## Clean-Room Rule

`references/9router/` and `references/9router-extended/` are local, read-only behavior references (git-ignored, MIT-licensed). `../rllm` is a second reference: an earlier Go implementation written without a framework.

- Read them to learn behavior and while writing our own logic. Never copy, mechanically translate, port, import or commit their code. Never carry over their package layout, stdlib choices or filenames: rllmx is a Goravel Lite program and its shape follows this file.
- Re-author data tables (provider registry, pricing, capabilities, model lists) by reading the reference and writing our own JSON. Never convert them with a script.
- Docs may name 9router, because compatibility is a product goal. Code, comments, identifiers, test names and fixtures never do. Exception: the on-disk compatibility surface (database identifiers, `modelLock_` keys, the API key format), spelled exactly as stored.
- Never copy reference bugs. Fix them and record each deliberate divergence in the PRD's `Divergences` section, indexed in `docs/DIVERGENCES.md`.
- **Reference first.** Before asking about a mechanism (routing, translation, OAuth flow, error handling, schema, UI behavior), read how the references do it and follow that. Ask only when the reference is silent, ambiguous, or conflicts with a rule here; then state what it does and why it cannot be followed.

## Parity Rule

Baseline: current 9router plus the features only 9router-extended adds. `docs/PARITY.md` holds the pinned versions, the status values and the sync log.

- Every parity row names the reference version it matches and a status: `todo`, `partial`, `done`, `diverged`, `deferred` or `out`.
- When a reference updates, run the Parity Sync playbook in `docs/WORKFLOWS.md` before touching code.
- A feature is `done` only when its tests pass and its parity row is updated in the same change.

## Where Things Live

| File | Holds |
|---|---|
| `docs/ARCHITECTURE.md` | Module map, cross-cutting decisions, approved modules and why each exists, terminal commands |
| `docs/prds/` | One PRD per area: status, design, parity rows, divergences, findings. **The design authority** |
| `docs/PARITY.md` | Pinned reference versions, status values, sync log |
| `docs/DIVERGENCES.md` | Index of divergence numbers; rows live in each PRD |
| `docs/WORKFLOWS.md` | Pipeline, startup order, operator procedures, Parity Sync |
| `TASKS.md` | Current step, open items, what the user must decide |
| `.env.example` | Every variable with its default (`PRD 17`) |
| `Makefile`, `Dockerfile`, `.goreleaser.yml` | Build, container, release |

Repository shape: `app/` product code, `bootstrap/` Goravel wiring, `config/` one file per concern, `routes/`, `web/` embedded dashboard, `pkg/` importable packages (none yet), `tools/mutant.sh`, `docs/`.

## Audit Scope

- Every alignment, deep scan, bug, security or error-handling analysis covers `app/`, `bootstrap/`, `config/`, `routes/`, `pkg/` **and** `web/` (`./...` plus the JS). Never `app/` alone.
- A docs or parity alignment pass compares code against `docs/ARCHITECTURE.md` and the PRDs.
- A finding in `pkg/` includes a check of its callers; a caller-side change re-checks the `pkg/` contract it relies on.
- `pkg/` is imported by other apps, so it also needs: every export documented, safe zero values, sentinel errors, no `app/` import, no facade, golden tests, and a fuzz target per parser.

---

## Compatibility Contract

Some formats are shared with 9router and are breaking to change. **Changing any needs user approval.** The format itself lives in the owning PRD; this table only says who owns it.

| Surface | Owner |
|---|---|
| Database tables, columns, indexes, `_meta`, `schemaVersion`, `kv` scopes | `app/repository` — `PRD 15` |
| Analytics store schema | `app/analytics` — `PRD 15` |
| JSON `data` columns and the `settings` row | `app/document` — `PRD 15` |
| Timestamps and stored-value formats | `app/dialect` — `PRD 15` |
| Sealed values (`enc:v1:`) | `app/sealer` — `PRD 15` |
| `providerConnections.data` per-model locks | `app/account` — `PRD 5` |
| API key, machine id | `app/identity` — `PRD 2` |
| Dashboard session, password hash | `app/auth` — `PRD 2` |

Additive changes only. A database written by a real 9router run must open, read, survive an untouched round trip and keep unmodified rows byte-for-byte identical (`PRD 15`). Test key, machine-id and JWT code with vectors computed independently from the formats. The fixture holds real credentials: git-ignored, test on a copy, skip when absent, never print row values.

## CLI

Artisan commands in `app/console/commands`, registered in `bootstrap/commands.go`, run as `rllmx artisan <name>`. The list is in `README.md`; the prompt and confirmation rules are in `PRD 23`. Local store only: no remote client mode, no TUI, no system tray, no OS service management. The container runs the `rllmx` binary, whose default is to start the server.

---

## Critical Constraints

### Build
- `CGO_ENABLED=0` always; no C/C++ in the default build. Targets: Linux, macOS, Windows, each on amd64 and arm64.
- Built with `go build` through the Makefile (`./artisan build` for a one-off). The Dockerfile uses a pinned Go base image, runs as a non-root user, and copies no `.env` and no folder that does not exist in the repository.
- `os/exec` only through one deny-by-default gate, `app/git`, the only code that builds an `exec.Cmd`; a test fails any other non-test file that imports it. The program, subcommand and flag set are one literal in that package; adding any needs user approval first. Every filter must pass before a process exists; a failure returns an error and logs at WARN without argument values. Full filter list: `PRD 11`, divergence 200.
- Anything else needing a spawned process is Out: devin-cli, Hermes, headroom installer, install commands stored inside skills, autostart, browsers (print the URL), installers, updaters (`PRD 22`).

### HTTP
Driver, server, timeouts, middleware order, routes, limits, CORS, shutdown: `PRD 1`. The rules that bind every change:
- Nothing configures or forks the Gin engine; the driver keeps its defaults.
- Streams go only through `stream.NewSSE`, or `stream.NewNDJSON` for an Ollama client. The framework's `*gin.BodyWriter` hides flush errors and copies every byte into memory, so it is never used for a stream.
- The guard is route middleware and reads the socket peer from `Request.RemoteAddr`. `ctx.Request().Ip()` is for logging and limiter keys only, never an access decision. Every route lives in `routes/` in the guarded group; one registered outside stops the start.
- Bound every body and close on oversized input. Stream with flushes; never buffer a whole stream except the documented empty-stream peek. A client disconnect cancels the upstream request through `context.Context`.
- Always verify TLS; never fall back to an insecure transport. Stdlib `net/http` for every client; `x/net/http2` only where stdlib cannot (h2c for Cursor).

### Network safety
Web fetch, image prefetch, headroom and user-defined base URLs go through the SSRF dialer guard (`PRD 14`). Provider nodes are admin-trusted, but loopback and link-local metadata addresses are denied unless the admin allows them. There is no outbound proxy or proxy pool; transports dial direct, so `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` and `NO_PROXY` are ignored.

### Secrets and privacy
- Randomness from `crypto/rand` only; compare secrets with `subtle.ConstantTimeCompare`.
- Never log tokens, API keys, cookies, passwords or prompt text at INFO. Never persist request or response bodies; they may appear only at DEBUG, redacted. Every `Log` call passes through the redacting channel (`PRD 1`, `PRD 17`).
- Data files `0600`, directories `0700`. No system temp dirs: runtime files live in `DATA_DIR` or configured paths.
- A request's identity is the API key or the dashboard session. Honor `X-Forwarded-For` and similar headers only from a trusted proxy peer. Local-only management routes (anything touching host files or secrets) stay local-only (`PRD 2`).
- Cache groups holding credentials are memory-only, even when the default store is Redis (`PRD 20`).

### Storage
Reach the database only through the Goravel `DB` facade, and `Schema` for DDL and checks. Detailed rules, per-connection names, plan-first migration, backup, read-only connections and the command allow-list: `PRD 15`.
- No `database/sql` handle, pool, dialect layer, `sqlx` wrapper or migration runner of ours. `app/dialect.Rebind` is the one gap: write SQL once with `?`.
- No `Orm` facade and no model anywhere, analytics included: plain structs with `db` tags and raw SQL. Product code never imports `gorm.io/*` or `charmbracelet/*`; a test fails a hand-written file that does.
- Official drivers only: `goravel/sqlite` and `goravel/postgres`. No second driver.
- Additive schema changes only. Nothing creates, alters or drops a shared table implicitly.
- The framework's destructive commands (`migrate:fresh`, `migrate:reset`, `migrate:rollback`, `migrate:refresh`, `db:wipe`, `db:seed`) must stay unreachable. Two gates stand between them and the user: `WithCommandsFilter` and the `Artisan` wrapper in `app/providers.CommandsServiceProvider`. Keep both. A new command goes on `bootstrap.CommandsAllowlist`, or it does not exist.
- What the facade does not give is a thin wrapper in `app/repository`, `app/dialect`, `app/document` or `app/sealer`. It wraps the facade; it never replaces it.
- Both engines pass the same suite. `TEST_PG_DSN` selects PostgreSQL (`make test-pg`); a test green on SQLite and broken on PostgreSQL stays invisible until a server runs it (`TASKS.md`).

### Configuration
Environment and `.env` through `config/*.go`, one `facades.Config().Add(...)` map per file, every value via `config.Env("NAME", default)`. No YAML, TOML, JSON config, no direct Viper. The typed view, validation, variable naming, the 9router mapping and the tested `.env.example` contract live in `PRD 17`.
- Goravel names as the framework defines them (`APP_*`, `JWT_SECRET`). Every other variable is `<GROUP>_<NAME>`, no prefix; dead 9router names are not read.
- Add a variable in the same change that reads it. Process environment overrides `.env`; every variable has a default.
- `.gitignore` ignores only the local `.env`; `.env.example` is committed; `.dockerignore` keeps `.env` out of the image.
- OAuth client ids and secrets are configuration, never literals in code or provider data (`PRD 6`).

### Routing, cache and usage
Behavior and limits: `PRD 5` (selection, error rules, backoff), `PRD 20` (cache), `PRD 16` (usage, pricing, quota). The boundaries:
- Cache only through the `Cache()` facade and the helpers in `app/cache`. No cache interface, memory store, Redis client, TTL map or singleflight loader of ours. The cache fails open.
- Usage holds structured values only: ids, provider, model, endpoint, status, tokens, cost, latency. Never a body, header or prompt.
- Error classification is data; a request-scoped 4xx never locks an account. Rotating refresh tokens get one in-flight call per provider and token.

### Skills Studio and rule hooks
A skill is a manifest, a prompt and optional declarative rules; no code execution. `hook.js` and install commands in imported skills are ignored and flagged in the UI. Rules use RE2 on bounded input and may inject a prompt, reroute or set a header, never `authorization`, `host`, `cookie` or `content-length`. Hooks and savers fail open. Layout, git sync and the dashboard page: `PRD 11`.

### Code
- Logging is the `Log` facade, nothing else. No `slog`, no lumberjack, no logging package of ours. No `fmt.Println` or `log.Fatal` in core packages. Channels and rotation: `config/logging.go` (`PRD 17`).
- `context.Context` comes first on long-running functions and handlers. Shutdown is OS signal plus cancellation: providers owning a resource return a runner so it closes in the order `ARCHITECTURE.md` lists. Mutex on all shared state; `errgroup` or `WaitGroup`; no leaks.
- Wrap errors with `fmt.Errorf %w`, use sentinel errors, never suppress silently. Errors to clients use the shape of the client's format, never internal detail (`PRD 19`).
- Use Goravel first: `DB`/`Schema`, `Cache`, `Log`, `Config`, `Schedule`, `Artisan`, `Hash`. Write a helper only for a gap the facade cannot fill, and make it wrap the facade. Before building anything that sounds like infrastructure (connection, pool, client, runner, loader), read `./artisan list | grep make:` and `goravel/framework/contracts/`.
- Product packages take the facade's interface (`cache.Driver`, `log.Log`, `db.DB`) as an argument, never the facade function, so a test can pass a fake. Facade functions are called only in `app/http`, `app/console`, `app/providers` and `config/`. A provider's `Register` only binds and `Boot` wires cheap things; anything that takes the lock, opens a store or listens runs in a runner's `Run`, because every provider boots for every Artisan command.
- No stubs: every function is complete. No `// TODO`, `// rest of code` or placeholder logic.
- One rule, one implementation. Never copy a helper into a second file or package: extract it to the package that owns the concept, test scaffolding included. Acceptable repetition: table-driven cases, tiny exhaustive switches, per-provider translators that would need a speculative interface to share.

### Dependencies
Preference order: (1) stdlib, (2) the framework and its helpers (`support/carbon`, `path`, `str`), (3) the official Goravel driver for a facade in use, (4) a module the framework already requires, through its API, (5) our own code. Everything CGO-free.

- Any module outside that needs user approval and a line in the `TASKS.md` Dependency gates table before `go.mod`, with the reason beside the code and in `docs/ARCHITECTURE.md`.
- Use Goravel's driver unless it breaks the compatibility contract; then record why in the PRD's `Divergences` section.
- Pin to releases whose `go` directive is not newer than `go.mod`; a bump needing a newer Go is a separate decision. `GOTOOLCHAIN=local` makes such a bump fail rather than download a toolchain.
- `goravel/framework` and `goravel/gin` are pinned to tags. Install a facade only for a use listed here.
- Facades product code may use: `Route`, `Http` (Gin), `Schedule`, `Cache`, `Log`, `DB`, `Schema`, `Hash`, `App`, `Artisan`, `Config`. Installed only as another facade's dependency: `Orm`, `RateLimiter`, `Session`, `Storage`, `Validation`, `View`. Not installed: Auth, Crypt (the sealed format is ours), Queue, Mail, Event, Telemetry. `Process` is unused and its facade file is gone. `./artisan package:install` needs approval.

### Naming (Goravel convention)
- Layout is flat: every product package is a direct child of `app/`. A package never nests inside another product package. Goravel's `app/http/{controllers,middleware}`, `app/console/commands`, `app/providers` and `app/facades` are the only nesting.
- A package name is one lower-case noun for what it holds (`auth`, `cache`). No underscores, no plural unless Goravel does, no abbreviation or invented suffix (`x`, `util`, `common`, `helpers`).
- A package name never repeats a Goravel one with another meaning. `app/providers` is Goravel's service providers; the upstream LLM provider code is `app/upstream`.
- Files are `snake_case.go`, named for the main type or topic; tests end `_test.go`, as `make:*` generates them.
- Types and exported names are `StudlyCase`; a controller ends in `Controller`, a middleware is named for its action (`RequireApiKey`), an Artisan command's signature is `group:action` (`db:backup`), a service provider ends in `ServiceProvider`.
- Names state their job in full words. The package name is not repeated in an exported name (`auth.Login`, not `auth.AuthLogin`).
- Goravel owns `app/http`, `app/console`, `app/providers`, `app/facades`, `app/models`, `bootstrap`, `config`, `routes`, `database`, `tests`, `storage`, `resources`. Never reuse one for another purpose: the database layer is `app/repository` with `app/dialect` and `app/document` beside it, and shared test helpers live in `tests/`.
- Go files in a package are flat and named for the type or topic, never a nested folder per provider. A provider file is `<provider_id>.go` in snake case.
- A cache group name (`toolcalls`) is data, not a package; it does not have to match a package name.
- Renaming a package or file changes the docs in the same change.

### Package Layout (app/ vs pkg/)
- New code goes in `app/`. Promote to `pkg/` only when the package imports no `app/` package, its exported API takes no product types (config, settings, storage rows, registry types), and a plausible consumer outside this project exists. When in doubt, keep it in `app/`. Product glue (controllers, middleware, providers, commands, `config/`) stays in its Goravel folder.
- `pkg/` is importable and tested, with no stability promise and no separate module.
- A package that encodes a persisted or wire format is a format owner: document the format beside the code; a change is breaking.

### Web Dashboard
Plain HTML, ES modules and CSS: no framework, no bundler, no build step, no CDN at runtime. A framework needs the written stack-spike decision in `docs/ARCHITECTURE.md`, which amends this section first. Pages, i18n and the API contract: `PRD 18`. Escape all dynamic text; never set `innerHTML` from data.

### Build Artifacts
- Output goes to `dist/` via the Makefile. Integration tests validate the compiled binary.
- `tools/mutant.sh <label> <file> <old> <new>` swaps one snippet, runs the package tests, prints CAUGHT, MISSED, BUILD or NOOP, restores the file and keeps a backup in `dist/mutant/`. Use it on trust-boundary code. A test that passes only because of a fixed value in the code it checks is not a catch.

---

## Non-Negotiable Rules

1. **No guessing** on wire formats, stored formats or ambiguous architecture. Check the reference first; if still ambiguous, pause, state the ambiguity and what the reference does, and ask.
2. **No release or git by the agent, and a short command list.** The agent may run only `go build`, `go vet`, `go test` (`-race` too), `staticcheck` and `tools/mutant.sh` (`make build`, `make test`, `make test-race` count as build and test). These are not the validation: you run `go vet ./...`, `go test -race ./...`, `staticcheck ./...`, `make build` and, for a step that adds commands, `./artisan list`, and a step is done only when you report them green. Everything else — `./artisan`, `./artisan package:install`, a release, a Docker push, all git operations — is yours: give the exact command and expected output, then wait.
3. **Minimal comments.** A comment says WHY, never WHAT or HOW: no step-by-step descriptions, no comments restating code, no section separators, no phase labels, task ids or doc section numbers, no `ponytail:` comments, no "add X when Y" notes (deferred work goes in the task list), no secrets or real credentials. Go doc comments on exported symbols: one line for simple functions, at most three for complex ones.
