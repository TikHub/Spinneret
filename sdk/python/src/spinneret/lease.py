"""Context-managed leases for the synchronous client."""

from __future__ import annotations

import logging
from datetime import datetime
from types import TracebackType
from typing import TYPE_CHECKING, Optional

from ._lease_base import LeaseBase
from ._lease_core import FinishPlan, LeaseState
from .errors import ReporterClosedError, SpinneretError
from .models import AcquireRequest, Report

if TYPE_CHECKING:
    from .client import Client

__all__ = ["ManagedLease"]

logger = logging.getLogger("spinneret.lease")


class ManagedLease(LeaseBase):
    """A lease acquired on ``__enter__`` and released on ``__exit__``.

    Reports are queued on the client's background reporter. The most recent
    report is held back until the next report or the end of the block so that
    it can carry ``release=True``: on exit the last report is marked as the
    releasing one. When nothing was reported, ``LeaseService/Release`` is
    called instead. With ``flush_on_exit`` exit waits for the reporter to
    deliver the queue.

    Exit does not raise because of release failures: a ``Release`` error whose
    reason says the lease has already ended (``lease_released``,
    ``lease_unknown``, ``lease_expired``) is logged at debug level, other errors
    at warning level. With ``raise_on_release_error=True`` those other
    ``Release`` errors are raised, unless the block itself raised.

    Example::

        with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
            with httpx.Client(**lease.httpx_kwargs()) as http:
                response = http.get("https://target.example.com/api/v1/feed")
            lease.report_response(response, markers=["empty_list"] if empty else [])
    """

    def __init__(
        self,
        client: Client,
        request: AcquireRequest,
        *,
        flush_on_exit: bool = False,
        raise_on_release_error: bool = False,
    ) -> None:
        """Create an unacquired lease; prefer :meth:`Client.lease`."""
        self._client = client
        self._state = LeaseState(request)
        self._flush_on_exit = flush_on_exit
        self._raise_on_release_error = raise_on_release_error

    def __enter__(self) -> ManagedLease:
        if self._state.acquired:
            raise RuntimeError("lease context manager is not reentrant")
        request = self._state.request
        response = self._client.acquire(
            request.site,
            request.client,
            request.uri,
            endpoint_group=request.endpoint_group,
            session_key=request.session_key,
            wait_ms=request.wait_ms,
        )
        self._state.activate(response)
        return self

    def __exit__(
        self,
        exc_type: Optional[type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        self._finish(may_raise=exc is None)

    def renew(self, extend_ms: int = 0) -> Optional[datetime]:
        """Extend the lease and return the new expiry."""
        self._state.ensure_open()
        response = self._client.renew(self.lease_id, extend_ms)
        self._state.renewed(response.expires_at)
        return self.expires_at

    def release(self) -> None:
        """Release the lease now (same semantics as leaving the ``with`` block normally).

        Raises:
            SpinneretError: ``LeaseService/Release`` failed and the lease was
                created with ``raise_on_release_error=True``.
        """
        self._finish(may_raise=True)

    def _finish(self, *, may_raise: bool) -> None:
        try:
            error = self._execute(self._state.finish())
        except Exception:
            logger.exception("spinneret failed to release lease %s", self._safe_lease_id())
            return
        if error is not None and may_raise:
            raise error

    def _execute(self, plan: FinishPlan) -> Optional[SpinneretError]:
        reporter = self._client.reporter
        error: Optional[SpinneretError] = None
        if plan.report is not None:
            try:
                reporter.submit(plan.report)
            except ReporterClosedError:
                try:
                    self._client.report([plan.report])
                except SpinneretError as err:
                    logger.warning("spinneret direct report delivery failed: %s", err)
                return None
        elif plan.rpc_release:
            try:
                self._released_by_rpc(self._client.release(self.lease_id))
            except SpinneretError as err:
                error = self._release_rpc_failed(err)
        if (
            self._flush_on_exit
            and not reporter.closed
            and not reporter.flush(reporter.options.close_timeout)
        ):
            logger.warning("spinneret reporter flush timed out on lease exit")
        return error

    def _submit(self, report: Report) -> None:
        reporter = self._client.reporter
        if reporter.closed:
            raise ReporterClosedError("reporter is closed", reason="reporter_closed")
        for ready in self._state.push(report):
            reporter.submit(ready)
