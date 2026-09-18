from __future__ import annotations

import httpx
import pytest

import spinneret as sp
from spinneret._retry import (
    is_retryable_background_error,
    should_retry_error,
    should_retry_exception,
)


def test_backoff_equal_jitter_bounds() -> None:
    backoff = sp.Backoff(initial=0.1, maximum=1.0, multiplier=2.0)
    assert backoff.delay(0, rng=lambda: 0.0) == pytest.approx(0.05)
    assert backoff.delay(0, rng=lambda: 1.0) == pytest.approx(0.1)
    assert backoff.delay(3, rng=lambda: 1.0) == pytest.approx(0.8)
    assert backoff.delay(10, rng=lambda: 1.0) == pytest.approx(1.0)
    assert backoff.delay(-1, rng=lambda: 1.0) == pytest.approx(0.1)


def test_retry_policy_delay_respects_retry_after() -> None:
    policy = sp.RetryPolicy(backoff=sp.Backoff(initial=0.1, maximum=0.1), max_retry_after=2.0)
    assert policy.delay(0, None, rng=lambda: 1.0) == pytest.approx(0.1)
    assert policy.delay(0, 1500, rng=lambda: 1.0) == pytest.approx(1.5)
    assert policy.delay(0, 10, rng=lambda: 1.0) == pytest.approx(0.1)
    assert policy.delay(0, 2001, rng=lambda: 1.0) is None


@pytest.mark.parametrize("kwargs", [{"max_retries": -1}, {"max_retry_after": -0.1}])
def test_retry_policy_validation(kwargs: dict[str, float]) -> None:
    with pytest.raises(ValueError, match="must be"):
        sp.RetryPolicy(**kwargs)  # type: ignore[arg-type]


def test_no_retry_constant() -> None:
    assert sp.NO_RETRY.max_retries == 0


@pytest.mark.parametrize(
    ("exc", "idempotent", "expected"),
    [
        (httpx.ConnectError("refused"), False, True),
        (httpx.ConnectTimeout("t"), False, True),
        (httpx.PoolTimeout("t"), False, True),
        (httpx.ReadTimeout("t"), False, False),
        (httpx.ReadTimeout("t"), True, True),
        (httpx.RemoteProtocolError("x"), False, False),
        (httpx.RemoteProtocolError("x"), True, True),
        (httpx.WriteError("x"), True, True),
        (httpx.ProxyError("x"), True, False),
        (httpx.UnsupportedProtocol("x"), True, False),
        (httpx.LocalProtocolError("x"), True, False),
        (ValueError("x"), True, False),
    ],
)
def test_should_retry_exception(exc: Exception, idempotent: bool, expected: bool) -> None:
    assert should_retry_exception(exc, idempotent) is expected


@pytest.mark.parametrize(
    ("error", "from_body", "idempotent", "expected"),
    [
        (sp.Unavailable("x", reason="rebuilding"), True, False, True),
        (sp.Unavailable("x", reason=""), False, False, False),
        (sp.Unavailable("x", reason=""), False, True, True),
        (sp.CircuitOpen("x", reason="circuit_open"), True, True, False),
        (sp.SitePaused("x", reason="site_paused"), True, True, False),
        (sp.TransportError("x"), True, True, False),
        (sp.NoIdentityAvailable("x", reason="no_identity_available"), True, True, False),
        (sp.InternalError("x"), True, True, False),
    ],
)
def test_should_retry_error(
    error: sp.SpinneretError, from_body: bool, idempotent: bool, expected: bool
) -> None:
    assert should_retry_error(error, from_body, idempotent) is expected


@pytest.mark.parametrize(
    ("error", "expected"),
    [
        (sp.TransportError("x"), True),
        (sp.Unavailable("x"), True),
        (sp.CircuitOpen("x", reason="circuit_open"), False),
        (sp.InternalError("x"), True),
        (sp.DeadlineExceeded("x"), True),
        (sp.Aborted("x"), True),
        (sp.ResourceExhausted("x", reason="rate_limited"), True),
        (sp.SpinneretError("x", code="unknown", http_status=502), True),
        (sp.InvalidArgument("x"), False),
        (sp.Unauthenticated("x"), False),
        (sp.PermissionDenied("x"), False),
    ],
)
def test_is_retryable_background_error(error: sp.SpinneretError, expected: bool) -> None:
    assert is_retryable_background_error(error) is expected
