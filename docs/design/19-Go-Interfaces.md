# Design 19 — Go Interfaces

**Status:** partial

| Type | Responsibility |
|---|---|
| `repository.Store` | Repositories (settings, connections, nodes, API keys, combos, kv, meta) and the `Tx` helper. `connections.go` holds CRUD, `locks.go` the per-model backoff and `credentials.go` the refresh-time swap |
| `analytics.Writer` | `Enqueue(Record) bool` (non-blocking), `Close(ctx)`; queries over `requests` and rollups |
| `saml.Verify`, `saml.Metadata` | Bytes in, the assertion that was proved out; and the service-provider document. Nothing in `app/access/saml` exports a function that parses without verifying |
| `http.Authenticator` | `Session`, `APIKey`, `RequireLogin`, `RequireAPIKey`. The guard's only view of login and keys. `auth.Authenticator` implements it; a nil one still denies everything. `auth.Authenticator` also carries `Mode`, `AllowSSO` and `IssueSSO` for the provider routes |
| `failure` | Error bodies and stream frames in the OpenAI, Anthropic, Gemini and flat formats |
| `stream.SSE` | The only event-stream writer |
| `foundation.Application` and the provider runners | `Start` and `Shutdown`; each resource-owning provider returns a runner, and the shutdown priority fixes the stop order |
| `cache.Cache` (`app/platform/cache`) | `Get`, `Set`, `Delete`, `Purge` and `Load` over groups, on top of the `Cache` facade; no method returns an error |
| `schedule.Schedule` | Named events with `SkipIfStillRunning`; our jobs add jitter and last-run status |
| `executor.Executor` | `Execute(ctx, Request) (Response, error)`: one call to one account, the response unread. `executor.Default` is the registry-driven executor with the retry table; built |
| `pipeline.Pipeline` | `Handle(ctx, Input, Opener) Output`: one client request to one answer and one usage row. Takes interfaces (`Resolver`, `Selector`, `Reporter`, `Nodes`, `Prices`, `Usage`, `Overrides`), never a facade. Built |
| `conversation.Store` | `Resolve(Input) string` names the session of a request; `Bound(session, connection)` is the keyed value an upstream may see. Built |
| `translate.Codec`, `translate.Request`, `translate.Response`, `translate.Chunk` | A format's reader and writer over the typed hub, registered by name; `Convert` goes source, hub, target. Built for request and non-stream answer, and for the stream: `NewStreamReader` and `NewStreamWriter` return a `StreamReader` and a `StreamWriter` that speak `Chunk` and `Frame`. `app/llm/stream` runs them (`Run`, `Collect`, `Tracker`, `AddClientBuffer`, `NewSSE`, `NewNDJSON`) |
| `upstream.Registry` | `ByID`, `Resolve`, `Models`, `HasModel`, `All`, `Reserved`; embedded data, validated at load. `app/llm/upstream`, not `app/providers` (Goravel owns that name). Built |
| `upstream.Table` | Price lookup (`Lookup`, `Cost`) over the embedded baseline and the `pricing` overrides. Built |
| `routing.Resolver`, `routing.Snapshot` | Parse a requested name into provider, model, suffix and marker; the registry's `Reserved` guards node prefixes; 5 s snapshot of nodes, aliases and combo names with `Invalidate`. Built |
| `account.Selector`, `account.Reporter` | Pick a connection (per-provider mutex, persisted counters, `Exclude`, `Prefer`); record failures and successes as `modelLock_*`. Built |
| `network.Allow`, `network.NewGuard` (`NewDialer`), `network.NewClient` | Operator allow list, the SSRF dialer guard and the guarded client every outbound call uses |
| `Checker` (`app/http/controllers`) | Key and model-list probes with fixed outcomes. The package exists (`health`, `version`, `init`, `auth` controllers); built; it holds the guarded client and returns one fixed outcome per check |

A row naming a package that does not exist yet is a target shape for a later step.

| Group | Packages |
|---|---|
| Built | `repository`, `analytics`, `http`, `http/controllers`, `failure`, `stream`, `cache`, `identity`, `auth`, `sso`, `oidc`, `saml`, `upstream`, `routing`, `account`, `translate`, `executor`, `conversation`, `pipeline`, `combo`, `health`, `probe`, `network`, `settings`, `sealer`, `dialect`, `document`, `file`, `text`, `lock`, `importer`, `redactor` |
| Built | `saver.Apply` (rewrite the hub request, fails open; `app/gateway/saver`, with the `rtk`, `headroom`, `caveman` and `ponytail` packages and the `prompt` topic under it) |
| Built | `relevance.Tokens`, `relevance.Weigh`, `relevance.Cosine`, `relevance.TriggerBoost`, `translate.Request.LastUserText`, `translate.Request.AppendSystem`, `document.Doc.Str/Bool/Int/Float`, `repository.IsSecretSetting` (the helpers the skill routers, the savers and the settings readers share) |
| Built | `skill.Registry`, `skill.Apply`, `skill.ClassifyLocal`, `skill.ClassifyECC`, `skill.SyncECC`, `skill.MatchRules`, `skill.Create/Update/Delete`, `conversation.SkillSessions` (`app/gateway/skill`: manifests, both TF-IDF routers, the injector, config fields, declarative rules, the Studio and the catalog sync, all fail open; the session table in `app/gateway/conversation`) |
| Built | `plan.Read`, `plan.Decide`, `plan.Inject`, `plan.Combos` (`app/gateway/plan`: the auto plan classifier, the architect prompt, the plan turn and the combo choice, no I/O; `pipeline.Input.Internal` marks the router's own calls, and `routing.Resolver.ComboList` is the ordered list the picker reads) |
| Built | `git.Gate.Clone`, `git.Gate.Download`, `git.Gate.Fetch` (`app/git`: the one process launcher, allowlisted arguments and a minimal environment) |
| Planned | `health.Tracker` (record outcomes, report breaker state; built, `app/gateway/health`) |

---

## Remaining work

A row naming a package that does not exist is a target shape, delivered by that package's design doc. Nothing is built here directly: the interfaces land with the code that implements them.

---

## Findings

Only the interfaces for built packages exist. The rest are target shapes, marked per row.

