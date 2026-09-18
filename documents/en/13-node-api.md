# Node API reference

**The complete reference for every RPC a crawler node calls: transports, authentication, request and response fields, real examples, the error model, retry rules and compatibility promises.**

[中文](../zh/13-node-api.md)

---

## Contents

- [Transports and URLs](#transports-and-urls)
- [Authentication](#authentication)
- [JSON conventions](#json-conventions)
- [Service map](#service-map)
- [LeaseService.Acquire](#leaseserviceacquire)
- [LeaseService.AcquireBatch](#leaseserviceacquirebatch)
- [LeaseService.Renew](#leaseservicerenew)
- [LeaseService.Release](#leaseservicerelease)
- [ReportService.Report](#reportservicereport)
- [ConfigService.GetConfig](#configservicegetconfig)
- [ConfigService.BatchGetConfig](#configservicebatchgetconfig)
- [ConfigService.WatchConfig](#configservicewatchconfig)
- [SecretService.GetSecret](#secretservicegetsecret)
- [The error model](#the-error-model)
- [Idempotency](#idempotency)
- [The recommended client loop](#the-recommended-client-loop)
- [Limits and rate limits](#limits-and-rate-limits)
- [Versioning and compatibility](#versioning-and-compatibility)

---

## Transports and URLs

The API is defined in `proto/spinneret/v1/*.proto` (package `spinneret.v1`) and served with
[Connect](https://connectrpc.com). Every RPC is a unary `POST` to

```text
POST <base-url>/spinneret.v1.<Service>/<Method>
```

There is no `/api` prefix and no path versioning: the version is in the package name. The base URL
is whatever the server (or the reverse proxy in front of it) listens on — with the Compose stack
that is `http://localhost:8080` by default (`SPINNERET_HTTP_ADDR` defaults to `:8080`).

Four wire protocols reach the same handlers:

| Protocol | `Content-Type` | Notes |
| --- | --- | --- |
| Plain HTTP + JSON | `application/json` | A bare `POST` with a JSON body. No client library needed. |
| Connect | `application/json` plus `Connect-Protocol-Version: 1` | What the SDKs send by default. |
| gRPC | `application/grpc` | Requires HTTP/2. The server speaks h2c on a plaintext listener and HTTP/2 when `SPINNERET_TLS_CERT_FILE` is set. |
| gRPC-Web | `application/grpc-web` | For browsers; nodes have no reason to use it. |

A plain JSON `POST` without the `Connect-Protocol-Version` header is accepted. Adding the header
makes the server answer in the Connect unary format in every case, which is what the SDKs rely on.

**Note.** With gRPC, `Spinneret-Reason` and `Spinneret-Retry-After-Ms` arrive as response
*trailers* rather than headers. With Connect and plain JSON they are ordinary response headers.

### Request headers

| Header | Required | Meaning |
| --- | --- | --- |
| `Authorization: Bearer spn_…` | yes | The API token. |
| `Content-Type: application/json` | yes | For the JSON transports. |
| `Connect-Protocol-Version: 1` | no | Selects the Connect unary protocol. |
| `X-Spinneret-Node` | no | Node instance name, used for per-node statistics. Sanitized to at most 128 characters of `A-Za-z0-9._:@/-`; anything else becomes `_`. |
| `Connect-Timeout-Ms` | no | Client deadline propagated to the handler. |

`X-Spinneret-Tenant` and `X-Spinneret-CSRF` are console headers. Nodes never send them: the tenant
and the namespace come from the token.

### Server-side timeouts

| Bound | Value |
| --- | --- |
| Default per-request deadline | 60 s |
| `ConfigService/WatchConfig` deadline | the requested `timeout_ms` (`0` means the 30 s default), capped at 60 s, plus 10 s of slack |
| Maximum request body on node services | 8 MiB |
| Read header timeout / idle timeout | 10 s / 120 s |

Responses larger than 4096 bytes are compressed when the client advertises a compression it
understands.

---

## Authentication

Every node call authenticates with an API token in the `Authorization` header:

```http
Authorization: Bearer spn_3xAmpL3...
```

A plaintext token is `spn_` followed by 43 base62 characters (47 characters in total). It is shown
once, when the token is created, and never again — the server stores only its SHA-256 digest. See
[Tenants, users and tokens](./11-access-control.md) for how tokens are created and revoked and
[CLI reference](./15-cli.md) for `spnr token`.

The token carries the namespace. A node never sends a namespace: `AcquireRequest` has no namespace
field at all, and the config RPCs accept an optional `namespace` only so that a client can assert
which namespace it thinks it is talking to — when set it must equal the token namespace.

### Scopes

Each RPC requires a scope on the token:

| RPC | Required scope | Argument |
| --- | --- | --- |
| `LeaseService/*` | `lease:acquire` | Optional site name (`lease:acquire:example-site`). Without an argument the scope covers every site of the namespace. |
| `ReportService/Report` | `report:write` | Optional site name. The batch is admitted when the token has `report:write` anywhere in the namespace; each report is then checked against the site of its lease and rejected individually with `scope_missing`. |
| `ConfigService/*` | `config:read` | Optional glob over the config group (`config:read:crawler*`). |
| `SecretService/GetSecret` | `secret:read` | **Required** glob over `"<namespace>/<path>"`, e.g. `secret:read:prod/signing/*`. |

Resolving `${secret:...}` references inside a config item additionally requires a `secret:read`
scope matching the referenced secret; without it `GetConfig` fails with `permission_denied` and the
attempt is audited.

A token may also carry an IP allowlist (IPs or CIDR prefixes) and a per-second rate limit. See
[Limits and rate limits](#limits-and-rate-limits).

---

## JSON conventions

These hold for every request and response on this page.

- **Field names are snake_case**, exactly as written in the `.proto` files: `lease_id`,
  `renew_before_ms`, `http_status`. The server registers a JSON codec with `UseProtoNames`, so it
  never emits `leaseId`.
- **Zero values are always emitted.** Responses contain `""`, `0`, `false`, `[]` and `{}` rather
  than omitting the field. Unset *messages* are `null` — `"proxy": null` when no proxy was
  assigned, `"expires_at": null` for a secret that does not expire.
- **Unknown request fields are ignored** (`DiscardUnknown`), so a newer client can talk to an older
  server without failing.
- **Timestamps** are RFC 3339 strings in UTC: `"2026-09-16T08:30:11.120Z"`.
- **Durations** are integer milliseconds in fields ending in `_ms`. There are no duration strings
  in the node API.
- **Integers**: the node API is `int32` throughout, so counts and millisecond fields are plain JSON
  numbers. The single exception is `Report.response_bytes`, an `int64` request field that may be
  sent either as `48213` or as `"48213"`.
- **IDs** are `<prefix>_<32 hex>` (`idt_…`, `pxy_…`, `sec_…`). Lease IDs are opaque; their internal
  shape is `lse_<32 hex>_<site key base36>_<shard, 2 hex>` and a client must not parse them.

---

## Service map

| Service | Procedures | Scope |
| --- | --- | --- |
| `LeaseService` | `Acquire`, `AcquireBatch`, `Renew`, `Release` | `lease:acquire` |
| `ReportService` | `Report` | `report:write` |
| `ConfigService` | `GetConfig`, `BatchGetConfig`, `WatchConfig` | `config:read` |
| `SecretService` | `GetSecret` | `secret:read` |

There is no separate `ReportBatch` procedure: `ReportService/Report` *is* the batch call — it takes
1 to 500 reports per request.

The examples below assume:

```bash
export SPINNERET_URL=http://localhost:8080
export SPINNERET_TOKEN=spn_...
export SPINNERET_NODE=crawler-01
```

---

## LeaseService.Acquire

Leases one identity — with a rendered credential and, when the rotation policy says so, a proxy —
for one request against a site endpoint group. This is the call on the hot path: it is what turns
"I want to send a request" into "here is who to send it as, through what, and for how long".

Lease operations are **not** written to the audit log; they are counted in acquire and lease
statistics instead.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `site` | string | yes | Site name inside the token namespace. | 1–64 characters |
| `client` | string | yes | Client type declared by the site, e.g. `web`, `mobile`, `partner`. | 1–32 characters |
| `uri` | string | no | Request path (optionally with a query) or absolute URL, used to match the endpoint group. Ignored when `endpoint_group` is set. | ≤ 2048 bytes (a fixed server limit, the same for every site) |
| `endpoint_group` | string | no | Explicit endpoint group name; takes precedence over `uri`. | ≤ 64 characters |
| `session_key` | string | no | Sticky-session key. When the rotation policy enables sticky sessions, requests with the same key reuse the same identity while it stays usable. | ≤ 256 characters |
| `wait_ms` | int32 | no | How long the server may wait for an identity to become available before failing. `0` fails immediately. | 0–5000 |

When both `uri` and `endpoint_group` are empty the `_default` endpoint group is used. A `uri` that
matches no URI rule of the site falls back to `_default` as well; only an explicitly named
`endpoint_group` that does not exist fails, with `endpoint_group_unknown`.

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `lease` | object | Lease metadata (below). |
| `credential` | object | The rendered credential (below). |
| `proxy` | object \| null | The assigned proxy, or `null` when the rotation policy assigns none. |
| `hints` | object | Client-side hints. |

`lease`:

| Field | Type | Meaning |
| --- | --- | --- |
| `lease_id` | string | Opaque lease ID. Send it with every report. |
| `identity_id` | string | ID of the leased identity (`idt_…`). |
| `identity_type` | string | Name of the identity type. |
| `endpoint_group` | string | Endpoint group the lease was issued for. |
| `expires_at` | timestamp | When the lease expires unless renewed. |
| `sticky` | bool | True when the identity was reused through `session_key`. |
| `probe` | bool | True when the lease is a probe — a half-open breaker probe or a pending identity being validated. Its reports decide a state transition, so report it accurately and do not drop it. |

`credential` is the delivery rendering of the identity payload, shaped by the `deliver` section of
its identity type. Unused segments are empty maps, an empty string, or `null`:

| Field | Type | Meaning |
| --- | --- | --- |
| `cookies` | map<string,string> | Cookies as name → value. |
| `cookie_header` | string | The same cookies rendered as a `Cookie` header value (`k1=v1; k2=v2`). |
| `headers` | map<string,string> | Request headers to add. |
| `query` | map<string,string> | Query parameters to add. |
| `json` | any \| null | Arbitrary JSON value, for example a request-body fragment. |
| `values` | object \| null | Free-form typed values: device parameters, signing keys, and similar. |

`proxy`:

| Field | Type | Meaning |
| --- | --- | --- |
| `proxy_id` | string | Proxy ID (`pxy_…`). |
| `url` | string | Full proxy URL **including credentials**, e.g. `http://user:pass@host:port`. Never log it. |
| `kind` | string | `datacenter`, `residential`, `mobile` or `tunnel`. |
| `region` | string | Region, usually an ISO country code; empty when unknown. |

`hints`:

| Field | Type | Meaning |
| --- | --- | --- |
| `renew_before_ms` | int32 | Renew the lease when fewer than this many milliseconds remain before `expires_at`. It is a quarter of the lease TTL. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "site": "example-site",
    "client": "web",
    "uri": "/search?q=shoes",
    "session_key": "",
    "wait_ms": 500
  }'
```

```json
{
  "lease": {
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
    "identity_type": "web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-16T08:31:11.120Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": {"sid": "a1b2c3d4"},
    "cookie_header": "sid=a1b2c3d4",
    "headers": {"User-Agent": "example-client/1.0", "Accept-Language": "en-US"},
    "query": {},
    "json": null,
    "values": {"device_id": "0f9c1a5e", "app_version": "12.4.0"}
  },
  "proxy": {
    "proxy_id": "pxy_0199c1d2f3a45b6c7d8e9f0a1b2c3d4e",
    "url": "http://user:pass@proxy.internal:8000",
    "kind": "residential",
    "region": "US"
  },
  "hints": {
    "renew_before_ms": 15000
  }
}
```

### Errors

| Connect code | Reason | What it means | What the client should do |
| --- | --- | --- | --- |
| `invalid_argument` | `site_unknown` | `site` is empty or not a site of the namespace. | Fix the configuration. Do not retry. |
| `invalid_argument` | `client_unknown` | The site does not declare that client. | Fix the configuration. Do not retry. |
| `invalid_argument` | `endpoint_group_unknown` | The named endpoint group does not exist for `site/client`. | Fix the configuration. Do not retry. |
| `invalid_argument` | `uri_invalid` | `uri` is empty, is neither a path starting with `/` nor an absolute `http(s)` URL, is longer than 2048 bytes, or contains control/whitespace characters or invalid UTF-8. | Fix the URI. Do not retry. |
| `invalid_argument` | `invalid_argument` | A field violates its validation rule (`wait_ms` out of range, `site` too long, …). | Fix the request. Do not retry. |
| `resource_exhausted` | `no_identity_available` | No usable identity within `wait_ms`: all cooling down, banned, quarantined or already leased. | Wait `Spinneret-Retry-After-Ms` (clamped to 50 ms–60 s), then retry. |
| `resource_exhausted` | `no_proxy_available` | An identity was available but no proxy matched the rotation policy. | Wait the retry hint, then retry. Check the proxy pool if it persists. |
| `unavailable` | `circuit_open` | The endpoint group's circuit breaker is open. | Stop sending to this endpoint group until the hint expires. Do not hammer. |
| `unavailable` | `site_paused` | An operator paused the site. The retry hint is 30000 ms. | Pause the whole site. |
| `unavailable` | `overloaded` | The server was at its acquire concurrency limit and shed the call before attempting it. No Redis command was issued, so nothing was consumed. | Wait `Spinneret-Retry-After-Ms` (jittered into 100–200 ms), then retry. Always safe to retry. |
| `unauthenticated` | `token_invalid` / `token_expired` / `token_revoked` | The token is not usable. | Stop. A human must fix the token. |
| `permission_denied` | `scope_missing` | The token has no `lease:acquire` for this site. | Stop. Fix the token scopes. |
| `permission_denied` | `ip_not_allowed` | The node's address is not in the token IP allowlist. | Stop. Fix the allowlist. |
| `resource_exhausted` | `rate_limited` | The token's per-second rate limit was exceeded. | Wait the retry hint and slow down. |

Acquire is **not idempotent**: a retried Acquire issues a second lease. The Go SDK only retries it
when the failure provably happened before the request was sent (dial and DNS errors) or when the
server itself answered `unavailable`.

**`overloaded` and `no_identity_available` mean different things and are now distinguishable.**
`no_identity_available` keeps its exact previous meaning: the identity pool was consulted and had
nothing usable, so the fix is more identities or a wider rotation policy. `overloaded` means the
server declined to consult the pool at all, because it already had as many acquire scripts in flight
at Redis as its budget allows; the fix is capacity or less offered load. A shed issues zero Redis
commands, which is why it is `unavailable` (both SDKs retry it automatically) and why retrying is
always safe. See [Performance and tuning](./17-performance.md) for the budget and the
`SPINNERET_ACQUIRE_*` levers.

---

## LeaseService.AcquireBatch

Leases up to `count` distinct identities in one round trip. Use it when a worker pool wants several
identities at once; it saves the per-call overhead of `count` Acquire calls.

### Request

Same fields as `Acquire`, plus:

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `count` | int32 | yes | Number of distinct identities requested. | 1–50 |

`session_key` only has an effect when `count` is 1; stickiness cannot apply to a set of distinct
identities. `wait_ms` bounds the wait for the *first* lease.

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `leases` | array of AcquireResponse | The leases that could be issued, each with its own credential, proxy and hints. May be shorter than `count`. |
| `requested` | int32 | The `count` that was asked for, echoed back. |

Partial success is normal: when fewer identities are available the call returns what it could
issue. An error is returned only when *none* could be issued.

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/AcquireBatch" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "site": "example-site",
    "client": "web",
    "endpoint_group": "detail",
    "count": 3,
    "wait_ms": 0
  }'
```

```json
{
  "leases": [
    {
      "lease": {
        "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
        "identity_type": "web_cookie",
        "endpoint_group": "detail",
        "expires_at": "2026-09-16T08:31:11.120Z",
        "sticky": false,
        "probe": false
      },
      "credential": {
        "cookies": {"sid": "a1b2c3d4"},
        "cookie_header": "sid=a1b2c3d4",
        "headers": {},
        "query": {},
        "json": null,
        "values": null
      },
      "proxy": null,
      "hints": {"renew_before_ms": 15000}
    },
    {
      "lease": {
        "lease_id": "lse_0199c1f4b1c28d4e9f60718293a4b5c6_1k3_12",
        "identity_id": "idt_0199c1e8c5528b3d0e1f4a6b7c8d9e0f",
        "identity_type": "web_cookie",
        "endpoint_group": "detail",
        "expires_at": "2026-09-16T08:31:11.121Z",
        "sticky": false,
        "probe": false
      },
      "credential": {
        "cookies": {"sid": "e5f6a7b8"},
        "cookie_header": "sid=e5f6a7b8",
        "headers": {},
        "query": {},
        "json": null,
        "values": null
      },
      "proxy": null,
      "hints": {"renew_before_ms": 15000}
    }
  ],
  "requested": 3
}
```

Two leases were issued out of three requested; the client uses the two it got.

### Errors

The same table as [Acquire](#leaseserviceacquire), plus `invalid_argument` / `invalid_argument`
when `count` is outside 1–50. Like Acquire, it is not idempotent.

---

## LeaseService.Renew

Extends an active lease. Call it when fewer than `hints.renew_before_ms` milliseconds remain before
`expires_at` and the node still needs the identity — a long paginated crawl, a slow target, a
retry after a timeout.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `lease_id` | string | yes | The lease to renew. | 1–128 characters |
| `extend_ms` | int32 | no | Extension counted from now. `0` uses the lease TTL of the rotation policy. | 0–1800000 (30 min) |

The new expiry never exceeds the **lease lifetime cap** of the rotation policy. When the cap is
reached the call fails with `lease_lifetime_exceeded` — the node must acquire a new lease rather
than keep the identity forever.

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `expires_at` | timestamp | The new expiry of the lease. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Renew" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "extend_ms": 60000
  }'
```

```json
{"expires_at": "2026-09-16T08:32:11.480Z"}
```

### Errors

| Connect code | Reason | What the client should do |
| --- | --- | --- |
| `failed_precondition` | `lease_released` | The lease was already released. Acquire a new one. |
| `failed_precondition` | `lease_expired` | The lease expired before the renew arrived. Acquire a new one; renew earlier next time. |
| `failed_precondition` | `lease_lifetime_exceeded` | The rotation policy's lifetime cap was reached. Acquire a new lease. |
| `invalid_argument` | `invalid_argument` | `extend_ms` is outside 0–1800000, or `lease_id` is malformed. |
| `not_found` | `lease_unknown` | The lease does not exist, belongs to another namespace, or the token does not hold `lease:acquire` for the lease's site. Acquire a new lease. |

**Renew and Release mask authorization failures on purpose.** A token without `lease:acquire` for
the lease's site is answered `not_found` / `lease_unknown`, not `scope_missing`, so that lease IDs
of other namespaces and sites are not revealed. Neither RPC ever returns `scope_missing`.

Renew is idempotent in the sense that retrying it is harmless: the worst case is a lease extended
a little further than intended.

---

## LeaseService.Release

Ends a lease early and returns the identity to the pool.

**Releasing is usually unnecessary.** The last report of a lease can set `"release": true`, which
releases it as part of the report and saves a round trip. Call `Release` only when the node
acquired a lease and then made no request at all.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `lease_id` | string | yes | The lease to release. | 1–128 characters |

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `released` | bool | `true` when the lease was active and is now released; `false` when it had already been released or had expired. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Release" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07"}'
```

```json
{"released": true}
```

### Errors

The call is **idempotent**: releasing a lease whose record still exists but has already ended
returns `released: false` instead of an error. Once the lease record itself has aged out, Release
answers `not_found` / `lease_unknown` — as it does for a malformed lease ID, a lease of another
namespace, and a lease on a site the token has no `lease:acquire` for (the same deliberate masking
described under Renew: Release never returns `scope_missing`). It can still fail with the
authentication errors of the tables above. A client should never fail a crawl because Release
failed — log it and move on.

---

## ReportService.Report

Ingests the observed results of requests made with leases. This is the other half of the hot path:
Spinneret's cooldowns, bans, health scores and circuit breakers are all driven by what nodes report
here.

**Nodes report facts, not decisions.** Classification, blame and the resulting actions are decided
by the server-side signal and action policies. `outcome_hint` is a suggestion the server uses only
when the signal policy sets `trust_outcome_hint` and the value is a known outcome.

One call carries 1 to 500 reports. Reports are validated **individually**: an invalid report is
listed in `rejected` while the rest of the batch is accepted.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `reports` | array of Report | yes | The reports to ingest. | 1–500 items |

Each `Report`:

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `report_id` | string | yes | Client-generated idempotency key. A UUID is recommended. | 1–64 characters of `[A-Za-z0-9_.:-]` |
| `lease_id` | string | yes | The lease the request was made with. | 1–128 characters |
| `uri` | string | yes | The requested path, optionally with a query. Strip the query when it carries credential values. | 1–2048 characters |
| `method` | string | no | HTTP method, e.g. `GET`. | ≤ 16 characters |
| `http_status` | int32 | no | Response status code. `0` means no response was received. | 0–999 |
| `business_code` | string | no | Business status code read out of the response body. | ≤ 64 characters |
| `error_kind` | string | no | Node-side transport error. Empty when the request completed. | one of `""`, `timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other` |
| `markers` | array of string | no | Response features the node recognized, e.g. `captcha_page`, `login_redirect`, `empty_list`. | ≤ 32 items, each 1–64 characters |
| `outcome_hint` | string | no | Outcome proposed by the node. | ≤ 32 characters |
| `latency_ms` | int32 | no | Request latency. | ≥ 0 |
| `response_bytes` | int64 | no | Response body size. Accepts `48213` or `"48213"`. | ≥ 0 |
| `started_at` | timestamp | **yes** | When the request started. | required |
| `finished_at` | timestamp | **yes** | When the request finished. | required, must not be before `started_at` |
| `release` | bool | no | Release the lease after this report is processed. | |

The known outcomes a server can classify to, and the values `outcome_hint` may usefully carry, are:
`success`, `empty`, `rate_limited`, `captcha`, `auth_invalid`, `forbidden`, `banned`,
`proxy_error`, `network_error`, `target_error`, `client_error`, `unknown`.

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `accepted` | int32 | Reports accepted for processing. |
| `duplicated` | int32 | Reports ignored because their `report_id` was already ingested. |
| `rejected` | array of RejectedReport | Reports that were not accepted. Always present, possibly empty. |

`RejectedReport`:

| Field | Type | Meaning |
| --- | --- | --- |
| `report_id` | string | The `report_id` as sent — possibly empty or invalid. |
| `reason` | string | `invalid_argument`, `lease_unknown` or `scope_missing`. |
| `message` | string | Human-readable detail. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "reports": [
      {
        "report_id": "0f0bb4b1-6c2e-4a35-9a6e-2f8c5d31a7c1",
        "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "uri": "/search",
        "method": "GET",
        "http_status": 200,
        "business_code": "0",
        "error_kind": "",
        "markers": [],
        "outcome_hint": "success",
        "latency_ms": 412,
        "response_bytes": 48213,
        "started_at": "2026-09-16T08:30:11.120Z",
        "finished_at": "2026-09-16T08:30:11.532Z",
        "release": true
      },
      {
        "report_id": "1a7f2c94-8d31-4f0a-bb55-6d2e19c47f80",
        "lease_id": "lse_0199c1f4b1c28d4e9f60718293a4b5c6_1k3_12",
        "uri": "/detail/1024",
        "method": "GET",
        "http_status": 0,
        "error_kind": "timeout",
        "markers": [],
        "latency_ms": 10000,
        "response_bytes": 0,
        "started_at": "2026-09-16T08:30:11.100Z",
        "finished_at": "2026-09-16T08:30:21.100Z",
        "release": true
      }
    ]
  }'
```

```json
{
  "accepted": 2,
  "duplicated": 0,
  "rejected": []
}
```

A batch with one bad lease comes back like this — the good reports still went through:

```json
{
  "accepted": 1,
  "duplicated": 0,
  "rejected": [
    {
      "report_id": "1a7f2c94-8d31-4f0a-bb55-6d2e19c47f80",
      "reason": "lease_unknown",
      "message": "lease is unknown or expired"
    }
  ]
}
```

### Per-report rejection reasons

| `reason` | Cause | What the client should do |
| --- | --- | --- |
| `invalid_argument` | A field violates its rule: bad `report_id` syntax, missing `started_at`/`finished_at`, `finished_at` before `started_at`, a marker longer than 64 characters, an unknown `error_kind`. | Fix the report. Never retry it unchanged. |
| `lease_unknown` | The lease ID is malformed, belongs to another namespace, or the lease is gone beyond the retention window. | Drop the report. The work it described is already lost to the system. |
| `scope_missing` | The token has no `report:write` for the site of that lease. | Fix the token scopes; keep the report if you can retry after. |

### Call-level errors

| Connect code | Reason | Meaning |
| --- | --- | --- |
| `invalid_argument` | `invalid_argument` | Zero reports, or more than 500. |
| `permission_denied` | `scope_missing` | The token has no `report:write` anywhere in the namespace. |
| `unauthenticated` | `token_invalid` / `token_expired` / `token_revoked` | |
| `resource_exhausted` | `rate_limited` | The token rate limit was exceeded. |
| `unavailable` | `internal` | The hot state is unreachable. The retry hint is 1000 ms. Retry the whole batch — accepted reports will come back as `duplicated`. |

---

## ConfigService.GetConfig

Returns one published configuration item. Only the **current published version** is served: drafts
are invisible to nodes. `${secret:...}` references in the content are resolved before delivery,
which is why a token that reads such an item also needs the matching `secret:read` scope.

See [Configuration center](./09-config-center.md) for how items, groups, versions and publishing
work.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `namespace` | string | no | When set it must equal the token namespace. | ≤ 64 characters |
| `group` | string | yes | Group name, e.g. `crawler` or the reserved `_runtime`. | 1–128 characters |
| `key` | string | yes | Item key, e.g. `search.json`. | 1–256 characters |

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `item` | ConfigItem | The published item. |

`ConfigItem`:

| Field | Type | Meaning |
| --- | --- | --- |
| `namespace` | string | Namespace name. |
| `group` | string | Group name. |
| `key` | string | Item key. |
| `format` | string | `json`, `yaml` or `text`. |
| `version` | int32 | Published version. It increases on every publish *and every rollback* and never repeats for a namespace/group/key: a new item starts at 1, and an item re-created after deletion continues above the last version published before deletion. |
| `content` | string | The published content, with secret references resolved. |
| `updated_at` | timestamp | When the version was published. |
| `has_secret_refs` | bool | True when the content contains resolved secret references. **Do not persist such content in plain text** — not in a local snapshot, not in a log. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/GetConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"group": "crawler", "key": "search.json"}'
```

```json
{
  "item": {
    "namespace": "prod",
    "group": "crawler",
    "key": "search.json",
    "format": "json",
    "version": 7,
    "content": "{\"page_size\":20,\"max_pages\":50,\"concurrency\":8}",
    "updated_at": "2026-09-16T07:55:02.000Z",
    "has_secret_refs": false
  }
}
```

### The reserved `_runtime` group

`_runtime` is a read-only, system-maintained group with two items a node can read like any other
config item:

| Key | Content |
| --- | --- |
| `breakers` | Endpoint group circuit breaker states: `{"namespace": …, "version": …, "sites": {"<site>": {"paused": bool, "groups": {"<client>/<group>": {"state": …, "open_until": …, "manual": bool, "reason": …}}}}}`. Only non-closed breakers are listed. |
| `site_switches` | Site pause switches: `{"namespace": …, "version": …, "sites": {"<site>": {"paused": bool, "reason": …, "paused_at": …}}}`. |

A token reaches them only when its `config:read` group glob matches `_runtime` — an unrestricted
`config:read` does. Watching `_runtime/breakers` is how a node learns about a trip in under a
second without waiting for its next Acquire to fail.

### Errors

| Connect code | Reason | What the client should do |
| --- | --- | --- |
| `not_found` | `not_found` | The item does not exist or has no published version. Fall back to a default; do not retry in a tight loop. |
| `permission_denied` | `scope_missing` | `config:read` does not cover the group, `secret:read` does not cover a referenced secret, or `namespace` was set to something other than the token namespace. Stop; fix the token or the request. |
| `failed_precondition` | `failed_precondition` | The published content references a `${secret:...}` that does not exist, or secret resolution is not available on this instance. Fix the item or the secret; do not retry. |
| `invalid_argument` | `invalid_argument` | A field is out of range. |
| `unavailable` | `internal` | The database is unreachable. Retry with backoff; keep serving the last known content. |

---

## ConfigService.BatchGetConfig

Fetches several published items in one round trip. Unknown or unpublished items are listed in
`missing` instead of failing the call.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `namespace` | string | no | When set it must equal the token namespace. | ≤ 64 characters |
| `items` | array of ConfigRef | yes | The items to fetch. Duplicates are served once. | 1–200 items |

`ConfigRef` is `{"group": string (1–128), "key": string (1–256)}`.

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `items` | array of ConfigItem | The published items, in request order. |
| `missing` | array of ConfigRef | Requested items that do not exist or have no published version. |

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/BatchGetConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "items": [
      {"group": "crawler", "key": "search.json"},
      {"group": "crawler", "key": "detail.json"},
      {"group": "_runtime", "key": "breakers"}
    ]
  }'
```

```json
{
  "items": [
    {
      "namespace": "prod",
      "group": "crawler",
      "key": "search.json",
      "format": "json",
      "version": 7,
      "content": "{\"page_size\":20,\"max_pages\":50,\"concurrency\":8}",
      "updated_at": "2026-09-16T07:55:02.000Z",
      "has_secret_refs": false
    },
    {
      "namespace": "prod",
      "group": "_runtime",
      "key": "breakers",
      "format": "json",
      "version": 12,
      "content": "{\"namespace\":\"prod\",\"version\":11,\"sites\":{\"example-site\":{\"paused\":false,\"groups\":{}}}}",
      "updated_at": "2026-09-16T08:30:00.000Z",
      "has_secret_refs": false
    }
  ],
  "missing": [
    {"group": "crawler", "key": "detail.json"}
  ]
}
```

### Errors

The whole request fails — no partial permission — when the token lacks `config:read` on **any**
requested group, or read access to any referenced secret. It also fails when the combined content
exceeds the server's node-read size budget (32 MiB by default). Otherwise the same table as
`GetConfig`, except that `not_found` never occurs: missing items go into `missing`.

---

## ConfigService.WatchConfig

The long poll nodes use to learn about configuration changes within a second, without polling in a
loop. It returns immediately with the items whose published version differs from the version the
caller says it holds; otherwise it blocks until such a change, the timeout, or the client
cancelling.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `namespace` | string | no | When set it must equal the token namespace. | ≤ 64 characters |
| `items` | array of WatchItem | yes | The items to watch with the version currently held. | 1–200 items |
| `timeout_ms` | int32 | no | How long the server may wait. `0` selects the server default of 30000. | 0–60000 |

`WatchItem`:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `group` | string | yes | Group name (1–128 characters). |
| `key` | string | yes | Item key (1–256 characters). |
| `version` | int32 | no | The version the caller holds. `0` means "I hold none", so the item is returned as soon as it has a published version. Must be ≥ 0. |

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `items` | array of ConfigItem | Items whose published version differs from the watched version. Empty when the poll timed out without a change. |

Semantics worth knowing:

- An item counts as changed when its published version **differs** from the held one. A rollback
  therefore also wakes the poll, because rollback publishes a new, higher version.
- Deleted and never-published items are never returned and do not end the poll.
- If the caller holds a *newer* version than this instance has cached (it read through another
  replica, or a bus event was lost), the item is re-read from the source of truth first. A node is
  never moved back to an older version and never sent the version it already holds.
- When the changed items exceed the response size budget a subset is returned; the rest come back
  on the next poll.

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/WatchConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "items": [
      {"group": "crawler", "key": "search.json", "version": 7},
      {"group": "_runtime", "key": "breakers", "version": 12}
    ],
    "timeout_ms": 30000
  }'
```

A change arrives:

```json
{
  "items": [
    {
      "namespace": "prod",
      "group": "crawler",
      "key": "search.json",
      "format": "json",
      "version": 8,
      "content": "{\"page_size\":20,\"max_pages\":80,\"concurrency\":8}",
      "updated_at": "2026-09-16T08:41:19.000Z",
      "has_secret_refs": false
    }
  ]
}
```

Nothing changed within the timeout:

```json
{"items": []}
```

The client loop is: hold a version per item, poll, apply whatever comes back, update the held
versions, poll again immediately. Set the HTTP read timeout to `timeout_ms` plus a few seconds of
grace — the SDKs use 5 s — because the server adds 10 s of slack to its own deadline.

### Errors

| Connect code | Reason | What the client should do |
| --- | --- | --- |
| `resource_exhausted` | `rate_limited` | Too many watchers are blocked on this instance (20000 by default, `SPINNERET_MAX_WATCHERS`). Back off and reconnect; consider fewer watchers per node. |
| `permission_denied` | `scope_missing` | `config:read` does not cover a watched group, `secret:read` does not cover a referenced secret, or `namespace` was set to something other than the token namespace. |
| `failed_precondition` | `failed_precondition` | A returned item references a `${secret:...}` that does not exist, or secret resolution is not available on this instance. Fix the item or the secret; do not retry. |
| `invalid_argument` | `invalid_argument` | Zero items, more than 200, `timeout_ms` above 60000, or a negative `version`. |
| `unavailable` | `internal` | Retry with backoff. |

A poll interrupted by a server shutdown ends with a cancellation, not an error worth alarming on:
reconnect.

---

## SecretService.GetSecret

Reads the plaintext of a secret in the token namespace. **Every read is written to the audit log**
with the version, the purpose (`api` for this RPC), the client IP and the result.

See [Secret vault](./10-secrets.md) for how secrets are stored and versioned.

### Request

| Field | Type | Required | Meaning | Limits |
| --- | --- | --- | --- | --- |
| `path` | string | yes | Secret path relative to the namespace, e.g. `signing/api_key`. | 1–256 characters matching `^[a-z0-9][a-z0-9_./-]*$` |
| `version` | int32 | no | Version to read. `0` reads the current version. | ≥ 0 |

### Response

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | The secret path. |
| `version` | int32 | The version that was read. |
| `value` | string | The plaintext. |
| `expires_at` | timestamp \| null | Expiry, or `null` when the secret does not expire. |

An **expired secret is still returned**, and the server logs a warning. Check `expires_at`
yourself if your node must refuse to use one.

### Example

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SecretService/GetSecret" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"path": "signing/api_key", "version": 0}'
```

```json
{
  "path": "signing/api_key",
  "version": 3,
  "value": "sk-live-7f3c9a21b845",
  "expires_at": null
}
```

### Errors

| Connect code | Reason | What the client should do |
| --- | --- | --- |
| `not_found` | `not_found` | The secret or that version does not exist. Do not retry. |
| `permission_denied` | `scope_missing` | The `secret:read` glob does not match `"<namespace>/<path>"`. The attempt is audited. Fix the token. |
| `permission_denied` | `permission_denied` | The caller is not an API token. `SecretService` is node-only; the console uses `SecretAdminService.RevealSecret`. |
| `invalid_argument` | `invalid_argument` | The path violates the pattern, or `version` is negative. |
| `unavailable` | `internal` | Retry with backoff. |

**Warning.** Cache secrets in memory only, for as short a time as the node can bear, and never
write them to disk or to a log line. Every read is audited, so a node that fetches a secret per
request will also flood the audit log.

---

## The error model

Errors use the Connect error format. The body is:

```json
{"code": "resource_exhausted", "message": "no identity available for example-site/web/search"}
```

and the machine-readable detail is in response headers, so a plain JSON client does not have to
decode the body:

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 1200
```

| Header | Meaning |
| --- | --- |
| `Spinneret-Reason` | The stable, snake_case reason. Present on every error produced by a handler. Errors raised while decoding the request — malformed JSON, or a body over the 8 MiB limit — carry only the Connect code, so a client must tolerate a missing reason and fall back to the code. |
| `Spinneret-Retry-After-Ms` | Suggested wait in milliseconds. Present only when the server has a useful number; absent means "no hint", not "retry immediately". |

Internal errors never leak a cause: the client sees `{"code":"internal","message":"internal error"}`
and the real error is in the server log.

### Connect code → HTTP status

| Connect code | HTTP |
| --- | --- |
| `invalid_argument` | 400 |
| `failed_precondition` | 400 |
| `unauthenticated` | 401 |
| `permission_denied` | 403 |
| `not_found` | 404 |
| `aborted` | 409 |
| `already_exists` | 409 |
| `resource_exhausted` | 429 |
| `canceled` | 499 |
| `internal` | 500 |
| `unavailable` | 503 |
| `deadline_exceeded` | 504 |

### Every reason

| Reason | Code | Retryable | Retry hint | Meaning and what to do |
| --- | --- | --- | --- | --- |
| `token_invalid` | `unauthenticated` | no | — | The token is malformed or unknown. Stop and fix the configuration. |
| `token_expired` | `unauthenticated` | no | — | The token's `expires_at` has passed. Issue a new token. |
| `token_revoked` | `unauthenticated` | no | — | The token was revoked in the console. Issue a new token. |
| `ip_not_allowed` | `permission_denied` | no | — | The node's address is outside the token IP allowlist. |
| `session_invalid` | `unauthenticated` | no | — | No credential at all was presented. For a node this means the `Authorization` header is missing. |
| `scope_missing` | `permission_denied` | no | — | The token's scopes do not grant the permission this RPC needs. Also appears per report inside a `Report` response. |
| `permission_denied` | `permission_denied` | no | — | The principal type is wrong for this RPC (for example a console user calling `SecretService`). |
| `site_unknown` | `invalid_argument` | no | — | `site` is empty or not a site of the namespace. |
| `client_unknown` | `invalid_argument` | no | — | The site does not declare that client. |
| `endpoint_group_unknown` | `invalid_argument` | no | — | The named endpoint group does not exist for `site/client`. |
| `uri_invalid` | `invalid_argument` | no | — | `uri` is empty, is neither a path starting with `/` nor an absolute `http(s)` URL, is longer than 2048 bytes, or contains control/whitespace characters or invalid UTF-8. |
| `invalid_argument` | `invalid_argument` | no | — | A field violates its validation rule. The message names the field. |
| `no_identity_available` | `resource_exhausted` | **yes** | 50 ms – 60 s | No usable identity within `wait_ms`. Wait the hint and retry. Persistent means the pool is too small or too much of it is cooling down. |
| `no_proxy_available` | `resource_exhausted` | **yes** | 50 ms – 60 s | No proxy matched the rotation policy. Wait the hint and retry. |
| `circuit_open` | `unavailable` | **yes, after the hint** | breaker's remaining open time | The endpoint group breaker is open. Stop sending to that endpoint group; do not retry before the hint. |
| `site_paused` | `unavailable` | **yes, after the hint** | 30000 ms | An operator paused the site. Pause the whole site. |
| `overloaded` | `unavailable` | **yes** | 100–200 ms (jittered) | The server is at its acquire concurrency limit and shed the call. No Redis command was issued, so retrying is always safe; the jitter keeps the shed population from returning in lockstep. This is *not* `no_identity_available`: the pool was never consulted. |
| `rebuilding` | `unavailable` | **yes** | 500 ms – 1 s | The hot state is being rebuilt. Node RPCs do not return it — an instance that is rebuilding fails its readiness check, so the load balancer takes it out. It reaches administrative callers such as `spnr rebuild`. Retry with backoff. |
| `lease_unknown` | `not_found` / per-report | no | — | The lease does not exist, belongs to another namespace, is past retention, or the token does not hold `lease:acquire` for the lease's site. Acquire a new lease; drop the report. |
| `lease_released` | `failed_precondition` | no | — | The lease was already released. Acquire a new one. |
| `lease_expired` | `failed_precondition` | no | — | The lease expired. Acquire a new one and renew earlier. |
| `lease_lifetime_exceeded` | `failed_precondition` | no | — | The rotation policy's lifetime cap was reached. Acquire a new lease. |
| `rate_limited` | `resource_exhausted` | **yes** | time until a token bucket refills | The API token's per-second limit, or the config watcher limit, was exceeded. Slow down. |
| `not_found` | `not_found` | no | — | The addressed resource does not exist. |
| `already_exists` | `already_exists` | no | — | Uniqueness violation. Administrative calls only. |
| `failed_precondition` | `failed_precondition` | no | — | The system is not in a state the operation requires. |
| `conflict` | `aborted` | maybe | — | A concurrent modification. Re-read and try again. Administrative calls only. |
| `internal` | `internal` or `unavailable` | **yes when `unavailable`** | sometimes | An unexpected failure, or a dependency (PostgreSQL, Valkey/Redis) is unreachable. Retry with backoff; check the server logs. |

`csrf_missing`, `login_throttled`, `query_too_large` and `query_timeout` also exist but only ever
reach the console, never a node.

### A rule of thumb for clients

1. `unauthenticated` or `permission_denied` → stop. Retrying cannot help; a human must act.
2. `invalid_argument` or `failed_precondition` → do not retry the same request. For lease
   preconditions, acquire a new lease.
3. `resource_exhausted` → wait `Spinneret-Retry-After-Ms` and retry.
4. `unavailable` → retry with jittered exponential backoff, **except** `circuit_open` and
   `site_paused`, where the right answer is to stop feeding that endpoint group or site until the
   hint elapses.
5. Transport failure with no response → retry only idempotent calls. `Acquire` and `AcquireBatch`
   are not idempotent; retry them only when the failure provably happened before the request was
   sent (DNS, dial refused).

The Go SDK's default policy is 2 retries, 100 ms initial backoff with equal jitter, 2 s maximum
per-delay, and it refuses to wait for a server hint longer than 5 s.

---

## Idempotency

| RPC | Idempotent | Why |
| --- | --- | --- |
| `Acquire` | **no** | A retry issues a second lease and consumes a second identity. |
| `AcquireBatch` | **no** | Same. |
| `Renew` | yes | Worst case the lease is extended a little further. |
| `Release` | yes | Releasing an ended lease returns `released: false`, not an error. |
| `Report` | **yes, by `report_id`** | See below. |
| `GetConfig`, `BatchGetConfig`, `WatchConfig` | yes | Reads. |
| `GetSecret` | yes | A read — but every call writes an audit entry, so retries are visible. |

### Report idempotency

Each report carries a client-generated `report_id`. The server remembers ingested IDs for the
dedup window — `SPINNERET_REPORT_DEDUP_TTL`, one hour by default — and counts a repeat as
`duplicated` instead of processing it twice. This is what makes a whole batch safe to retry after a
timeout or a 503: the reports that got through the first time come back as `duplicated`, the rest
are accepted.

Rules for clients:

- Generate the `report_id` **once**, when you build the report, and reuse it on every retry. A
  fresh UUID per attempt defeats the whole mechanism.
- Use a UUID or another value with real entropy. Do not use a counter that restarts when the node
  restarts.
- Do not retry a report rejected with `invalid_argument`: it will be rejected again.
- A report arriving long after its lease ended is still accepted at ingest, but only reports that
  are timely — the lease is active, or ended at most `SPINNERET_LATE_REPORT_WINDOW` ago (10 minutes
  by default) — keep the lease resolvable. Deliver reports promptly.

---

## The recommended client loop

The shape every SDK implements, and the shape to copy in a language that has no SDK:

```text
# once per process
config = GetConfig(group, key)            # or BatchGetConfig
start background: watch_loop(config)
start background: report_flusher()        # batches reports, flushes on size or interval

# per request
function fetch(uri):
    attempt = 0
    loop:
        try:
            grant = Acquire(site, client, uri, wait_ms = 500)
        catch error:
            switch reason(error):
                case "no_identity_available", "no_proxy_available", "rate_limited":
                    sleep(retry_after(error) or backoff(attempt)); attempt += 1; continue
                case "circuit_open", "site_paused":
                    pause_endpoint_group(retry_after(error)); return ENDPOINT_PAUSED
                case "token_invalid", "token_expired", "token_revoked",
                     "scope_missing", "ip_not_allowed":
                    fatal(error)                     # a human must fix this
                default:
                    if transport_failure(error) and happened_before_send(error):
                        sleep(backoff(attempt)); attempt += 1; continue
                    raise error

        report_id = new_uuid()
        started   = now()
        deadline_watcher:                            # only for long or paginated work
            if grant.lease.expires_at - now() < grant.hints.renew_before_ms:
                Renew(grant.lease.lease_id, extend_ms = 0)

        try:
            response = http_send(uri,
                                 headers  = grant.credential.headers,
                                 cookies  = grant.credential.cookies,
                                 query    = grant.credential.query,
                                 proxy    = grant.proxy?.url)
            markers = detect_markers(response)       # "captcha_page", "empty_list", ...
            enqueue_report({
                report_id:      report_id,
                lease_id:       grant.lease.lease_id,
                uri:            strip_query(uri),
                method:         "GET",
                http_status:    response.status,
                business_code:  read_business_code(response),
                markers:        markers,
                latency_ms:     now() - started,
                response_bytes: len(response.body),
                started_at:     started,
                finished_at:    now(),
                release:        true                 # last report releases the lease
            })
            return response
        catch transport_error as err:
            enqueue_report({
                report_id:   report_id,
                lease_id:    grant.lease.lease_id,
                uri:         strip_query(uri),
                method:      "GET",
                http_status: 0,
                error_kind:  classify(err),          # timeout | conn_reset | conn_refused
                                                     # | proxy_auth | tls | dns | other
                latency_ms:  now() - started,
                started_at:  started,
                finished_at: now(),
                release:     true
            })
            raise err

# background
function report_flusher():
    loop:
        batch = take_up_to(500, from = queue, or_after = 1s)
        if batch is empty: continue
        result = Report(batch)                        # retry the whole batch on unavailable
        for rejected in result.rejected:
            log(rejected.report_id, rejected.reason, rejected.message)

function watch_loop(held):
    loop:
        changed = WatchConfig(items = held.as_watch_items(), timeout_ms = 30000)
        for item in changed:
            apply(item)
            held[item.group, item.key] = item.version
```

Five things this loop gets right, and that a hand-written client usually gets wrong:

1. **One `report_id` per request, generated before the attempt**, reused on every delivery retry.
2. **Every lease is reported**, including the failures. A lease that is never reported teaches the
   server nothing and only ends when it expires.
3. **`release: true` on the last report** instead of a separate `Release` call.
4. **Retry hints are obeyed.** `Spinneret-Retry-After-Ms` is the server telling you when there will
   be something to hand out; ignoring it turns a cooldown into a stampede.
5. **`circuit_open` and `site_paused` stop the work**, they do not become a retry loop.

A `probe` lease deserves one more rule: report it, and report it accurately. A probe's report is
what closes a half-open breaker or activates a pending identity.

The Python and Go SDKs do all of this for you — see [SDKs and examples](./14-sdks.md).

---

## Limits and rate limits

| Limit | Value | Where it comes from |
| --- | --- | --- |
| `wait_ms` | 0–5000 | `AcquireRequest`, `AcquireBatchRequest` |
| `count` | 1–50 | `AcquireBatchRequest` |
| `extend_ms` | 0–1800000 (30 min) | `RenewRequest` |
| Reports per call | 1–500 | `ReportRequest` |
| Markers per report | ≤ 32, each 1–64 characters | `Report.markers` |
| `uri` in a report | 1–2048 characters | `Report.uri` |
| Config items per call | 1–200 | `BatchGetConfigRequest`, `WatchConfigRequest` |
| `timeout_ms` | 0–60000 (0 = 30000) | `WatchConfigRequest` |
| Blocked watchers per instance | 20000 | `SPINNERET_MAX_WATCHERS` |
| Combined config content per read | 32 MiB | server default |
| Request body on node services | 8 MiB | server |
| `X-Spinneret-Node` | ≤ 128 characters after sanitization | server |
| Report dedup window | 1 h | `SPINNERET_REPORT_DEDUP_TTL` |
| Late report window | 10 min | `SPINNERET_LATE_REPORT_WINDOW` |

### The token rate limit

Each API token may carry `rate_limit_rps` (0 = unlimited, maximum 1000000). It is a token bucket
with a capacity of one second of burst, applied **per server instance** — a token limited to 100
rps against three replicas can do 300 rps in the worst case. Exceeding it fails the call with
`resource_exhausted` / `rate_limited` and a `Spinneret-Retry-After-Ms` equal to the time until the
next token refills.

The limit counts *calls*, not leases or reports. Batching is therefore the cheapest way to stay
under it: one `Report` with 500 reports costs one call, and one `AcquireBatch` with 50 costs one.

---

## Versioning and compatibility

The wire contract is `proto/spinneret/v1/*.proto`. The comments in those files are normative: when
this page and a `.proto` comment disagree, the `.proto` is right.

**What will not change inside `spinneret.v1`:**

- A field is never removed, renumbered, or given a different type or meaning.
- An RPC is never removed or renamed, and its request and response message types stay the same.
- A reason string is never repurposed. An existing reason keeps its meaning.
- The JSON conventions above — snake_case names, emitted zero values, RFC 3339 timestamps,
  millisecond `_ms` integers — stay as they are.

`buf.yaml` configures `breaking: use: FILE`, so `buf breaking --against <ref>` checks a proto change
against a committed revision. Running it is a manual step: CI runs `buf lint` and `buf generate`
only, so a breaking proto change does not by itself fail the build.

**What may change, and what a client must tolerate:**

- **New fields** may be added to any request or response. Ignore fields you do not know; do not
  fail on them. The server already does the same for requests (`DiscardUnknown`).
- **New RPCs** may be added to an existing service.
- **New reason strings** may be added. Handle an unknown reason by falling back to the Connect code
  — that is exactly why the code and the reason are both sent.
- **New values** may appear in open string fields such as `proxy.kind` and `identity_type`.
- **Retry hints** are advisory and their values may change.
- **Defaults** behind the wire (lease TTL, watch timeout, dedup window) are operator-tunable and
  differ between deployments. Never hard-code them; read `hints.renew_before_ms` rather than
  assuming a TTL.

A genuinely incompatible change would be a new package, `spinneret.v2`, served alongside `v1`.

**Rolling upgrades.** During an upgrade a client may hit a new instance and an old one in the same
second. Because unknown request fields are discarded and new response fields are additive, both
directions work. The one thing to keep in mind is config versions: an instance that has not yet
seen a publish may still serve the previous version, and the next `WatchConfig` will correct it —
the server re-reads from the source of truth when a caller holds a newer version than it has
cached.

---

## Next

- [SDKs and examples](./14-sdks.md) — the Python and Go clients that implement this page for you.
- [Concepts](./04-concepts.md) — what a lease, a report, a signal and an action actually are.
- [Configuration center](./09-config-center.md) — the other side of `GetConfig` and `WatchConfig`.
- [Secret vault](./10-secrets.md) — how the value `GetSecret` returns is stored and rotated.
- [Tenants, users and tokens](./11-access-control.md) — creating a token with the right scopes.
- [Troubleshooting](./18-troubleshooting.md) — what to do when a reason keeps coming back.
