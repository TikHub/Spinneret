# Spinneret API protos

`proto/spinneret/v1` is the source of truth of the Spinneret API (package `spinneret.v1`). Services are served with
[Connect](https://connectrpc.com): every RPC is `POST /spinneret.v1.<Service>/<Method>` and can be called with plain
HTTP + JSON (`Content-Type: application/json`), the Connect protocol, gRPC or gRPC-Web.

Generate Go code (messages in `gen/go/spinneret/v1`, handlers and clients in `spinneretv1connect`) with:

```sh
./scripts/buf-generate.sh      # buf lint + buf generate, serialized
```

Validation rules are written with [protovalidate](https://protovalidate.com) (`buf.validate`) and enforced by the server.

## Services

| Caller | Services |
| --- | --- |
| Nodes (API token) | `LeaseService`, `ReportService`, `ConfigService`, `SecretService` |
| Console (session) | `AuthService`, `TenantAdminService`, `AccessAdminService`, `SiteAdminService`, `IdentityAdminService`, `ProxyAdminService`, `PolicyAdminService`, `BreakerAdminService`, `ConfigAdminService`, `SecretAdminService`, `NotificationAdminService`, `DashboardService` |

## JSON conventions

- **Field names are snake_case** (`lease_id`, `renew_before_ms`), exactly as written in the `.proto` files.
- **Zero values are always emitted**: `""`, `0`, `false`, `[]` and `{}` appear in responses; unset messages (for
  example `expires_at` of a token that never expires, or `proxy` when no proxy is assigned) are `null`. Fields
  declared `optional` in requests may be omitted to mean "unchanged" / "no filter".
- **Unknown request fields are ignored**, so newer clients can talk to older servers.
- **Timestamps** are RFC 3339 strings in UTC, e.g. `"2026-09-16T08:30:11.120Z"`.
- **`int64` values are strings in responses**, as mandated by the proto3 JSON mapping; requests accept both
  numbers and strings. `int32` values are plain JSON numbers in both directions.
  - Node APIs are `int32` throughout — counts and millisecond durations (`wait_ms`, `extend_ms`, `timeout_ms`,
    `latency_ms`, `renew_before_ms`) are numbers. The single `int64` there is `Report.response_bytes`, a
    request field that may be sent as `48213` or `"48213"`.
  - Admin and dashboard aggregates are `int64` and therefore come back quoted: `"acquires": "416"`,
    `"response_bytes": "48213"`, `"cooldown_remaining_ms": "30000"`, `RequestEventsSummary.total` and its
    `outcomes` map. Page sizes, versions, the list `total` and the counts inside import/bulk results stay
    `int32` (plain numbers).
- **Durations**: node-facing APIs use integer milliseconds in fields ending with `_ms` (`wait_ms`, `extend_ms`,
  `timeout_ms`). Admin APIs use duration strings such as `"500ms"`, `"30s"`, `"10m"`, `"24h"`, `"7d"`, `"1h30m"` and
  `"permanent"`.
- **Enum-like values are lower-case strings** (`"rate_limited"`, `"half_open"`), never protobuf enum names.
- **IDs are strings** of the form `<prefix>_<32 hex>` (`idt_…`, `pxy_…`); lease IDs are opaque.
- **Namespaces and sites are addressed by name** in console requests (`"namespace": "prod"`), resolved inside the tenant
  selected by the `X-Spinneret-Tenant` header. Node requests never need a namespace: it comes from the token.
- **Pagination**: list requests take `page_size` (0 = default 50, max 500) and an opaque `page_token`; responses return
  `next_page_token` (empty on the last page) and, where cheap, `total`.

## Errors

Errors use the Connect error format (`{"code": "resource_exhausted", "message": "..."}`). The machine-readable reason
and retry hint are returned as response headers so plain JSON clients do not need to decode error details:

```http
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 1200
```

## Headers

| Header | Direction | Meaning |
| --- | --- | --- |
| `Authorization: Bearer spn_…` | request | API token (nodes, CI) |
| `X-Spinneret-Node` | request | node instance name used for per-node statistics (≤ 128 chars) |
| `X-Spinneret-Tenant` | request | active tenant ID for console requests |
| `X-Spinneret-CSRF: 1` | request | required for cookie-authenticated requests |
| `Spinneret-Reason` | response | error reason, e.g. `circuit_open` |
| `Spinneret-Retry-After-Ms` | response | suggested wait before retrying |
