# Spinneret Go SDK

[中文文档](README.zh-CN.md)

Go client for [Spinneret](https://github.com/Evil0ctal/Spinneret), the control plane that leases
identities (cookies, device parameters, accounts) and proxies to crawler nodes, turns request
reports into cooldowns, bans and circuit breaking, and distributes configuration and secrets.

- `Client` on top of the generated Connect clients, speaking Connect JSON (default) or gRPC
- Authentication (`Authorization: Bearer`) and node (`X-Spinneret-Node`) headers on every call
- Typed errors carrying the server reason and retry hint (`IsNoIdentity`, `IsCircuitOpen`, ...)
- `Lease` helper: applies the credential to an `*http.Request`, exposes the proxy for an
  `http.Transport`, queues reports and releases the lease with the last report
- Background `Reporter`: batches of 100 reports or every 200 ms, bounded queue, retries with backoff
- `ConfigWatcher`: long polling, change callbacks, version tracking, local snapshots without secrets
- `ClassifyError`: maps `net/http` failures to report error kinds

The package lives in the main module: `github.com/Evil0ctal/Spinneret/sdk/go/spinneret`
(Go 1.27+). Its only runtime dependencies are `connectrpc.com/connect` and
`google.golang.org/protobuf`.

## Installation

```bash
go get github.com/Evil0ctal/Spinneret@latest
```

```go
import "github.com/Evil0ctal/Spinneret/sdk/go/spinneret"
```

## Configuration

`spinneret.New(spinneret.Options{...})` validates the options and does not contact the server.
Empty `BaseURL`, `Token` and `Node` fall back to the environment:

| Option | Variable | Meaning | Default |
| --- | --- | --- | --- |
| `BaseURL` | `SPINNERET_URL` | Server URL, e.g. `https://spinneret.internal` (a path prefix is allowed) | required |
| `Token` | `SPINNERET_TOKEN` | Node API token (`spn_...`) | required |
| `Node` | `SPINNERET_NODE` | Node name sent as `X-Spinneret-Node` (sanitized to `[A-Za-z0-9._:@-]`, 128 chars) | host name |
| `UseGRPC` | | gRPC (binary protobuf, HTTP/2) instead of Connect JSON | `false` |
| `Timeout` | | Bound of each attempt of a unary call | `10s` |
| `HTTPClient` | | Custom `connect.HTTPClient` (TLS roots, egress proxy, ...) | tuned `http.Client` |
| `Retry` | | `*RetryPolicy`; `spinneret.NoRetry()` disables retries | 2 retries |
| `Reporter` | | `ReporterOptions` of `client.Reporter()` | see below |
| `Logger` | | `*slog.Logger` | `slog.Default()` |
| `UserAgent` | | Prepended to `spinneret-go/<version>` | |

```go
client, err := spinneret.New(spinneret.Options{
	BaseURL: "https://spinneret.internal",
	Token:   os.Getenv("SPINNERET_TOKEN"),
	Node:    "crawler-hk-03",
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

## Quick start

```go
ctx := context.Background()
target := "https://target.example.com/api/v1/search?keyword=go"

lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "shop", Client: "web", Uri: target})
switch {
case spinneret.IsNoIdentity(err), spinneret.IsNoProxy(err):
	time.Sleep(spinneret.RetryAfterOf(err)) // wait for capacity
	return nil
case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
	return errPaused // pause this endpoint group or site
case err != nil:
	return err
}
defer lease.Close(ctx) // releases with the last report, or calls LeaseService/Release

transport, err := lease.Transport(nil) // clone of http.DefaultTransport with the leased proxy
if err != nil {
	return err
}
defer transport.CloseIdleConnections()
httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}

req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
lease.Apply(req) // cookies, headers and query parameters of the credential
started := time.Now()
resp, err := httpClient.Do(req)
if err != nil {
	return lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
}
defer resp.Body.Close()
body, _ := io.ReadAll(resp.Body)
return lease.ReportResponse(resp, spinneret.ReportInput{
	StartedAt:     started,
	ResponseBytes: int64(len(body)),
	Markers:       detectMarkers(body), // e.g. "captcha_page", "empty_list"
})
```

See [`examples/basic/main.go`](examples/basic/main.go) for a complete node:

```bash
export SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_xxx
go run ./sdk/go/examples/basic -site shop -client web -target https://... -config crawler/search.json
```

## Protocols

| | Connect JSON (default) | gRPC (`UseGRPC: true`) |
| --- | --- | --- |
| Transport | HTTP/1.1 or HTTP/2 | HTTP/2 only (ALPN over TLS, h2c for `http://`) |
| Encoding | JSON with snake_case field names; unknown response fields ignored | binary protobuf |
| Error reason / retry hint | response headers | response trailers |

Both protocols expose the same `Error` values. Load balancers must pass HTTP/2 end to end for
gRPC; the Docker Compose load balancer (Caddy) accepts h2c on port 8080.

## Leases

`client.Lease(ctx, *AcquireRequest)` calls `LeaseService/Acquire` and returns a `*Lease`;
`client.LeaseBatch(ctx, *AcquireBatchRequest)` wraps every lease of `AcquireBatch`, and
`client.NewLease(resp, uri)` wraps a response obtained elsewhere. A lease is safe for concurrent use.

| Member | Description |
| --- | --- |
| `ID()`, `IdentityID()`, `Info()`, `Hints()`, `Response()`, `URI()` | Lease metadata (`Info().GetProbe()`, `GetSticky()`, ...) |
| `ExpiresAt()` | Expiry, updated by `Renew` |
| `Credential()` | `cookies`, `cookie_header`, `headers`, `query`, `json`, `values` |
| `Proxy()`, `ProxyURL()` | Assigned proxy (`nil` when none); the URL carries credentials, never log it |
| `Transport(base)` | Clone of `base` (or `http.DefaultTransport`) routed through the proxy |
| `Apply(req)` | Merge the credential into an `*http.Request` |
| `Report(ReportInput)` | Queue a report |
| `ReportResponse(resp, ReportInput)` | Report an `*http.Response` (status, method, path, `Content-Length`; 407 sets `proxy_auth`) |
| `ReportError(err, ReportInput)` | Report a failed request with `error_kind` from `ClassifyError` (method and path from `*url.Error`) |
| `Renew(ctx, extend)` | Extend the lease (`0` = policy TTL) |
| `Close(ctx)` | Release the lease (idempotent) |

Release semantics:

- Reports are queued on `client.Reporter()`. The most recent report is held back until the next
  report or `Close`, so that it can carry `release: true`.
- `Close` sends the held-back report with `release: true`. When nothing was reported it calls
  `LeaseService/Release`; no report is invented, so report failed requests yourself (`ReportError`)
  when the server should count them.
- `Close` returns the error of the `Release` call, except reasons saying that the lease has already
  ended (`lease_released`, `lease_unknown`, `lease_expired`, `lease_lifetime_exceeded`). When the
  reporter is already closed, the last report is delivered directly.
- `ReportInput{Release: true}` releases immediately; later reports fail with `IsLeaseGone(err)`.
- One lease can serve several requests (pagination): report every request, `Close` releases.
- `client.Reporter().Flush(ctx)` waits until queued reports are delivered.

### Credentials

`Apply(req)` (and `spinneret.ApplyCredential(req, cred)`) never overrides what the request already
carries:

- headers are set unless the request has them (`Host` is ignored: net/http takes it from the URL);
- cookies are merged into a single `Cookie` header, skipping names the request already sends; without
  a cookie map and without request cookies, `cookie_header` is sent verbatim (no re-encoding);
- query parameters are appended when missing, keeping the existing query string byte for byte so
  that signed parameters stay valid.

`credential.GetValues().AsMap()` and `credential.GetJson().AsInterface()` give access to typed
values (device parameters, request body fragments). `http.Transport` supports `http://`,
`https://` and `socks5://` proxy URLs.

### Reports

`ReportInput` fields: `HTTPStatus`, `Method`, `URI`, `BusinessCode`, `ErrorKind`, `Markers`,
`OutcomeHint`, `Latency`, `ResponseBytes`, `StartedAt`, `FinishedAt`, `ReportID`, `Release`.

- `ReportID` defaults to a random UUID, `FinishedAt` to now, `StartedAt` to `FinishedAt - Latency`,
  and `Latency` to `FinishedAt - StartedAt`.
- `URI` defaults to the acquired URI and is reduced to the request path (query string and fragment
  removed, at most 2048 characters): endpoint groups match on the path, and query parameters often
  carry signed credential values. A lease acquired with `endpoint_group` only needs an explicit `URI`.
- The input is validated like the server does (`error_kind` values, 32 markers of 1..64 characters,
  status 0..999, ...); invalid input returns an `invalid_argument` error without queuing anything.
- `ReportResponse` cannot see the size of chunked bodies: pass `ResponseBytes` after reading the body.

## Background reporter

`client.Reporter()` (or `spinneret.NewReporter(send, options, logger)`) batches reports:

- a batch is sent when `BatchSize` (100) reports are queued or the oldest waited `FlushInterval`
  (200 ms), with at most `MaxBatchSize` (500) reports per call;
- the queue is bounded by `MaxQueueSize` (10 000): the oldest reports are dropped, counted in
  `Stats().Dropped` and logged (throttled);
- failures for which `IsRetryable(err)` holds (transport errors, `unavailable`, `internal`, `unknown`,
  `deadline_exceeded`, `aborted`, `resource_exhausted`) are retried with jittered exponential backoff
  (`InitialBackoff` 500 ms .. `MaxBackoff` 30 s, at least the server retry hint); other failures drop
  the batch;
- reports rejected by the server are counted, logged and passed to `OnRejected`;
- `Submit` never blocks on the network and fills `report_id`, `finished_at` and `started_at` when empty;
- `Flush(ctx)` sends the queue now and waits; `Close(ctx)` (called by `client.Close`) delivers what it
  can until `ctx` ends (`CloseTimeout`, 5 s, when `ctx` has no deadline), aborts the in-flight call
  and returns an error when reports were dropped.

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

## Configuration center

```go
watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
	Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "_runtime", Key: "breakers"}},
	SnapshotDir: "/var/cache/spinneret",
	OnChange: func(item *spinneret.ConfigItem) { reload(item.GetContent()) },
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

- `Start` loads the items with `BatchGetConfig` and starts a goroutine that long-polls `WatchConfig`
  (`Timeout` 30 s, max 60 s) with the known versions, stores the latest items and calls the listeners
  for every change (initial values included). Listeners run sequentially; a panicking listener is
  logged and does not stop the watcher. `Stop` cancels the in-flight long poll.
- Failed polls are retried with backoff (`InitialBackoff` 1 s .. `MaxBackoff` 30 s).
- When the server is unavailable at start (`IsRetryable` errors), the snapshots are loaded instead and
  `FromSnapshot()` is `true` until the server answers. Authorization and validation errors are
  returned by `Start`, which can then be called again.
- With `SnapshotDir`, every fetched item is written atomically (temp file, `fsync`, rename; files
  `0600`, directories `0700`) to `<dir>/<host>/<namespace>/<group>/<key>.json` with percent-encoded
  components, the same layout and format as the Python SDK.
- Secret material is never written to disk: items with `has_secret_refs` (the server resolved
  `${secret:...}` references into the content), content still containing `${secret:` and items
  matched by `TreatAsSecret` are kept in memory only, and older snapshots of them are removed.
  `TreatAsSecret` can only add secret items; a predicate that panics counts as true. Encrypted
  snapshots written by the Python SDK are ignored.

One-off reads: `GetConfig`, `BatchGetConfig`, `WatchConfig` (`timeout_ms` 0 = 30 s) and `GetSecret`
(`version` 0 = current).

## Direct calls

Every node RPC is available with the generated request and response types, re-exported as aliases
(`spinneret.AcquireRequest`, `spinneret.Report`, ...): `Acquire`, `AcquireBatch`, `Renew`, `Release`,
`Report` (1..500 reports, synchronous), `GetConfig`, `BatchGetConfig`, `WatchConfig`, `GetSecret`.
`client.LeaseService()` and friends return the underlying generated clients (authenticated, without
retries or error conversion).

## Errors

Every call returns `*spinneret.Error` (use `spinneret.AsError(err)` or `errors.As`):

| Field / method | Meaning |
| --- | --- |
| `Code` | `connect.Code`, e.g. `connect.CodeResourceExhausted` |
| `Reason` | `Spinneret-Reason`, e.g. `no_identity_available`; `transport` when no response was received |
| `Message` | Server message |
| `RetryAfter` | `Spinneret-Retry-After-Ms` as a `time.Duration` (0 when absent) |
| `Procedure` | Failed RPC |
| `ErrorKind`, `Transport()` | Classification of transport failures |
| `FromServer()` | The server sent the error (not synthesized from a transport failure or bare HTTP status) |
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
| `ReasonOf(err) == spinneret.ReasonClientClosed` | `failed_precondition` | create a new client |

Responses without a Connect error body (for example a load balancer page) keep the code derived from
the HTTP status, with an empty reason.

## Retries and timeouts

- Each attempt is bounded by `Timeout` (10 s); `Acquire`/`AcquireBatch` add `wait_ms`; `WatchConfig`
  uses `timeout_ms` (30 s when 0) plus 5 s. A shorter deadline on `ctx` wins. The deadline sent to
  the server (`Connect-Timeout-Ms` / `grpc-timeout`) is one second later than the client-side
  timeout, so an expired attempt is always reported as a transport timeout instead of racing a
  `deadline_exceeded` answer caused by the same deadline.
- `RetryPolicy{MaxRetries: 2, InitialBackoff: 100ms, MaxBackoff: 2s, MaxRetryAfter: 5s}`: transport
  failures and `unavailable` answers are retried with jittered exponential backoff; `circuit_open`
  and `site_paused` are never retried; server hints above `MaxRetryAfter` are returned instead of
  waited for; certificate errors are never retried.
- `Acquire` and `AcquireBatch` are not idempotent: they are retried only when the failure provably
  happened before the request was sent (dial and DNS errors) or when the server itself answered
  `unavailable`. Ambiguous failures (per-call timeout, connection reset, a bare 502/503/504) are
  returned.
- The background reporter and the config watcher do not use this policy; they retry with their own
  backoff.

## Error kinds

`spinneret.ClassifyError(err)` maps request errors to report `error_kind` values: `timeout`,
`conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns` or `other` (`""` for `nil`). It understands
`*url.Error` and the `net`, `syscall`, `crypto/tls` and `crypto/x509` errors it wraps, HTTP proxy
`CONNECT` refusals (`Proxy Authentication Required`) and SOCKS5 authentication failures, and falls
back to the error message. A `407` response of a plain HTTP proxy is a response, not an error: report it
with `ReportResponse`, which sets `proxy_auth`.

## Logging and security

The SDK logs through `Options.Logger` (default `slog.Default()`): retries at debug level, dropped or
rejected reports and failed deliveries at warning or error level. It never logs tokens, credentials,
proxy URLs, config contents or secret values; the token is only sent in the `Authorization` header.

## Development

```bash
go vet ./sdk/go/...
go test -race -count=1 -cover ./sdk/go/...
golangci-lint run ./sdk/go/...
```

Unit tests serve the generated handlers from `httptest` servers backed by fakes, over Connect JSON
and gRPC (h2c), and never access external networks. Live tests run against a deployment such as the
Docker Compose stack with the mocktarget site:

```bash
export SPINNERET_LIVE_ADMIN_PASSWORD=...   # admin password of the stack
go test -tags live -race -count=1 -run TestLive ./sdk/go/spinneret
```

They create a namespace named `gosdk-<run>` with a site, identities, a token, a config item and a
secret, exercise both protocols, and delete everything afterwards (`SPINNERET_LIVE_URL`,
`SPINNERET_LIVE_TARGET`, `SPINNERET_LIVE_GRPC_URL`, `SPINNERET_LIVE_TENANT` and
`SPINNERET_LIVE_ADMIN_USER` override the defaults).
