# API reference

[中文文档](api.zh-CN.md) · [Deployment guide](deployment.md) · [Operations runbook](operations.md)

The API is defined in [`proto/spinneret/v1`](../proto/spinneret/v1) and served with
[Connect](https://connectrpc.com): every RPC is `POST /spinneret.v1.<Service>/<Method>` and can be called
with plain HTTP + JSON, the Connect protocol, gRPC or gRPC-Web. Wire-level conventions (JSON mapping,
pagination, headers) are in [`proto/README.md`](../proto/README.md).

This document covers the four **node** services that crawler nodes call, and gives an overview of the admin
services the console uses.

- [Calling the API](#calling-the-api)
- [LeaseService](#leaseservice)
- [ReportService](#reportservice)
- [ConfigService](#configservice)
- [SecretService](#secretservice)
- [Errors](#errors)
- [Retry guidance](#retry-guidance)
- [Admin API](#admin-api)

---

## Calling the API

```http
POST /spinneret.v1.LeaseService/Acquire HTTP/1.1
Host: spinneret.internal:8080
Authorization: Bearer spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
X-Spinneret-Node: crawler-hk-03
Content-Type: application/json

{"site":"example","client":"web","uri":"/site/search?q=shoes","wait_ms":2000}
```

| Header | Required | Meaning |
| --- | --- | --- |
| `Authorization: Bearer spn_…` | yes | Node API token. It determines the namespace — node requests never name one |
| `Content-Type: application/json` | yes | Also accepted: `application/proto`, `application/connect+json`, `application/grpc` |
| `X-Spinneret-Node` | recommended | Node instance name (≤ 128 chars) used for per-node statistics; absent shows as `_` |

Conventions that matter for node clients:

- Field names are **snake_case**, exactly as in the `.proto` files.
- Zero values are always present in responses (`""`, `0`, `false`, `[]`, `{}`); an unset message is `null`.
- **Unknown request fields are ignored**, so a newer client can talk to an older server and vice versa.
- Timestamps are RFC 3339 UTC strings: `"2026-09-17T20:11:54.243Z"`.
- Durations in node APIs are integer **milliseconds** in `*_ms` fields.
- `int64` fields come back as JSON **strings** (proto3 JSON mapping); requests accept numbers or strings.
  In node APIs the only `int64` is `Report.response_bytes`.

Create a token with the `spnr` CLI (see [operations.md](operations.md#api-tokens-and-scopes)):

```bash
spnr token create --tenant default --namespace default --name crawler-hk \
  --scope lease:acquire:example --scope report:write:example --scope config:read:crawler \
  --expires 720h
```

---

## LeaseService

Requires the `lease:acquire[:<site>]` scope.

### Acquire

Leases one identity — and, if the rotation policy says so, a proxy — for the request you are about to make.

| Field | Type | Meaning |
| --- | --- | --- |
| `site` | string, 1–64 | Site name inside the token's namespace |
| `client` | string, 1–32 | Client type, e.g. `web`, `app` |
| `uri` | string, ≤ 2048 | Path, path + query, or absolute URL, used to match the endpoint group. Only the path is matched |
| `endpoint_group` | string, ≤ 64 | Explicit group; takes precedence over `uri` |
| `session_key` | string, ≤ 256 | Sticky-session key; reuses the same identity while the rotation policy's `sticky` is enabled |
| `wait_ms` | int32, 0–5000 | How long the server may wait for an identity to become available. `0` fails immediately |

```bash
curl -s http://localhost:8080/spinneret.v1.LeaseService/Acquire \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: crawler-hk-03" \
  -H 'Content-Type: application/json' \
  -d '{"site":"example","client":"web","uri":"/site/search?q=shoes","wait_ms":2000}'
```

```json
{
  "lease": {
    "lease_id": "lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
    "identity_id": "idt_01a0b0a4dc4774759bba124ee0e6be8f",
    "identity_type": "example_web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-17T20:12:54.398Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": { "csrftoken": "csrf-17", "sessionid": "example-17" },
    "cookie_header": "",
    "headers": { "User-Agent": "ExampleCrawler/17" },
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": {
    "proxy_id": "pxy_01a0b0a4dc4b7c5984d858b85e92f447",
    "url": "http://expx1:secret@mocktarget:9091",
    "kind": "datacenter",
    "region": ""
  },
  "hints": { "renew_before_ms": 15000 }
}
```

**`credential` always has the same six segments**, whatever the identity type is; merge the non-empty ones
into your request:

| Segment | Type | Use |
| --- | --- | --- |
| `cookies` | map | Cookie jar of the request |
| `cookie_header` | string | Ready-made `Cookie` header value (`k1=v1; k2=v2`); use it when `cookies` is empty |
| `headers` | map | Merge into request headers |
| `query` | map | Merge into query parameters — do not drop existing ones |
| `json` | any | Body fragment, or whatever the type defines; `null` when unused |
| `values` | object | Free-form typed values, e.g. a token your signing code needs |

`proxy` is `null` when the policy assigns none. `hints.renew_before_ms` is a quarter of the lease TTL:
renew when less than that remains. `lease.probe: true` means this lease decides a state transition (a
half-open breaker probe, or a `pending` identity being validated) — report it accurately and do not drop it.

The lease is what ties your request to the server's bookkeeping: **always report it**, successful or not.
An unreported lease expires and is counted as `abandoned`.

### AcquireBatch

Same fields plus `count` (1–50); `session_key` only applies when `count` is 1. Returns the leases that
could be issued (possibly fewer than requested) and the number requested. It fails only when none could be
issued.

```json
{ "leases": [ { "lease": {…}, "credential": {…}, "proxy": {…}, "hints": {…} } ], "requested": 10 }
```

### Renew

Extends an active lease. `extend_ms` is counted from now (`0` = the policy's lease TTL, max 1 800 000).
The new expiry never exceeds the policy's `max_lease_lifetime`; hitting that cap fails with
`lease_lifetime_exceeded`.

```bash
-d '{"lease_id":"lse_…","extend_ms":0}'
```

```json
{ "expires_at": "2026-09-17T20:14:54.398Z" }
```

### Release

Ends a lease early. Usually unnecessary — set `release: true` on the lease's last report instead, which
releases and reports in one call. The call is idempotent: `released` is `false` when the lease had already
ended.

```bash
-d '{"lease_id":"lse_…"}'
```

```json
{ "released": true }
```

---

## ReportService

Requires the `report:write[:<site>]` scope.

### Report

Ingests 1–500 reports. **Report facts, not verdicts**: the server's signal policy decides the outcome, so
your nodes do not need to be redeployed when detection changes.

| Field | Type | Notes |
| --- | --- | --- |
| `report_id` | string, 1–64 of `[A-Za-z0-9_.:-]` | Idempotency key; a UUID is ideal. Repeats inside `SPINNERET_REPORT_DEDUP_TTL` (1 h) count as `duplicated` |
| `lease_id` | string | The lease the request was made with |
| `uri` | string, 1–2048 | Requested path. Strip the query string: groups match on the path, and queries often carry signed values |
| `method` | string, ≤ 16 | `GET`, `POST`, … |
| `http_status` | int32, 0–999 | `0` means no response was received |
| `business_code` | string, ≤ 64 | Status code from the response body, when your target has one |
| `error_kind` | enum string | `""`, `timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other` |
| `markers` | up to 32 strings, 1–64 chars | Response features you recognized: `captcha_page`, `login_redirect`, `empty_list`, … |
| `outcome_hint` | string, ≤ 32 | Your proposed outcome; used only when the signal policy sets `trust_outcome_hint` |
| `latency_ms` | int32 ≥ 0 | Request latency |
| `response_bytes` | int64 ≥ 0 | Body size (JSON string or number) |
| `started_at` | timestamp | **Required** |
| `finished_at` | timestamp | **Required**, not before `started_at` |
| `release` | bool | Release the lease after this report is processed |

```bash
curl -s http://localhost:8080/spinneret.v1.ReportService/Report \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"reports":[{
        "report_id":"9f1c4c40-2f6a-4d6e-9c63-8f1b1d8f0a11",
        "lease_id":"lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
        "uri":"/site/search","method":"GET","http_status":200,
        "business_code":"","error_kind":"","markers":[],
        "latency_ms":143,"response_bytes":48213,
        "started_at":"2026-09-17T20:11:54.100Z",
        "finished_at":"2026-09-17T20:11:54.243Z",
        "release":true}]}'
```

```json
{ "accepted": 1, "duplicated": 0, "rejected": [] }
```

Reports are validated **individually**, so one bad report never rejects the batch:

```json
{
  "accepted": 0,
  "duplicated": 0,
  "rejected": [
    { "report_id": "x1", "reason": "lease_unknown", "message": "lease is unknown or expired" }
  ]
}
```

Rejection reasons: `invalid_argument`, `lease_unknown` (expired, released or never existed) and
`scope_missing`. The call itself returns `200`; check `rejected` rather than the HTTP status.

Ingest is asynchronous: an accepted report is queued to a Redis stream shard and processed by a worker
within milliseconds. Reports arriving later than `SPINNERET_LATE_REPORT_WINDOW` (10 minutes) are still
recorded but no longer change identity state.

Batching advice: both SDKs queue reports and flush every 200 ms or every 100 reports. If you build your own
client, batch similarly — one RPC per request works but wastes round trips.

---

## ConfigService

Requires the `config:read[:<group glob>]` scope. Only the currently published version is served, with
`${secret:…}` references resolved — which additionally requires a matching `secret:read` scope.

### GetConfig

```bash
-d '{"group":"crawler","key":"example.json"}'
```

```json
{
  "item": {
    "namespace": "default",
    "group": "crawler",
    "key": "example.json",
    "format": "json",
    "version": 3,
    "content": "{\"search_page_size\": 10, \"item_fields\": [\"id\", \"title\"]}",
    "updated_at": "2026-09-17T18:33:09.468485Z",
    "has_secret_refs": false
  }
}
```

`not_found` when the item does not exist or has no published version. `has_secret_refs: true` means the
content contains resolved secrets — **do not write it to a plaintext cache**.

### BatchGetConfig

Up to 200 items; unknown ones are listed in `missing` instead of failing the call.

```bash
-d '{"items":[{"group":"crawler","key":"example.json"},{"group":"crawler","key":"missing.json"}]}'
```

```json
{
  "items": [ { "namespace": "default", "group": "crawler", "key": "example.json", "version": 3, … } ],
  "missing": [ { "group": "crawler", "key": "missing.json" } ]
}
```

### WatchConfig

Long poll. Send the versions you hold; the call returns immediately with the items whose published version
differs, or waits up to `timeout_ms` (0 = 30 000, max 60 000) and returns an empty list.

```bash
-d '{"items":[{"group":"crawler","key":"example.json","version":3}],"timeout_ms":30000}'
```

```json
{ "items": [] }
```

```json
{ "items": [ { "group": "crawler", "key": "example.json", "version": 4, "content": "…", … } ] }
```

Version `0` means "I hold nothing", so the item is returned as soon as it has a published version. Loop
immediately after each response, updating the versions you hold; a change is visible in well under a
second. Set your HTTP client timeout above the wait (35 s is a good floor). Concurrent watches per instance
are bounded by `SPINNERET_MAX_WATCHERS` (20 000); beyond it the call fails with `resource_exhausted`.

The reserved, read-only group `_runtime` exposes `breakers` and `site_switches`, so a node can watch the
control state and back off before its next `Acquire` fails.

---

## SecretService

Requires a `secret:read:<glob over "<namespace>/<path>">` scope. **Every read is written to the audit log.**

### GetSecret

```bash
-d '{"path":"signing/api_key","version":0}'
```

```json
{
  "path": "signing/api_key",
  "version": 2,
  "value": "sk-live-…",
  "expires_at": null
}
```

`version: 0` reads the current version. Paths are namespace-relative and match `^[a-z0-9][a-z0-9_./-]*$`.
Cache the value in memory for the life of the process rather than reading it per request, and never write
it to disk.

---

## Errors

Errors use the Connect error body plus two response headers, so plain JSON clients never have to decode
error details:

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

| Header | Meaning |
| --- | --- |
| `Spinneret-Reason` | Machine-readable reason — branch on this, not on the message |
| `Spinneret-Retry-After-Ms` | Suggested wait in milliseconds, when the server can estimate one |

With the gRPC protocol the same values arrive as trailers. Both SDKs expose them as typed errors
(`spinneret.IsCircuitOpen(err)`, `RetryAfterOf(err)` in Go; `NoIdentityAvailable`, `err.retry_after_ms` in
Python).

### Reason table

| Reason | Connect code | HTTP | When | What to do |
| --- | --- | --- | --- | --- |
| `token_invalid` | `unauthenticated` | 401 | Unknown or malformed token | Fix the token; do not retry |
| `token_expired` | `unauthenticated` | 401 | Past `expires_at` | Issue a new token |
| `token_revoked` | `unauthenticated` | 401 | Revoked in the console | Issue a new token |
| `ip_not_allowed` | `permission_denied` | 403 | Client IP outside the token's allow-list | Fix the allow-list or the egress IP |
| `scope_missing` | `permission_denied` | 403 | Token lacks the scope for this call or site | Grant the scope |
| `permission_denied` | `permission_denied` | 403 | Console user lacks the permission | — |
| `session_invalid` | `unauthenticated` | 401 | Console session expired, revoked or the account was disabled | Sign in again |
| `csrf_missing` | `permission_denied` | 403 | Cookie-authenticated unsafe request without `X-Spinneret-CSRF: 1` | Send the header |
| `login_throttled` | `resource_exhausted` | 429 | 5 failed logins per user or 20 per IP in 15 min | Wait it out |
| `site_unknown` | `invalid_argument` | 400 | No such site in the token's namespace | Fix the request; do not retry |
| `client_unknown` | `invalid_argument` | 400 | Site has no such client type | Fix the request |
| `endpoint_group_unknown` | `invalid_argument` | 400 | Explicit `endpoint_group` does not exist | Fix the request |
| `uri_invalid` | `invalid_argument` | 400 | URI could not be parsed | Fix the request |
| `invalid_argument` | `invalid_argument` | 400 | Validation failure | Fix the request |
| `no_identity_available` | `resource_exhausted` | 429 | Nothing passes the availability rules right now | **Wait `Spinneret-Retry-After-Ms`, then retry** |
| `no_proxy_available` | `resource_exhausted` | 429 | Policy needs a proxy, none usable | Wait and retry; check proxy health |
| `circuit_open` | `unavailable` | 503 | Endpoint group breaker is open | Pause this group for the hinted time |
| `site_paused` | `unavailable` | 503 | Site switch is off | Pause this site; do not hammer |
| `rebuilding` | `unavailable` | 503 | Hot state is being rebuilt | Retry with backoff |
| `rate_limited` | `resource_exhausted` | 429 | Per-token rate limit | Slow down |
| `lease_unknown` | `not_found` | 404 | Lease never existed or is long gone | Acquire again |
| `lease_released` / `lease_expired` | `failed_precondition` | 400 | Lease already ended | Acquire again |
| `lease_lifetime_exceeded` | `failed_precondition` | 400 | `Renew` hit `max_lease_lifetime` | Release and acquire a new lease |
| `not_found` | `not_found` | 404 | Config item, secret or object missing | Do not retry |
| `already_exists` | `already_exists` | 409 | Name taken | Choose another name |
| `failed_precondition` | `failed_precondition` | 400 | Operation not valid in the current state | Read the message |
| `conflict` | `aborted` | 409 | Concurrent modification | Re-read and retry once |
| `internal` | `internal` | 500 | Server-side failure | Retry with backoff; check server logs |

In `Report`, per-report failures are **not** RPC errors: the call returns `200` with entries in `rejected`.

---

## Retry guidance

| Situation | Retry? | How |
| --- | --- | --- |
| Connection refused, DNS failure, timeout **before** the request was sent | yes | Exponential backoff with jitter; safe for every RPC |
| `unavailable` / `internal` (server-side) | yes | Backoff with jitter, a few attempts |
| `resource_exhausted` with `Spinneret-Retry-After-Ms` | yes | Sleep the hinted time first, then retry |
| `circuit_open`, `site_paused` | not immediately | Stop leasing for that group/site for the hinted duration; retrying only burns quota |
| `unauthenticated`, `permission_denied`, `invalid_argument`, `not_found` | no | Fix the configuration |
| `Report` after a timeout, with the same `report_id` | yes | Idempotent within the dedup window — the retry returns `duplicated` |
| `Acquire` after a timeout | careful | Not idempotent: a retry may issue a second lease. Retry only when the failure happened **before** the request was sent, or when the error is an explicit server `unavailable` |

Both SDKs implement exactly this: two retries with 100 ms–2 s equal-jitter backoff, the server's
`Retry-After` hint honoured up to 5 s, and `Acquire`/`AcquireBatch` retried only for pre-send failures or an
explicit `unavailable`.

Client-side rules that keep a fleet healthy:

- Give `Acquire` a `wait_ms` (500–2000) instead of a tight retry loop — the server queues you efficiently.
- Treat `circuit_open` and `site_paused` as a signal to pause the *worker*, not to retry the *request*.
- Always report, even for failures: `http_status: 0` with the right `error_kind` is exactly what the signal
  policy needs to blame the proxy instead of the identity.
- Use one `report_id` per attempt and keep it stable across retries of the same report.

---

## Admin API

The console uses these services with a session cookie; scripts can use a token with the `admin` scope (or a
narrower one where noted). Console requests carry `X-Spinneret-Tenant: <tenant id>`, address namespaces by
**name** in the request body, and unsafe cookie-authenticated requests need `X-Spinneret-CSRF: 1`.

| Service | RPCs |
| --- | --- |
| `AuthService` | `Login`, `Logout`, `GetMe`, `ChangePassword` |
| `TenantAdminService` | Tenants and namespaces: `ListTenants`, `CreateTenant`, `UpdateTenant`, `DeleteTenant`, `ListNamespaces`, `CreateNamespace`, `UpdateNamespace`, `DeleteNamespace` |
| `AccessAdminService` | Tokens, users, role bindings, audit: `ListTokens`, `CreateToken`, `RevokeToken`, `ListUsers`, `CreateUser`, `UpdateUser`, `ResetPassword`, `ListRoleBindings`, `CreateRoleBinding`, `DeleteRoleBinding`, `ListAuditLogs` |
| `SiteAdminService` | Sites, endpoint groups, URI rules: `ListSites`, `GetSite`, `CreateSite`, `UpdateSite`, `DeleteSite`, `ListEndpointGroups`, `CreateEndpointGroup`, `UpdateEndpointGroup`, `DeleteEndpointGroup`, `ListURIRules`, `ReplaceURIRules`, `TestURI` |
| `IdentityAdminService` | Identity types, identities, accounts: `…IdentityType(s)`, `PreviewDelivery`, `ListIdentities`, `GetIdentity`, `ImportIdentities`, `UpdateIdentityPayload`, `UpdateIdentity`, `OperateIdentities`, `BulkOperateIdentities`, `RevertActions`, `ListStateEvents`, `GetIdentityHotState`, `ListAccounts`, `UpsertAccount`, `OperateAccount` |
| `ProxyAdminService` | `ListProxies`, `GetProxy`, `ImportProxies`, `UpdateProxy`, `OperateProxies`, `DeleteProxies`, `CheckProxy`, `GetProviderStats` |
| `PolicyAdminService` | `ListPolicies`, `GetPolicy`, `CreatePolicy`, `SaveDraft`, `PublishPolicy`, `RollbackPolicy`, `DeletePolicy`, `ListPolicyVersions`, `DiffPolicyVersions`, `ListBindings`, `SetBinding`, `DeleteBinding`, `ResolvePolicies`, `DebugReport`, `ValidatePolicy` |
| `BreakerAdminService` | `ListBreakers`, `GetBreaker`, `OpenBreaker`, `CloseBreaker`, `ListBreakerEvents`, `SetSitePaused` |
| `ConfigAdminService` | `ListConfigItems`, `GetConfigItem`, `CreateConfigItem`, `SaveConfigDraft`, `PublishConfig`, `RollbackConfig`, `DeleteConfigItem`, `ListConfigVersions`, `DiffConfigVersions` |
| `SecretAdminService` | `ListSecrets`, `GetSecret`, `CreateSecret`, `UpdateSecret`, `DeleteSecret`, `RevealSecret`, `ListSecretVersions`, `ListSecretAccessLogs`, `GetKEKStatus`, `StartKEKRewrap` |
| `NotificationAdminService` | `ListChannels`, `CreateChannel`, `UpdateChannel`, `DeleteChannel`, `TestChannel`, `ListAlertEvents` |
| `DashboardService` | `GetOverview`, `GetTimeSeries`, `GetHeatmap`, `ListRiskEvents`, `QueryRequestEvents`, `GetNodeStats` |

Example — sign in and list sites:

```bash
curl -s -c cookies.txt http://localhost:8080/spinneret.v1.AuthService/Login \
  -H 'Content-Type: application/json' -H 'X-Spinneret-CSRF: 1' \
  -d '{"username":"admin","password":"…"}'
# the response carries the user, their tenants, namespaces and role bindings

curl -s -b cookies.txt http://localhost:8080/spinneret.v1.SiteAdminService/ListSites \
  -H 'Content-Type: application/json' -H 'X-Spinneret-CSRF: 1' \
  -H 'X-Spinneret-Tenant: ten_01a0afa70af372698c40d9d64c36123b' \
  -d '{"namespace":"default"}'
```

Admin conventions:

- **Pagination**: `page_size` (0 = 50, max 500) and an opaque `page_token`; responses carry
  `next_page_token` (empty on the last page) and, where cheap, `total`.
- **Durations** are strings here, not milliseconds: `"500ms"`, `"30s"`, `"10m"`, `"24h"`, `"7d"`,
  `"1h30m"`, `"permanent"`.
- **Optional fields** may be omitted to mean "unchanged" in updates, or "no filter" in lists.
- Every mutating admin RPC writes an **audit log** entry (`AccessAdminService/ListAuditLogs`).
- Admin requests are read-your-writes across instances: a write is visible to the next admin call even if it
  lands on another replica.

Non-Connect HTTP endpoints:

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `GET /healthz` | public | Liveness |
| `GET /readyz` | public | Readiness: PostgreSQL, Redis, hot state, catalog; `draining` while shutting down |
| `GET /metrics` | public on the main listener | Prometheus metrics — put it on `SPINNERET_METRICS_ADDR` to restrict it |
| `GET /api/v1/events/stream?tenant=<id>&namespace=<name>` | session | SSE stream of console events, filtered by permission |
| `GET /*` | public | The embedded console (SPA fallback) |

The `.proto` files are the source of truth for every message and validation rule. Regenerate clients for any
language with [buf](https://buf.build) from [`proto/spinneret/v1`](../proto/spinneret/v1).
