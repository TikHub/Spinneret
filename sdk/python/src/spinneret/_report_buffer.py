"""Batching buffer and bookkeeping shared by the thread and asyncio reporters."""

from __future__ import annotations

import logging
import random
import time
from collections import deque
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Optional

from ._retry import Backoff, is_retryable_background_error
from .errors import SpinneretError
from .models import MAX_REPORTS_PER_CALL, RejectedReport, Report, ReportResponse

__all__ = ["QueuedReport", "ReportBuffer", "ReporterOptions", "ReporterStats"]

logger = logging.getLogger("spinneret.reporter")

_DROP_LOG_INTERVAL = 10.0
_DEFAULT_BACKOFF = Backoff(initial=0.5, maximum=30.0)


@dataclass(frozen=True)
class ReporterOptions:
    """Tuning of the background reporter.

    Attributes:
        flush_interval: Maximum seconds a report waits in the queue before a send.
        batch_size: Queue length that triggers an immediate send.
        max_batch_size: Maximum reports per ``Report`` call (server limit 500).
        max_queue_size: Bound of the queue; the oldest reports are dropped beyond it.
        backoff: Backoff between failed deliveries.
        close_timeout: Default seconds :meth:`close` spends delivering the queue.
        on_rejected: Optional callback invoked for every report rejected by the server.
    """

    flush_interval: float = 0.2
    batch_size: int = 100
    max_batch_size: int = MAX_REPORTS_PER_CALL
    max_queue_size: int = 10_000
    backoff: Backoff = _DEFAULT_BACKOFF
    close_timeout: float = 5.0
    on_rejected: Optional[Callable[[RejectedReport], None]] = None

    def __post_init__(self) -> None:
        if self.flush_interval <= 0:
            raise ValueError("flush_interval must be > 0")
        if not 1 <= self.max_batch_size <= MAX_REPORTS_PER_CALL:
            raise ValueError(f"max_batch_size must be within 1..{MAX_REPORTS_PER_CALL}")
        if not 1 <= self.batch_size <= self.max_batch_size:
            raise ValueError("batch_size must be within 1..max_batch_size")
        if self.max_queue_size < self.max_batch_size:
            raise ValueError("max_queue_size must be >= max_batch_size")
        if self.close_timeout < 0:
            raise ValueError("close_timeout must be >= 0")


@dataclass(frozen=True)
class ReporterStats:
    """Counters of a reporter since it was created.

    Attributes:
        submitted: Reports handed to :meth:`submit`.
        sent: Reports included in successful ``Report`` calls.
        accepted: Reports accepted by the server.
        duplicated: Reports ignored by the server as duplicates.
        rejected: Reports rejected by the server (dropped permanently).
        dropped: Reports dropped locally (queue overflow, permanent errors, close timeout).
        failed_sends: ``Report`` calls that failed.
        queued: Reports currently waiting in the queue.
    """

    submitted: int = 0
    sent: int = 0
    accepted: int = 0
    duplicated: int = 0
    rejected: int = 0
    dropped: int = 0
    failed_sends: int = 0
    queued: int = 0


@dataclass(frozen=True)
class QueuedReport:
    """A report waiting in the queue with its monotonic enqueue time."""

    report: Report
    enqueued_at: float


class ReportBuffer:
    """Bounded FIFO of pending reports with delivery bookkeeping.

    The buffer is not synchronized; the thread reporter guards it with a lock
    and the asyncio reporter only touches it from the event loop.
    """

    def __init__(
        self,
        options: ReporterOptions,
        clock: Callable[[], float] = time.monotonic,
        rng: Callable[[], float] = random.random,
    ) -> None:
        self._options = options
        self._clock = clock
        self._rng = rng
        self._queue: deque[QueuedReport] = deque()
        self._submitted = 0
        self._sent = 0
        self._accepted = 0
        self._duplicated = 0
        self._rejected = 0
        self._dropped = 0
        self._failed_sends = 0
        self._last_drop_log = -_DROP_LOG_INTERVAL
        self._drops_since_log = 0
        self.failures = 0

    def __len__(self) -> int:
        return len(self._queue)

    @property
    def options(self) -> ReporterOptions:
        """Options of the buffer."""
        return self._options

    def add(self, report: Report) -> None:
        """Append a report, dropping the oldest one when the queue is full."""
        self._submitted += 1
        self._queue.append(QueuedReport(report, self._clock()))
        self._trim()

    def due(self) -> bool:
        """Whether a batch should be sent now."""
        if not self._queue:
            return False
        if len(self._queue) >= self._options.batch_size:
            return True
        return self._clock() - self._queue[0].enqueued_at >= self._options.flush_interval

    def seconds_until_due(self) -> Optional[float]:
        """Seconds until the oldest report is due, or ``None`` when empty."""
        if not self._queue:
            return None
        if len(self._queue) >= self._options.batch_size:
            return 0.0
        elapsed = self._clock() - self._queue[0].enqueued_at
        return max(0.0, self._options.flush_interval - elapsed)

    def take(self) -> list[QueuedReport]:
        """Remove and return up to ``max_batch_size`` of the oldest reports."""
        count = min(len(self._queue), self._options.max_batch_size)
        return [self._queue.popleft() for _ in range(count)]

    def requeue(self, batch: Sequence[QueuedReport]) -> None:
        """Put a failed batch back at the front of the queue (oldest first)."""
        self._queue.extendleft(reversed(batch))
        self._trim()

    def drop_all(self, why: str) -> int:
        """Drop every queued report, returning how many were dropped."""
        count = len(self._queue)
        if count:
            self._queue.clear()
            self._dropped += count
            logger.warning("spinneret reporter dropped %d queued reports: %s", count, why)
        return count

    def next_delay(self, error: Optional[SpinneretError]) -> float:
        """Backoff before retrying after a failure (increments the failure count)."""
        self.failures += 1
        delay = self._options.backoff.delay(self.failures - 1, self._rng)
        if error is not None and error.retry_after_ms is not None:
            delay = max(delay, min(error.retry_after_ms / 1000.0, self._options.backoff.maximum))
        return delay

    def record_failure(self, batch: Sequence[QueuedReport], error: Exception) -> bool:
        """Record a failed send.

        Returns:
            ``True`` when the batch should be retried; otherwise the batch is
            counted as dropped.
        """
        self._failed_sends += 1
        if isinstance(error, SpinneretError) and is_retryable_background_error(error):
            logger.warning(
                "spinneret report delivery failed (%d reports, attempt %d), will retry: %s",
                len(batch),
                self.failures + 1,
                error,
            )
            return True
        self._dropped += len(batch)
        self.failures = 0
        if isinstance(error, SpinneretError):
            logger.error(
                "spinneret report delivery failed permanently, dropped %d reports: %s",
                len(batch),
                error,
            )
        else:
            logger.error(
                "spinneret report delivery raised unexpectedly, dropped %d reports",
                len(batch),
                exc_info=error,
            )
        return False

    def record_response(
        self, batch: Sequence[QueuedReport], response: ReportResponse
    ) -> list[RejectedReport]:
        """Record a successful send and return the reports rejected by the server."""
        self.failures = 0
        self._sent += len(batch)
        self._accepted += response.accepted
        self._duplicated += response.duplicated
        self._rejected += len(response.rejected)
        if response.rejected:
            first = response.rejected[0]
            logger.warning(
                "spinneret rejected %d of %d reports (first: %s: %s %s)",
                len(response.rejected),
                len(batch),
                first.report_id,
                first.reason,
                first.message,
            )
            for rejected in response.rejected[1:]:
                logger.debug(
                    "spinneret rejected report %s: %s %s",
                    rejected.report_id,
                    rejected.reason,
                    rejected.message,
                )
        return list(response.rejected)

    def notify_rejected(self, rejected: Sequence[RejectedReport]) -> None:
        """Invoke the ``on_rejected`` callback; call it without holding locks."""
        callback = self._options.on_rejected
        if callback is None:
            return
        for item in rejected:
            try:
                callback(item)
            except Exception:
                logger.exception("spinneret on_rejected callback raised")

    def stats(self) -> ReporterStats:
        """Snapshot of the counters."""
        return ReporterStats(
            submitted=self._submitted,
            sent=self._sent,
            accepted=self._accepted,
            duplicated=self._duplicated,
            rejected=self._rejected,
            dropped=self._dropped,
            failed_sends=self._failed_sends,
            queued=len(self._queue),
        )

    def _trim(self) -> None:
        overflow = len(self._queue) - self._options.max_queue_size
        if overflow <= 0:
            return
        for _ in range(overflow):
            self._queue.popleft()
        self._dropped += overflow
        self._drops_since_log += overflow
        now = self._clock()
        if now - self._last_drop_log >= _DROP_LOG_INTERVAL:
            logger.warning(
                "spinneret report queue full (max %d): dropped %d oldest reports "
                "(%d dropped in total)",
                self._options.max_queue_size,
                self._drops_since_log,
                self._dropped,
            )
            self._last_drop_log = now
            self._drops_since_log = 0
