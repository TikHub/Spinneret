from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Callable, Coroutine, Sequence
from typing import Any

import pytest

import spinneret as sp

FAST = sp.Backoff(initial=0.01, maximum=0.02)


def _report(index: int) -> sp.Report:
    return sp.Report(report_id=f"r{index}", lease_id="lse_1", uri="/a")


class AsyncFakeSender:
    def __init__(self, outcomes: Sequence[object] = ()) -> None:
        self.batches: list[list[str]] = []
        self.outcomes = list(outcomes)
        self.gate: asyncio.Event | None = None

    async def __call__(self, reports: Sequence[sp.Report]) -> sp.ReportResponse:
        if self.gate is not None:
            await self.gate.wait()
        self.batches.append([r.report_id for r in reports])
        outcome = self.outcomes.pop(0) if self.outcomes else None
        if isinstance(outcome, BaseException):
            raise outcome
        if isinstance(outcome, sp.ReportResponse):
            return outcome
        return sp.ReportResponse(accepted=len(reports))

    @property
    def sent_ids(self) -> list[str]:
        return [rid for batch in self.batches for rid in batch]


async def _wait_until(predicate: object, timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():  # type: ignore[operator]
            return
        await asyncio.sleep(0.005)
    raise AssertionError("condition not met in time")


async def test_batch_size_and_interval_triggers() -> None:
    sender = AsyncFakeSender()
    options = sp.ReporterOptions(flush_interval=0.05, batch_size=3, max_batch_size=3)
    async with sp.AsyncReporter(sender, options) as reporter:
        for i in range(3):
            reporter.submit(_report(i))
        await _wait_until(lambda: len(sender.batches) == 1)
        reporter.submit(_report(3))
        await asyncio.sleep(0.01)
        assert len(sender.batches) == 1
        await _wait_until(lambda: len(sender.batches) == 2, timeout=1.0)
        stats = reporter.stats
    assert sender.batches == [["r0", "r1", "r2"], ["r3"]]
    assert (stats.sent, stats.accepted) == (4, 4)


async def test_flush_and_max_batch_size() -> None:
    sender = AsyncFakeSender()
    options = sp.ReporterOptions(flush_interval=30, batch_size=2, max_batch_size=2)
    reporter = sp.AsyncReporter(sender, options)
    assert await reporter.flush(timeout=0.1)
    sender.gate = asyncio.Event()
    for i in range(5):
        reporter.submit(_report(i))
    sender.gate.set()
    assert await reporter.flush(timeout=3)
    assert [len(batch) for batch in sender.batches] == [2, 2, 1]
    await reporter.close()
    assert reporter.closed
    with pytest.raises(sp.ReporterClosedError):
        reporter.submit(_report(9))
    await reporter.close()
    assert await reporter.flush() is True


async def test_retries_then_delivers_and_drops_permanent_failures() -> None:
    sender = AsyncFakeSender(
        [
            sp.TransportError("down"),
            sp.Unavailable("x", reason="rebuilding", retry_after_ms=5),
            None,
            sp.InvalidArgument("bad"),
            ValueError("bug"),
        ]
    )
    options = sp.ReporterOptions(flush_interval=0.01, backoff=FAST)
    async with sp.AsyncReporter(sender, options) as reporter:
        reporter.submit(_report(1))
        assert await reporter.flush(timeout=3)
        reporter.submit(_report(2))
        assert await reporter.flush(timeout=3)
        reporter.submit(_report(3))
        assert await reporter.flush(timeout=3)
        stats = reporter.stats
    assert sender.batches == [["r1"], ["r1"], ["r1"], ["r2"], ["r3"]]
    assert (stats.failed_sends, stats.dropped, stats.sent) == (4, 2, 1)


async def test_rejected_callback() -> None:
    seen: list[str] = []
    response = sp.ReportResponse(
        rejected=[sp.RejectedReport(report_id="r1", reason="invalid_argument")]
    )
    sender = AsyncFakeSender([response])
    options = sp.ReporterOptions(
        flush_interval=0.01, on_rejected=lambda item: seen.append(item.report_id)
    )
    async with sp.AsyncReporter(sender, options) as reporter:
        reporter.submit(_report(1))
        assert await reporter.flush(timeout=3)
    assert seen == ["r1"]
    assert reporter.stats.rejected == 1


async def test_overflow_drops_oldest(caplog: pytest.LogCaptureFixture) -> None:
    sender = AsyncFakeSender()
    sender.gate = asyncio.Event()
    options = sp.ReporterOptions(
        flush_interval=0.01, batch_size=1, max_batch_size=1, max_queue_size=2
    )
    reporter = sp.AsyncReporter(sender, options)
    caplog.set_level(logging.WARNING, logger="spinneret.reporter")
    reporter.submit(_report(0))
    await _wait_until(lambda: reporter.stats.queued == 0)
    for i in range(1, 6):
        reporter.submit(_report(i))
    assert reporter.stats.dropped == 3
    sender.gate.set()
    await reporter.close(timeout=3)
    assert sender.sent_ids == ["r0", "r4", "r5"]
    assert "report queue full" in caplog.text


async def test_flush_timeout_and_close_timeout_drop() -> None:
    sender = AsyncFakeSender([sp.TransportError("down")] * 10_000)
    options = sp.ReporterOptions(flush_interval=30, backoff=sp.Backoff(initial=5, maximum=5))
    reporter = sp.AsyncReporter(sender, options)
    reporter.submit(_report(1))
    reporter.submit(_report(2))
    assert await reporter.flush(timeout=0.1) is False
    started = time.monotonic()
    await reporter.close(timeout=0.2)
    assert time.monotonic() - started < 2.0
    assert reporter.stats.dropped == 2


async def test_close_cancels_stuck_sender(caplog: pytest.LogCaptureFixture) -> None:
    sender = AsyncFakeSender()
    sender.gate = asyncio.Event()
    reporter = sp.AsyncReporter(sender, sp.ReporterOptions(flush_interval=0.01))
    reporter.submit(_report(1))
    await _wait_until(lambda: reporter.stats.queued == 0)
    with caplog.at_level(logging.WARNING, logger="spinneret.reporter"):
        await reporter.close(timeout=0)
    assert "cancelling" in caplog.text
    assert reporter.stats.queued == 1  # the in-flight batch was requeued on cancellation


def test_submit_requires_running_loop() -> None:
    reporter = sp.AsyncReporter(AsyncFakeSender())
    with pytest.raises(RuntimeError):
        reporter.submit(_report(1))
    assert reporter.options.batch_size == 100


def _run_in_new_loop(main: Callable[[], Coroutine[Any, Any, None]]) -> None:
    """Run ``main`` like ``asyncio.run`` without replacing the current event loop."""
    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(main())
        pending = asyncio.all_tasks(loop)
        for task in pending:
            task.cancel()
        if pending:
            loop.run_until_complete(asyncio.gather(*pending, return_exceptions=True))
    finally:
        loop.close()


def test_worker_task_is_restarted_on_a_new_event_loop(caplog: pytest.LogCaptureFixture) -> None:
    sender = AsyncFakeSender()
    reporter = sp.AsyncReporter(sender, sp.ReporterOptions(flush_interval=30))

    async def first_loop() -> None:
        reporter.submit(_report(1))
        assert await reporter.flush(timeout=2)
        reporter.submit(_report(2))  # left queued when the loop ends

    async def second_loop() -> None:
        with caplog.at_level(logging.WARNING, logger="spinneret.reporter"):
            assert await reporter.flush(timeout=2)
        reporter.submit(_report(3))
        await reporter.close(timeout=2)

    _run_in_new_loop(first_loop)
    _run_in_new_loop(second_loop)
    assert sender.sent_ids == ["r1", "r2", "r3"]
    assert "restarting" in caplog.text


def test_close_on_a_new_event_loop_delivers_queue() -> None:
    sender = AsyncFakeSender()
    reporter = sp.AsyncReporter(sender, sp.ReporterOptions(flush_interval=30))

    async def first_loop() -> None:
        reporter.submit(_report(1))

    async def second_loop() -> None:
        await reporter.close(timeout=2)
        await reporter.close(timeout=2)

    _run_in_new_loop(first_loop)
    _run_in_new_loop(second_loop)
    assert sender.sent_ids == ["r1"]


def test_flush_and_close_ignore_a_task_of_another_loop() -> None:
    sender = AsyncFakeSender()
    reporter = sp.AsyncReporter(sender, sp.ReporterOptions(flush_interval=30))
    other = asyncio.new_event_loop()
    try:
        reporter._task = other.create_task(asyncio.sleep(0))
        reporter._inflight = 1

        async def probe() -> None:
            assert await reporter.flush(timeout=0.1) is False
            await reporter.close(timeout=0.1)

        _run_in_new_loop(probe)
        other.run_until_complete(reporter._task)
    finally:
        other.close()
