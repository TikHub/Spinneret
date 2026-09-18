# SDKs and examples

**The client libraries a crawler node uses to talk to Spinneret: the Python SDK, the Go SDK, the runnable example node, and what to do when your language has neither.**

[中文](../zh/14-sdks.md)

---

## Contents

- [Choosing a client](#choosing-a-client)
- [Python SDK](#python-sdk)
  - [Installation](#installation)
  - [Connecting](#connecting)
  - [A complete node](#a-complete-node)
  - [Leases](#leases)
  - [Release semantics](#release-semantics)
  - [Reports](#reports)
  - [The background reporter](#the-background-reporter)
  - [Configuration center](#configuration-center)
  - [Secrets](#secrets)
  - [Errors](#errors)
  - [Retries and timeouts](#retries-and-timeouts)
  - [Error kinds](#error-kinds)
  - [Processes and event loops](#processes-and-event-loops)
  - [API reference](#api-reference)
- [Go SDK](#go-sdk)
  - [Installation](#installation-1)
  - [Options](#options)
  - [A complete node](#a-complete-node-1)
  - [Protocols](#protocols)
  - [Leases](#leases-1)
  - [Credentials](#credentials)
  - [Reports](#reports-1)
  - [The background reporter](#the-background-reporter-1)
  - [Configuration center](#configuration-center-1)
  - [Secrets](#secrets-1)
  - [Errors](#errors-1)
  - [Retries and timeouts](#retries-and-timeouts-1)
  - [API reference](#api-reference-1)
- [The example crawler](#the-example-crawler)
- [Writing a client without an SDK](#writing-a-client-without-an-sdk)
- [Next](#next)

---

## Choosing a client

A node needs two things: a server URL and an API token. Everything else comes from Spinneret at
request time. Three ways to ask for it:

| Client | Location | Transport | Use it when |
| --- | --- | --- | --- |
| Python SDK | `sdk/python` | Connect over HTTP with JSON | Python 3.9+ nodes, sync or asyncio |
| Go SDK | `sdk/go/spinneret` | Connect JSON (default) or gRPC | Go 1.27+ nodes |
| Plain HTTP + JSON | — | Connect over HTTP with JSON | Any other language |

Both SDKs cover exactly the four node services — `LeaseService`, `ReportService`, `ConfigService`
and `SecretService` — described in the [Node API reference](./13-node-api.md). Neither exposes the
administrative API; that is the console and [`spnr`](./15-cli.md).

---

## Python SDK

Source: `sdk/python`. Version 0.1.0. Requires Python 3.9+, `httpx>=0.27` and `pydantic>=2.6`.

### Installation

From a checkout of the repository:

```bash
pip install ./sdk/python                # the SDK
pip install './sdk/python[crypto]'      # + encrypted snapshots of configs that reference secrets
```

The distribution name is `spinneret`, so `pip install spinneret` installs it from any index the
package has been published to. SOCKS proxies handed out by Spinneret additionally need
`pip install 'httpx[socks]'`.

### Connecting

`Settings` resolves explicit arguments first and falls back to the environment.

| Variable | Argument | Meaning | Default |
| --- | --- | --- | --- |
| `SPINNERET_URL` | `url` | Server base URL, e.g. `https://spinneret.internal` (a path prefix is allowed; query, fragment and embedded credentials are rejected) | required |
| `SPINNERET_TOKEN` | `token` | Node API token (`spn_...`) | required |
| `SPINNERET_NODE` | `node` | Node name sent as `X-Spinneret-Node`; characters outside `[A-Za-z0-9._:@-]` become `-`, cut to 128 characters | host name |
| `SPINNERET_CACHE_DIR` | `cache_dir` | Directory of config snapshots | `~/.spinneret/cache` |

```python
import spinneret

client = spinneret.Client()                                    # from the environment
client = spinneret.Client("https://spinneret.internal", "spn_xxx", node="crawler-a-03")
```

A missing or invalid URL or token raises `ConfigurationError` at construction time; the constructor
does not contact the server. Every call carries `Authorization: Bearer <token>`,
`X-Spinneret-Node`, `Connect-Protocol-Version: 1` and
`User-Agent: spinneret-python/0.1.0 httpx/<version>`.

`AsyncClient` takes the same arguments and has the same methods, awaited, plus `aclose()`.

### A complete node

`sdk/python/examples/basic_usage.py` — the whole script, with its module docstring shortened: watch
a config item, lease, request, report.

```python
"""Basic usage of the Spinneret Python SDK."""

from __future__ import annotations

import json
import logging
import time

import httpx

import spinneret

SITE = "example-site"
CLIENT = "web"
TARGET = "https://target.example.com/api/v1/search?keyword=spinneret"

logger = logging.getLogger("example")


def detect_markers(response: httpx.Response) -> list[str]:
    """Recognize response features the server cannot see (the body is not uploaded)."""
    markers: list[str] = []
    if "verify" in str(response.url) or "captcha" in response.text[:2048]:
        markers.append("captcha_page")
    try:
        payload = response.json()
    except ValueError:
        return markers
    if isinstance(payload, dict) and not payload.get("data"):
        markers.append("empty_list")
    return markers


def crawl_once(client: spinneret.Client) -> None:
    """Lease an identity, send one request and report what happened."""
    try:
        with client.lease(site=SITE, client=CLIENT, uri=TARGET, session_key="task-8842") as lease:
            with httpx.Client(timeout=15, **lease.httpx_kwargs()) as http:
                try:
                    response = http.get(TARGET)
                except httpx.HTTPError as exc:
                    lease.report_exception(exc)
                    return
            lease.report_response(response, markers=detect_markers(response))
    except spinneret.NoIdentityAvailable as exc:
        logger.info("no identity available, retry in %.1fs", exc.retry_after or 1.0)
        time.sleep(exc.retry_after or 1.0)
    except (spinneret.CircuitOpen, spinneret.SitePaused) as exc:
        logger.warning("endpoint paused by Spinneret (%s), backing off", exc.reason)
        time.sleep(exc.retry_after or 30.0)


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    with spinneret.Client() as client:
        watcher = client.config_watcher(["crawler/search.json"], on_change=log_change)
        with watcher:
            item = watcher.get("crawler", "search.json")
            settings = json.loads(item.content) if item is not None else {}
            for _ in range(int(settings.get("requests", 3))):
                crawl_once(client)
        stats = client.reporter.stats
        logger.info("reports sent=%d dropped=%d", stats.sent, stats.dropped)


def log_change(item: spinneret.ConfigItem) -> None:
    """Config change callback (never log the content: it may contain resolved secrets)."""
    logger.info("config %s/%s is now version %d", item.group, item.key, item.version)


if __name__ == "__main__":
    main()
```

```bash
export SPINNERET_URL=https://spinneret.internal
export SPINNERET_TOKEN=spn_xxx
python sdk/python/examples/basic_usage.py
```

The asyncio shape is the same:

```python
async with spinneret.AsyncClient() as client:
    async with client.lease(site="example-site", client="web", uri="/api/v1/feed") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.example.com/api/v1/feed")
        lease.report_response(response)
```

`report`, `report_response` and `report_exception` are plain (non-blocking) methods on the async
lease too; only `renew()`, `release()` and the client calls are awaited.

### Leases

```python
client.lease(site, client, uri="", *, endpoint_group="", session_key="", wait_ms=0,
             flush_on_exit=False, raise_on_release_error=False)
```

Returns a context manager that calls `LeaseService/Acquire` on entry. Arguments map one to one onto
the RPC: `endpoint_group` takes precedence over `uri`, `session_key` drives sticky sessions, and
`wait_ms` (0..5000) is how long the server may wait for a free identity.

| Member | Description |
| --- | --- |
| `lease_id`, `identity_id`, `info`, `expires_at`, `hints` | Lease metadata (`info.probe`, `info.sticky`, `info.identity_type`, `info.endpoint_group`, `hints.renew_before_ms`) |
| `credential` | `cookies`, `cookie_header`, `headers`, `query`, `json_value` (JSON key `json`), `values` |
| `proxy` | `proxy_id`, `url`, `kind`, `region`, or `None` |
| `response` | The full `AcquireResponse` |
| `httpx_kwargs(headers=, params=, cookies=)` | `dict(headers, cookies, params, proxy)` for `httpx.Client(...)` |
| `report(status, *, latency_ms, markers, business_code, error_kind, release, **fields)` | Queue a report |
| `report_response(response, *, markers, business_code, release, **fields)` | Report an `httpx.Response` |
| `report_exception(exc, *, markers, release, **fields)` | Report a failed request, `error_kind` from `classify_exception` |
| `renew(extend_ms=0)` | Extend the lease (`0` = the policy lease TTL); `await` on the async lease |
| `release()` | End the lease now; `await` on the async lease |
| `acquired`, `released` | State flags |

`httpx_kwargs()` merges the credential and proxy into client arguments. Credential headers, query
parameters and cookies come first; values you pass explicitly override them (header names compare
case-insensitively). When the credential has no cookie map, its `cookie_header` becomes a `Cookie`
header — or is parsed into cookies when you also pass `cookies=`. `proxy` must go to the client
constructor: httpx does not accept a proxy per request.

Direct calls exist for everything the context manager wraps: `acquire`, `acquire_batch(site,
client, count, ...)` (1..50 identities), `renew`, `release`, and `report(reports)` (1..500 reports,
delivered synchronously).

### Release semantics

This is the part that is easy to get wrong in a hand-written client, so read it once.

- Reports are queued on the client's background reporter. The **most recent report is held back**
  until the next report or the end of the block, so that it can carry `release: true`.
- On exit the last report is sent with `release: true`, **also when the block raised**. When
  nothing was reported, the lease is released with `LeaseService/Release`; no report is invented,
  so report failed requests yourself (`report_exception`) if the server should count them.
- `report(..., release=True)` releases immediately; later reports raise `LeaseReleased`.
- `flush_on_exit=True` waits (up to `ReporterOptions.close_timeout`) until the queue is delivered.
- Release failures do not escape the block by default. A `Release` error with reason
  `lease_released`, `lease_unknown` or `lease_expired` means the lease has already ended and is
  logged at debug level; other errors are logged at warning level. With
  `raise_on_release_error=True` those other errors are raised from the `with` statement and from
  `release()` — unless the block itself raised, in which case its exception is never replaced.
- One lease can serve several requests (pagination): report every request, the last one releases.

### Reports

| Field | Default | Notes |
| --- | --- | --- |
| `report_id` | random UUID4 | Idempotency key, `^[A-Za-z0-9_.:-]{1,64}$` |
| `lease_id` | the lease | |
| `uri` | the acquired URI | Reduced to the request path, at most 2048 characters |
| `method` | `""` | At most 16 characters |
| `http_status` (`status=`) | `0` | 0..999; 0 when no response was received |
| `business_code` | `""` | Numbers are accepted and converted to strings, at most 64 characters |
| `error_kind` | `""` | Must be empty or one of the [error kinds](#error-kinds) |
| `markers` | `[]` | At most 32 markers of 1..64 characters |
| `outcome_hint` | `""` | At most 32 characters; the signal policy decides, this only hints |
| `latency_ms` | derived | From `finished_at - started_at` when omitted |
| `response_bytes` | `0` | |
| `started_at` | `finished_at - latency_ms` | |
| `finished_at` | now | |
| `release` | `false` | |

The reported `uri` is always reduced to the request path: the query string and fragment are
removed and the result is capped at 2048 characters. Endpoint groups match on the path only, and
query parameters frequently carry signed credential values that must not end up in request
analytics. A `407` response is reported with `error_kind="proxy_auth"`.

**Note.** A lease acquired with `endpoint_group` and no `uri` produces an empty report URI. The
SDK's own `Report` model requires one (`min_length=1`), so `report()` raises
`pydantic.ValidationError` locally and nothing is queued; the server would reject it as well. Pass
`uri=` to `report()` in that case.

### The background reporter

`client.reporter` is a `Reporter` (a daemon thread) or, on `AsyncClient`, an `AsyncReporter` (an
asyncio task). Both batch reports into `ReportService/Report` calls.

| Option | Default | Meaning |
| --- | --- | --- |
| `flush_interval` | `0.2` | Seconds a report may wait before a send |
| `batch_size` | `100` | Queue length that triggers an immediate send |
| `max_batch_size` | `500` | Reports per call (the server limit) |
| `max_queue_size` | `10_000` | Queue bound; beyond it the oldest reports are dropped |
| `backoff` | `Backoff(initial=0.5, maximum=30.0)` | Between failed deliveries |
| `close_timeout` | `5.0` | Seconds `close()` spends delivering the queue |
| `on_rejected` | `None` | Callback per report rejected by the server |

```python
options = spinneret.ReporterOptions(
    flush_interval=0.2,
    batch_size=100,
    max_queue_size=10_000,
    on_rejected=lambda r: log.warning("rejected %s: %s", r.report_id, r.reason),
)
client = spinneret.Client(reporter_options=options)
```

- Transport errors and `unavailable`, `internal`, `deadline_exceeded`, `aborted` and
  `resource_exhausted` responses are retried with jittered exponential backoff; `circuit_open` and
  `site_paused` are not. Other failures drop the batch.
- Reports rejected by the server are dropped, logged and passed to `on_rejected`.
- `flush(timeout)` sends the queue now; `close(timeout)` — also called by `client.close()` —
  delivers what it can and stops. The sync reporter also closes at interpreter exit; with asyncio
  always `await client.aclose()`.
- A worker that is no longer running (a thread in a process created with `os.fork()`, or a task of
  an event loop that has ended) is restarted on the next `submit`, `flush` or `close`.
- `client.reporter.stats` returns `submitted`, `sent`, `accepted`, `duplicated`, `rejected`,
  `dropped`, `failed_sends` and `queued`. Export `dropped` to your own metrics: it is the only
  signal that a node is losing reports.

### Configuration center

```python
def on_change(item: spinneret.ConfigItem) -> None:
    reload_settings(item.content)


with client.config_watcher(
    ["crawler/search.json", ("_runtime", "breakers")], on_change=on_change
) as watcher:
    item = watcher.get("crawler", "search.json")
    changed = watcher.wait_for_change("crawler", "search.json", timeout=60)
```

Items are spelled as `"group/key"`, `(group, key)` or `ConfigKey(group=..., key=...)`; 1..200 of
them per watcher.

- `start()` loads the items with `BatchGetConfig`; a thread (or asyncio task) then long-polls
  `WatchConfig` with `timeout_ms` 30 000 (HTTP read timeout `timeout_ms + 5 s`), stores the latest
  items and invokes the callbacks for every changed item, initial values included. The loop ends
  when the watcher is stopped or the client is closed.
- Every successfully fetched non-secret item is written atomically (temp file, `fsync`, rename,
  mode `0600`, directories `0700`) to `<cache_dir>/<host>/<namespace>/<group>/<key>.json` with
  percent-encoded components. `<namespace>` is the literal `_token_namespace` when the watcher sets
  no namespace — the usual case, because a node token implies its namespace.
- When the server is unavailable at start (transport error, `unavailable`, `internal`,
  `deadline_exceeded`, `resource_exhausted` or another 5xx), the snapshots are loaded instead and
  `watcher.from_snapshot` stays `True` until the server answers. Authorization and validation
  errors are raised by `start()`.
- **Secret items are never written to disk in plain text.** The server resolves `${secret:...}`
  references before delivery and sets `has_secret_refs` on every item whose published content
  contained one; the watcher keeps such items in memory only and removes any older snapshot.
  Content that still contains an unresolved `${secret:` marker counts as secret too.
- `treat_as_secret` is an additional override for items that embed sensitive values directly,
  e.g. `treat_as_secret=lambda item: item.group == "signing"`. It can only add secret items, never
  exempt one flagged by the server; a predicate that raises counts as true.
- `cache_secrets=True` writes secret items encrypted with AES-256-GCM under a key derived from the
  API token with HKDF-SHA256 (fresh salt and nonce per file). This needs the `cryptography`
  package (`spinneret[crypto]`); without it, `cache_secrets=True` raises `ConfigurationError`.
  Snapshots encrypted with a previous token cannot be read after token rotation.
- `snapshots=False` disables the cache; `cache_dir=` overrides the directory.

Other watcher members: `items()`, `add_listener()`, `remove_listener()`, `running`,
`snapshot_store`, `stop(timeout)`.

One-off reads without a watcher: `get_config(group, key)`, `batch_get_config(items)` and
`watch_config(items, timeout_ms=30_000)`.

### Secrets

```python
secret = client.get_secret("signing/api_key")          # version=0 reads the current version
value = secret.value                                    # hidden from repr
pinned = client.get_secret("signing/api_key", version=3)
```

`GetSecretResponse` carries `path`, `version`, `value` and `expires_at`. Cache the value in memory
for as long as your rotation window allows, never on disk. See [Secret vault](./10-secrets.md).

### Errors

Every error derives from `SpinneretError` with `code`, `reason`, `message`, `retry_after_ms`
(`retry_after` in seconds) and `http_status`.

| Exception | Code / reason | What the node should do |
| --- | --- | --- |
| `Unauthenticated` | `unauthenticated` (`token_invalid`, `token_expired`, `token_revoked`, `ip_not_allowed`) | stop and alert |
| `PermissionDenied` | `permission_denied` (`scope_missing`) | stop and alert |
| `InvalidArgument` | `invalid_argument` (`site_unknown`, `client_unknown`, `uri_invalid`, ...) | fix the caller |
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

Responses without a Connect error body — a load balancer page, say — are mapped from the HTTP
status following the Connect protocol (401 → `unauthenticated`, 403 → `permission_denied`,
404 → `unimplemented`, 429/502/503/504 → `unavailable`).

### Retries and timeouts

- Timeouts (`spinneret.Timeouts`): connect 3 s, read 10 s (plus `wait_ms` for acquire), write 10 s,
  pool 10 s, and `watch_grace` 5 s added to the long-poll wait.
- `RetryPolicy(max_retries=2, backoff=Backoff(0.1, 2.0, 2.0), max_retry_after=5.0)`: transport
  errors and `unavailable` responses are retried with jittered exponential backoff; `circuit_open`
  and `site_paused` are never retried; a server retry hint above `max_retry_after` is raised
  instead of waited for.
- `Acquire` and `AcquireBatch` are not idempotent: they are retried only when the request provably
  never reached the server (connection refused, connect timeout, pool timeout) or when the server
  answered `unavailable`. Ambiguous failures (read timeout, connection reset) are raised.
- `overloaded` (`unavailable`) means the server was at its acquire concurrency limit and shed the
  call before attempting it. No Redis command was issued, so it is retryable like any other
  `unavailable`: the retry honours `Spinneret-Retry-After-Ms`, which the server jitters into
  100–200 ms. It is not `no_identity_available` — the identity pool was never consulted — and needs
  no SDK change.
- `spinneret.NO_RETRY` disables retries.

### Error kinds

`spinneret.classify_exception(exc)` maps a request exception to a report `error_kind`:

| Value | Meaning |
| --- | --- |
| `timeout` | The request did not finish in time |
| `conn_reset` | The connection was reset, aborted or closed mid-response |
| `conn_refused` | The connection was refused, the host or network is unreachable |
| `proxy_auth` | The proxy rejected the credentials (also a `407` response) |
| `tls` | TLS handshake or certificate failure |
| `dns` | Name resolution failed |
| `other` | Anything else |

It understands httpx exceptions (including their cause chain), socket/SSL errors and common
messages. An `httpx.HTTPStatusError` yields `""` — report its status instead — except `407`.

### Processes and event loops

Create clients **after** forking worker processes, for example in the worker start-up hook of
gunicorn or uvicorn: `httpx` connection pools must not be shared between processes. An
`AsyncClient` belongs to the event loop it is used on; create one per loop.

The SDK logs through `logging.getLogger("spinneret")` (children `spinneret.reporter`,
`spinneret.lease`, `spinneret.config`) with a `NullHandler` installed. It never logs tokens,
credentials, proxy URLs, config contents or secret values, and model `repr`s hide those fields.

### API reference

| Symbol | Kind | Purpose |
| --- | --- | --- |
| `Client` / `AsyncClient` | class | Node API client; `acquire`, `acquire_batch`, `renew`, `release`, `lease`, `report`, `get_config`, `batch_get_config`, `watch_config`, `config_watcher`, `get_secret`, `close` / `aclose` |
| `ManagedLease` / `AsyncManagedLease` | class | Context-managed lease (see [Leases](#leases)) |
| `Reporter` / `AsyncReporter` | class | Background batching; `submit`, `flush`, `close`, `stats`, `options`, `closed` |
| `ConfigWatcher` / `AsyncConfigWatcher` | class | Long-poll watch loop; `start`, `stop`, `get`, `items`, `wait_for_change`, `add_listener`, `remove_listener`, `from_snapshot`, `running`, `snapshot_store` |
| `Settings` | dataclass | `url`, `token`, `node`, `cache_dir`, `host`, `from_env()` |
| `Timeouts` | dataclass | `connect`, `read`, `write`, `pool`, `watch_grace` |
| `RetryPolicy`, `Backoff`, `NO_RETRY` | dataclass / constant | Retry tuning of unary calls |
| `ReporterOptions`, `ReporterStats` | dataclass | Reporter tuning and counters |
| `WatchOptions` | dataclass | `timeout_ms`, `backoff` of a watch loop |
| `SnapshotStore`, `SecretPredicate`, `ChangeCallback`, `AsyncChangeCallback`, `ConfigKeyLike` | class / types | Snapshot layout, the secret predicate, the two change-callback signatures and what counts as a config key (`"group/key"`, `(group, key)` or `ConfigKey`) |
| `classify_exception` | function | Exception → `error_kind` |
| `ErrorKind`, `Outcome`, `SECRET_REF_MARKER` | constants | Report `error_kind` values, outcome classes, `${secret:` marker |
| `AcquireRequest`, `AcquireResponse`, `Lease`, `Credential`, `Proxy`, `Hints`, `Report`, `ReportResponse`, `RejectedReport`, `ConfigItem`, `ConfigKey`, `WatchItem`, `GetSecretResponse`, ... | pydantic models | One per node API message; immutable, tolerant of unknown fields |
| `SpinneretError` and subclasses | exceptions | See [Errors](#errors) |

Development:

```bash
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

Tests use `respx` and `httpx.MockTransport` and never access the network.

---

## Go SDK

Source: `sdk/go/spinneret`. Version 0.1.0. Go 1.27+. Its only runtime dependencies are
`connectrpc.com/connect` and `google.golang.org/protobuf`.

### Installation

The package lives in the main module:

```bash
go get github.com/TikHub/Spinneret@latest
```

```go
import "github.com/TikHub/Spinneret/sdk/go/spinneret"
```

### Options

`spinneret.New(spinneret.Options{...})` validates the options and does not contact the server.
Empty `BaseURL`, `Token` and `Node` fall back to the environment.

| Option | Variable | Meaning | Default |
| --- | --- | --- | --- |
| `BaseURL` | `SPINNERET_URL` | Server URL, e.g. `https://spinneret.internal` (a path prefix is allowed) | required |
| `Token` | `SPINNERET_TOKEN` | Node API token (`spn_...`) | required |
| `Node` | `SPINNERET_NODE` | Node name sent as `X-Spinneret-Node`, sanitized to `[A-Za-z0-9._:@-]` and 128 characters | host name |
| `UseGRPC` | | gRPC (binary protobuf, HTTP/2) instead of Connect JSON | `false` |
| `Timeout` | | Bound of each attempt of a unary call | `10s` |
| `HTTPClient` | | Custom `connect.HTTPClient` (TLS roots, egress proxy, ...) | tuned `http.Client` |
| `Retry` | | `*RetryPolicy`; `spinneret.NoRetry()` disables retries | 2 retries |
| `Reporter` | | `ReporterOptions` of `client.Reporter()` | see below |
| `Logger` | | `*slog.Logger` | `slog.Default()` |
| `UserAgent` | | Prepended to `spinneret-go/0.1.0` | |

```go
client, err := spinneret.New(spinneret.Options{
	BaseURL: "https://spinneret.internal",
	Token:   os.Getenv("SPINNERET_TOKEN"),
	Node:    "crawler-a-03",
})
if err != nil {
	return err
}
defer client.Close(context.Background()) // delivers queued reports
```

The default HTTP client dials with a 3 s timeout, honors `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`,
negotiates HTTP/2 over TLS and, with `UseGRPC`, uses unencrypted HTTP/2 (h2c) for `http://` URLs.
A custom client must not set `http.Client.Timeout` below the `WatchConfig` wait (35 s by default):
per-call deadlines are applied through the request context instead.

### A complete node

The core of `sdk/go/examples/basic/main.go` — lease, request through the leased proxy, report:

```go
// crawlOnce leases an identity, sends one request and reports what happened.
func crawlOnce(ctx context.Context, client *spinneret.Client, cfg config, logger *slog.Logger) error {
	lease, err := client.Lease(ctx, &spinneret.AcquireRequest{
		Site:   cfg.site,
		Client: cfg.client,
		Uri:    cfg.target,
		WaitMs: 500,
	})
	switch {
	case spinneret.IsNoIdentity(err), spinneret.IsNoProxy(err):
		sleep(ctx, waitFor(err, time.Second)) // wait for capacity
		return nil
	case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
		sleep(ctx, waitFor(err, 30*time.Second)) // the endpoint group or site is switched off
		return nil
	case err != nil:
		return fmt.Errorf("acquire: %w", err)
	}
	defer func() {
		// Release even when ctx was cancelled by a signal.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if err := lease.Close(closeCtx); err != nil {
			logger.Warn("release", slog.String("lease_id", lease.ID()), slog.String("error", err.Error()))
		}
	}()

	transport, err := lease.Transport(nil) // clone of http.DefaultTransport with the leased proxy
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: requestTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.target, nil)
	if err != nil {
		return err
	}
	lease.Apply(req) // cookies, headers and query parameters of the credential
	started := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started, HTTPStatus: resp.StatusCode})
	}
	return lease.ReportResponse(resp, spinneret.ReportInput{
		StartedAt:     started,
		ResponseBytes: int64(len(body)),
		Markers:       detectMarkers(body), // e.g. "captcha_page", "empty_list"
	})
}
```

Run the whole example:

```bash
export SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_xxx
go run ./sdk/go/examples/basic \
  -site example-site -client web \
  -target https://target.example.com/api/v1/search \
  -config crawler/search.json
```

Flags: `-site`, `-client`, `-target`, `-requests` (default 3), `-config` (a `group/key` to watch,
optional), `-grpc`.

### Protocols

| | Connect JSON (default) | gRPC (`UseGRPC: true`) |
| --- | --- | --- |
| Transport | HTTP/1.1 or HTTP/2 | HTTP/2 only (ALPN over TLS, h2c for `http://`) |
| Encoding | JSON with snake_case field names; unknown response fields ignored | binary protobuf |
| Error reason / retry hint | response headers | response trailers |

Both protocols produce the same `*spinneret.Error` values. Load balancers must pass HTTP/2 end to
end for gRPC, and the Compose load balancer does not: `deploy/compose/config/Caddyfile` gives its
`reverse_proxy` an `http` transport with no `versions h2c 2`, so Caddy terminates the client's
HTTP/2 and forwards HTTP/1.1 to the plaintext `spinneret:8080` upstream. Port 8080 of the stack is
therefore Connect JSON only. To use `UseGRPC: true` against the Compose stack, either add
`versions h2c 2` to that transport block, or reach an instance directly — the `spinneret` service
publishes no host port, so you have to publish one yourself. This is why the live tests take a
separate `SPINNERET_LIVE_GRPC_URL`.

### Leases

`client.Lease(ctx, *AcquireRequest)` calls `Acquire` and returns a `*Lease`;
`client.LeaseBatch(ctx, *AcquireBatchRequest)` wraps every lease of `AcquireBatch`; and
`client.NewLease(resp, uri)` wraps a response obtained elsewhere. A `*Lease` is safe for concurrent
use.

| Member | Description |
| --- | --- |
| `ID()`, `IdentityID()`, `Info()`, `Hints()`, `Response()`, `URI()` | Lease metadata (`Info().GetProbe()`, `GetSticky()`, `GetIdentityType()`, `GetEndpointGroup()`) |
| `ExpiresAt()`, `Released()` | Expiry (updated by `Renew`) and state |
| `Credential()` | `cookies`, `cookie_header`, `headers`, `query`, `json`, `values` |
| `Proxy()`, `ProxyURL()` | Assigned proxy (`nil` when none); the URL carries credentials, never log it |
| `Transport(base)` | Clone of `base` (or `http.DefaultTransport`) routed through the proxy |
| `Apply(req)` | Merge the credential into an `*http.Request` |
| `Report(ReportInput)` | Queue a report |
| `ReportResponse(resp, ReportInput)` | Report an `*http.Response` (status, method, path, `Content-Length`; 407 sets `proxy_auth`) |
| `ReportError(err, ReportInput)` | Report a failed request; `error_kind` from `ClassifyError`, method and path from a `*url.Error` |
| `Renew(ctx, extend)` | Extend the lease (`0` = the policy lease TTL) |
| `Close(ctx)` | Release the lease (idempotent) |

Release semantics match the Python SDK:

- Reports are queued on `client.Reporter()`. The most recent report is held back until the next
  report or `Close`, so that it can carry `release: true`.
- `Close` sends the held-back report with `release: true`. When nothing was reported it calls
  `LeaseService/Release`; no report is invented, so report failed requests yourself with
  `ReportError` when the server should count them.
- `Close` returns the error of the `Release` call, except reasons saying the lease has already
  ended (`lease_released`, `lease_unknown`, `lease_expired`, `lease_lifetime_exceeded`). When the
  reporter is already closed, the last report is delivered directly.
- `ReportInput{Release: true}` releases immediately; later reports fail with `IsLeaseGone(err)`.
- One lease can serve several requests: report every request, `Close` releases.
- `client.Reporter().Flush(ctx)` waits until queued reports are delivered.

### Credentials

`Apply(req)` (and the free function `spinneret.ApplyCredential(req, cred)`) never overrides what
the request already carries:

- headers are set unless the request already has them (`Host` is skipped — net/http takes it from
  the URL);
- cookies are merged into a single `Cookie` header, skipping names the request already sends;
  without a cookie map and without request cookies, `cookie_header` is sent verbatim, unencoded;
- query parameters are appended when missing, keeping the existing query string byte for byte so
  that signed parameters stay valid.

`credential.GetValues().AsMap()` and `credential.GetJson().AsInterface()` give access to typed
values such as device parameters or request body fragments. `http.Transport` supports `http://`,
`https://` and `socks5://` proxy URLs.

### Reports

`ReportInput` fields: `HTTPStatus`, `Method`, `URI`, `BusinessCode`, `ErrorKind`, `Markers`,
`OutcomeHint`, `Latency`, `ResponseBytes`, `StartedAt`, `FinishedAt`, `ReportID`, `Release`.

- `ReportID` defaults to a random UUID, `FinishedAt` to now, `StartedAt` to
  `FinishedAt - Latency`, and `Latency` to `FinishedAt - StartedAt`.
- `URI` defaults to the acquired URI and is reduced to the request path (query string and fragment
  removed, at most 2048 characters). A lease acquired with `endpoint_group` only needs an explicit
  `URI`.
- The input is validated exactly as the server validates it (`error_kind` values, at most 32
  markers of 1..64 characters, status 0..999, `report_id` pattern, non-negative latency, ...).
  Invalid input returns an `invalid_argument` error without queuing anything.
- `ReportResponse` cannot see the size of a chunked body: pass `ResponseBytes` after reading it.

### The background reporter

`client.Reporter()` (or `spinneret.NewReporter(send, options, logger)`):

| Option | Default | Meaning |
| --- | --- | --- |
| `FlushInterval` | `200ms` | Longest a report waits before a send |
| `BatchSize` | `100` | Queue length that triggers an immediate send |
| `MaxBatchSize` | `500` | Reports per call (the server limit) |
| `MaxQueueSize` | `10000` | Queue bound; beyond it the oldest reports are dropped |
| `InitialBackoff` | `500ms` | First delay after a failed delivery |
| `MaxBackoff` | `30s` | Cap of the delay between failed deliveries |
| `CloseTimeout` | `5s` | Bound of `Close` when its context has no deadline |
| `OnRejected` | `nil` | Callback per rejected report, on the reporter goroutine |

```go
client, err := spinneret.New(spinneret.Options{
	Reporter: spinneret.ReporterOptions{
		FlushInterval: 200 * time.Millisecond,
		BatchSize:     100,
		MaxQueueSize:  10_000,
		OnRejected: func(r *spinneret.RejectedReport) {
			log.Printf("rejected %s: %s", r.GetReportId(), r.GetReason())
		},
	},
})
stats := client.Reporter().Stats() // Submitted, Sent, Accepted, Duplicated, Rejected, Dropped, FailedSends, Queued
```

Failures for which `IsRetryable(err)` holds — transport errors, `unavailable` (except
`circuit_open` and `site_paused`), `internal`, `unknown`, `data_loss`, `deadline_exceeded`,
`aborted`, `resource_exhausted` — are retried with jittered exponential backoff, at least as long
as the server retry hint. Other failures drop the batch. `Submit` never blocks on the network and
fills `report_id`, `finished_at` and `started_at` when empty. `Close(ctx)`, called by
`client.Close`, delivers what it can until `ctx` ends, aborts the in-flight call and returns an
error when reports were dropped.

### Configuration center

```go
watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
	Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "_runtime", Key: "breakers"}},
	SnapshotDir: "/var/cache/spinneret",
	OnChange:    func(item *spinneret.ConfigItem) { reload(item.GetContent()) },
})
if err != nil {
	return err
}
if err := watcher.Start(ctx); err != nil { // the loop stops when ctx ends, on Stop or client.Close
	return err
}
item, ok := watcher.Get("crawler", "search.json")
versions := watcher.Versions()
changed, err := watcher.WaitForChange(ctx, "crawler", "search.json")
```

`WatcherOptions`: `Items` (1..200), `Namespace`, `Timeout` (default 30 s, max 60 s), `SnapshotDir`,
`TreatAsSecret`, `OnChange`, `InitialBackoff` (1 s), `MaxBackoff` (30 s).
`spinneret.ParseConfigKey("group/key")` builds a `ConfigKey` from a string.

- `Start` loads the items with `BatchGetConfig` and starts a goroutine that long-polls
  `WatchConfig` with the known versions, stores the latest items and calls the listeners for every
  change, initial values included. Listeners run sequentially, must not block for long and must
  treat items as read-only; a panicking listener is logged and does not stop the watcher. `Stop`
  cancels the in-flight long poll.
- A poll that comes back with no changes in under 200 ms is followed by a 200 ms sleep
  (`watchMinPollInterval`), so a misconfigured intermediary that answers the long poll immediately
  cannot turn the watch into a busy loop. Keep this in mind when lowering `Timeout`.
- Failed polls are retried with backoff. When the server is unavailable at start (`IsRetryable`
  errors), the snapshots are loaded instead and `FromSnapshot()` is `true` until the server
  answers. Authorization and validation errors are returned by `Start`, which can then be called
  again.
- With `SnapshotDir`, every fetched item is written atomically (temp file, `fsync`, rename; files
  `0600`, directories `0700`) to `<dir>/<host>/<namespace>/<group>/<key>.json` with percent-encoded
  components — the same layout and format as the Python SDK. `<namespace>` is the literal
  `_token_namespace` when `Namespace` is empty, the usual case.
- Secret material is never written to disk: items with `has_secret_refs`, content still containing
  `${secret:` and items matched by `TreatAsSecret` stay in memory only and older snapshots of them
  are removed. `TreatAsSecret` can only add secret items; a predicate that panics counts as true.
  Encrypted snapshots written by the Python SDK are ignored.

Other members: `Keys()`, `Items()`, `AddListener()`, `Done()`.

One-off reads: `GetConfig`, `BatchGetConfig` and `WatchConfig` (`timeout_ms` 0 = 30 s).

### Secrets

```go
secret, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{Path: "signing/api_key"})
if err != nil {
	return err
}
value := secret.GetValue()

pinned, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{
	Path:    "signing/api_key",
	Version: 3,
})
```

`Path` is relative to the token's namespace, at most 256 characters, and must match
`^[a-z0-9][a-z0-9_./-]*$`. `Version: 0` reads the current version. The token needs a `secret:read`
scope that covers `<namespace>/<path>`, and every read is written to the audit log.
`GetSecretResponse` carries `path`, `version`, `value` and `expires_at` (`nil` when the secret does
not expire). Cache the value in memory for as long as your rotation window allows, never on disk.
See [Secret vault](./10-secrets.md).

### Errors

Every call returns a `*spinneret.Error` (use `spinneret.AsError(err)` or `errors.As`):

| Field / method | Meaning |
| --- | --- |
| `Code` | `connect.Code`, e.g. `connect.CodeResourceExhausted` |
| `Reason` | `Spinneret-Reason`, e.g. `no_identity_available`; `transport` when no response was received |
| `Message` | Server message |
| `RetryAfter` | `Spinneret-Retry-After-Ms` as a `time.Duration` (0 when absent) |
| `Procedure` | The RPC that failed |
| `ErrorKind`, `Transport()` | Classification of transport failures |
| `FromServer()` | The server sent the error (not synthesized from a transport failure or a bare HTTP status) |
| `Unwrap()` | Underlying `*connect.Error` / `*url.Error` |

| Helper | Code / reason | What the node should do |
| --- | --- | --- |
| `IsUnauthenticated` | `unauthenticated` (`token_invalid`, ...) | stop and alert |
| `IsPermissionDenied` | `permission_denied` (`scope_missing`) | stop and alert |
| `CodeOf(err) == connect.CodeInvalidArgument` | `site_unknown`, `uri_invalid`, ... | fix the caller |
| `IsNoIdentity`, `IsNoProxy` | `resource_exhausted` | wait `RetryAfterOf(err)`, retry |
| `IsCircuitOpen`, `IsSitePaused` | `unavailable` | pause the endpoint group / site |
| `IsLeaseGone` | `lease_unknown`, `lease_released`, `lease_expired`, `lease_lifetime_exceeded` | acquire again |
| `IsTransport` | reason `transport` | retry later |
| `IsRetryable` | see the reporter section | retry the background delivery |
| `ReasonOf(err) == spinneret.ReasonClientClosed` | `failed_precondition` | create a new client |

Responses without a Connect error body keep the code derived from the HTTP status, with an empty
reason. The package exports every reason as a constant (`spinneret.ReasonNoIdentityAvailable`,
`spinneret.ReasonCircuitOpen`, ...).

### Retries and timeouts

- Each attempt is bounded by `Timeout` (10 s); `Acquire`/`AcquireBatch` add `wait_ms`;
  `WatchConfig` uses `timeout_ms` (30 s when 0) plus 5 s. A shorter deadline on `ctx` wins. The
  deadline sent to the server is one second later than the client-side timeout, so an expired
  attempt is always reported as a transport timeout instead of racing a `deadline_exceeded`
  answer caused by the same deadline.
- `RetryPolicy{MaxRetries: 2, InitialBackoff: 100ms, MaxBackoff: 2s, MaxRetryAfter: 5s}`:
  transport failures and `unavailable` answers are retried with jittered exponential backoff;
  `circuit_open` and `site_paused` are never retried; server hints above `MaxRetryAfter` are
  returned instead of waited for; certificate errors are never retried.
- `Acquire` and `AcquireBatch` are not idempotent: they are retried only when the failure provably
  happened before the request was sent (dial and DNS errors) or when the server itself answered
  `unavailable`. Ambiguous failures (per-call timeout, connection reset, a bare 502/503/504) are
  returned.
- `overloaded` (`unavailable`) means the server was at its acquire concurrency limit and shed the
  call before attempting it. No Redis command was issued, so it is retryable like any other
  `unavailable`: the retry honours `Spinneret-Retry-After-Ms`, which the server jitters into
  100–200 ms. It is not `no_identity_available` — the identity pool was never consulted — and needs
  no SDK change.
- The background reporter and the config watcher do not use this policy; they retry with their own
  backoff.

`spinneret.ClassifyError(err)` maps request errors to the same `error_kind` values as the Python
SDK (`timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other`; `""` for `nil`).
It understands `*url.Error` and the `net`, `syscall`, `crypto/tls` and `crypto/x509` errors it
wraps, HTTP proxy `CONNECT` refusals and SOCKS5 authentication failures, and falls back to the
error message. A `407` response of a plain HTTP proxy is a response, not an error: report it with
`ReportResponse`, which sets `proxy_auth`.

### API reference

| Symbol | Kind | Purpose |
| --- | --- | --- |
| `New`, `Options`, `Client` | func / struct | Client construction and the node RPCs: `Acquire`, `AcquireBatch`, `Renew`, `Release`, `Report`, `GetConfig`, `BatchGetConfig`, `WatchConfig`, `GetSecret`, `Lease`, `LeaseBatch`, `NewLease`, `NewConfigWatcher`, `Reporter`, `Close`; accessors `BaseURL()`, `Node()`, `Logger()`, `Closed()` |
| `Lease`, `ReportInput` | struct | Lease helper and report building |
| `ApplyCredential`, `ParseCookieHeader`, `ProxyURL`, `ReportURI`, `NewReportID` | func | Credential merging, parsing a `Cookie` header into a name → value map, proxy URL, URI reduction, idempotency keys |
| `Reporter`, `ReporterOptions`, `ReporterStats`, `NewReporter`, `SendFunc` | struct / func | Background batching |
| `ConfigWatcher`, `WatcherOptions`, `ConfigKey`, `ParseConfigKey` | struct / func | Long-poll watch loop |
| `Error`, `AsError`, `CodeOf`, `ReasonOf`, `RetryAfterOf`, `Is*` | struct / func | Typed errors |
| `RetryPolicy`, `DefaultRetryPolicy`, `NoRetry` | struct / func | Retry tuning |
| `ClassifyError`, `ErrorKind*` | func / const | Error kinds |
| `SanitizeNodeName`, `DefaultNodeName`, `Version`, `Env*`, `Header*`, `Reason*`, `Max*` | func / const | Node naming, limits, header and reason names |
| `AcquireRequest`, `AcquireResponse`, `LeaseInfo`, `Credential`, `ProxyAssignment`, `Hints`, `Report`, `ReportResponse`, `RejectedReport`, `ConfigItem`, `ConfigRef`, `WatchItem`, `GetSecretResponse`, ... | type aliases | The generated messages, re-exported so callers need not import the generated package |
| `LeaseService()`, `ReportService()`, `ConfigService()`, `SecretService()` | methods | The underlying generated clients: authenticated, without retries or error conversion |

Development:

```bash
go vet ./sdk/go/...
go test -race -count=1 -cover ./sdk/go/...
golangci-lint run ./sdk/go/...
```

Unit tests serve the generated handlers from `httptest` servers backed by fakes, over Connect JSON
and gRPC (h2c), and never access external networks. Live tests run against a real deployment such
as the Compose stack with the mock target:

```bash
export SPINNERET_LIVE_ADMIN_PASSWORD=...   # admin password of the stack
go test -tags live -race -count=1 -run TestLive ./sdk/go/spinneret
```

They create a namespace named `gosdk-<run>` with a site, identities, a token, a config item and a
secret, exercise both protocols, and delete everything afterwards. `SPINNERET_LIVE_URL`,
`SPINNERET_LIVE_TARGET`, `SPINNERET_LIVE_GRPC_URL`, `SPINNERET_LIVE_TENANT` and
`SPINNERET_LIVE_ADMIN_USER` override the defaults.

---

## The example crawler

`examples/fastapi-crawler` is a small but complete node: FastAPI in front, the Python SDK behind.
It is the fastest way to see the whole loop — lease, request through a proxy, report, and
Spinneret reacting — without writing any code.

Every HTTP call to the crawler:

1. **leases** an identity (cookies + User-Agent) and a proxy for the URI it is about to fetch,
   with `AsyncClient.lease(site, client="web", uri=...)`;
2. **requests** the target with `httpx.AsyncClient(**lease.httpx_kwargs())` — credential and proxy
   are merged into the client arguments;
3. **reports** the facts it observed with `lease.report_response(response, markers=[...])`: status,
   latency, size and page markers such as `captcha_page` or `login_redirect`. The report releases
   the lease when the `async with` block ends.

Spinneret turns those reports into outcomes (signal policy), cooldowns, bans and expiry (action
policy) and circuit breaking. The crawler never decides that an identity is burnt. It also watches
the config item `crawler/example.json` with a `ConfigWatcher`.

In the demo the target is the repository's mock target (`test/mocktarget`), which serves
`/site/...` pages and an authenticating HTTP proxy, and announces page features in the
`X-Mock-Marker` and `X-Mock-Business-Code` response headers. A real crawler parses the page
instead.

### Endpoints

| Method and path | Description |
| --- | --- |
| `GET /crawl/search?q=<text>` | Crawls `/site/search?q=<text>` (endpoint group `search`) |
| `GET /crawl/item/{id}` | Crawls `/site/item/{id}` (endpoint group `detail`) |
| `GET /config` | Current version and content of `crawler/example.json` from the config watcher |
| `GET /healthz` | Liveness, plus whether the config watcher is running |

A crawl answers `200` with `{"ok", "status", "identity_id", "proxy_id", "endpoint_group",
"markers", "business_code", "data"}`. Lease failures are mapped to HTTP answers: `503` for
`circuit_open` / `site_paused` / `overloaded`, `429` for every `resource_exhausted` (`no_identity_available`,
`no_proxy_available`, `rate_limited`) — both answers carry `Retry-After` from the server hint —
and `502` for other Spinneret errors and for requests that never
got a response (reported with their `error_kind`).

### Running it against the Compose stack

Prerequisites: the stack in `deploy/compose` is initialized and running (see
[Installation and deployment](./02-installation.md)), plus `python3` and `curl` on the host.

```bash
scripts/example-quickstart.sh            # or: make example
```

The script is idempotent. It

1. starts the stack without recreating running containers and starts the mock target (Compose
   profile `example`),
2. signs in as the administrator through the API and creates, in namespace `default`: site
   `example` with the endpoint groups `search` (prefix `/site/search`) and `detail` (template
   `/site/item/{id}`), the identity type `example_web_cookie`, 20 identities, 2 mock proxies, the
   published policies `example-rotation` (`bind_identity` proxies, 1 s reuse interval) and
   `example-signal`, and the config item `crawler/example.json`,
3. creates a node token with the scopes `lease:acquire:example`, `report:write:example` and
   `config:read:crawler`, and writes it to `deploy/compose/.env` as `EXAMPLE_TOKEN` (an existing
   valid token is reused),
4. builds and starts the `example-crawler` service on `http://localhost:18000` and calls
   `/crawl/search`, `/crawl/item/42` and `/config`.

`scripts/example-quickstart.sh --reset` deletes the example site, its token, proxies, policies and
config first.

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

Make the target misbehave and watch Spinneret react in the console — identities, breakers, request
explorer:

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_URL` | — (Compose: `http://lb:8080`) | Spinneret base URL, read by the SDK |
| `SPINNERET_TOKEN` | — (Compose: `EXAMPLE_TOKEN`) | Node token, read by the SDK |
| `SPINNERET_NODE` | host name (Compose: `example-crawler`) | Node name sent with every call |
| `SPINNERET_CACHE_DIR` | `/tmp/spinneret-cache` in the image | Config snapshot directory |
| `MOCK_TARGET_URL` | `http://mocktarget:9090` | Base URL of the crawled site |
| `EXAMPLE_SITE` / `EXAMPLE_CLIENT` | `example` / `web` | Site and client the leases are taken for |
| `EXAMPLE_CONFIG_GROUP` / `EXAMPLE_CONFIG_KEY` | `crawler` / `example.json` | Watched config item |
| `EXAMPLE_LEASE_WAIT_MS` | `2000` | Acquire wait for a free identity (0..5000) |
| `EXAMPLE_REQUEST_TIMEOUT_S` | `10` | Target request timeout (1..120) |
| `EXAMPLE_PORT` (Compose) | `18000` | Host port of the crawler |

### Which parts to copy into a real node

`app/crawler.py` is the part worth copying — it is the whole pattern in forty lines:

```python
async with client.lease(settings.site, client=settings.client, uri=path, wait_ms=settings.lease_wait_ms) as lease:
    async with httpx.AsyncClient(
        **lease.httpx_kwargs(),
        timeout=settings.request_timeout,
        follow_redirects=False,  # a login redirect is a signal to report, not to follow
    ) as http:
        try:
            response = await http.get(settings.mock_target_url + path, params=dict(params or {}))
        except httpx.HTTPError as exc:
            lease.report_exception(exc)
            raise UpstreamError(f"request to the target failed: {type(exc).__name__}") from exc
    markers = markers_from(response.headers)
    business_code = response.headers.get(BUSINESS_CODE_HEADER, "")
    lease.report_response(response, markers=markers, business_code=business_code)
```

`app/main.py` shows the process-level wiring: one `AsyncClient` and one `ConfigWatcher` created in
the FastAPI lifespan and closed on shutdown, and a small function mapping `SpinneretError`
subclasses to the HTTP answers of your own API.

Four habits from the example that belong in every node:

- **One client per process and event loop.** Create it in the lifespan (after forking workers) and
  `await client.aclose()` on shutdown, so that queued reports — including the lease releases — are
  delivered.
- **Do not follow redirects blindly.** A login redirect is a fact to report, not a page to fetch.
- **Report facts only.** Keep outcome classification in the signal policy, where it can be changed
  and tested (`PolicyAdminService/DebugReport`, or the rule debugger in the console) without
  redeploying nodes. See [Policies](./08-policies.md).
- **Never log credentials or proxy URLs.** The SDK models hide them from `repr`; your own logging
  must not undo that.

One implementation note: the example opens one `httpx.AsyncClient` per lease because httpx binds
proxies to the client. A busy node should keep a small pool of clients keyed by proxy URL.

### Running the example outside Docker

```bash
cd examples/fastapi-crawler
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt -e ../../sdk/python
.venv/bin/python -m pytest -q                                   # unit tests (respx, no network)
EXAMPLE_URL=http://localhost:18000 .venv/bin/python -m pytest -q tests/test_integration.py
SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_... MOCK_TARGET_URL=http://localhost:19090 \
  .venv/bin/uvicorn app.main:app --port 8000
```

Outside Docker the mock proxies stored in Spinneret (`mocktarget:9091`) are not resolvable from the
host: use the Compose service, or add `127.0.0.1 mocktarget` to `/etc/hosts` and publish port 9091.

---

## Writing a client without an SDK

The node API is Connect over HTTP with JSON: an ordinary `POST` with a JSON body to a path derived
from the service and method. Anything that can do HTTP can be a node. The full message reference is
in [Node API reference](./13-node-api.md); this section is the minimum that gets you running.

### The shape of every call

```
POST <base-url>/spinneret.v1.<Service>/<Method>
Content-Type: application/json
Accept: application/json
Connect-Protocol-Version: 1
Authorization: Bearer spn_xxx
X-Spinneret-Node: crawler-a-03
```

Success is `200` with the response message as JSON. Failure is a non-2xx status with a JSON body
carrying `code` and `message`, plus two response headers: `Spinneret-Reason` (the machine-readable
reason) and `Spinneret-Retry-After-Ms` (the retry hint, when there is one).

### A worked node in four calls

Acquire a lease:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: crawler-a-03' \
  -d '{"site":"example-site","client":"web","uri":"/api/v1/search","wait_ms":500}'
```

```json
{
  "lease": {
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
    "identity_type": "example_web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-18T09:31:05.412Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": {"sid": "…"},
    "cookie_header": "sid=…",
    "headers": {"User-Agent": "…"},
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": {"proxy_id": "pxy_0199c1d2f3a45b6c7d8e9f0a1b2c3d4e", "url": "http://user:pass@proxy.internal:8080", "kind": "datacenter", "region": "US"},
  "hints": {"renew_before_ms": 30000}
}
```

Send the request yourself through `proxy.url`, applying `credential.headers`,
`credential.cookie_header` (or `credential.cookies`) and `credential.query`. Then report what
happened, releasing the lease with the same call:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: crawler-a-03' \
  -d '{"reports":[{
        "report_id":"2f1c9a6e-9d2b-4a1f-8a0e-1c6d4f2b7e35",
        "lease_id":"lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "uri":"/api/v1/search",
        "method":"GET",
        "http_status":200,
        "markers":[],
        "latency_ms":412,
        "response_bytes":21840,
        "started_at":"2026-09-18T09:30:35.000Z",
        "finished_at":"2026-09-18T09:30:35.412Z",
        "release":true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

Read a config item:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/GetConfig" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"group":"crawler","key":"search.json"}'
```

Watch it for changes — the call blocks until something changes or `timeout_ms` elapses (0 means the
server default of 30 000 ms, the maximum is 60 000 ms), and returns only the changed items:

```bash
curl -sS --max-time 40 -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/WatchConfig" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"items":[{"group":"crawler","key":"search.json","version":7}],"timeout_ms":30000}'
```

An empty `{"items":[]}` means the wait expired with no change: send the same request again with the
versions you still hold. Set the HTTP read timeout to `timeout_ms` plus a few seconds — both SDKs
add 5 s.

### What the SDKs do that you must now do yourself

| Behaviour | What it means for a hand-written client |
| --- | --- |
| **Retry on the retry hint** | On `resource_exhausted`/`no_identity_available` and `unavailable` — including `unavailable`/`overloaded`, which is the server shedding an acquire before it reached Redis and is therefore always safe to retry — wait `Spinneret-Retry-After-Ms` and retry. Never retry `circuit_open` or `site_paused` — they are decisions, not transient failures; pause the endpoint group or site instead. Never repeat `Acquire` after an ambiguous failure (read timeout, connection reset): it is not idempotent and you would leak an identity for the length of a lease. Retrying is safe only when the request provably never left your process, or when the server itself answered `unavailable`. |
| **Lease renewal** | A lease expires at `lease.expires_at`. If your request can outlive it, call `LeaseService/Renew` with `{"lease_id": …, "extend_ms": 0}` when fewer than `hints.renew_before_ms` milliseconds remain. Renewal is bounded by the lifetime cap of the rotation policy: `lease_lifetime_exceeded` means acquire a new lease. |
| **Release with the last report** | Do not call `Release` after every request. Hold the last report back and send it with `"release": true`; call `LeaseService/Release` only when you have nothing to report. An unreleased lease is not lost — it expires — but the identity stays busy until it does. |
| **Report batching** | One `Report` call takes 1..500 reports. Buffer them and flush on a short interval (200 ms) or a batch size (100), from a background worker, with a bounded queue so that a slow server cannot grow your memory. Drop the oldest reports when the queue is full, and count the drops. |
| **Report idempotency** | Set `report_id` to a UUID you generate. Retrying a batch is then free: duplicates come back in `duplicated`, not as double counts. |
| **Config watch reconnection** | The watch loop is: send `WatchConfig` with the versions you hold → apply the returned items → send again immediately. On failure, back off (1 s doubling to 30 s with jitter) and reconnect; do not hot-loop. Keep serving the last known values throughout. |
| **Snapshot fallback** | Write each fetched non-secret item to a local file so a node can start while the control plane is down. Never persist an item whose `has_secret_refs` is true, or whose content still contains `${secret:`. |
| **URI hygiene in reports** | Send the request **path** only: strip the query string and fragment, cap at 2048 characters. Endpoint groups match on the path, and query parameters often carry signed credential values you do not want in request analytics. |
| **Error classification** | Reports of failed requests carry `error_kind`: one of `timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other`. Map your language's network exceptions onto those; the signal policy depends on them. A `407` response is a response — report `http_status: 407` with `error_kind: "proxy_auth"`. |
| **Report timestamps** | `started_at` and `finished_at` are required, RFC 3339 UTC. If you only measured a latency, compute `started_at = finished_at - latency_ms`. |

Two more rules that are not the SDK's doing but are just as load-bearing: never log the proxy URL
(it contains credentials) or the credential itself, and keep outcome classification on the server.
Report `markers` for what you saw — `captcha_page`, `empty_list`, `login_redirect` — and let the
signal policy decide what they mean.

---

## Next

- [Node API reference](./13-node-api.md) — every message, field and error reason the SDKs wrap.
- [Configuration center](./09-config-center.md) — how config items, versions and the watch protocol work on the server side.
- [Secret vault](./10-secrets.md) — what `${secret:...}` resolves to, and how a node is allowed to read one.
- [Policies](./08-policies.md) — where the markers and reports your node sends actually get interpreted.
- [Tenants, users and tokens](./11-access-control.md) — the scopes a node token needs.
- [Troubleshooting](./18-troubleshooting.md) — symptom → cause → fix, including the full error-reason table.
