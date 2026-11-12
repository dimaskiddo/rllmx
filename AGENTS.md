# Router-LLM - Agent Instructions

Instructions for AI coding agents working in this repository. Humans: start at [`README.md`](README.md) and [`ROADMAP.md`](ROADMAP.md).

Router-LLM (RLLM, binary `rllmx`): LLM gateway in Go on Goravel Lite. One endpoint speaks the OpenAI, Anthropic, Gemini, Responses and Ollama client APIs and routes to many upstream providers. Go rewrite of 9router; opens an existing 9router database as a drop-in replacement. Open source (MIT).

**Scope:** LLM request router. Reject MITM interception, tunnels, relay deployers, process managers, auto-installers, cloud sync, licensing, and anything that shells out beyond the git allowlist.

**This file holds rules.** Behavior, formats, design: `docs/design/` (one design doc per area). Cross-cutting design and the full Go conventions: `docs/ARCHITECTURE.md`. Never restate a design doc rule here; cite it.

---

## Workflow Rules

1. Before every session and plan, read this file, [`README.md`](README.md), `docs/PARITY.md`, then **only the design doc the task touches** (`docs/design/README.md` indexes them). Plans cite the design doc they implement; on conflict, update the design doc first, with approval. A design doc that reaches `done` gets a **Findings** section.
2. Before hand-writing a provider, command, middleware, route file, config file or test, run `./artisan list | grep make:` and scaffold with the matching `make:*` command (`--help` first).
3. Never rework items marked done unless told. Update [`ROADMAP.md`](ROADMAP.md) after a task that changes what is built.
4. Never write the whole codebase in one response.

## Skills

`goravel-development` (`.agents/skills/goravel-development/`, plus `CUSTOM.md` beside it if present) covers the framework. This file beats any skill.

## Clean-Room & Parity

- 9router is a behavior reference only. Never copy, port, import or commit its code, layout or filenames.
- Re-author data tables (providers, prices, capabilities, models) by hand from the reference. Never convert with a script.
- Docs may name 9router. Code, comments, identifiers, tests and fixtures never do, except the on-disk compatibility surface (database identifiers, `modelLock_` keys, API key format) and the one response header existing video clients read, `x-9router-connection-id`.
- **Reference first:** read how 9router does a mechanism before asking. Ask only when it is silent, ambiguous or conflicts with a rule here.
- **Behavior follows 9router 0.5.116.** Translation and execution (translation shapes, the client-compatible headers a login's token carries, request shape, parameter rules, lane choice) follow the reference's behavior, written independently. Router improvements stay: cache, account selection, locks, backoff, 503 and capacity handling, and the Security rules. A kept difference keeps its divergence row.
- Never copy a reference bug. Fix it and record the divergence in the design doc's `Divergences` section, indexed in `docs/DIVERGENCES.md`.
- Every parity row names the reference version and a status (`todo`, `partial`, `done`, `diverged`, `deferred`, `out`). A feature is `done` only when its tests pass and its row is updated in the same change.

## Compatibility Contract

Formats shared with 9router. **Changing any needs maintainer approval.** Additive only. The format lives in the owner's design doc.

| Surface | Owner |
|---|---|
| Tables, columns, indexes, `_meta`, `schemaVersion`, `kv` scopes | `app/store/repository`, `design doc 15` |
| Analytics schema | `app/store/analytics`, `design doc 15` |
| JSON `data` columns, `settings` row | `app/store/document`, `design doc 15` |
| Timestamps and stored values | `app/store/dialect`, `design doc 15` |
| Sealed values (`enc:v1:`) | `app/store/sealer`, `design doc 15` |
| Per-model locks in `providerConnections.data` | `app/gateway/account`, `design doc 5` |
| API key, machine id | `app/access/identity`, `design doc 2` |
| Dashboard session, password hash | `app/access/auth`, `design doc 2` |

A database written by real 9router must open, read, round-trip untouched, and keep unmodified rows byte-identical. Test key, machine-id and JWT code with independently computed vectors.

## CLI

Artisan commands in `app/console/commands`, registered in `bootstrap/commands.go`, run as `rllmx artisan <name>`. Local store only: no remote client, TUI, tray or OS service management. The container runs `rllmx`, which starts the server. Prompt rules: `design doc 23`.

---

## Critical Constraints

Full wording and reasons for the Go rules: `docs/ARCHITECTURE.md`, Go Conventions.

### Build
- `CGO_ENABLED=0` always, except `make test-race` and `make test-pg`, which set it to 1 because `go test -race` requires cgo; no binary is built that way. Linux, macOS, Windows, amd64 and arm64. Go 1.26 (`go.mod`), `GOTOOLCHAIN=local`; a `go` directive bump is its own decision.
- Build through the Makefile (`./artisan build` for a one-off). Dockerfile: pinned base, non-root, no `.env`.
- `os/exec` only through `app/git`, the one deny-by-default gate; a test fails any other importer. Anything else needing a process is out (`design doc 11`, `design doc 22`).

### Goravel
- Framework first (`DB`/`Schema`, `Cache`, `Log`, `Config`, `Schedule`, `Artisan`, `Hash`). A helper only for a gap the facade cannot fill, and it wraps the facade. Before building infrastructure read `./artisan list | grep make:` and `goravel/framework/contracts/`.
- Facade functions only in `app/http`, `app/console`, `app/providers`, `bootstrap/`, `config/`. Product packages take the facade's interface so a test passes a fake.
- A provider's `Register` binds, `Boot` wires cheap things; anything that locks, opens a store or listens runs in a runner's `Run`.
- No `Orm`, models, `database/sql` handle or migration runner of ours; official drivers; additive schema only; destructive commands stay behind `WithCommandsFilter` and the `Artisan` wrapper, new ones on `bootstrap.CommandsAllowlist` (`design doc 15`).
- Config in `config/*.go` via `config.Env("NAME", default)`; add a variable in the change that reads it (`design doc 17`).
- Gin engine untouched; streams only via `stream.NewSSE`/`stream.NewNDJSON`, never `*gin.BodyWriter`; the guard reads `Request.RemoteAddr`, never `Ip()` (`design doc 1`).
- Cache only via the `Cache()` facade and `app/platform/cache`, failing open (`design doc 20`). Usage rows hold metadata only (`design doc 16`). A request-scoped 4xx never locks an account (`design doc 5`).

### Context & Concurrency
- `ctx` first on anything that blocks or does I/O. Request work derives from the request context; no `context.Background()` on a request path. Never store a `Context` in a struct except a runner's lifetime context.
- Every goroutine has an owner, an exit path and a cancel. Fan-out through `errgroup` with `SetLimit`. Every blocking loop selects on `ctx.Done()`.
- `time.NewTimer` with `Stop` inside a loop, never `time.After`.
- No lock held across I/O, a channel operation or a callback. Read-mostly state swaps with `atomic.Pointer`. The sender closes a channel.
- Shutdown is signal plus cancellation; a provider owning a resource returns a runner. `go test -race` always.

### Memory & Speed
- Stream; never buffer a whole body, stream or result set. Bound every read and every structure that grows from input (size, count, depth, TTL).
- Allocate once: preallocate when the size is known, `strings.Builder` with `Grow`, `[]byte` on hot paths, no `string`/`[]byte` round trips.
- `sync.Pool` only for hot buffers: reset on Get, drop oversized ones on Put, never pool a secret.
- Read or patch JSON bodies with `gjson`/`sjson`; typed structs elsewhere; no `map[string]any` round trip.
- Compile a regexp once at package level. No reflection, `unsafe` or `go:linkname`. No `defer` in a tight loop.
- Reuse `http.Client` and `Transport`; drain and close every body; set timeouts.

### Security
- `crypto/rand` only; `subtle.ConstantTimeCompare` for secrets.
- Never log tokens, keys, cookies, passwords or prompt text at INFO; never persist bodies (DEBUG only, redacted). Every `Log` call passes the redacting channel (`design doc 1`, `design doc 17`).
- Validate at trust boundaries (client input, upstream answers, data files). Fail closed for access; fail open for savers, skills and cache.
- TLS always verified. Every outbound URL goes through the SSRF dialer guard; no outbound proxy (`design doc 14`).
- Data files `0600`, dirs `0700`, no system temp dirs.
- Identity is the API key or dashboard session; forwarding headers count only from a trusted proxy peer; local-only routes stay local-only (`design doc 2`).
- Credentials only in memory-only cache groups (`design doc 20`). OAuth client ids and secrets are configuration, never literals (`design doc 6`).
- Client errors use the client's format, no internal detail (`design doc 19`). Skills are data, never code (`design doc 11`).
- `govulncheck ./...` shows no called vulnerability before a step is done.

### Clean Code
- Small functions, early returns. Accept interfaces, return concrete types; define an interface where it is used, none for a single implementation except a facade fake.
- Wrap errors with `%w`, lower-case messages, sentinels with `errors.Is`/`As`; never swallow one. `panic` only in start-up wiring.
- No stubs, `// TODO` or dead code; delete what a change makes unused. One rule, one implementation: extract to the package that owns the concept, test scaffolding included.
- Useful zero values; no package-level mutable state; `init()` only to register codecs.
- Tests: stdlib `testing`, table-driven, beside the code; a fuzz target per parser; golden tests for formats; `tools/mutant.sh` on trust-boundary code.

### Naming & Layout
- `app/` is grouped into **domain folders**: `platform` (cache network settings file text ttl redactor failure), `protocol` (sigv4 eventstream cosy keccak), `store` (repository data importer analytics sealer dialect document lock), `access` (auth identity oidc saml sso), `llm` (upstream routing stream quota zed, and the large translate, executor and oauth), `gateway` (pipeline catalog conversation prefetch account). Goravel's `app/http`, `app/console`, `app/providers`, `app/facades` stay where the framework puts them. A domain folder holds packages; a package holds sub-packages only when its provider-specific code separates cleanly from a shared framework (about 25 source files and a framework plus independent families). `translate` splits by wire format, `executor` by provider family and `oauth` by provider, its `Dialects` table built in `oauth/dialects`.
- **Dependency direction is down only:** `platform`, then `protocol`, then `store`, then `access`, then `llm`, then `gateway`. A package imports its own domain's packages and the domains before it, never one after. A sub-package imports its parent's framework and never a sibling; a parent never imports a sub-package except in a wiring package (`executor/mux` builds the executor table). `tests/imports_test.go` fails an upward or sibling import, and a format package may be imported only by the pipeline that registers it and by tests.
- Package: one lower-case noun, no `util`/`common`/`helpers`/`shared`, never a Goravel name reused (`app/providers` is Goravel's; upstream LLM code is `app/llm/upstream`). The folder path is the domain, the package clause is the noun.
- No package-level mutable registry. Provider sub-packages are wired by explicit construction in one place per domain, not by `init()`. The one exception stays: a `translate` codec may register itself in `init()`, and the package that runs requests blank-imports every format.
- Files `snake_case.go` for the type or topic, one word per segment: `call_headers.go`, `device_login_test.go`, never `callheaders.go`. A single English word needs no underscore (`pipeline.go`). A provider file is `<provider_id>.go`. Exported names `StudlyCase`, not repeating the package (`auth.Login`); `…Controller`, `…ServiceProvider`; Artisan signature `group:action`.
- `pkg/` only if it imports no `app/` package, takes no product types, and has an outside consumer; there: documented exports, safe zero values, sentinel errors, golden tests, a fuzz target per parser. A format owner documents the format beside the code; a change is breaking.
- Renaming a package or file updates the docs in the same change. Audits cover `app/`, `bootstrap/`, `config/`, `routes/`, `pkg/` and `web/`.

### Dependencies
- Order: stdlib, the framework, the official Goravel driver for a facade in use, a module the framework already requires, our own code. CGO-free.
- Anything else needs maintainer approval, an entry in the approved-module list in `docs/ARCHITECTURE.md`, and the reason recorded there before `go.mod` changes. Pin releases whose `go` directive is not newer than `go.mod`. `./artisan package:install` needs approval.

### Web Dashboard & Artifacts
- Plain HTML, ES modules, CSS: no framework, bundler, build step or runtime CDN. Escape dynamic text; no `innerHTML` from data (`design doc 18`).
- Output to `dist/` via the Makefile. `tools/mutant.sh <label> <file> <old> <new>` prints CAUGHT, MISSED, BUILD or NOOP.

---

## Non-Negotiable Rules

1. **No guessing** on wire formats, stored formats or ambiguous architecture. Check the reference; if still unclear, stop, state the ambiguity and what the reference does, and ask.
2. **Commands.** While working: `go build`, `go vet`, `go test` (`-race` too), `staticcheck`, `govulncheck`, `tools/mutant.sh` (`make build|test|test-race` count). The final gate is `go vet ./... && go test -race ./... && staticcheck ./... && make build` (plus `./artisan list` for a change that adds commands), run by the maintainer or CI; a change is done only when it is green. `./artisan`, `go get`, `go mod tidy`, releases, Docker pushes and git operations are the maintainer's.
3. **Minimal comments.** WHY only, never WHAT or HOW. No step lists, no restating code, no separators, no phase labels, task ids or doc section numbers, no "add X when Y" (that belongs in the roadmap), no secrets. Doc comments on exported symbols start with the name: one line for simple, three at most for complex.

## References

| Path | Holds |
|---|---|
| `app/` | Product code in domain folders, one package per concern (`Naming & Layout`) |
| `bootstrap/`, `config/`, `routes/` | Goravel wiring; one config file per concern; the guarded route table |
| `pkg/` | Importable packages (none yet) |
| `web/` | Embedded dashboard files |
| `tests/` | Shared test helpers and doubles |
| `tools/mutant.sh` | Mutation check |
| `references/` | Read-only 9router sources (git-ignored; Clean-Room) |
| `docs/design/` | One design doc per area: status, design, parity rows, divergences, findings. **Design authority**; index in `docs/design/README.md` |
| `docs/ARCHITECTURE.md` | Module map, design decisions, approved modules, Go conventions, terminal commands |
| `docs/PARITY.md`, `docs/DIVERGENCES.md` | Pinned references and status vocabulary; divergence index |
| `docs/WORKFLOWS.md` | Pipeline, startup order and operator procedures |
| `ROADMAP.md` | What is built, partial and planned, written for readers |
| `.env.example` | Every variable with its default (`design doc 17`) |
| `Makefile`, `Dockerfile`, `.goreleaser.yml` | Build, container, release |
