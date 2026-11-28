# Metrics

Router-LLM serves its counters at `GET /metrics` in [OpenMetrics 1.0](https://github.com/OpenObservability/OpenMetrics/blob/main/specification/OpenMetrics.md) text, so Prometheus can scrape them and Grafana can chart them. It uses no OpenTelemetry pipeline and no outbound call: the process answers, the scraper asks.

The analytics database stays the record of everything. The endpoint counts what this process has served since it started, on the same dimensions the Usage page groups by, so an outside dashboard can rebuild that page.

## Turn it on

The endpoint is off until one credential is set. With neither, `GET /metrics` is a 404.

| Variable | Rule |
|---|---|
| `METRICS_TOKEN` | Bearer token, at least 32 characters. |
| `METRICS_USERNAME` | HTTP Basic user, no colon. |
| `METRICS_PASSWORD` | HTTP Basic password, at least 16 characters. Set with the username, both or neither. |

Both may be set; either is accepted. A refused attempt counts against the same per-address limiter the sign-in uses, so a password cannot be guessed at line rate. Lengths, a half pair or a control character stop the start.

The endpoint carries account names, which can be email addresses. Keep it off the public internet: restrict it at the reverse proxy or firewall as well.

## Check it

```bash
# Bearer
curl -sH "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:20128/metrics | head

# Basic
curl -s -u "$METRICS_USERNAME:$METRICS_PASSWORD" http://127.0.0.1:20128/metrics | head
```

An accepted scrape starts with `# HELP` and `# TYPE` lines and ends with `# EOF`. Without the credential it answers `401`, and a Basic refusal carries `WWW-Authenticate`.

```
# HELP rllmx_requests Requests by provider, model, account, key, endpoint, status class and cache.
# TYPE rllmx_requests counter
rllmx_requests_total{provider="openai",model="gpt-5",connection_id="c1",api_key_id="k1",endpoint="/v1/chat/completions",code="2xx",cached="false"} 42
```

## Connect Prometheus

Bearer:

```yaml
scrape_configs:
  - job_name: rllmx
    scheme: https              # http only inside a private network
    metrics_path: /metrics
    scrape_interval: 30s
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/rllmx-token
    static_configs:
      - targets: ["gateway.example.com:20128"]
```

HTTP Basic:

```yaml
scrape_configs:
  - job_name: rllmx
    scheme: https
    metrics_path: /metrics
    scrape_interval: 30s
    basic_auth:
      username: prom
      password_file: /etc/prometheus/rllmx-password
    static_configs:
      - targets: ["gateway.example.com:20128"]
```

A 15 to 60 second interval is plenty: the counters move when a request finishes, not faster.

## Connect Grafana

Add the Prometheus data source, then import [`grafana-usage.json`](grafana-usage.json) (Dashboards, New, Import). Pick the data source when asked. The dashboard rebuilds the Usage page panels from the queries below.

## What is exported

Usage counters share one dimension tuple, the one the usage rollups group by:

- `provider`, `model`
- `connection_id`: the upstream account
- `api_key_id`: the client key
- `endpoint`

Names are not labels on these counters. They join in from the two `_info` families, so renaming a key or an account does not split a series.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `rllmx_requests` (`_total`) | counter | tuple, `code`, `cached` | Finished requests. `code` is `2xx`, `3xx`, `4xx`, `5xx` or `other`; `cached` is `true` for an answer served from the response cache. |
| `rllmx_tokens` (`_total`) | counter | tuple, `kind` | Tokens. `kind` is `prompt`, `completion`, `cached`, `cache_write` or `reasoning`. |
| `rllmx_cost_usd` (`_total`) | counter | tuple | Spend in US dollars, for requests whose model has a price. |
| `rllmx_cost_unknown_requests` (`_total`) | counter | tuple | Requests whose model has no price. The page's "unknown cost" count. |
| `rllmx_request_duration_seconds` | histogram | `provider`, `model` | Request time, arrival to the last byte. |
| `rllmx_ttfb_seconds` | histogram | `provider`, `model` | Time to the first token. |
| `rllmx_metrics_series_folded` (`_total`) | counter | none | Tuples filed under `other` after the series cap. |
| `rllmx_inflight_requests` | gauge | `provider`, `model`, `connection_id` | Requests running right now, read at scrape time. |
| `rllmx_api_key` (`_info`) | info | `api_key_id`, `api_key_name`, `user` | One series per API key. `user` is the owning standard user, empty for the admin's keys. Never the key itself. |
| `rllmx_account` (`_info`) | info | `connection_id`, `provider`, `account_name` | One series per upstream account, named as the Usage page names it (its own name, its email, or a shortened id). |
| `rllmx_build` (`_info`) | info | `version`, `commit` | The running build. |
| `process_start_time_seconds` | gauge | none | Unix time the process started, the anchor for counter resets. |
| `go_goroutines` | gauge | none | Goroutines running now. |
| `go_heap_objects_bytes` | gauge | none | Allocated heap bytes. |

Both histograms use the buckets `0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300` seconds and `+Inf`.

### Limits

- **Counters restart at zero.** The registry is in memory, so a restart resets every series. `rate()` and `increase()` handle the reset; for a total older than the process, read the analytics database or the Usage page.
- **The request log is not a metric.** The per-request rows the Usage page lists are rows in the analytics table; a log pipeline is the outside route for them.
- **The tuple is capped** at 20000 series. A new tuple past the cap keeps its `provider` and `model` and files `connection_id`, `api_key_id` and `endpoint` under `other`; each fold increments `rllmx_metrics_series_folded_total`. The provider and model names are capped too, at 2000 pairs: a pair past it is counted as `other`/`other` in every family, because a client chooses the model it names. Every label is the stored value, bounded the way the usage row is. In practice both caps are far above a normal deployment.
- **Nothing secret is exported.** No key value, no prompt text, no error message. Account names, which can be email addresses, are exported, which is why the endpoint needs a credential.

## Query cookbook

Replace `$__range` with the dashboard range in Grafana, or a duration such as `1h` in Prometheus.

Totals over the window:

```promql
sum(increase(rllmx_requests_total[$__range]))
sum(increase(rllmx_tokens_total{kind="prompt"}[$__range]))
sum(increase(rllmx_tokens_total{kind="completion"}[$__range]))
sum(increase(rllmx_tokens_total{kind="cached"}[$__range]))
sum(increase(rllmx_cost_usd_total[$__range]))
sum(increase(rllmx_cost_unknown_requests_total[$__range]))
```

The chart (one point per step):

```promql
sum(rate(rllmx_tokens_total[$__interval]))
sum(rate(rllmx_cost_usd_total[$__interval]))
sum(rate(rllmx_requests_total[$__interval]))
```

By provider:

```promql
sum by (provider) (increase(rllmx_requests_total[$__range]))
sum by (provider) (increase(rllmx_tokens_total[$__range]))
sum by (provider) (increase(rllmx_cost_usd_total[$__range]))
```

By model:

```promql
sum by (provider, model) (increase(rllmx_cost_usd_total[$__range]))
```

By account, with the account name:

```promql
sum by (provider, model, connection_id) (increase(rllmx_cost_usd_total[$__range]))
  * on (connection_id) group_left (account_name) rllmx_account_info
```

By API key, with the key name:

```promql
sum by (provider, model, api_key_id) (increase(rllmx_cost_usd_total[$__range]))
  * on (api_key_id) group_left (api_key_name) rllmx_api_key_info
```

By user:

```promql
sum by (user) (increase(rllmx_cost_usd_total[$__range]) * on (api_key_id) group_left (user) rllmx_api_key_info)
```

By endpoint:

```promql
sum by (endpoint, provider, model) (increase(rllmx_requests_total[$__range]))
```

Error ratio, per provider:

```promql
sum by (provider) (rate(rllmx_requests_total{code=~"4xx|5xx"}[$__interval]))
  / sum by (provider) (rate(rllmx_requests_total[$__interval]))
```

p95 latency, per provider:

```promql
histogram_quantile(0.95, sum by (provider, le) (rate(rllmx_request_duration_seconds_bucket[$__interval])))
```

Spend per user, per day:

```promql
sum by (user) (increase(rllmx_cost_usd_total[$__interval]) * on (api_key_id) group_left (user) rllmx_api_key_info)
```

Live table (the Usage page's in-flight list):

```promql
sum by (provider, model, connection_id) (rllmx_inflight_requests)
```
