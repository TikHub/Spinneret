"""Synchronous Spinneret client built on :class:`httpx.Client`."""

from __future__ import annotations

import logging
import threading
import time
import weakref
from collections.abc import Iterable, Sequence
from os import PathLike
from types import TracebackType
from typing import TYPE_CHECKING, Any, TypeVar

import httpx

from . import _calls as calls
from ._calls import Call, ConfigKeyLike, Timeouts
from ._report_buffer import ReporterOptions
from ._retry import NO_RETRY, RetryPolicy, should_retry_error, should_retry_exception
from .errors import REASON_CLIENT_CLOSED, FailedPrecondition, error_from_response
from .lease import ManagedLease
from .models import (
    AcquireBatchResponse,
    AcquireRequest,
    AcquireResponse,
    BatchGetConfigResponse,
    ConfigItem,
    GetSecretResponse,
    ReleaseResponse,
    RenewResponse,
    Report,
    ReportResponse,
    SpinneretModel,
    WatchConfigResponse,
    WatchItem,
)
from .reporter import Reporter
from .settings import Settings

if TYPE_CHECKING:
    from ._snapshot import SecretPredicate
    from .watcher import ChangeCallback, ConfigWatcher

__all__ = ["Client"]

logger = logging.getLogger("spinneret")

R = TypeVar("R", bound=SpinneretModel)

# Total seconds close() waits for watcher threads to finish.
_WATCHER_STOP_TIMEOUT = 1.0


class Client:
    """Thread-safe synchronous client of the Spinneret node API.

    Example::

        with spinneret.Client() as client:  # SPINNERET_URL / SPINNERET_TOKEN
            with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
                with httpx.Client(**lease.httpx_kwargs()) as http:
                    response = http.get("https://target.example.com/api/v1/feed")
                lease.report_response(response)
    """

    def __init__(
        self,
        url: str | None = None,
        token: str | None = None,
        *,
        node: str | None = None,
        cache_dir: str | PathLike[str] | None = None,
        settings: Settings | None = None,
        retry: RetryPolicy | None = None,
        timeouts: Timeouts | None = None,
        reporter_options: ReporterOptions | None = None,
        http_client: httpx.Client | None = None,
    ) -> None:
        """Create a client.

        Args:
            url: Server base URL (default ``SPINNERET_URL``).
            token: API token (default ``SPINNERET_TOKEN``).
            node: Node name (default ``SPINNERET_NODE`` or the host name).
            cache_dir: Snapshot directory (default ``SPINNERET_CACHE_DIR``).
            settings: Complete settings; when given, the four arguments above are ignored.
            retry: Retry policy of unary calls (default: 2 retries).
            timeouts: HTTP timeouts (default: connect 3 s, read 10 s).
            reporter_options: Options of the background :attr:`reporter`.
            http_client: Pre-configured ``httpx.Client`` to use; it is not closed
                by :meth:`close`.

        Raises:
            ConfigurationError: When the URL or token is missing or invalid.
        """
        self._settings = settings or Settings.from_env(
            url=url, token=token, node=node, cache_dir=cache_dir
        )
        self._retry = retry or RetryPolicy()
        self._timeouts = timeouts or Timeouts()
        self._reporter_options = reporter_options or ReporterOptions()
        self._headers = calls.build_headers(self._settings)
        self._owns_http = http_client is None
        self._http = http_client or httpx.Client(timeout=self._timeouts.for_call())
        self._lock = threading.Lock()
        self._reporter: Reporter | None = None
        self._watchers: weakref.WeakSet[ConfigWatcher] = weakref.WeakSet()
        self._closed = False

    @property
    def settings(self) -> Settings:
        """Resolved settings of this client."""
        return self._settings

    @property
    def timeouts(self) -> Timeouts:
        """HTTP timeouts of this client."""
        return self._timeouts

    @property
    def closed(self) -> bool:
        """Whether :meth:`close` has been called."""
        return self._closed

    @property
    def reporter(self) -> Reporter:
        """Background reporter delivering reports through this client (created lazily).

        After :meth:`close` the reporter is closed: submitting raises
        :class:`ReporterClosedError`.
        """
        with self._lock:
            if self._reporter is None:
                self._reporter = Reporter(self._send_reports, self._reporter_options)
                if self._closed:
                    self._reporter.close(0)
            return self._reporter

    # -- LeaseService -------------------------------------------------------

    def acquire(
        self,
        site: str,
        client: str,
        uri: str = "",
        *,
        endpoint_group: str = "",
        session_key: str = "",
        wait_ms: int = 0,
    ) -> AcquireResponse:
        """Lease one identity (and proxy) for a request.

        Raises:
            NoIdentityAvailable: No identity is available; see ``retry_after``.
            CircuitOpen: The endpoint group breaker is open.
            SitePaused: The site is paused.
            SpinneretError: Any other failure.
        """
        return self._call(calls.acquire(site, client, uri, endpoint_group, session_key, wait_ms))

    def acquire_batch(
        self,
        site: str,
        client: str,
        count: int,
        uri: str = "",
        *,
        endpoint_group: str = "",
        session_key: str = "",
        wait_ms: int = 0,
    ) -> AcquireBatchResponse:
        """Lease up to ``count`` (1..50) distinct identities in one call."""
        return self._call(
            calls.acquire_batch(site, client, count, uri, endpoint_group, session_key, wait_ms)
        )

    def renew(self, lease_id: str, extend_ms: int = 0) -> RenewResponse:
        """Extend a lease (``extend_ms=0`` uses the policy lease TTL)."""
        return self._call(calls.renew(lease_id, extend_ms))

    def release(self, lease_id: str) -> ReleaseResponse:
        """Release a lease explicitly (idempotent)."""
        return self._call(calls.release(lease_id))

    def lease(
        self,
        site: str,
        client: str,
        uri: str = "",
        *,
        endpoint_group: str = "",
        session_key: str = "",
        wait_ms: int = 0,
        flush_on_exit: bool = False,
        raise_on_release_error: bool = False,
    ) -> ManagedLease:
        """Return a context manager that acquires a lease and releases it on exit.

        See :class:`ManagedLease` for the reporting and release semantics.

        Args:
            site: Site name, e.g. ``"shop"``.
            client: Client type of the site, e.g. ``"web"``.
            uri: Request URI used to match the endpoint group.
            endpoint_group: Explicit endpoint group (takes precedence over ``uri``).
            session_key: Sticky-session key.
            wait_ms: Time the server may wait for an identity.
            flush_on_exit: Wait for the reporter to deliver its queue on exit.
            raise_on_release_error: Raise a failed ``LeaseService/Release`` call
                (made on exit when nothing was reported) from a block that
                raised nothing. Failures saying that the lease has already
                ended are never raised.
        """
        request = AcquireRequest(
            site=site,
            client=client,
            uri=uri,
            endpoint_group=endpoint_group,
            session_key=session_key,
            wait_ms=wait_ms,
        )
        return ManagedLease(
            self,
            request,
            flush_on_exit=flush_on_exit,
            raise_on_release_error=raise_on_release_error,
        )

    # -- ReportService ------------------------------------------------------

    def report(self, reports: Sequence[Report]) -> ReportResponse:
        """Send 1..500 reports synchronously (use :attr:`reporter` for batching)."""
        return self._call(calls.report(reports))

    # -- ConfigService ------------------------------------------------------

    def get_config(self, group: str, key: str, *, namespace: str = "") -> ConfigItem:
        """Fetch the current version of one config item.

        Raises:
            NotFound: The item does not exist.
        """
        return self._call(calls.get_config(namespace, group, key)).item

    def batch_get_config(
        self, items: Iterable[ConfigKeyLike], *, namespace: str = ""
    ) -> BatchGetConfigResponse:
        """Fetch several config items; unknown ones are listed in ``missing``."""
        return self._call(calls.batch_get_config(namespace, items))

    def watch_config(
        self,
        items: Iterable[WatchItem | dict[str, Any]],
        *,
        namespace: str = "",
        timeout_ms: int = 30_000,
    ) -> WatchConfigResponse:
        """Long-poll until a watched item changes or ``timeout_ms`` elapses.

        Returns the changed items (empty on timeout). Prefer :meth:`config_watcher`
        for a managed watch loop with snapshots.
        """
        return self._call(
            calls.watch_config(namespace, items, timeout_ms, self._timeouts.watch_grace)
        )

    def config_watcher(
        self,
        items: Iterable[ConfigKeyLike],
        *,
        namespace: str = "",
        timeout_ms: int = 30_000,
        cache_dir: str | PathLike[str] | None = None,
        snapshots: bool = True,
        cache_secrets: bool = False,
        treat_as_secret: SecretPredicate | None = None,
        on_change: ChangeCallback | None = None,
    ) -> ConfigWatcher:
        """Create a :class:`ConfigWatcher` bound to this client (not started)."""
        from .watcher import ConfigWatcher

        watcher = ConfigWatcher(
            self,
            items,
            namespace=namespace,
            timeout_ms=timeout_ms,
            cache_dir=cache_dir,
            snapshots=snapshots,
            cache_secrets=cache_secrets,
            treat_as_secret=treat_as_secret,
            on_change=on_change,
        )
        with self._lock:
            self._watchers.add(watcher)
        return watcher

    # -- SecretService ------------------------------------------------------

    def get_secret(self, path: str, *, version: int = 0) -> GetSecretResponse:
        """Read a secret value (``version=0`` reads the current version)."""
        return self._call(calls.get_secret(path, version))

    # -- lifecycle ----------------------------------------------------------

    def close(self, timeout: float | None = None) -> None:
        """Stop watchers, flush the reporter and close the HTTP client.

        Args:
            timeout: Seconds allowed for delivering queued reports
                (default ``ReporterOptions.close_timeout``).
        """
        with self._lock:
            if self._closed:
                return
            self._closed = True
            reporter = self._reporter
            watchers = list(self._watchers)
        # Signal every watcher before waiting so that the wait is bounded overall.
        for watcher in watchers:
            watcher.stop(timeout=0)
        deadline = time.monotonic() + _WATCHER_STOP_TIMEOUT
        for watcher in watchers:
            watcher.stop(timeout=max(0.0, deadline - time.monotonic()))
        if reporter is not None:
            reporter.close(timeout)
        if self._owns_http:
            self._http.close()

    def __enter__(self) -> Client:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"Client(url={self._settings.url!r}, node={self._settings.node!r})"

    # -- transport ----------------------------------------------------------

    def _send_reports(self, reports: Sequence[Report]) -> ReportResponse:
        return self._call(calls.report(reports), retry=NO_RETRY)

    def _call(self, call: Call[R], *, retry: RetryPolicy | None = None) -> R:
        if self._http.is_closed:
            raise FailedPrecondition(
                f"{call.procedure}: the client has been closed", reason=REASON_CLIENT_CLOSED
            )
        policy = retry or self._retry
        url = call.url(self._settings.url)
        body = call.body()
        timeout = call.timeout(self._timeouts)
        attempt = 0
        while True:
            try:
                response = self._http.post(
                    url, content=body, headers=self._headers, timeout=timeout
                )
            except httpx.TransportError as exc:
                if attempt < policy.max_retries and should_retry_exception(exc, call.idempotent):
                    delay = policy.delay(attempt)
                    if delay is not None:
                        _log_retry(call, attempt, delay, exc)
                        time.sleep(delay)
                        attempt += 1
                        continue
                raise calls.transport_error(call, exc) from exc
            if response.is_success:
                return calls.decode_response(call, response.content)
            error, from_body = error_from_response(
                response.status_code, response.headers, response.content
            )
            if attempt < policy.max_retries and should_retry_error(
                error, from_body, call.idempotent
            ):
                delay = policy.delay(attempt, error.retry_after_ms)
                if delay is not None:
                    _log_retry(call, attempt, delay, error)
                    time.sleep(delay)
                    attempt += 1
                    continue
            raise error


def _log_retry(call: Call[Any], attempt: int, delay: float, cause: BaseException) -> None:
    logger.debug(
        "retrying %s (attempt %d) in %.3fs after %s",
        call.procedure,
        attempt + 2,
        delay,
        type(cause).__name__,
    )
