"""asyncio-based background reporter that batches reports to ``ReportService/Report``."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import time
from collections.abc import Awaitable, Callable, Sequence
from types import TracebackType
from typing import Optional

from ._report_buffer import QueuedReport, ReportBuffer, ReporterOptions, ReporterStats
from .errors import ReporterClosedError, SpinneretError
from .models import Report, ReportResponse

__all__ = ["AsyncReporter"]

logger = logging.getLogger("spinneret.reporter")

_STOP_GRACE = 0.5

AsyncSendFunc = Callable[[Sequence[Report]], Awaitable[ReportResponse]]


class AsyncReporter:
    """Batches reports in an asyncio task and delivers them in the background.

    Semantics match :class:`spinneret.Reporter`: sends are triggered by
    ``batch_size`` or ``flush_interval``, transient failures are retried with
    backoff, rejected reports are dropped and the queue is bounded (drop
    oldest). The worker task starts on the first :meth:`submit`, which must be
    called from the event loop thread, and is restarted by :meth:`submit` when
    it is no longer running on the current loop. Always ``await close()``
    before the event loop stops, otherwise queued reports are lost.
    """

    def __init__(self, send: AsyncSendFunc, options: Optional[ReporterOptions] = None) -> None:
        """Create a reporter.

        Args:
            send: Coroutine function delivering one batch; raises :class:`SpinneretError`.
            options: Batching options.
        """
        self._send = send
        self._options = options or ReporterOptions()
        self._buffer = ReportBuffer(self._options)
        self._task: Optional[asyncio.Task[None]] = None
        self._wakeup: Optional[asyncio.Event] = None
        self._closed_event: Optional[asyncio.Event] = None
        self._progress: Optional[asyncio.Event] = None
        self._inflight = 0
        self._flush_waiters = 0
        self._closing = False
        self._close_deadline = 0.0

    @property
    def options(self) -> ReporterOptions:
        """Options of this reporter."""
        return self._options

    @property
    def closed(self) -> bool:
        """Whether :meth:`close` has been called."""
        return self._closing

    @property
    def stats(self) -> ReporterStats:
        """Snapshot of the delivery counters."""
        return self._buffer.stats()

    def submit(self, report: Report) -> None:
        """Queue a report for delivery (non-blocking; call from the event loop).

        Raises:
            ReporterClosedError: When the reporter has been closed.
            RuntimeError: When called without a running event loop.
        """
        if self._closing:
            raise ReporterClosedError("reporter is closed", reason="reporter_closed")
        self._ensure_task()
        self._buffer.add(report)
        self._wake()

    async def flush(self, timeout: Optional[float] = None) -> bool:
        """Send every queued report now and wait for the deliveries to finish.

        Returns:
            ``True`` when the queue was drained, ``False`` on timeout.
        """
        deadline = None if timeout is None else time.monotonic() + timeout
        if len(self._buffer) and not self._closing:
            self._ensure_task()
        loop = asyncio.get_running_loop()
        self._flush_waiters += 1
        self._wake()
        try:
            while len(self._buffer) or self._inflight:
                task, progress = self._task, self._progress
                if task is None or task.done() or task.get_loop() is not loop or progress is None:
                    return False
                remaining = None if deadline is None else deadline - time.monotonic()
                if remaining is not None and remaining <= 0:
                    return False
                progress.clear()
                waiter = asyncio.ensure_future(progress.wait())
                try:
                    await asyncio.wait(
                        {waiter, task}, timeout=remaining, return_when=asyncio.FIRST_COMPLETED
                    )
                finally:
                    waiter.cancel()
            return True
        finally:
            self._flush_waiters -= 1

    async def close(self, timeout: Optional[float] = None) -> None:
        """Deliver the queued reports (for at most ``timeout`` seconds) and stop.

        Reports still queued when the timeout expires are dropped. Closing is
        idempotent.
        """
        budget = self._options.close_timeout if timeout is None else max(0.0, timeout)
        if not self._closing:
            self._closing = True
            self._close_deadline = time.monotonic() + budget
        if len(self._buffer):
            # Deliver the queue even when the worker is gone or bound to another loop.
            self._ensure_task()
        task = self._task
        if task is None or task.done() or task.get_loop() is not asyncio.get_running_loop():
            return
        if self._closed_event is not None:
            self._closed_event.set()
        self._wake()
        remaining = max(0.0, self._close_deadline - time.monotonic()) + _STOP_GRACE
        done, _ = await asyncio.wait({task}, timeout=remaining)
        if not done:
            logger.warning("spinneret reporter did not stop within %.1fs, cancelling", budget)
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task

    async def __aenter__(self) -> AsyncReporter:
        return self

    async def __aexit__(
        self,
        exc_type: Optional[type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        await self.close()

    # -- worker -------------------------------------------------------------

    def _ensure_task(self) -> None:
        loop = asyncio.get_running_loop()
        task = self._task
        if task is not None and not task.done() and task.get_loop() is loop:
            return
        if task is not None:
            # The worker ended unexpectedly or belongs to an event loop that is
            # gone (for example a previous ``asyncio.run``): start a new one.
            logger.warning("spinneret reporter task is not running, restarting it")
            self._inflight = 0
        self._wakeup = asyncio.Event()
        self._closed_event = asyncio.Event()
        self._progress = asyncio.Event()
        self._task = loop.create_task(self._run(), name="spinneret-reporter")

    def _wake(self) -> None:
        if self._wakeup is not None:
            self._wakeup.set()

    def _notify_progress(self) -> None:
        if self._progress is not None:
            self._progress.set()

    async def _run(self) -> None:
        while True:
            batch = await self._next_batch()
            if batch is None:
                return
            self._inflight = len(batch)
            try:
                response = await self._send([entry.report for entry in batch])
            except asyncio.CancelledError:
                self._buffer.requeue(batch)
                self._inflight = 0
                raise
            except Exception as exc:
                await self._handle_failure(batch, exc)
            else:
                rejected = self._buffer.record_response(batch, response)
                self._buffer.notify_rejected(rejected)
                self._inflight = 0
            self._notify_progress()

    async def _next_batch(self) -> Optional[list[QueuedReport]]:
        wakeup = self._wakeup
        if wakeup is None:  # pragma: no cover - the task is created with the event
            return None
        while True:
            if self._closing:
                if not len(self._buffer):
                    return None
                if time.monotonic() >= self._close_deadline:
                    self._buffer.drop_all("close timeout expired")
                    return None
                return self._buffer.take()
            if self._buffer.due() or (self._flush_waiters and len(self._buffer)):
                return self._buffer.take()
            wakeup.clear()
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(wakeup.wait(), timeout=self._buffer.seconds_until_due())

    async def _handle_failure(self, batch: list[QueuedReport], exc: Exception) -> None:
        retry = self._buffer.record_failure(batch, exc)
        self._inflight = 0
        if not retry:
            return
        self._buffer.requeue(batch)
        if self._closing and time.monotonic() >= self._close_deadline:
            self._buffer.drop_all("close timeout expired")
            return
        delay = self._buffer.next_delay(exc if isinstance(exc, SpinneretError) else None)
        self._notify_progress()
        if self._closing:
            await asyncio.sleep(min(delay, max(0.0, self._close_deadline - time.monotonic())))
            return
        closed = self._closed_event
        if closed is None:  # pragma: no cover - the task is created with the event
            return
        with contextlib.suppress(asyncio.TimeoutError):
            await asyncio.wait_for(closed.wait(), timeout=delay)
