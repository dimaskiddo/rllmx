# Router-LLM - Screenshots

The dashboard, page by page. The captures below come from the dark theme at a desktop width; every page has the same layout in the light theme. Settings that live in a dialog open over the page they belong to.

---

## Sign in

### Login

![The sign-in page with the Router-LLM mark above the username and password fields](images/02-Login.png)

The password form. When single sign-on is configured, its button appears beside it.

---

## Routing

### Endpoint & Keys

![The Endpoint & Keys page with the base URL and the API key list](images/03-Endpoint-and-Keys.png)

The one base URL clients point at, the key guard, and the keys with their access lists and budgets.

### Providers

![The Providers page with every OAuth provider and its readiness state](images/04-Providers.png)

Every provider, OAuth or custom node, with its connections, readiness and a single test action.

### Combos

![The Combos page with the member list and the smallest context and output each combo promises](images/05-Combos.png)

Combos: an ordered member list with fallback, round robin or the smart router, and the smallest context and max output across the members.

### Token Saver

![The Token Saver page with RTK, prompt styles and the safety-paragraph stripper](images/06-Token-Saver.png)

RTK tool-output filters, the built-in prompt styles, Allow REA and the watermark remover.

### Skills

![The Skills page with the skill registry and the synced catalog](images/07-Skills.png)

The skill registry, Skills Studio rules and the synced catalog. Skills are data, never code.

### API key settings

![The Edit the key dialog with the owner, cache switch, model restriction and budget windows](images/14-API-Key-Settings.png)

A key's owner, response-cache switch, model restriction and day, month and lifetime budgets.

### Combo settings

![The Edit the combo dialog with an ordered list of models](images/15-Combo-Settings.png)

A combo's members, reordered by dragging or with the arrows. The name is the model a client calls.

---

## Monitoring

### Usage

![The Usage page with request rows, tokens and cost](images/08-Usage.png)

One metadata-only row per request: key, provider, model, tokens, cost and latency. No prompts, no bodies.

### Quota tracker

![The Quota page with each provider's limits](images/09-Quota-Tracker.png)

Each provider's quota windows, and a reset where the provider grants one.

### Logs

![The Logs page with the console log](images/13-Logs.png)

The last 500 log lines, already redacted and streamed as they happen. Clearing empties the window and leaves the log file untouched.

---

## Administration

### Users

![The Users page with dashboard accounts and roles](images/10-Users.png)

Dashboard accounts and their roles. A standard user sees only the keys assigned to them and those keys' usage.

### Settings

![The Settings page with runtime settings](images/11-Settings.png)

Runtime settings: the account strategy, the combo strategy, and the security and sign-in pages (password, OIDC and SAML).

### About

![The About page with the version, update check and credits](images/12-About.png)

The version and commit, the release check, the admin update action, the credits and the license.
