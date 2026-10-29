# Operations

## Routing bots to kagemusha

kagemusha renders; **your edge decides who gets the render**. Route bot user agents (search engines, link previews, AI crawlers, …) to kagemusha and everyone else to the SPA, and exclude kagemusha's own user agent (`kagemusha`) from the match. For an nginx example, see the [single server setup](security.md#single-server-vps-setup).

Run several small workers behind a load balancer rather than one big one. Workers exit on purpose (see below); the orchestrator (ECS, Kubernetes) replaces them.

## Lifecycle

- A supervisor owns the browser: it restarts it every `KAGEMUSHA_BROWSER_RESTART_PERIOD`, and when it stops answering for `KAGEMUSHA_WATCHDOG_TIMEOUT_MS`.
- The worker exits after `KAGEMUSHA_WORKER_MAX_RENDERS` renders or `KAGEMUSHA_WORKER_MAX_AGE`, each shortened by a random 0–20% so workers started together do not recycle together. The effective limits are logged at startup. Exit code `0` is a planned recycle, `1` a crash.
- `SIGTERM` drains in-flight renders (up to `PAGE_TIMEOUT + 5s`) and exits `0`.
- `/healthz` answers `503` when the browser does not answer, or when every slot has been busy for `KAGEMUSHA_STALL_TIMEOUT` with no render starting or finishing. It never takes a render slot. `kagemusha healthcheck` probes it from inside the container.

## Timeouts

The slowest answer is `KAGEMUSHA_QUEUE_TIMEOUT_MS` + `KAGEMUSHA_PAGE_TIMEOUT_MS` + 5 s (capture). Keep it below the idle timeout of your load balancer and CDN, or clients disconnect before you answer. The defaults (20 s + 30 s + 5 s) fit a 60 s timeout.

- The queue timeout bounds only the wait for a slot (`503` after that). A render that started is never cut by it.
- The page timeout is absolute: the partial DOM is returned, marked `X-Kagemusha-Trigger: timeout`.
- A client that disconnects does not cancel the render; others waiting for the same URL still get it.

## Caching

- **The CDN is the main cache.** `KAGEMUSHA_CACHE_CONTROL` defaults to `no-store`, so *the CDN caches nothing* until you set it, for example `public, max-age=10800, stale-while-revalidate=86400`. Only `200` carries it.
- The **in-process cache** (`KAGEMUSHA_CACHE_TTL`, short is enough) absorbs bots retrying right after a render. It keeps only `200`, under the single-flight key, and answers with `X-Kagemusha-Cache: hit` and `Age`. Memory ≈ `KAGEMUSHA_CACHE_MAX_ENTRIES` × page size.
- `KAGEMUSHA_STRIP_QUERY_PARAMS` (e.g. `utm_*,fbclid,gclid`) makes URLs that differ only in tracking parameters share one render and one cache entry. Match your CDN cache key.

## Lighter renders

One render fans out into dozens of sub-requests, and your own WAF sees kagemusha's egress IP as a burst.

- `KAGEMUSHA_BLOCK_DOMAINS`: drop analytics, ads and session recording. Always-on beacons also keep the network from ever going idle, so renders wait for the page timeout.
- `KAGEMUSHA_BLOCK_RESOURCE_TYPES=Image,Media,Font`: the tags stay in the HTML, the bytes are never fetched.
- `window.prerenderReady = true` from your app ends the render the moment the page is done, instead of waiting `KAGEMUSHA_IDLE_QUIET_MS` of network silence. It also keeps renders correct on small CPUs, where a slow first paint can look like silence.
- **Impatient preview bots** (Slack, Facebook: 5–9 s client timeouts) only need OGP tags. If full renders are slow, answer them upstream with a lightweight endpoint.
- **Chromium** needs shared memory: `--disable-dev-shm-usage` is on, but give the container `--shm-size=512m` anyway.

## Metrics

`GET /metrics` serves the Prometheus text format (`KAGEMUSHA_METRICS=off` removes it). It never takes a render slot, and no URL appears in any label.

| Metric | Use |
|---|---|
| `kagemusha_requests_total{outcome}` | What happened to each request: `rendered`, `cache_hit`, `queue_timeout`, `render_failed`, `target_not_allowed`, `target_lookup_failed`, `denied_path`, `invalid_url` |
| `kagemusha_renders_total{code,trigger}` | Actual renders; compare with `outcome="rendered"` to see how much single-flight saves |
| `kagemusha_render_duration_seconds` | Render duration histogram |
| `kagemusha_queue_running`, `kagemusha_queue_waiting`, `kagemusha_max_concurrent` | Scale out on `waiting` (or `running / max_concurrent`) rather than CPU |
| `kagemusha_browser_restarts_total{reason}`, `kagemusha_browser_relaunch_failures_total` | `planned`, `watchdog`, `requested`; a rising `watchdog` means hangs |
| `kagemusha_worker_renders_total`, `process_start_time_seconds` | Progress toward the worker recycle limits |
| `kagemusha_cache_entries` | In-process cache size (when enabled) |

Worker exit reasons are not metrics (the process is gone); they are logged and visible as the exit code.

## Logs

JSON lines on stdout. Every render request ends with exactly one `"msg":"request"` line; `outcome` matches `kagemusha_requests_total`:

```json
{"time":"…","level":"INFO","msg":"request","outcome":"rendered","status":200,"url":"https://shop.example/items/1","ua":"Mozilla/5.0 (compatible; Googlebot/2.1; …)","total_ms":6120,"trigger":"ready","render_ms":5980}
```

| Field | Meaning |
|---|---|
| `outcome` | As in `kagemusha_requests_total` |
| `status` | HTTP status kagemusha answered |
| `url` / `ua` | Target URL (tracking parameters stripped) and the caller's user agent. Forwarded headers are never logged |
| `total_ms` / `render_ms` | Time until the answer / time the browser spent rendering (`render_ms` only when this request waited for a render) |
| `trigger`, `err` | Completion trigger; error text on failures |

Levels: `ERROR` for `render_failed`, `WARN` for `queue_timeout` and rejected targets, `INFO` otherwise. Lifecycle events (worker limits, browser restarts, exit reasons) use other messages. Lightpanda writes its own logs (logfmt) to stderr.

```bash
# Slowest renders and who asked for them
jq -c 'select(.msg=="request" and .render_ms) | {render_ms, url, ua}' kagemusha.log | sort -t: -k2 -nr | head
```

```
# CloudWatch Logs Insights: outcomes and p95 per 5 minutes
filter msg = "request"
| stats count(*) as requests, pct(render_ms, 95) as p95_render_ms by outcome, bin(5m)
```
