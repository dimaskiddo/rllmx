# PRD 19 — Go Interfaces

**Status:** partial · **Steps:** 1.0 to 1.10

| Type | Responsibility |
|---|---|
| `repository.Store` | Repositories (settings, connections, nodes, API keys, combos, kv, meta) and the `Tx` helper. `connections.go` holds CRUD, `locks.go` the per-model backoff and `credentials.go` the refresh-time swap |
| `analytics.Writer` | `Enqueue(Record) bool` (non-blocking), `Close(ctx)`; queries over `requests` and rollups |
| `saml.Verify`, `saml.Metadata` | Bytes in, the assertion that was proved out; and the service-provider document. Nothing in `app/access/saml` exports a function that parses without verifying |
| `http.Authenticator` | `Session`, `APIKey`, `RequireLogin`, `RequireAPIKey`. The guard's only view of login and keys. `auth.Authenticator` implements it since 1.6a; a nil one still denies everything. `auth.Authenticator` also carries `Mode`, `AllowSSO` and `IssueSSO` for the provider routes |
| `failure` | Error bodies and stream frames in the OpenAI, Anthropic, Gemini and flat formats |
| `stream.SSE` | The only event-stream writer |
| `foundation.Application` and the provider runners | `Start` and `Shutdown`; each resource-owning provider returns a runner, and the shutdown priority fixes the stop order |
| `cache.Cache` (`app/platform/cache`) | `Get`, `Set`, `Delete`, `Purge` and `Load` over groups, on top of the `Cache` facade; no method returns an error |
| `schedule.Schedule` | Named events with `SkipIfStillRunning`; our jobs add jitter and last-run status |
| `executor.Executor` | `Execute(ctx, Request) (Response, error)`: one call to one account, the response unread. `executor.Default` is the registry-driven executor with the retry table; built in 1.8c |
| `pipeline.Pipeline` | `Handle(ctx, Input, Opener) Output`: one client request to one answer and one usage row. Takes interfaces (`Resolver`, `Selector`, `Reporter`, `Nodes`, `Prices`, `Usage`, `Overrides`), never a facade. Built in 1.8c |
| `conversation.Store` | `Resolve(Input) string` names the session of a request; `Bound(session, connection)` is the keyed value an upstream may see. Built in 1.8c |
| `translate.Codec`, `translate.Request`, `translate.Response`, `translate.Chunk` | A format's reader and writer over the typed hub, registered by name; `Convert` goes source, hub, target. Built in 1.8a for request and non-stream answer, and in 1.8b for the stream: `NewStreamReader` and `NewStreamWriter` return a `StreamReader` and a `StreamWriter` that speak `Chunk` and `Frame`. `app/llm/stream` runs them (`Run`, `Collect`, `Tracker`, `AddClientBuffer`, `NewSSE`, `NewNDJSON`) |
| `upstream.Registry` | `ByID`, `Resolve`, `Models`, `HasModel`, `All`, `Reserved`; embedded data, validated at load. `app/llm/upstream`, not `app/providers` (Goravel owns that name). Built in 1.7a |
| `upstream.Table` | Price lookup (`Lookup`, `Cost`) over the embedded baseline and the `pricing` overrides. Built in 1.7a |
| `routing.Resolver`, `routing.Snapshot` | Parse a requested name into provider, model, suffix and marker; the registry's `Reserved` guards node prefixes; 5 s snapshot of nodes, aliases and combo names with `Invalidate`. Built in 1.7a |
| `account.Selector`, `account.Reporter` | Pick a connection (per-provider mutex, persisted counters, `Exclude`, `Prefer`); record failures and successes as `modelLock_*`. Built in 1.7b |
| `network.Allow`, `network.NewGuard` (`NewDialer`), `network.NewClient` | Operator allow list, the SSRF dialer guard and the guarded client every outbound call uses |
| `Checker` (`app/http/controllers`) | Key and model-list probes with fixed outcomes. The package exists (`health`, `version`, `init`, `auth` controllers); built in 1.7d; it holds the guarded client and returns one fixed outcome per check |

Rows naming a package that does not exist yet are target shapes for later steps: built today are `repository`, `analytics`, `http`, `http/controllers`, `failure`, `stream`, `cache`, `identity`, `auth`, `sso`, `oidc`, `saml`, `upstream`, `routing`, `account`, `translate`, `executor`, `conversation`, `pipeline`, `network`, `settings`, `sealer`, `dialect`, `document`, `file`, `text`, `lock`, `importer` and `redactor`. Also planned: `combo.Strategy` (order and run members), `saver.Transformer` (transform a translated body, fails open), `skill.Hook` (evaluate a rule, return actions), `health.Tracker` (record outcomes, report breaker state).

---

## Remaining work

A row naming a package that does not exist is a target shape, delivered by that package's PRD. Nothing is built here directly: the interfaces land with the code that implements them.

---

## Findings

Only the interfaces for built packages exist. The rest are target shapes, marked per row.
