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
| Magics and calls | An expression may use only the magics the CSP build defines (`$event`, `$store`, `$refs`, `$el`, `$data`, `$id`, `$dispatch`, `$nextTick`, `$root`, `$watch`); `$index` is not one. Every function a fragment calls must be defined by its page script or a module the script mixes in. An event handler never assigns a property |
| Components | A page script holds state and lifecycle; larger pages mix in modules (`provider-connections`, `-models`, `-settings`, `-login`, and `ui/format`). A pure rule with no imports, such as `renumber`, `psdBody` or `mergeConnection`, lives in `provider-status.js`, so a script can load it on its own. A text input is bound with `setField('path', $event)`, not a setter per field |
| Accessibility | Every `<dialog>` names itself by `aria-labelledby` to a heading in the same file. The toast stack is a manual popover with `role="status"` and `aria-live`, so a toast shows above an open modal and is announced. Both entry scripts set `<html lang>` from the locale. Icon-only buttons carry a visually hidden label or a `title` |
| Secrets | A dialog that holds a typed secret clears it on its own `close` event, which Escape fires too. `nextPath` resolves the `next` address as the browser will and accepts only `/dashboard` paths |
| Dead code | Every mapped icon is drawn by an `x-icon` somewhere, and every `.rl-` rule in `components.css` is used by a page or script. A test fails the first unused one |
| Pages | One shell serves `/dashboard` and `/dashboard/*path`. `js/routes.json` maps a path to a page; the History-API router imports `js/pages/<page>.js`, its `Alpine.data` component, then fetches `pages/<page>.html`, parses it and attaches its nodes, so a fragment is never built from data. An unrouted path shows `pages/not-found.html`. Tests pair every route with its two files and refuse an orphan fragment |
| Server routes | `/` (public, 302 to `/dashboard`), `/login` (public), `/callback` (public, `no-store`), `/static/*file` (public), `/dashboard` and `/dashboard/*path` (`ClassPage`). The guard's redirect carries no return path; the login page reads `?next=` itself and follows it only when it is a `/dashboard` path |
| Libraries | A new library is an entry in `docs/ARCHITECTURE.md` first. Its files are copied unchanged into `static/lib/` and listed with their sha256 in `VERSIONS.json`. Chart.js uses its UMD file, because the ES build needs an import map |
| Console Log | `GET /api/translator/console-logs` returns the ring and `GET …/stream` follows it: `init` with the ring, `lines` in batches, `clear` when the ring is emptied, a comment every 25 s. The lines are already redacted, so the page renders them as text and never re-fetches the log file |
| Secrets | The API masks them and the dashboard never receives raw tokens. The one exception is a deliberate reveal: a signed-in operator asks for one key by id and the answer is logged (D330) |

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
| Overrides | Offered for every provider, including one with no built-in header. The server decides a reserved name or a bad value, and the page shows its error as it is |
| Node writes | Stricter than the reference: prefix matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, is not a built-in id or alias, and is unique; base URL is http or https with a host, no credentials, query or fragment, at most 2048 bytes. Create, update (with its connections) and delete (with its connections) are each one transaction. Rows that break these rules still read and resolve |

---

## Parity: Dashboard Pages

Plain HTML, ES modules, CSS; same flows and `/api/*` contract (design doc 18).

| Pages | Status |
|---|---|
| login, the shell and router, endpoint and keys (base URL, key guard, keys: create, reveal, pause, rename, delete, access list, budgets, response cache), i18n `en` and `id`, theme switch | done (4.1a) |
| providers: list (search, filter, status tags, test all), detail (connections, order, round robin, every login flow, custom endpoints), models (list, alias, custom, disabled, test), header overrides | done (4.1b) |
| 4.2 audit fixes: login directives, reorder, sticky limit, polling and router races, key/value editor, secrets cleared, contrast, dialogs and toasts, split provider modules | done (4.2) |
| combos, token saver, setup wizard, home | todo (4.3) |
| usage, quota, console log, media providers (per kind, combos), profile, translator playground, basic chat, pricing settings, skills Studio (extended), routing health, auto plan (extended) | todo; auto plan's nine keys are already readable and writable through `/api/settings` |
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
- [ ] 4.3: combos, usage charts, logs, settings, skills studio
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
| A provider card shows the provider's image from a bundled asset folder (D402) | A tile with the first two letters of the name | The registry holds no image, and adding one per provider is a vendored asset per file with no behaviour behind it |
| A priority move writes 0-based indexes to the two rows and leaves the rest (D403) | The list is renumbered from 1 and only the rows whose number changed are written | Priority is validated from 1; a swap that leaves duplicates or a zero is refused or reorders the list unpredictably |
| A strategy write rebuilds the provider's entry and loses any other key in it (D404) | The entry is edited: only `fallbackStrategy` and `stickyRoundRobinLimit` change, and other keys and providers are kept | An imported 9router row may carry proxy keys; dropping them silently changes routing |
| A connection with the same name is overwritten without a word (D405) | The 409 `PROVIDER_NAME_CONFLICT` asks, and replacing sends `allowOverwrite` | A pasted wrong key would destroy a working connection |
| One shared model-test error line (D406) | Each model keeps its own result and error | A later success must not hide an earlier failure |
| The override card is hidden when the provider has no built-in header (D407) | Offered for every provider | The server accepts an override for any provider, so the card reflects what it can do |
| Errors arrive as `alert()` next to toasts and inline text (D408) | A toast or an inline line, and nothing else | One channel, one place to look |
| A bare strategy word is stored but the selector reads only the object (D409) | The selector reads both shapes, and only `fill-first` and `round-robin` are accepted on write | A saved strategy must do what it says; `smart` and `fallback` are combo words that behaved as fill-first |
| A deleted extra field stays stored and every edit turns a number into text (D410) | A removed row is sent as `null`, an untouched row is not sent, and an edited one keeps its type | The merge removes a key only on `null`, and a provider that reads a number must still get one |

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
| 4.2 browser pass: the key whitelist offered no built-in model, and the budget text named UTC | The suggestions add every model of a provider with an active connection; the budget columns say per day and per month and the hint names the server's time zone |
| 4.2 browser pass: status, failure tag, priority and the budget table gave no meaning or unit | Words for each status and tag with the code kept, a labelled priority, and budget columns that name the UTC window, the unit and what an empty box means |
| 4.2: a browser run was still not possible in this environment | The browser checklist below stands open: CSP console, both locales on the login page, move up twice quickly, sticky limit saves, leave a login mid-flow (no further `status` or `poll` requests), the masked-secret edit, dark-mode switch, 375 px detail page, a toast above an open dialog, a keyboard pass over every dialog, in Chrome and Firefox |
