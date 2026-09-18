# Changelog

All notable changes to the Spinneret Python SDK are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-09-17

### Added

- `Client` (httpx, thread-safe) and `AsyncClient` (asyncio) for the node services
  `LeaseService` (`Acquire`, `AcquireBatch`, `Renew`, `Release`), `ReportService` (`Report`),
  `ConfigService` (`GetConfig`, `BatchGetConfig`, `WatchConfig`) and `SecretService`
  (`GetSecret`) over Connect-over-HTTP with JSON.
- `Settings` resolved from `SPINNERET_URL`, `SPINNERET_TOKEN`, `SPINNERET_NODE` and
  `SPINNERET_CACHE_DIR`.
- Immutable, tolerant pydantic v2 models for every request and response (unknown fields ignored,
  `null` treated as unset, 64-bit integers accepted as strings, RFC 3339 timestamps).
- Typed exceptions mapped from Connect codes and the `Spinneret-Reason` /
  `Spinneret-Retry-After-Ms` headers, including `NoIdentityAvailable`, `NoProxyAvailable`,
  `CircuitOpen`, `SitePaused`, `LeaseUnknown`, `LeaseReleased`, `LeaseExpired` and
  `TransportError`.
- `RetryPolicy` with jittered exponential backoff that never repeats non-idempotent acquires
  after ambiguous failures; per-call timeouts (connect 3 s, read 10 s, watch `timeout_ms + 5 s`).
- `ManagedLease` / `AsyncManagedLease` context managers with `httpx_kwargs()`, `report()`,
  `report_response()`, `report_exception()`, `renew()` and release on exit: the last queued report
  carries `release: true`, or `LeaseService/Release` is called when nothing was reported. Release
  errors with reason `lease_released`, `lease_unknown` or `lease_expired` are ignored (debug log),
  other errors are logged as warnings and raised only with `raise_on_release_error=True` when the
  block itself raised nothing.
- `Reporter` (thread) and `AsyncReporter` (asyncio task): batching every 200 ms or 100 reports,
  at most 500 per call, bounded drop-oldest queue, retries with backoff, rejection callback,
  graceful `close()`.
- `ConfigWatcher` / `AsyncConfigWatcher`: long polling, change callbacks, `wait_for_change()`,
  atomic local snapshots with fallback when the server is unavailable, and a secret caching
  policy: items the server flags with `has_secret_refs` (published content contained resolved
  `${secret:...}` references), items still containing a `${secret:` marker and items matched by
  the additional `treat_as_secret` predicate are never written in plain text; they stay in memory,
  or are encrypted on disk with `cache_secrets=True` (HKDF-SHA256 + AES-256-GCM, optional
  `cryptography` dependency).
- `classify_exception()` mapping request exceptions to report `error_kind` values.
- Reports carry the request path only (query string removed, capped at 2048 characters) and
  `error_kind` is validated against the values accepted by the server.
- Calls on a closed client raise `FailedPrecondition` with reason `client_closed`; watchers stop
  when their client is closed.
- Background reporters restart a worker that is no longer running (forked child process, ended
  event loop) instead of silently keeping reports queued.
