"""Asynchronous Spinneret client built on :class:`httpx.AsyncClient`."""

from __future__ import annotations

import asyncio
import logging
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
from .async_lease import AsyncManagedLease
from .async_reporter import AsyncReporter
from .errors import REASON_CLIENT_CLOSED, FailedPrecondition, error_from_response
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
from .settings import Settings

if TYPE_CHECKING:
    from ._snapshot import SecretPredicate
    from .async_watcher import AsyncChangeCallback, AsyncConfigWatcher

__all__ = ["AsyncClient"]

logger = logging.getLogger("spinneret")

R = TypeVar("R", bound=SpinneretModel)


class AsyncClient:
    """asyncio client of the Spinneret node API.

    Example::

        async with spinneret.AsyncClient() as client:
            async with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
                async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
                    response = await http.get("https://target.example.com/api/v1/feed")
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
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        """Create a client; see :class:`spinneret.Client` for the arguments.

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
        self._http = http_client or httpx.AsyncClient(timeout=self._timeouts.for_call())
        self._reporter: AsyncReporter | None = None
        self._watchers: weakref.WeakSet[AsyncConfigWatcher] = weakref.WeakSet()
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
        """Whether :meth:`aclose` has been called."""
        return self._closed

    @property
    def reporter(self) -> AsyncReporter:
        """Background reporter delivering reports through this client (created lazily).

        After :meth:`aclose` the reporter is closed: submitting raises
        :class:`ReporterClosedError`.
        """
        if self._reporter is None:
            self._reporter = AsyncReporter(self._send_reports, self._reporter_options)
        return self._reporter

    # -- LeaseService -------------------------------------------------------

    async def acquire(
        self,
        site: str,
        client: str,
        uri: str = "",
        *,
        endpoint_group: str = "",
        session_key: str = "",
        wait_ms: int = 0,
    ) -> AcquireResponse:
        """Lease one identity (and proxy); see :meth:`spinneret.Client.acquire`."""
        return await self._call(
            calls.acquire(site, client, uri, endpoint_group, session_key, wait_ms)
        )

    async def acquire_batch(
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
        return await self._call(
            calls.acquire_batch(site, client, count, uri, endpoint_group, session_key, wait_ms)
        )

    async def renew(self, lease_id: str, extend_ms: int = 0) -> RenewResponse:
        """Extend a lease (``extend_ms=0`` uses the policy lease TTL)."""
        return await self._call(calls.renew(lease_id, extend_ms))

    async def release(self, lease_id: str) -> ReleaseResponse:
        """Release a lease explicitly (idempotent)."""
        return await self._call(calls.release(lease_id))

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
    ) -> AsyncManagedLease:
        """Return an async context manager that acquires a lease and releases it on exit.

        Arguments match :meth:`spinneret.Client.lease`; see :class:`AsyncManagedLease`.
        """
        request = AcquireRequest(
            site=site,
            client=client,
            uri=uri,
            endpoint_group=endpoint_group,
            session_key=session_key,
            wait_ms=wait_ms,
        )
        return AsyncManagedLease(
            self,
            request,
            flush_on_exit=flush_on_exit,
            raise_on_release_error=raise_on_release_error,
        )

    # -- ReportService ------------------------------------------------------

    async def report(self, reports: Sequence[Report]) -> ReportResponse:
        """Send 1..500 reports directly (use :attr:`reporter` for batching)."""
        return await self._call(calls.report(reports))

    # -- ConfigService ------------------------------------------------------

    async def get_config(self, group: str, key: str, *, namespace: str = "") -> ConfigItem:
        """Fetch the current version of one config item."""
        return (await self._call(calls.get_config(namespace, group, key))).item

    async def batch_get_config(
        self, items: Iterable[ConfigKeyLike], *, namespace: str = ""
    ) -> BatchGetConfigResponse:
        """Fetch several config items; unknown ones are listed in ``missing``."""
        return await self._call(calls.batch_get_config(namespace, items))

    async def watch_config(
        self,
        items: Iterable[WatchItem | dict[str, Any]],
        *,
        namespace: str = "",
        timeout_ms: int = 30_000,
    ) -> WatchConfigResponse:
        """Long-poll until a watched item changes or ``timeout_ms`` elapses."""
        return await self._call(
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
        on_change: AsyncChangeCallback | None = None,
    ) -> AsyncConfigWatcher:
        """Create an :class:`AsyncConfigWatcher` bound to this client (not started)."""
        from .async_watcher import AsyncConfigWatcher

        watcher = AsyncConfigWatcher(
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
        self._watchers.add(watcher)
        return watcher

    # -- SecretService ------------------------------------------------------

    async def get_secret(self, path: str, *, version: int = 0) -> GetSecretResponse:
        """Read a secret value (``version=0`` reads the current version)."""
        return await self._call(calls.get_secret(path, version))

    # -- lifecycle ----------------------------------------------------------

    async def aclose(self, timeout: float | None = None) -> None:
        """Stop watchers, flush the reporter and close the HTTP client."""
        if self._closed:
            return
        self._closed = True
        watchers = list(self._watchers)
        if watchers:
            await asyncio.gather(*(watcher.stop() for watcher in watchers))
        # Closing also covers a reporter that was never used, so that later
        # submissions fail with ReporterClosedError instead of being lost.
        await self.reporter.close(timeout)
        if self._owns_http:
            await self._http.aclose()

    async def __aenter__(self) -> AsyncClient:
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        await self.aclose()

    def __repr__(self) -> str:
        return f"AsyncClient(url={self._settings.url!r}, node={self._settings.node!r})"

    # -- transport ----------------------------------------------------------

    async def _send_reports(self, reports: Sequence[Report]) -> ReportResponse:
        return await self._call(calls.report(reports), retry=NO_RETRY)

    async def _call(self, call: Call[R], *, retry: RetryPolicy | None = None) -> R:
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
                response = await self._http.post(
                    url, content=body, headers=self._headers, timeout=timeout
                )
            except httpx.TransportError as exc:
                if attempt < policy.max_retries and should_retry_exception(exc, call.idempotent):
                    delay = policy.delay(attempt)
                    if delay is not None:
                        _log_retry(call, attempt, delay, exc)
                        await asyncio.sleep(delay)
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
                    await asyncio.sleep(delay)
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
