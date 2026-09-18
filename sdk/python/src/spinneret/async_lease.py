"""Context-managed leases for the asyncio client."""

from __future__ import annotations

import logging
from datetime import datetime
from types import TracebackType
from typing import TYPE_CHECKING

from ._lease_base import LeaseBase
from ._lease_core import FinishPlan, LeaseState
from .errors import ReporterClosedError, SpinneretError
from .models import AcquireRequest, Report

if TYPE_CHECKING:
    from .async_client import AsyncClient

__all__ = ["AsyncManagedLease"]

logger = logging.getLogger("spinneret.lease")


class AsyncManagedLease(LeaseBase):
    """A lease acquired on ``__aenter__`` and released on ``__aexit__``.

    Semantics match :class:`spinneret.ManagedLease` (including
    ``raise_on_release_error``); :meth:`report`, :meth:`report_response` and
    :meth:`report_exception` are plain (non-blocking) methods that queue on the
    client's :class:`AsyncReporter`.

    Example::

        async with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
            async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
                response = await http.get("https://target.example.com/api/v1/feed")
            lease.report_response(response)
    """

    def __init__(
        self,
        client: AsyncClient,
        request: AcquireRequest,
        *,
        flush_on_exit: bool = False,
        raise_on_release_error: bool = False,
    ) -> None:
        """Create an unacquired lease; prefer :meth:`AsyncClient.lease`."""
        self._client = client
        self._state = LeaseState(request)
        self._flush_on_exit = flush_on_exit
        self._raise_on_release_error = raise_on_release_error

    async def __aenter__(self) -> AsyncManagedLease:
        if self._state.acquired:
            raise RuntimeError("lease context manager is not reentrant")
        request = self._state.request
        response = await self._client.acquire(
            request.site,
            request.client,
            request.uri,
            endpoint_group=request.endpoint_group,
            session_key=request.session_key,
            wait_ms=request.wait_ms,
        )
        self._state.activate(response)
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        await self._finish(may_raise=exc is None)

    async def renew(self, extend_ms: int = 0) -> datetime | None:
        """Extend the lease and return the new expiry."""
        self._state.ensure_open()
        response = await self._client.renew(self.lease_id, extend_ms)
        self._state.renewed(response.expires_at)
        return self.expires_at

    async def release(self) -> None:
        """Release the lease now (same semantics as leaving the ``async with`` block normally).

        Raises:
            SpinneretError: ``LeaseService/Release`` failed and the lease was
                created with ``raise_on_release_error=True``.
        """
        await self._finish(may_raise=True)

    async def _finish(self, *, may_raise: bool) -> None:
        try:
            error = await self._execute(self._state.finish())
        except Exception:
            logger.exception("spinneret failed to release lease %s", self._safe_lease_id())
            return
        if error is not None and may_raise:
            raise error

    async def _execute(self, plan: FinishPlan) -> SpinneretError | None:
        reporter = self._client.reporter
        error: SpinneretError | None = None
        if plan.report is not None:
            if reporter.closed:
                try:
                    await self._client.report([plan.report])
                except SpinneretError as err:
                    logger.warning("spinneret direct report delivery failed: %s", err)
                return None
            reporter.submit(plan.report)
        elif plan.rpc_release:
            try:
                self._released_by_rpc(await self._client.release(self.lease_id))
            except SpinneretError as err:
                error = self._release_rpc_failed(err)
        if (
            self._flush_on_exit
            and not reporter.closed
            and not await reporter.flush(reporter.options.close_timeout)
        ):
            logger.warning("spinneret reporter flush timed out on lease exit")
        return error

    def _submit(self, report: Report) -> None:
        reporter = self._client.reporter
        if reporter.closed:
            raise ReporterClosedError("reporter is closed", reason="reporter_closed")
        for ready in self._state.push(report):
            reporter.submit(ready)
