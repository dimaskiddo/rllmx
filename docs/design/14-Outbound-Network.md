# Design 14 — Outbound Network (`app/platform/network/`)

**Status:** partial

Built: `network.Classify`, `network.Allow`/`ParseAllow`, `network.NewDialer` (the guard), `network.NewTransport`/`NewClient`, and the two consumers — the SSO identity-provider client and the health probe. Still: the provider executors, the key `Checker` and web fetch, which move onto the same client as they are built.

Two deviations from the target below, both deliberate:

- **The probe follows three redirects.** It followed none before; a relocation to the health path is what a load balancer does, so up to three hops are followed and a fourth refused. `app/http/probe_test.go` pins both.
- **The `/api/health` probe permits its own host.** The target comes from `SERVER_`/`APP_*` settings, not from a request, so the probe adds that one host to the allow list it uses. A redirect is still checked against that list, so the probe cannot be steered elsewhere.

### Transport

One configuration for every upstream call (`network.NewTransport`, `network.NewClient`, both planned).

| Item | Behavior |
|---|---|
| TLS | Always verified, or newer. The key-exchange group is `UPSTREAM_TLS_CURVE`: `x25519` (the default) offers only that group, `default` offers the standard list. Go can restrict the set, not its order, so one group is the faithful equivalent of the reference's `ecdhCurve` |
| Response headers | Bounded by `UPSTREAM_CONNECT_TIMEOUT` |
| HTTP/2 | Where the provider supports it. Every client is stdlib `net/http`; `golang.org/x/net/http2` only where stdlib cannot (h2c for Cursor) |
| DPI-shaped networks | Adopted in Go terms (D289). The SSRF guard dials its own permitted addresses, so it races them itself: the first address gets 250 ms and the next joins while it is still pending (RFC 6555 fast fallback), and the first success wins. A pooled connection the far end dropped silently costs one immediate replay on a fresh connection before any wait; the classifier covers a reset, a broken pipe, a closed socket and an early end, and deliberately excludes a name that does not resolve, a refusal, an unreachable host, a timeout, a TLS refusal and a cancelled request. The idle pool stays at 90 s because the replay absorbs a dead socket and Go keeps no TLS session here |
| Proxy | None. `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` and `NO_PROXY` are not read |
| Redirects | `MaxRedirects` 0 means the default of 5, a negative value follows none, a caller may lower it. Never https to http. Image prefetch, probes and the SSO client use 3; the key `Checker` refuses any redirect to another host, port or scheme |
| Body cap | 1 MiB by default; a negative value turns the cap and the overall timeout off for streamed answers |

**No outbound proxy.**

- Every transport dials direct and reads no `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` or `NO_PROXY`.
- There is no proxy setting, no per-connection proxy, no pool, and no relay deployer (Vercel, Netlify, Deno, Cloudflare).
- A database imported from 9router may carry proxy keys, `proxyPools` or `proxyPoolFitness`; they are left exactly as stored (divergence 214).
- MITM and tunnels are Deferred.

### SSRF Guard

The guard lives in the dialer (`network.NewDialer`), so it covers every dial of every request, including each redirect hop. The dialer resolves the name itself, keeps only permitted addresses and connects to one of them directly, so the checked address is the connected address (no rebinding gap).

| Class (`network.Classify`) | Addresses | Rule |
|---|---|---|
| Public | Everything else | Always allowed |
| Internal | Loopback, `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `0/8`, multicast, reserved, `fc00::/7`, `ff00::/8`, unspecified | Needs a match in the operator's list |
| Never | `169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254`, invalid | Refused even when listed |

- IPv4 inside IPv6 (`::ffff:a.b.c.d`, NAT64 `64:ff9b::/96`, deprecated `::a.b.c.d`) is judged by the embedded address; a zone suffix is ignored.
- The list is `UPSTREAM_ALLOW_PRIVATE`: host names (case and a trailing dot ignored; a listed name may resolve to internal addresses), single addresses and CIDR ranges. It comes from the environment on purpose, so a dashboard session cannot widen it. A bad entry stops the start and the error never repeats the value.
- `network.ErrBlocked` names no address.
- The guard covers provider nodes, key checks, web fetch, image prefetch, headroom and the SSO identity-provider client. A provider or IdP on a private network needs a list entry.

### Key Checks & Model Lists (`app/http/controllers`)

One `Checker`, built once in the provider wiring (`app/providers`) on `network.NewClient` (15 s, 3 redirects, 1 MiB, the operator's list), serves `POST /api/providers/{id}/test`, `POST /api/providers/validate`, `POST /api/provider-nodes/validate` and `GET /api/providers/{id}/models`.

| Rule | Detail |
|---|---|
| Redirects | Any redirect to another host, port or scheme is refused, so a typed key never reaches a second address |
| Outcome text | One fixed message per outcome: `Invalid API key`, `blocked address`, `could not connect`, `timed out`, `certificate not trusted`, `redirected to another address`, `response too large`, `endpoint not found`, `provider error`, `unexpected response`, `test not supported yet`. Upstream text, status codes and addresses are never returned, stored or logged |
| Node connection | Probed at the node's own base URL, never the copy on the connection |
| Special providers | OAuth, cookie, AWS-signed and similar answer `test not supported yet`; the row is not written |
| Empty key | `Invalid API key` without a request |

| Probe kind | Request |
|---|---|
| OpenAI | `GET /models`, then a one-token chat only when a model id is given |
| Anthropic | `GET /models`; for a saved connection a one-token `POST /v1/messages`, where anything but 401 and 403 means the key works |
| Embedding | `GET /models`, then `POST /embeddings` with a model, reporting `dimensions` |
| Registry | The entry's validation URL, else `/models` next to its chat URL, with the entry's own auth rule and static headers |

---

## Remaining work

The dialer, the transport, the client, the redirect policy and the allow list are built; three consumers use them (SSO, the health probe, the key `Checker`).

- [x] The key `Checker`
- [ ] The provider executors and the web fetch move onto `network.NewClient`
- [ ] The image prefetch gets the operator's allow list
- [ ] `UPSTREAM_CONNECT_TIMEOUT` governs the executors

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 15 | Never retried insecurely; no switch | No silent downgrade |
| 209 | The probe follows at most three redirects, and its own host is the one entry on the allow list it uses | A load balancer's relocation to `/api/health` works; a redirect anywhere else is refused by the same guard, so a compromised local answer cannot steer the probe |
| 214 | The outbound proxy and proxy pool feature is removed, not deferred: no `proxyPools` table is created, no `outboundProxy*` default is merged, no route or setting takes a proxy field. On a database imported from 9router the table, `proxyPoolFitness` and every stored proxy key stay exactly as stored, and `db:import-config` still skips the three `outboundProxy*` names | A router that dials direct has nothing to configure; leaving foreign data alone keeps the file openable by 9router (scope cut) |
| 215 | A key check stores and returns only its own fixed message (`Invalid API key`, `could not connect`,.), never the provider's text, status or address. A `Checker` pass does not erase a cooldown and a failing check creates none | The reference stores the provider's error text in the connection, where a key, an address or a prompt fragment can land; a probe is not a request and must not hide or invent a lock (improvement) |
| 289 | The reference's transport tuning reads three environment names (`NINEROUTER_TLS_ECDH_CURVE`, `FETCH_KEEPALIVE_TIMEOUT_MS`, `FETCH_FRESH_RETRY`) and keeps a kill switch for its stale-socket replay. Here the curve is this program's own `UPSTREAM_TLS_CURVE` with two values, the idle pool is fixed at 90 s, and the replay always runs once. The reference's browser-identical handshake path stays disabled, as it does in the reference | Configuration is this program's own variables and settings row, never another program's environment (same reason as D263 and D271); a replay whose whole point is to be invisible should not depend on an operator remembering it exists (trade-off) |
| 230 | A host name on the operator's allow list is still refused when it resolves to a link-local or metadata address (`169.254/16`, `fe80::/10`, `fd00:ec2::254`, also through the mapped, compatible and NAT64 forms). The list lifts the Internal class only; the Never class has no entry that lifts it. `64:ff9b:1::/48` is Internal | Whoever controls DNS for a listed name could otherwise point it at the cloud metadata service; the dialer already held this rule for an address literal and the name path did not (improvement) |
| 4 | Loopback calls only. **(partial)**no compose file ships | No exposed sidecar |
| 146 | One rule in `network`: `localhost`, loopback address, brackets, trailing dot, IPv4-mapped form; zoned address never local | Guard, URL checks, provider checks cannot disagree |
| 31, 75, 76 | Guard is the dialer: resolve once, keep permitted addresses, dial that address, every redirect hop too; covers every user-defined base URL. Link-local, metadata always refused; loopback, private only via `UPSTREAM_ALLOW_PRIVATE`. **(partial)**image prefetch has no allow list (stricter); web fetch | No rebinding window; cannot be forgotten on a new route |
| 96, 97 | Every probe uses guarded client; redirect to another host, port, scheme refused | Local request not a trusted target; typed key stays at one address |
| 126 | The key `Checker` follows no redirect, and when the shared client follows one it refuses any hop that downgrades to plain HTTP | Credential never reaches another host or crosses to clear text |

---

**0.5.116 carried a transport change, not only a Node one.** Its subject is a DPI-shaped network (`736be957`); nothing in the reference branches on the provider.

| Reference part | How it lands here |
|---|---|
| Fast fallback | A race in the guard (RFC 6555) |
| A restricted TLS key-exchange group | `UPSTREAM_TLS_CURVE` |
| An immediate stale-socket replay | `executor.Default`, with the reference's exclusion list |
| A 4 s keep-alive cap | Not copied: the replay makes a dead socket cheap, so the pool stays long |
| A browser-identical handshake (uTLS/JA3) | What the reference disables too; out of scope |

## Findings

None recorded.

