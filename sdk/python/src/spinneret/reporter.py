"""Thread-based background reporter that batches reports to ``ReportService/Report``."""

from __future__ import annotations

import atexit
import logging
import os
import threading
import time
import weakref
from collections.abc import Callable, Sequence
from types import TracebackType
from typing import Optional

from ._report_buffer import QueuedReport, ReportBuffer, ReporterOptions, ReporterStats
from .errors import ReporterClosedError, SpinneretError
from .models import Report, ReportResponse

__all__ = ["Reporter"]

logger = logging.getLogger("spinneret.reporter")

_JOIN_GRACE = 0.5
_ATEXIT_TIMEOUT = 2.0

SendFunc = Callable[[Sequence[Report]], ReportResponse]

# Reporters whose synchronization primitives must be recreated in a forked child.
_LIVE_REPORTERS: weakref.WeakSet[Reporter] = weakref.WeakSet()


class Reporter:
    """Batches reports in a daemon thread and delivers them in the background.

    Reports are sent when ``batch_size`` reports are queued or the oldest one
    has waited ``flush_interval`` seconds, at most ``max_batch_size`` per call.
    Failed deliveries caused by transport errors or server-side failures are
    retried with backoff; permanently failing batches and reports rejected by
    the server are dropped. The queue is bounded: when full, the oldest
    reports are dropped and counted in :attr:`stats`.

    :meth:`submit` never blocks on the network. Call :meth:`close` (or close the
    owning client) to deliver the remaining reports; an ``atexit`` hook closes
    reporters that are still open at interpreter shutdown. The worker thread
    is restarted by :meth:`submit` when it is no longer alive, for example in a
    child process created with ``os.fork()``.
    """

    def __init__(
        self,
        send: SendFunc,
        options: Optional[ReporterOptions] = None,
        *,
        name: str = "spinneret-reporter",
    ) -> None:
        """Create a reporter.

        Args:
            send: Delivers one batch; must raise :class:`SpinneretError` on failure.
            options: Batching options.
            name: Name of the worker thread.
        """
        self._send = send
        self._options = options or ReporterOptions()
        self._name = name
        self._buffer = ReportBuffer(self._options)
        self._cond = threading.Condition(threading.Lock())
        self._backoff_wakeup = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._inflight = 0
        self._flush_waiters = 0
        self._closing = False
        self._close_deadline = 0.0
        self._atexit_hook: Optional[Callable[[], None]] = None
        _LIVE_REPORTERS.add(self)

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
        with self._cond:
            return self._buffer.stats()

    def submit(self, report: Report) -> None:
        """Queue a report for delivery.

        Raises:
            ReporterClosedError: When the reporter has been closed.
        """
        with self._cond:
            if self._closing:
                raise ReporterClosedError("reporter is closed", reason="reporter_closed")
            self._buffer.add(report)
            self._ensure_thread()
            self._cond.notify_all()

    def flush(self, timeout: Optional[float] = None) -> bool:
        """Send every queued report now and wait for the deliveries to finish.

        Returns:
            ``True`` when the queue was drained, ``False`` on timeout.
        """
        deadline = None if timeout is None else time.monotonic() + timeout
        with self._cond:
            if self._thread is threading.current_thread():
                # Called from a callback on the worker thread, which cannot
                # deliver while it waits: report the state instead of blocking.
                return not (len(self._buffer) or self._inflight)
            if len(self._buffer) and not self._closing:
                self._ensure_thread()
            self._flush_waiters += 1
            self._cond.notify_all()
            try:
                while len(self._buffer) or self._inflight:
                    thread = self._thread
                    if thread is None or not thread.is_alive():
                        return False
                    remaining = None if deadline is None else deadline - time.monotonic()
                    if remaining is not None and remaining <= 0:
                        return False
                    self._cond.wait(remaining)
                return True
            finally:
                self._flush_waiters -= 1

    def close(self, timeout: Optional[float] = None) -> None:
        """Deliver the queued reports (for at most ``timeout`` seconds) and stop.

        Reports still queued when the timeout expires are dropped. Closing is
        idempotent.
        """
        budget = self._options.close_timeout if timeout is None else max(0.0, timeout)
        with self._cond:
            if not self._closing:
                self._closing = True
                self._close_deadline = time.monotonic() + budget
            if len(self._buffer):
                # Deliver reports inherited without a live worker (after fork).
                self._ensure_thread()
            thread = self._thread
            self._cond.notify_all()
        self._backoff_wakeup.set()
        if self._atexit_hook is not None:
            atexit.unregister(self._atexit_hook)
            self._atexit_hook = None
        if thread is None or thread is threading.current_thread():
            return
        thread.join(max(0.0, self._close_deadline - time.monotonic()) + _JOIN_GRACE)
        if thread.is_alive():
            logger.warning("spinneret reporter did not stop within %.1fs", budget)

    def __enter__(self) -> Reporter:
        return self

    def __exit__(
        self,
        exc_type: Optional[type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        self.close()

    # -- worker -------------------------------------------------------------

    def _ensure_thread(self) -> None:
        if self._thread is not None and self._thread.is_alive():
            return
        if self._thread is not None:
            logger.warning("spinneret reporter worker thread is not running, restarting it")
        if self._atexit_hook is not None:
            atexit.unregister(self._atexit_hook)
        thread = threading.Thread(target=self._run, name=self._name, daemon=True)
        self._thread = thread
        self._inflight = 0
        self._atexit_hook = _atexit_closer(weakref.ref(self))
        atexit.register(self._atexit_hook)
        thread.start()

    def _reset_after_fork(self) -> None:
        # Only the forking thread survives in the child: locks held by the
        # parent's worker would never be released, so recreate the primitives.
        # Queued reports are kept (the server deduplicates by report_id).
        self._cond = threading.Condition(threading.Lock())
        self._backoff_wakeup = threading.Event()
        self._thread = None
        self._inflight = 0
        self._flush_waiters = 0

    def _run(self) -> None:
        while True:
            with self._cond:
                batch = self._next_batch()
                if batch is None:
                    self._cond.notify_all()
                    return
                self._inflight = len(batch)
            try:
                response = self._send([entry.report for entry in batch])
            except Exception as exc:
                self._handle_failure(batch, exc)
            else:
                with self._cond:
                    rejected = self._buffer.record_response(batch, response)
                    self._inflight = 0
                    self._cond.notify_all()
                self._buffer.notify_rejected(rejected)

    def _next_batch(self) -> Optional[list[QueuedReport]]:
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
            self._cond.wait(self._buffer.seconds_until_due())

    def _handle_failure(self, batch: list[QueuedReport], exc: Exception) -> None:
        with self._cond:
            retry = self._buffer.record_failure(batch, exc)
            if retry and self._closing and time.monotonic() >= self._close_deadline:
                self._buffer.requeue(batch)
                self._buffer.drop_all("close timeout expired")
                retry = False
            elif retry:
                self._buffer.requeue(batch)
            self._inflight = 0
            self._cond.notify_all()
            if not retry:
                return
            delay = self._buffer.next_delay(exc if isinstance(exc, SpinneretError) else None)
            if self._closing:
                delay = min(delay, max(0.0, self._close_deadline - time.monotonic()))
                self._backoff_wakeup.clear()
        self._backoff_wakeup.wait(delay)


def _reset_reporters_after_fork() -> None:
    for reporter in list(_LIVE_REPORTERS):
        reporter._reset_after_fork()


if hasattr(os, "register_at_fork"):  # pragma: no branch - unavailable on Windows
    os.register_at_fork(after_in_child=_reset_reporters_after_fork)


def _atexit_closer(ref: weakref.ReferenceType[Reporter]) -> Callable[[], None]:
    def close_at_exit() -> None:
        reporter = ref()
        if reporter is not None and not reporter.closed:
            reporter.close(timeout=min(_ATEXIT_TIMEOUT, reporter.options.close_timeout))

    return close_at_exit
