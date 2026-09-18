# Spinneret Python SDK

[中文文档](README.zh-CN.md)

Python client for [Spinneret](https://github.com/TikHub/Spinneret), the control plane that leases
identities (cookies, device parameters, accounts) and proxies to crawler nodes, turns request
reports into cooldowns, bans and circuit breaking, and distributes configuration and secrets.

- Sync (`Client`) and asyncio (`AsyncClient`) clients on top of `httpx`
- Typed pydantic v2 models for every node API message
- Lease context managers that merge credentials and proxies into `httpx` arguments and
  release the lease with the last report (or `LeaseService/Release` when nothing was reported)
- Background reporters that batch reports (every 200 ms or 100 reports, bounded queue)
- Config watchers with long polling, change callbacks and atomic local snapshots
- Typed exceptions carrying the server reason and retry hint

Requires Python 3.9+, `httpx>=0.27` and `pydantic>=2.6`.

## Installation

```bash
pip install spinneret                # SDK
pip install 'spinneret[crypto]'      # + encrypted snapshots of configs that reference secrets
```

SOCKS proxies returned by Spinneret need `pip install 'httpx[socks]'`.

## Configuration

| Variable | Meaning | Default |
| --- | --- | --- |
| `SPINNERET_URL` | Server base URL, e.g. `https://spinneret.internal` | required |
| `SPINNERET_TOKEN` | Node API token (`spn_...`) | required |
| `SPINNERET_NODE` | Node name sent as `X-Spinneret-Node` | host name |
| `SPINNERET_CACHE_DIR` | Directory of config snapshots | `~/.spinneret/cache` |

Explicit arguments win over the environment:

```python
client = spinneret.Client("https://spinneret.internal", "spn_xxx", node="crawler-hk-03")
```

## Quick start

```python
import httpx
import spinneret

with spinneret.Client() as client:
    with client.lease(site="shop", client="web", uri="/api/v1/search") as lease:
        with httpx.Client(**lease.httpx_kwargs()) as http:
            response = http.get("https://target.example.com/api/v1/search")
        lease.report_response(
            response, markers=["empty_list"] if not response.json().get("data") else []
        )
```

Async:

```python
async with spinneret.AsyncClient() as client:
    async with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.example.com/api/v1/feed")
        lease.report_response(response)
```

See [`examples/basic_usage.py`](examples/basic_usage.py) for a complete node.

## Leases

`client.lease(site, client, uri="", *, endpoint_group="", session_key="", wait_ms=0,
flush_on_exit=False, raise_on_release_error=False)` returns a context manager that calls
`LeaseService/Acquire` on entry.

Inside the block:

| Member | Description |
| --- | --- |
| `lease_id`, `identity_id`, `info`, `expires_at` | Lease metadata (`info.probe`, `info.sticky`, ...) |
| `credential` | `cookies`, `cookie_header`, `headers`, `query`, `json_value` (JSON key `json`), `values` |
| `proxy` | `proxy_id`, `url`, `kind`, `region` or `None` |
| `httpx_kwargs(headers=, params=, cookies=)` | `dict(headers, cookies, params, proxy)` for `httpx.Client(...)`; explicit arguments override credential values; `cookie_header` becomes a `Cookie` header when the credential has no cookie map |
| `report(status, *, latency_ms, markers, business_code, error_kind, release, **fields)` | Queue a report (`uri`, `method`, `outcome_hint`, `response_bytes`, `started_at`, `finished_at`, `report_id` are optional fields) |
| `report_response(response, *, markers, business_code, release)` | Report an `httpx.Response` (status, method, path, latency, size) |
| `report_exception(exc, *, markers, release)` | Report a failed request with `error_kind` from `classify_exception` |
| `renew(extend_ms=0)` | Extend the lease (`await` on the async lease) |
| `release()` | End the lease now (same as leaving the block without an exception; `await` on the async lease) |

Release semantics:

- Reports are queued on the client's background reporter. The most recent report is held back
  until the next report or the end of the block, so that it can carry `release: true`.
- On exit the last report is sent with `release: true`, also when the block raised. When nothing
  was reported, the lease is released with `LeaseService/Release` (`{"lease_id": ...}`); no
  report is invented, so report failed requests yourself (`report_exception`) if the server
  should count them.
- `report(..., release=True)` releases immediately; later reports raise `LeaseReleased`.
- `flush_on_exit=True` waits (up to `ReporterOptions.close_timeout`) until the reporter queue is
  delivered.
- Release failures do not escape the block by default. A `Release` error with reason
  `lease_released`, `lease_unknown` or `lease_expired` means the lease has already ended and is
  logged at debug level; other errors (and failed direct report deliveries) are logged at warning
  level. With `raise_on_release_error=True` those other `Release` errors are raised from the
  `with` statement and from `release()`, unless the block itself raised (its exception is never
  replaced).
- One lease can serve several requests (pagination): report every request, the last one releases.

`report_id` defaults to a random UUID4, `finished_at` to now and `started_at` to
`finished_at - latency_ms`. `business_code` accepts numbers and strings; `error_kind` must be empty
or one of the values listed under [Error kinds](#error-kinds). The reported `uri` is reduced to
the request path (query string and fragment removed, at most 2048 characters): the server matches
endpoint groups on the path only, and query parameters often carry signed credential values. A
`407` response is reported with `error_kind="proxy_auth"`.

Direct calls are available too: `acquire`, `acquire_batch(site, client, count, ...)`, `renew`,
`release`, `report(reports)` (1..500 reports, synchronous).

## Background reporter

`client.reporter` (`Reporter` thread / `AsyncReporter` task) batches reports:

- sends when 100 reports are queued or the oldest waited 200 ms, at most 500 per call;
- bounded queue of 10 000 reports: when full the oldest reports are dropped, counted in
  `reporter.stats.dropped` and logged (throttled);
- transport errors, `unavailable`, `internal`, `deadline_exceeded` and `resource_exhausted` are
  retried with jittered exponential backoff (0.5 s .. 30 s); other failures drop the batch;
- reports rejected by the server are dropped, logged and passed to `on_rejected`;
- `flush(timeout)` sends the queue now; `close(timeout)` (also called by `client.close()`)
  delivers what it can within `close_timeout` (5 s) and stops. The sync reporter also closes at
  interpreter exit; with asyncio always `await client.aclose()`.
- a worker that is no longer running (a thread in a process created with `os.fork()`, or a task of
  an event loop that has ended) is restarted on the next `submit`, `flush` or `close`.

```python
options = spinneret.ReporterOptions(
    flush_interval=0.2,
    batch_size=100,
    max_queue_size=10_000,
    on_rejected=lambda r: log.warning("rejected %s", r.reason),
)
client = spinneret.Client(reporter_options=options)
```

## Configuration center

```python
def on_change(item: spinneret.ConfigItem) -> None:
    reload_settings(item.content)


with client.config_watcher(
    ["crawler/search.json", ("_runtime", "breakers")], on_change=on_change
) as watcher:
    item = watcher.get("crawler", "search.json")
    changed = watcher.wait_for_change("crawler", "search.json", timeout=60)
```

- `start()` loads the items with `BatchGetConfig`; a thread (or asyncio task) then long-polls
  `WatchConfig` (`timeout_ms` 30 000, HTTP read timeout `timeout_ms + 5 s`), stores the latest
  items and invokes the callbacks for every changed item (initial values included). The loop ends
  when the client is closed.
- Every successfully fetched non-secret item is written atomically (temp file + `fsync` + rename, mode
  `0600`) to `<cache_dir>/<host>/<namespace>/<group>/<key>.json`.
- When the server is unavailable at start (transport error, `unavailable`, `internal`,
  `deadline_exceeded`, `resource_exhausted` or another 5xx), the snapshots are loaded instead and
  `watcher.from_snapshot` is `True` until the server answers. Authorization and validation errors
  are raised by `start()`.
- Secret items are never written to disk in plain text: by default they are kept in memory only
  and any older snapshot of the item is removed. The server resolves `${secret:...}` references before
  delivery and sets `ConfigItem.has_secret_refs` (JSON `has_secret_refs`) on every item whose
  published content contained such references; the watcher treats these items as secret
  automatically. Content that still contains an unresolved `${secret:` marker is treated as secret
  too.
- `treat_as_secret` is an additional override for items that embed sensitive values directly
  instead of referencing secrets, e.g. `treat_as_secret=lambda item: item.group == "signing"`. It
  can only add secret items, never exempt an item flagged by the server; a predicate that raises
  counts as true.
- With `cache_secrets=True` secret items are written encrypted with AES-256-GCM using a key
  derived from the API token with HKDF-SHA256 (fresh salt and nonce per file). The standard library has no AES-GCM, so this requires the `cryptography` package
  (`spinneret[crypto]`); without it `cache_secrets=True` raises `ConfigurationError`. Snapshots
  encrypted with a previous token cannot be read after token rotation.
- `snapshots=False` disables the cache; `cache_dir=` overrides the directory.

One-off reads: `get_config(group, key)`, `batch_get_config(items)`, `watch_config(items)`
(`timeout_ms=0` uses the server default wait of 30 s) and `get_secret(path, version=0)`.

## Errors

All errors derive from `SpinneretError` with `code`, `reason`, `message`, `retry_after_ms`
(`retry_after` in seconds) and `http_status`.

| Exception | Code / reason | What the node should do |
| --- | --- | --- |
| `Unauthenticated` | `unauthenticated` (`token_invalid`, ...) | stop and alert |
| `PermissionDenied` | `permission_denied` (`scope_missing`) | stop and alert |
| `InvalidArgument` | `invalid_argument` (`site_unknown`, `uri_invalid`, ...) | fix the caller |
| `NoIdentityAvailable` / `NoProxyAvailable` | `resource_exhausted` | wait `retry_after`, retry |
| `ResourceExhausted` | `resource_exhausted` (`rate_limited`, ...) | wait `retry_after` |
| `CircuitOpen` / `SitePaused` | `unavailable` | pause the endpoint group / site |
| `Unavailable` | `unavailable` (`rebuilding`, ...) | retry later |
| `LeaseUnknown` | `not_found` (`lease_unknown`) | acquire again |
| `LeaseReleased` / `LeaseExpired` | `failed_precondition` | acquire again |
| `NotFound`, `AlreadyExists`, `Aborted`, `DeadlineExceeded`, `Unimplemented`, `InternalError` | matching code | |
| `TransportError` (subclass of `Unavailable`) | no response; `error_kind` set | retry later |
| `ConfigurationError` | invalid SDK configuration | fix the configuration |
| `ReporterClosedError` | report submitted after close | |
| `FailedPrecondition` (reason `client_closed`) | call on a closed client | create a new client |

Responses without a Connect error body (for example a load balancer page) are mapped from the
HTTP status following the Connect protocol.

## Retries and timeouts

- Timeouts: connect 3 s, read 10 s (plus `wait_ms` for acquire), write 10 s, pool 10 s
  (`spinneret.Timeouts`).
- `RetryPolicy(max_retries=2)`: transport errors and `unavailable` responses are retried with
  jittered exponential backoff; `circuit_open` and `site_paused` are never retried; server retry
  hints above `max_retry_after` (5 s) are raised instead of waited for.
- `Acquire` and `AcquireBatch` are not idempotent: they are retried only when the request provably
  never reached the server (connection refused, connect or pool timeout) or when the server
  answered `unavailable`. Ambiguous failures (read timeout, connection reset) are raised.
- `overloaded` (`unavailable`) means the server was at its acquire concurrency limit and shed the
  call before attempting it. No Redis command was issued, so it is retryable like any other
  `unavailable`: the retry honours `Spinneret-Retry-After-Ms`, which the server jitters into
  100–200 ms. It is not `no_identity_available` — the identity pool was never consulted — and needs
  no SDK change.
- `spinneret.NO_RETRY` disables retries.

## Error kinds

`spinneret.classify_exception(exc)` maps request exceptions to report `error_kind` values:
`timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns` or `other`. It understands
httpx exceptions (including their causes), socket/SSL errors and common messages. An
`httpx.HTTPStatusError` yields `""` (report the status instead) except 407 (`proxy_auth`).

## Processes and event loops

Create clients after forking worker processes (for example in the worker start-up hook of
gunicorn or uvicorn): `httpx` connection pools must not be shared between processes. An
`AsyncClient` belongs to the event loop it is used on; create one per loop.

## Logging and security

The SDK logs through `logging.getLogger("spinneret")` (children `spinneret.reporter`,
`spinneret.lease`, `spinneret.config`) with a `NullHandler` installed. It never logs tokens,
credentials, proxy URLs, config contents or secret values, and model `repr`s hide those fields.

## Development

```bash
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

Tests use `respx` and `httpx.MockTransport` and never access the network.
