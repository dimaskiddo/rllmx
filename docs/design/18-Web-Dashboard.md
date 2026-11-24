# Design 18 — Web Dashboard (`web/`)

**Status:** partial

`web/` holds the dashboard shell, the sign-in page, the OAuth callback page, the page fragments, the JS core, the translations, and the vendored libraries in `static/lib/`. `web/embed.go` (`web.Static`, `go:embed static`) serves it through `PagesController`. `GET /` redirects to `/dashboard`, `/login` serves `static/login.html`, `/dashboard` and `/dashboard/*path` serve `static/shell.html` as `ClassPage`, and `/callback` serves its own page with `no-store`.

`GET /static/*file` serves the assets and the page fragments. A page and an asset answer with an `ETag` and `no-cache`; `Asset` serves only an allow-listed extension and refuses dot, backslash, colon and control segments.

The stack is decided in step 4.0. Alpine.js (CSP build) holds the state and binds the markup, Tabler CSS gives the layout and controls, `theme.css` and `components.css` give the flat Basecoat-like look, Lucide gives the icons, Chart.js the charts and Sortable.js the drag order. All five are vendored and pinned (`docs/ARCHITECTURE.md`, Approved Web Assets).

There is no bundler, build step or runtime CDN. Pages call the same `/api/*` paths and JSON shapes as the reference dashboard. The page table is below.

| Area | Pages |
|---|---|
| Home | home |
| Providers | list, new, detail (connections, models, overrides, test) |
| Routing | combos (presets, suggestions), endpoint (base URL, API keys, token saver) |
| Media | per-kind provider pages, combos |
| Monitoring | usage, quota, console log |
| Network | token saver |
| Tools | translator playground (shown when the planned `TRANSLATOR_ENABLED` is true), basic chat, skills (Studio) |
| Account | profile, login, setup wizard |
| Settings | pricing, general |
| Extended | routing health, auto plan, skills |

| Rule | Detail |
|---|---|
| i18n | `web/static/i18n/en.json` and `id.json`, flat dotted keys; the readable `locale` cookie is set by `/api/locale`, and a switch reloads the page. Markup uses `x-t="key"`, `label('key')` where the key is chosen at render, and JS `t("key")`; a Go test holds both files to the same keys and resolves every use. A new locale is a new JSON file |
| Escaping | Dynamic text uses `x-text`, `textContent` or a bound attribute, never `innerHTML`, `x-html` or `insertAdjacentHTML`. `tests/web_test.go` fails any of them in our own files |
| Icons | `x-icon="name"` reads the raw word and looks it up in `js/ui/icons.js`; the name maps to one vendored `lucide/icons/<name>.mjs`. A name that is not mapped is a test failure |
| Expressions | The Go scan refuses `=>`, a template literal, spread and the `window`, `document`, `JSON` and `Math` globals inside an Alpine attribute, because the CSP build cannot parse them |
| Page policy | `default-src 'self'; img-src 'self' data:`. No inline script, import map, `style` attribute or `eval`. A page is a static file plus a script file; the theme bootstrap is a script file, not a snippet |
| Alpine | The CSP build. Expressions are property reads, comparisons, ternaries, literals and method calls (arguments are evaluated by the same rules). No arrow function, template literal, spread, global (`window`, `Math`, `JSON`) or property assignment inside `@event`. Anything longer is a method of an `Alpine.data` component. Style bindings use the object form, `:style="{ width: w }"`, never a string |
| Directives | `js/ui/directives.js` registers `x-t`, `x-icon` and `x-tip`, which sets the hover text of an icon-only button from a key. A page served on its own, `login.html` or the shell, loads a script that imports it, because an unregistered directive is a silent no-op and the page renders without text. A test pairs every such page with the import |
| Search | A search box carries `x-on:input.debounce.250ms`, so the list re-renders after a pause and not on every character. A test fails a `type="search"` without it |
| Reload | A page's `load()` shows "Loading" the first time only. A later load, after a save or a switch, swaps the data under what is on screen; raising the flag again hid the whole list and made the page jump |
| Selects | A select built from a list marks its chosen option with `x-bind:selected`. A value on the select alone is applied before the options exist, and the browser falls back to the first one. A test fails the old form |
| Model pickers | A page that offers model names fills `choices` from `loadModelChoices()` in `js/pages/model-choices.js` (aliases, combo names, live models of active providers, custom nodes). A test fails a datalist built from `choices` on a page that does not declare it |
| Compact counts | A chart's value axis reads 1.5K, 2.0M, 3.0B, 1.0T through `compactTokens` in `js/ui/format.js` |
| Magics and calls | An expression may use only the magics the CSP build defines (`$event`, `$store`, `$refs`, `$el`, `$data`, `$id`, `$dispatch`, `$nextTick`, `$root`, `$watch`); `$index` is not one. Every function a fragment calls must be defined by its page script or a module the script mixes in. An event handler never assigns a property |
| Components | A page script holds state and lifecycle; larger pages mix in modules (`provider-connections`, `-models`, `-settings`, `-login`, and `ui/format`). A pure rule with no imports, such as `renumber`, `psdBody` or `mergeConnection`, lives in `provider-status.js`, so a script can load it on its own. A text input is bound with `setField('path', $event)`, not a setter per field |
| Accessibility | Every `<dialog>` names itself by `aria-labelledby` to a heading in the same file. The toast stack is a manual popover with `role="status"` and `aria-live`, so a toast shows above an open modal and is announced. Both entry scripts set `<html lang>` from the locale. Icon-only buttons carry a visually hidden label or a `title` |
| Secrets | A dialog that holds a typed secret clears it on its own `close` event, which Escape fires too. `nextPath` resolves the `next` address as the browser will and accepts only `/dashboard` paths |
| Dead code | Every mapped icon is drawn by an `x-icon` somewhere, and every `.rl-` rule in `components.css` is used by a page or script. A test fails the first unused one |
| Pages | One shell serves `/dashboard` and `/dashboard/*path`. `js/routes.json` maps a path to a page; the History-API router imports `js/pages/<page>.js`, its `Alpine.data` component, then fetches `pages/<page>.html`, parses it and attaches its nodes, so a fragment is never built from data. An unrouted path shows `pages/not-found.html`. Tests pair every route with its two files and refuse an orphan fragment |
| Server routes | `/` (public, 302 to `/dashboard`), `/login` (public), `/callback` (public, `no-store`), `/static/*file` (public), `/dashboard` and `/dashboard/*path` (`ClassPage`). The guard's redirect carries no return path; the login page reads `?next=` itself and follows it only when it is a `/dashboard` path |
| Libraries | A new library is an entry in `docs/ARCHITECTURE.md` first. Its files are copied unchanged into `static/lib/` and listed with their sha256 in `VERSIONS.json`. Chart.js uses its UMD file, because the ES build needs an import map |
| Console Log | `GET /api/translator/console-logs` returns the ring (500 lines) and `GET …/stream` follows it: `init` with the ring, `lines` in batches, `clear` when the ring is emptied, a comment every 25 s. The lines are already redacted, so the page renders them as text and never re-fetches the log file. It is drawn as a terminal (D441): a status dot, a level filter, search, copy and clear, and the view follows the tail until the reader scrolls up. SSE does the job a WebSocket would, because the traffic is one way, and no module is added for it |
| Secrets | The API masks them and the dashboard never receives raw tokens. The one exception is a deliberate reveal: a signed-in operator asks for one key by id and the answer is logged (D330) |
| Sidebar | Two groups in one nav: Router (Endpoint & Keys, Providers, Combos, Token Saver, Skills) and System (Usage, Quota, Pricing, Users, Settings, Console Log). `router.js` `navRoots` lists the same order, and `tests/web_test.go` fails when the two drift, and again when a menu entry and its page title read differently. One header button changes the sidebar's shape: under 768px it opens the nav as a drawer over the page and closes on a tap away, Escape or a route change; wider, it flips a rail that keeps the icons and the accessible names (D435) |
| Roles | The shell reads `/api/auth/status` and shows a standard user only Endpoint & Keys and Usage; every other entry is admin-only and the page behind it is refused by the server (`design doc 2`). The Endpoint page hides the create and delete key controls, the pause/resume buttons and the settings switches; an edit by a user reaches the name alone, with every other field in a disabled fieldset. The Users page is admin-only through the sidebar and the server. The Usage page asks for the live feed, the active count and the latency stream only behind `isAdmin()`; the totals, the chart and the log are the user-allowed reads |
| Accounts | `/dashboard/users` lists the standard-user accounts with the keys each holds and the SSO badge an account single sign-on provisioned carries, and its dialog creates, edits and deletes through `/api/users`. The Endpoint page keeps one read of the same list for its owner selects and the key badge |
| Streams | The usage feed, the latency feed and the console log are one `EventSource` each, closed in the component's `destroy()` so leaving the page ends the read. The log tab and the active count poll only while the tab is visible; a hidden tab skips its tick |
| Charts | Chart.js draws the usage trend and the two bar charts. A chart is built by a component method from `ui/charts.js` and destroyed with the page; no Chart.js call sits in an Alpine expression, where the instance has nowhere to live. The provider topology is our own SVG, drawn node by node with `createElementNS`, so no React Flow and no new library |
| Quota | One card per connection a reading answers for, in pages of 10 limits. A reading's 401 is the provider refusing the stored credential, not the session ending, so the page reads it with its own fetch and shows the message instead of the shared `api()` sign-out |
| Layout | A switch is a flex row with no Tabler padding or float, so it centres in a card header, a toolbar or a list row like the controls beside it; a select there takes its text's width, not the whole line. A stacked label keeps 0.75rem from the control above it and none when it opens a group. A card's primary switch sits in its `.rl-card-head`, right; secondary controls stay inline in the body. The usage page caps the topology and clips the recent table to the card's height, its model column 45% wide and cut with the full name in `title`. A budget meter list is one grid, so every bar is the same width whatever its numbers; a chart and the topology name a provider node by its name, not its id. A combo row puts the name, the strategy selects and the actions on one line. A provider card's action row is pinned to its foot, so every card in a row is one height. A capability icon is an inline-flex box, so it centres against the text it follows |

### Provider Routes (`app/http/controllers`)

Protected: a session, or a local caller while `requireLogin` is off; cross-site writes are refused. The account logins under `/api/oauth/*` are always-protected (`design doc 2`).

| Routes | Purpose |
|---|---|
| `GET /api/providers/catalog` | Provider names, categories, service kinds, regions and login shapes |
| `GET/POST /api/providers`, `GET/PUT/DELETE /api/providers/{id}` | Connections |
| `GET/PUT /api/providers/{id}/overrides` | Header overrides (design doc 8) |
| `POST /api/providers/{id}/test`, `POST /api/providers/validate`, `GET /api/providers/{id}/models` | Key checks and model lists (design doc 14) |
| `GET/POST /api/provider-nodes`, `PUT/DELETE /api/provider-nodes/{id}`, `POST /api/provider-nodes/validate` | Custom nodes |
| `GET/POST /api/skills`, `GET/PUT/DELETE /api/skills/{id}`, `POST /api/skills/sync` | Skills Studio: the list, the catalog sync and a custom skill's create, read, edit and delete (design doc 11) |

| Rule | Detail |
|---|---|
| Output | Everything returned about a connection or node goes through an allow list (`ConnectionView`, `NodeView`). A new stored field stays hidden until listed. A name holding 32 or more key characters shows its first eight plus `***`. A base URL shows without credentials, query or fragment |
| fal key | A fal key is judged by `GET https://api.fal.ai/v1/models?limit=1` with `Authorization: Key …`: the queue host answers a bare 404 before it reads a key, so only the model catalog tells a bad key from a good one |
| Input | Writes accept only listed fields. `modelLock_*`, `backoffLevel`, tokens and the proxy family cannot be written |
| Creating | Only for a registered provider (API-key, free, cookie or `apikey` mode) or an existing node. A provider whose data file has not landed cannot get a new connection from the dashboard; its existing connections are still listed, selected and locked. `design doc 8` lists the ids and their status (all `todo` today) |
| Node connection | Always takes `prefix`, `apiType`, `baseUrl` and `nodeName` from the node |
| Catalog | An allow list over `Registry.All()`: id, alias, display name, category, service kinds, region ids and the login shape. No URL, client id, scope, header or model list leaves the process. `enabled` is false exactly when `NewFlow` would refuse the start |
| Logins | The page opens the sign-in address with `noopener` and polls `GET /api/oauth/status` (code) or `POST /api/oauth/poll` (device). Every timer is cleared when the dialog closes or the page leaves, so no login outlives its page. `/callback` posts no message, so polling is the only finish signal |
| Models | A registry provider lists `/api/models` plus its custom models; disabling writes under the provider's alias, and an alias names `provider/model` by provider id. A custom endpoint lists its own models and imports the live list from its first active connection |
| Overrides | Rows start from the provider's own headers, key request and login together, with the computed beta list and the internal keys left out; a changed row is marked, only changed rows are saved, so an unedited default still follows the router's next version, and Reset clears the stored overrides. The server decides a reserved name or a bad value, and the page shows its error as it is |
| Node writes | Stricter than the reference: prefix matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, is not a built-in id or alias, and is unique; base URL is http or https with a host, no credentials, query or fragment, at most 2048 bytes. Create, update (with its connections) and delete (with its connections) are each one transaction. Rows that break these rules still read and resolve |

---

## Parity: Dashboard Pages

Plain HTML, ES modules, CSS; same flows and `/api/*` contract (design doc 18).

| Pages | Status |
|---|---|
| login, the shell and router, endpoint and keys (base URL, key guard, keys: create, reveal, pause, rename, delete, access list, budgets with their remaining share, response cache), i18n `en` and `id`, theme switch | done (4.1a) |
| providers: list (search, filter, status tags, test all), detail (connections, order, round robin, every login flow, custom endpoints), models (list, alias, custom, disabled, test), header overrides | done (4.1b) |
| 4.2 audit fixes: login directives, reorder, sticky limit, polling and router races, key/value editor, secrets cleared, contrast, dialogs and toasts, split provider modules | done (4.2) |
| home: the dashboard root opens Endpoint & Keys, as 4.1a decided | done (4.1a) |
| combos: list, search, per-combo strategy, smart policy, judge and router, create/edit with drag and arrow reorder, delete, auto-suggest with weights, prompt and accept | done (4.3a) |
| usage: period, five totals, live pills, provider topology, recent requests, trend chart, provider and model bars, grouped table, request log | done (4.3b) |
| quota: provider and status filters, per-account limits with reset countdowns, reset credits, active switch, auto-refresh | done (4.3b) |
| console log: the ring over its stream, level colours, search, clear | done (4.3b) |
| settings: login, password, auth mode, OIDC and SAML, routing strategy and sticky limits | done (4.3c) |
| pricing: per-provider tables, edit, save, reset | done (4.3c) |
| skills Studio: list, switch, mode, config fields, create/edit with prompt and rules, delete, catalog sync, auto plan | done (4.3d) |
| users: accounts with their keys and the SSO badge, create/edit/delete through the user dialog | done (4.5d) |
| token saver: RTK, Headroom, Caveman, Ponytail, custom prompt | done (4.3d); a prompt's level is a select, and the watermark remover reads its own switch from the skills list because a skill's keys are not in the settings view (4.6) |
| media providers (per kind, combos), translator playground, basic chat, routing health, setup wizard | todo (5.x); the system-one router card waits for the smart router's own page |
| profile as the reference draws it (database backup and import, proxy, observability, shutdown, update check) | out: the routes do not exist here |
| cli-tools, mitm, pxpipe, other i18n locales | deferred |
| proxy pages and the per-connection proxy fields | out |
| landing | out |

---

## Remaining work
**The callback page and the `/api/*` groups listed below are built; the pages are not.**

- [x] 4.0: the stack decision, the vendored libraries, `GET /static/*file`, the page policy, the theme layer and `tests/web_test.go`
- [x] 4.1a: the routes `/`, `/login`, `/dashboard` and `/dashboard/*path`, the shell and router, the `ui` core (`api`, i18n, icons, toast, confirm dialog, copy), the theme switch, i18n (`web/static/i18n/{en,id}.json`), login, endpoint and keys
- [x] 4.1b: the provider catalog route, the providers list and detail, every login flow, custom nodes, models, aliases, custom and disabled models, header overrides
- [x] 4.2: the audit of 4.0 to 4.1b, its fixes and the web scans
- [x] 4.3a: the vendored Chart.js and Sortable.js, the nav and router table, the combos page
- [x] 4.3b: the usage page with its two charts and the SVG topology, the quota page, the console log
- [x] 4.3c: the settings page and the pricing page, plus `fallbackStrategy` on the settings write
- [x] 4.3d: the skills list and Studio, the catalog sync, auto plan, the token saver page
- [x] `/api/settings`, `/api/locale`, `/api/tags` and the Console Log ring with its read and stream routes (2.6a); `/api/keys/*` (2.1)
- [x] `/api/models/*`, `/api/pricing`, `/api/combos/*` and presets (2.6b)
- [x] `/api/usage/*` and its live feed (2.6c)
- [x] `/api/models/catalog-sync` and its status route (2.6e)
- **Done when:** no `innerHTML` from data, every dynamic string escaped (`tests/web_test.go` checks the first).

---

## Divergences

| Reference mechanism | Ours | Why |
|---|---|---|
| The login page goes to the dashboard when `requireLogin` is off, authenticated or not (D401) | It goes only when `/api/auth/status` says `authenticated`; otherwise the form stays, with a note | Keys, settings and provider logins need a session even with login off, so that rule would send the browser to a dashboard whose first read answers 401 and back again |
| A provider card shows the provider's image from a bundled asset folder (D402) | The images are imported and shown; the first two letters remain the fallback for an id with no image or a failed load | A missing image must not leave a hole; each mark stays its vendor's |
| A priority move writes 0-based indexes to the two rows and leaves the rest (D403) | The list is renumbered from 1 and only the rows whose number changed are written | Priority is validated from 1; a swap that leaves duplicates or a zero is refused or reorders the list unpredictably |
| A strategy write rebuilds the provider's entry and loses any other key in it (D404) | The entry is edited: only `fallbackStrategy` and `stickyRoundRobinLimit` change, and other keys and providers are kept | An imported 9router row may carry proxy keys; dropping them silently changes routing |
| A connection with the same name is overwritten without a word (D405) | The 409 `PROVIDER_NAME_CONFLICT` asks, and replacing sends `allowOverwrite` | A pasted wrong key would destroy a working connection |
| One shared model-test error line (D406) | Each model keeps its own result and error | A later success must not hide an earlier failure |
| The override card is hidden when the provider has no built-in header (D407) | Offered for every provider | The server accepts an override for any provider, so the card reflects what it can do |
| Errors arrive as `alert()` next to toasts and inline text (D408) | A toast or an inline line, and nothing else | One channel, one place to look |
| A bare strategy word is stored but the selector reads only the object (D409) | The selector reads both shapes, and only `fill-first` and `round-robin` are accepted on write | A saved strategy must do what it says; `smart` and `fallback` are combo words that behaved as fill-first |
| A deleted extra field stays stored and every edit turns a number into text (D410) | A removed row is sent as `null`, an untouched row is not sent, and an edited one keeps its type | The merge removes a key only on `null`, and a provider that reads a number must still get one |

| A preset button pair sits in a `hidden` div, and the bulk strategy select calls a handler that is never defined (D425) | Presets stay API-only (`/api/combos/presets`) and the list has no bulk toolbar | A button that throws is worse than no button; per-card strategy is the working path |
| The combos page edits the vision and audio capacity pools (D426) | Retired with D337: one block per capability, with its enable and round-robin switches and its ordered models | The pool is built (doc 3), so the page edits it instead of leaving a saved switch with no effect |
| The usage page's Details tab lists stored request and response bodies (D427) | No Details tab; the Logs tab is the request record | `/api/usage/request-details` is out (doc 16); usage rows hold metadata only |
| The reference settings page carries database backup and import, an outbound proxy, observability, a shutdown button and an update check (D428) | Login, single sign-on and routing only | Those routes do not exist here: there is no backup route, proxy is deferred (doc 22), observability is stored with no behavior (doc 17), and `/api/version/shutdown` is absent |
| The quota page hides single limit rows and turns empty accounts off in bulk (D429) | Per account: refresh, reset credits, active switch, auto-refresh | `quotaVisibility` is stored but not in the settings allow list, and the two bulk actions need a key of their own; the actions that change state stay explicit and confirmed |
| The Studio writes `hook_script` and shell install commands (D430) | The Studio writes a prompt or rules, and the server refuses the other fields | A skill is data, never code (doc 11); nothing here runs a process |
| The topology is a React Flow graph with an elliptic layout (D431) | An SVG ring drawn node by node with `createElementNS` | No new library and no layout engine; a provider node is a circle, a label and a link |
| Pricing is a modal under `/dashboard/settings/pricing` (D432) | One page at `/dashboard/pricing`, one table per provider, saving only the providers touched | A modal hides the table behind a click and rewrites the whole map on save |
| The reference collapses the sidebar only as a phone drawer (D435) | The same drawer on a phone, and a remembered icon-only rail on a wider screen | A laptop window narrows too, and an operator who reads the icons wants the width back; the choice is stored beside the theme |
| The Console Log is a plain list in a card (D441) | A dark terminal: monospace, a colour per level, a status dot that says connecting, live or reconnecting, a level filter, copy, and a follow-the-tail that lets go when the reader scrolls up, with a jump button; the ring holds 500 lines | A reader scrolled up to an older line was pulled back to the bottom on every batch, and a warning was never coloured because the page matched a level name the logger does not write |

## Findings

| Finding | Decision |
|---|---|
| The standard Alpine build compiles expressions with `Function`, which the page policy blocks | The CSP build, 3.17.5. Its evaluator was read in the source: calls evaluate their arguments with the same rules, and `setAttribute`, `createElement`, `appendChild` and `insertAdjacentHTML` are refused by name inside an expression |
| Chart.js's ES build imports the bare name `@kurkle/color`, which needs an import map | The UMD file, a classic script with the global `Chart`. It holds no `eval`, `innerHTML` or inline style write |
| Tabler's stylesheet draws 25 controls from inline SVG `data:` images | The policy adds `img-src 'self' data:` and nothing else. Scripts and styles stay on `default-src 'self'` |
| The `vendor` folder name is git-ignored | The libraries live in `web/static/lib/` |
| The tarballs carry no licence file for Alpine or Tabler | The text was taken from each project's repository and stored beside the files |
| A browser run was not possible in this environment | The CSP spike page was written and served with the real policy, but the headless browser was refused, so no PASS line was seen. 4.1 opens the first page in Chrome and Firefox with the console open and records the result here |
| `web_test.go` can read files, not run them | It checks forbidden constructs, hashes and references. A script error shows only in a browser |
| A translation or an icon was to be picked by an evaluated expression, which the CSP build would run | `x-t` and `x-icon` are directives that read the raw attribute word. The word is looked up, never evaluated, so a key can never become code |
| `/api/auth/status` did not say which single sign-on type is configured, and its labels sit behind an authenticated settings read | The status answer gains `oidc`, `saml` and both labels. Both are public by nature and no secret |
| The shell loads before its own data, and the page policy forbids an inline theme snippet | `static/js/theme.js` is a classic script in the head, so the theme is set before the first paint |
| A `<dialog>` cannot be opened from an Alpine store | One confirm dialog in the shell, filled and shown by `ui/dialog.js`, and each page's own `<dialog>` opened through `$refs` |
| A pause tooltip that says "Pause" names the verb, not the state the click leaves the key in | Play says "Active" and Pause says "Inactive", as the cards do |
| Tabler's paragraph margin cannot collapse out of a list row, so the first row had 0.75rem above and 1.75rem below | A row's paragraphs lose their bottom margin, and the first row adds no top padding of its own |
| Tabler gives a checkbox a bottom margin only, so one under a text input touched it | A dialog's `.form-check` gets a top margin |
| A per-row alias-removal button sat disabled in almost every row | The alias dialog clears the alias itself, behind a confirmation |
| The failure badge printed the count, the lowercase word and a tag that said "Error" again ("2 error Error") | One helper builds "2 Error (429)", the reference's shape |
| Copying a key revealed it on screen as a side effect | Copy fetches the value and leaves the row masked; only the eye button reveals |
| The custom-provider hint wrote "OpenAI- or Anthropic-compatible", and the suspended hyphen read as a stray dash | The hint says "compatible with OpenAI or Anthropic" |
| A stretched-link provider name underlined on hover because of a page rule | The hover rule removes the underline; the pointer still says it is a link |
| An account provisioned by single sign-on has no password its user knows, so asking for the current one is a dead end | `/api/auth/status` answers `needsPassword`, the shell hides the current field, and the request sends it empty; the server skips the check for a provisioned row only |
| A provisioned row and a hand-made one look the same in the users card | The card marks a provisioned row "SSO" and hides an email that repeats the username |
| `PATCH /api/settings` refused the per-provider strategy object the selector reads | The rule takes the bare word or the object, keeps an entry's other keys, and bounds the sticky limit from 1 to 100 |
| The registry describes no per-provider form fields | One key/value editor for `providerSpecificData` on create and edit, plus a region select from the catalog. A masked value is shown read-only and never sent back |
| The catalog lists only chat providers, so a media-only provider has no card yet | The pages filter on `kinds` containing `llm`; media pages in 4.3 read the same catalog |
| OpenCode Free takes no credential, and no connection can be created for it today | The detail page says so and offers no add path; making a keyless provider route is a step of its own |
| 4.2: the sign-in page rendered with no text or icons, because `login.js` never registered `x-t` and `x-icon` | `ui/directives.js`, imported by every entry script, and a test that pairs each standalone page with it |
| 4.2: the reorder buttons called `move($index, …)`, which Alpine does not define, and the providers list called `nodeLabel`, which only the detail page defined | Named loop indexes; one shared `nodeTypeLabel`; two scans that check every `$name` and every called function against what the page can reach |
| 4.2: the settings write accepted `smart` and `fallback` as a provider strategy and refused `fill-first`, which the selector uses | `account.KnownStrategy` is the one list, the selector reads the bare word too, and the combo words are refused (D409) |
| 4.2: an OIDC issuer with a path passed the settings API, turned the password form off, and then failed discovery | The API stores what `oidc.ParseIssuer` returns, and `Usable` requires a valid issuer for both OIDC and SAML before the form is hidden |
| 4.2: `auth:mode` kept the stored client secret when the issuer or client id changed | The secret is asked again whenever either differs from the stored one |
| 4.2 browser pass: a registry provider showed `claude/undefined` and no built-in models | The list is read from `model`, the field `/api/models` sends; custom models still come from their own list. Built-in rows carry a badge |
| 4.2 browser pass: a node's models showed its internal id, and the whole provider card was not clickable | Models show `alias/model`, the stored target stays out of view; the card's link is stretched across the card |
| 4.2 browser pass: icon-only buttons gave no hint of what they do | Each carries a hover title from its hidden label, through `x-tip` or a bound `title` where the action follows state; a test fails an icon-only button with neither |
| 4.2 browser pass: the model rows' buttons did not line up, because a built-in row had no delete button | Every row keeps every action. The delete button is disabled for a built-in with a tooltip that points to Disable, no server call deletes one, and the list shows built-in models first |
| 4.2 browser pass: a stray ">" showed inside the provider card button, and "Test one by one" was hidden for a single connection | The markup is fixed and a scan fails any tag that ends in `>>`; the button shows from one connection |
| 4.2 browser pass: a double line above the first row of a list, key pause and resume that never enabled, and a lowercase Connections heading | The first row is found by type, since a template is the first child; the key buttons compare `=== true` because Alpine's CSP build turns an undefined result into an empty `disabled`; models are titled by id with their name or alias form under it |
| 4.2 browser pass: provider cards showed initials instead of the provider's mark | Each card shows the provider's logo, imported from the reference's own image set, with a per-card fallback to the initials tile for an id the set has no file for |
| 4.2 browser pass: a provider's badge token differed from the reference, and `ocg/…` from a real database did not resolve | The badge is the reference's UI token, every reference routing token stays resolvable as an extra alias, and no stored combo changes |
| 4.2 browser pass: budget units were lowercase and a built-in model's description disappeared when a custom row of the same id replaced it | The three units are capitalised in both locales; a custom row with no name of its own keeps the built-in description |
| 4.2 browser pass: the resume action used the same check mark as a finished step | Resume and enable draw a play icon; pause keeps the pause icon |
| 4.2 browser pass: a list row was taller below its text than above it | A row's paragraphs drop Tabler's bottom margin, so every gap is 0.75rem plus one 1px rule |
| 4.2 browser pass: the key whitelist offered no built-in model, and the budget text named UTC | The suggestions add every model of a provider with an active connection; the budget columns say per day and per month and the hint names the server's time zone |
| 4.2 browser pass: status, failure tag, priority and the budget table gave no meaning or unit | Words for each status and tag with the code kept, a labelled priority, and budget columns that name the UTC window, the unit and what an empty box means |
| 4.3: the combos bulk handler is undefined and the shared combo modal is never imported; the quota page's limit card and progress bar are dead, and the console config's poll interval is unused | None of the four is copied; the parity table above records what replaces them |
| 4.3: the settings write refused `fallbackStrategy`, which the account selector reads, so the global round-robin switch could not be saved on any page | The key is in `settingsRules` with the two names `account.KnownStrategy` accepts, and a combo word is refused (D409's rule) |
| 4.3: the shared `api()` helper sends the browser to the sign-in page on any 401, but a quota reading answers 401 when the stored provider credential expired | The quota page reads a connection through its own fetch, which shows the provider's message; the session 401 still comes from the guard |
| 4.3: the reference's grouped table expands into children | Our stats maps are already keyed per model, account, key and endpoint, so the table is flat; there is nothing to expand |
| 4.3: the reference marks rows with requests in flight, but `pending` names models only | The breakdown shows no pending column; the live pills show the in-flight count, where every key is known |
| 4.3: the reference creates a Default Key when the home page finds none | Not copied: a key is created when a person asks for one, so a first visit writes nothing |
| 4.3: the headroom default address cannot be a literal in a page (`http://` is refused by the origin scan, and rightly so) | The field carries a translated placeholder; the address is stored only when the operator types one |
| 4.3: a browser run is still not possible in this environment | The 4.3 additions to the browser checklist: the combos drag save, the suggest dialog, a period switch, a stream reconnect after a restart, a real quota reading, settings saved and re-read, pricing saved and reset, a skill created and edited |
| 4.2: a browser run was still not possible in this environment | The browser checklist below stands open: CSP console, both locales on the login page, move up twice quickly, sticky limit saves, leave a login mid-flow (no further `status` or `poll` requests), the masked-secret edit, dark-mode switch, 375 px detail page, a toast above an open dialog, a keyboard pass over every dialog, in Chrome and Firefox |
| A control flipped by the reader stays flipped when its save fails, and an unchanged state never re-applies it. | One `patchSettings` throws the reason; each control puts its stored value back. |
| The role was read three times per view and defaulted to admin when the read failed. | `ui/session.js` reads it once and answers the standard-user view on failure; the API narrows either way. |
| A canvas cannot parse `light-dark()`, which is how Tabler declares the body colour. | Chart text and grid come from `--rl-chart-text` and `--rl-chart-grid`, set by each theme, and a theme switch repaints the charts. |
| A topology redrawn on every live frame drops the keyboard focus. | The redraw waits while a node holds focus; each node has a role and a name. |
| A focus ring at 35% alpha does not reach 3:1 against the surface. | `--rl-ring` is 65% in both themes, pinned by a test. |
| An `await` in `start()` lets a page that was already left open a stream nobody will close. | Every page that opens a stream after an await checks `gone`; a scan fails one that does not. |
| 4.4: the page created a Default Key on every load with none, against the row above. | The page reads the list and shows "No key yet."; the scan fails the post. |
| 4.5c: Tabler keeps a bottom margin and a float on every `.form-check`, so a header switch sat about 0.375rem above the select beside it, and a `.form-select` asks for the full line, so two selectors stacked | `.rl-switch` is a flex row with no Tabler padding, float or bottom margin; a select inside a card head or a row takes its text's width |
| 4.5c: the combo row declared three grid columns while the strategy selects sat inside the first, so the actions landed in the middle column; the dialog list was keyed by index, so a drag and Alpine's rebinding fought | The selects are the row's own second child; the list is keyed by model and Sortable runs in fallback mode so a button handle starts the drag everywhere |
