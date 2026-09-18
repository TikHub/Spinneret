from __future__ import annotations

import logging
import threading
import time
import weakref
from collections.abc import Sequence

import httpx
import pytest
import respx

import spinneret as sp
from spinneret._report_buffer import ReportBuffer
from spinneret.reporter import _atexit_closer, _reset_reporters_after_fork

from ._helpers import REPORT_PATH

FAST = sp.Backoff(initial=0.01, maximum=0.02)


def _report(index: int) -> sp.Report:
    return sp.Report(report_id=f"r{index}", lease_id="lse_1", uri="/a")


class FakeSender:
    """Records batches and replays scripted outcomes (exceptions or responses)."""

    def __init__(self, outcomes: Sequence[object] = ()) -> None:
        self.batches: list[list[str]] = []
        self.outcomes = list(outcomes)
        self.lock = threading.Lock()
        self.gate: threading.Event | None = None

    def __call__(self, reports: Sequence[sp.Report]) -> sp.ReportResponse:
        if self.gate is not None:
            self.gate.wait(5)
        with self.lock:
            self.batches.append([r.report_id for r in reports])
            outcome = self.outcomes.pop(0) if self.outcomes else None
        if isinstance(outcome, BaseException):
            raise outcome
        if isinstance(outcome, sp.ReportResponse):
            return outcome
        return sp.ReportResponse(accepted=len(reports))

    @property
    def sent_ids(self) -> list[str]:
        with self.lock:
            return [rid for batch in self.batches for rid in batch]


def _wait_until(predicate: object, timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():  # type: ignore[operator]
            return
        time.sleep(0.005)
    raise AssertionError("condition not met in time")


def test_batch_size_triggers_immediate_send() -> None:
    sender = FakeSender()
    options = sp.ReporterOptions(flush_interval=30, batch_size=5, max_batch_size=10)
    with sp.Reporter(sender, options) as reporter:
        for i in range(5):
            reporter.submit(_report(i))
        _wait_until(lambda: len(sender.batches) == 1)
        assert sender.batches[0] == ["r0", "r1", "r2", "r3", "r4"]


def test_flush_interval_triggers_send() -> None:
    sender = FakeSender()
    options = sp.ReporterOptions(flush_interval=0.05, batch_size=100)
    with sp.Reporter(sender, options) as reporter:
        reporter.submit(_report(1))
        assert sender.batches == []
        _wait_until(lambda: len(sender.batches) == 1, timeout=1.0)
        stats = reporter.stats
        assert (stats.submitted, stats.sent, stats.accepted, stats.queued) == (1, 1, 1, 0)


def test_flush_respects_max_batch_size() -> None:
    sender = FakeSender()
    options = sp.ReporterOptions(flush_interval=30, batch_size=3, max_batch_size=3)
    reporter = sp.Reporter(sender, options)
    sender.gate = threading.Event()
    for i in range(7):
        reporter.submit(_report(i))
    sender.gate.set()
    assert reporter.flush(timeout=3)
    assert [len(batch) for batch in sender.batches] == [3, 3, 1]
    assert sender.sent_ids == [f"r{i}" for i in range(7)]
    reporter.close()


def test_flush_without_reports_and_after_close() -> None:
    reporter = sp.Reporter(FakeSender())
    assert reporter.flush(timeout=0.1)
    reporter.close()
    assert reporter.closed
    with pytest.raises(sp.ReporterClosedError):
        reporter.submit(_report(1))
    reporter.close()


def test_flush_times_out_while_server_fails() -> None:
    sender = FakeSender([sp.TransportError("down")] * 1000)
    options = sp.ReporterOptions(flush_interval=30, backoff=FAST)
    reporter = sp.Reporter(sender, options)
    reporter.submit(_report(1))
    assert reporter.flush(timeout=0.1) is False
    reporter.close(timeout=0.05)
    assert reporter.stats.dropped == 1


def test_retries_transient_failures_then_delivers() -> None:
    sender = FakeSender(
        [
            sp.TransportError("refused"),
            sp.InternalError("boom", http_status=500),
            sp.ResourceExhausted("slow down", reason="rate_limited", retry_after_ms=10),
        ]
    )
    options = sp.ReporterOptions(flush_interval=0.01, backoff=FAST)
    with sp.Reporter(sender, options) as reporter:
        reporter.submit(_report(1))
        reporter.submit(_report(2))
        assert reporter.flush(timeout=3)
        stats = reporter.stats
    assert sender.batches[-1] == ["r1", "r2"]
    assert len(sender.batches) == 4
    assert (stats.failed_sends, stats.sent, stats.dropped) == (3, 2, 0)


@pytest.mark.parametrize(
    "error",
    [
        sp.InvalidArgument("bad batch"),
        sp.PermissionDenied("scope", reason="scope_missing"),
        ValueError("bug in sender"),
    ],
)
def test_permanent_failures_drop_the_batch(error: Exception) -> None:
    sender = FakeSender([error])
    options = sp.ReporterOptions(flush_interval=0.01, backoff=FAST)
    with sp.Reporter(sender, options) as reporter:
        reporter.submit(_report(1))
        assert reporter.flush(timeout=3)
        reporter.submit(_report(2))
        assert reporter.flush(timeout=3)
        stats = reporter.stats
    assert sender.batches == [["r1"], ["r2"]]
    assert (stats.dropped, stats.failed_sends, stats.sent) == (1, 1, 1)


def test_rejected_reports_are_counted_and_reported() -> None:
    rejected: list[sp.RejectedReport] = []
    response = sp.ReportResponse(
        accepted=1,
        duplicated=1,
        rejected=[sp.RejectedReport(report_id="r3", reason="lease_unknown", message="gone")],
    )
    sender = FakeSender([response])

    def on_rejected(item: sp.RejectedReport) -> None:
        rejected.append(item)
        raise RuntimeError("callback bugs must not kill the reporter")

    options = sp.ReporterOptions(flush_interval=30, batch_size=3, on_rejected=on_rejected)
    with sp.Reporter(sender, options) as reporter:
        for i in range(1, 4):
            reporter.submit(_report(i))
        assert reporter.flush(timeout=3)
        stats = reporter.stats
    assert [r.report_id for r in rejected] == ["r3"]
    assert (stats.accepted, stats.duplicated, stats.rejected) == (1, 1, 1)


def test_queue_overflow_drops_oldest(caplog: pytest.LogCaptureFixture) -> None:
    sender = FakeSender()
    sender.gate = threading.Event()
    options = sp.ReporterOptions(
        flush_interval=0.01, batch_size=1, max_batch_size=1, max_queue_size=3
    )
    reporter = sp.Reporter(sender, options)
    caplog.set_level(logging.WARNING, logger="spinneret.reporter")
    reporter.submit(_report(0))
    _wait_until(lambda: reporter.stats.queued == 0)  # r0 is in flight, blocked by the gate
    for i in range(1, 8):
        reporter.submit(_report(i))
    stats = reporter.stats
    assert (stats.dropped, stats.queued) == (4, 3)
    assert "report queue full" in caplog.text
    sender.gate.set()
    assert reporter.flush(timeout=3)
    reporter.close()
    assert sender.sent_ids == ["r0", "r5", "r6", "r7"]


def test_close_delivers_pending_reports() -> None:
    sender = FakeSender()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=30))
    for i in range(3):
        reporter.submit(_report(i))
    reporter.close(timeout=2)
    assert sender.sent_ids == ["r0", "r1", "r2"]
    assert reporter.stats.queued == 0


def test_close_gives_up_after_timeout() -> None:
    sender = FakeSender([sp.TransportError("down")] * 10_000)
    options = sp.ReporterOptions(flush_interval=30, backoff=sp.Backoff(initial=5, maximum=5))
    reporter = sp.Reporter(sender, options)
    for i in range(3):
        reporter.submit(_report(i))
    started = time.monotonic()
    reporter.close(timeout=0.2)
    assert time.monotonic() - started < 2.0
    assert reporter.stats.dropped == 3


def test_close_warns_when_worker_is_stuck(caplog: pytest.LogCaptureFixture) -> None:
    sender = FakeSender()
    sender.gate = threading.Event()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=0.01))
    reporter.submit(_report(1))
    _wait_until(lambda: reporter.stats.queued == 0)
    with caplog.at_level(logging.WARNING, logger="spinneret.reporter"):
        reporter.close(timeout=0)
    assert "did not stop" in caplog.text
    sender.gate.set()


def test_atexit_hook_closes_open_reporter() -> None:
    sender = FakeSender()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=30))
    reporter.submit(_report(1))
    hook = _atexit_closer(weakref.ref(reporter))
    hook()
    assert reporter.closed
    assert sender.sent_ids == ["r1"]
    hook()  # already closed: no-op


def test_reporter_through_client(settings: sp.Settings, router: respx.MockRouter) -> None:
    route = router.post(REPORT_PATH).mock(
        side_effect=[
            httpx.Response(503, json={"code": "unavailable", "message": "x"}),
            httpx.Response(200, json={"accepted": 2}),
        ]
    )
    options = sp.ReporterOptions(flush_interval=0.01, backoff=FAST)
    with sp.Client(settings=settings, reporter_options=options) as client:
        client.reporter.submit(_report(1))
        client.reporter.submit(_report(2))
        assert client.reporter.flush(timeout=3)
    # The client's unary retry policy is disabled for background delivery.
    assert route.call_count == 2


@pytest.mark.parametrize(
    "kwargs",
    [
        {"flush_interval": 0},
        {"max_batch_size": 501},
        {"max_batch_size": 0},
        {"batch_size": 0},
        {"batch_size": 200, "max_batch_size": 100},
        {"max_queue_size": 10},
        {"close_timeout": -1},
    ],
)
def test_options_validation(kwargs: dict[str, float]) -> None:
    with pytest.raises(ValueError, match="must be"):
        sp.ReporterOptions(**kwargs)  # type: ignore[arg-type]


def test_buffer_scheduling_with_fake_clock() -> None:
    now = [100.0]
    buffer = ReportBuffer(
        sp.ReporterOptions(flush_interval=1.0, batch_size=2, max_batch_size=2),
        clock=lambda: now[0],
        rng=lambda: 1.0,
    )
    assert buffer.seconds_until_due() is None
    assert not buffer.due()
    buffer.add(_report(1))
    assert buffer.seconds_until_due() == pytest.approx(1.0)
    now[0] += 0.4
    assert buffer.seconds_until_due() == pytest.approx(0.6)
    assert not buffer.due()
    buffer.add(_report(2))
    assert buffer.due()
    assert buffer.seconds_until_due() == 0.0
    batch = buffer.take()
    buffer.add(_report(3))
    buffer.requeue(batch)
    assert [entry.report.report_id for entry in buffer.take()] == ["r1", "r2"]
    assert buffer.options.batch_size == 2
    assert buffer.next_delay(None) == pytest.approx(0.5)
    retry_hint = sp.ResourceExhausted("x", retry_after_ms=20_000)
    assert buffer.next_delay(retry_hint) == pytest.approx(20.0)
    assert buffer.drop_all("test") == 1
    assert buffer.drop_all("test") == 0


def test_flush_from_worker_callback_does_not_block() -> None:
    results: list[tuple[bool, float]] = []
    holder: dict[str, sp.Reporter] = {}

    def on_rejected(_: sp.RejectedReport) -> None:
        started = time.monotonic()
        results.append((holder["reporter"].flush(timeout=5), time.monotonic() - started))

    rejected = sp.ReportResponse(rejected=[sp.RejectedReport(report_id="r1", reason="x")])
    sender = FakeSender([rejected])
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=0.01, on_rejected=on_rejected))
    holder["reporter"] = reporter
    reporter.submit(_report(1))
    _wait_until(lambda: results)
    drained, waited = results[0]
    assert drained is True
    assert waited < 1.0
    reporter.close()


def test_dead_worker_is_restarted(caplog: pytest.LogCaptureFixture) -> None:
    sender = FakeSender()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=0.01))
    reporter.submit(_report(1))
    assert reporter.flush(timeout=2)
    first = reporter._thread
    assert first is not None
    # Simulate a worker that is gone (as in a forked child) while reports are queued.
    stale = threading.Thread(target=lambda: None)
    stale.start()
    stale.join()
    with reporter._cond:
        reporter._thread = stale
        reporter._buffer.add(_report(2))
    assert reporter.flush(timeout=2)
    with caplog.at_level(logging.WARNING, logger="spinneret.reporter"):
        reporter.submit(_report(3))
        assert reporter.flush(timeout=2)
    assert sender.sent_ids == ["r1", "r2", "r3"]
    assert reporter._thread is not stale
    reporter.close()


def test_close_delivers_queue_without_live_worker() -> None:
    sender = FakeSender()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=30))
    stale = threading.Thread(target=lambda: None)
    stale.start()
    stale.join()
    reporter._thread = stale
    reporter._buffer.add(_report(1))
    reporter.close(timeout=2)
    assert sender.sent_ids == ["r1"]


def test_fork_reset_recreates_primitives() -> None:
    sender = FakeSender()
    reporter = sp.Reporter(sender, sp.ReporterOptions(flush_interval=30))
    # State inherited by a forked child: a worker that no longer exists, a
    # lock it held at fork time and a queued report.
    stale = threading.Thread(target=lambda: None)
    stale.start()
    stale.join()
    reporter._thread = stale
    reporter._inflight = 3
    reporter._buffer.add(_report(1))
    old_cond, old_event = reporter._cond, reporter._backoff_wakeup
    old_cond.acquire()
    try:
        _reset_reporters_after_fork()
    finally:
        old_cond.release()
    assert reporter._cond is not old_cond
    assert reporter._backoff_wakeup is not old_event
    assert reporter._thread is None
    reporter.submit(_report(2))
    assert reporter.flush(timeout=2)
    assert sender.sent_ids == ["r1", "r2"]
    reporter.close()
