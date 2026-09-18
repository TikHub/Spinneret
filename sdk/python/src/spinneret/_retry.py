"""Retry policy and retry classification shared by the sync and async clients."""

from __future__ import annotations

import random
from collections.abc import Callable
from dataclasses import dataclass
from typing import Optional

import httpx

from .errors import (
    Aborted,
    DeadlineExceeded,
    InternalError,
    ResourceExhausted,
    SpinneretError,
    TransportError,
    Unavailable,
)

__all__ = [
    "NO_RETRY",
    "Backoff",
    "RetryPolicy",
    "is_retryable_background_error",
    "should_retry_error",
    "should_retry_exception",
]

# Reasons of `unavailable` errors that are decisions, not transient failures.
_NON_RETRYABLE_UNAVAILABLE_REASONS = frozenset({"circuit_open", "site_paused"})

# Transport failures that happen before any request byte reaches the server,
# so retrying cannot duplicate a side effect.
_PRE_SEND_ERRORS: tuple[type[Exception], ...] = (
    httpx.ConnectError,
    httpx.ConnectTimeout,
    httpx.PoolTimeout,
)

# Transport failures caused by configuration, which retrying cannot fix.
_PERMANENT_TRANSPORT_ERRORS: tuple[type[Exception], ...] = (
    httpx.UnsupportedProtocol,
    httpx.LocalProtocolError,
    httpx.ProxyError,
)


@dataclass(frozen=True)
class Backoff:
    """Exponential backoff with "equal jitter" (half fixed, half random).

    Attributes:
        initial: Base delay of the first retry in seconds.
        maximum: Upper bound of a single delay in seconds.
        multiplier: Growth factor per attempt.
    """

    initial: float = 0.1
    maximum: float = 2.0
    multiplier: float = 2.0

    def delay(self, attempt: int, rng: Callable[[], float] = random.random) -> float:
        """Return the delay before retry number ``attempt`` (0-based)."""
        base = min(self.maximum, self.initial * (self.multiplier ** max(0, attempt)))
        half = base / 2.0
        return half + half * rng()


@dataclass(frozen=True)
class RetryPolicy:
    """Retry policy of unary calls.

    Transport errors and ``unavailable`` responses (except ``circuit_open`` and
    ``site_paused``) are retried with jittered exponential backoff. Calls that
    are not idempotent (``Acquire``, ``AcquireBatch``) are only retried when the
    failure provably happened before the request was sent (connection refused,
    connect timeout, pool timeout) or when the server explicitly answered
    ``unavailable``.

    Attributes:
        max_retries: Number of retries after the first attempt (0 disables retries).
        backoff: Backoff between attempts.
        max_retry_after: A server retry hint longer than this (seconds) is not
            waited for; the error is raised instead.
    """

    max_retries: int = 2
    backoff: Backoff = Backoff()
    max_retry_after: float = 5.0

    def __post_init__(self) -> None:
        if self.max_retries < 0:
            raise ValueError("max_retries must be >= 0")
        if self.max_retry_after < 0:
            raise ValueError("max_retry_after must be >= 0")

    def delay(
        self,
        attempt: int,
        retry_after_ms: Optional[int] = None,
        rng: Callable[[], float] = random.random,
    ) -> Optional[float]:
        """Return the delay before retry ``attempt`` or ``None`` when the hint is too long."""
        delay = self.backoff.delay(attempt, rng)
        if retry_after_ms is not None:
            hint = retry_after_ms / 1000.0
            if hint > self.max_retry_after:
                return None
            delay = max(delay, hint)
        return delay


#: Policy that never retries.
NO_RETRY = RetryPolicy(max_retries=0)


def should_retry_exception(exc: Exception, idempotent: bool) -> bool:
    """Decide whether an httpx transport exception may be retried."""
    if isinstance(exc, _PERMANENT_TRANSPORT_ERRORS):
        return False
    if isinstance(exc, _PRE_SEND_ERRORS):
        return True
    return idempotent and isinstance(exc, httpx.TransportError)


def should_retry_error(error: SpinneretError, from_connect_body: bool, idempotent: bool) -> bool:
    """Decide whether an error response may be retried.

    Args:
        error: The mapped error.
        from_connect_body: Whether the server sent a Connect error body. A bare
            HTTP 502/503/504 (for example from a load balancer) is ambiguous: the
            request may have reached the server.
        idempotent: Whether repeating the call is safe.
    """
    if not isinstance(error, Unavailable) or isinstance(error, TransportError):
        return False
    if error.reason in _NON_RETRYABLE_UNAVAILABLE_REASONS:
        return False
    return from_connect_body or idempotent


def is_retryable_background_error(error: SpinneretError) -> bool:
    """Decide whether a background delivery (reports, watches) should be retried later."""
    if isinstance(error, Unavailable):
        return error.reason not in _NON_RETRYABLE_UNAVAILABLE_REASONS
    if isinstance(error, (InternalError, DeadlineExceeded, Aborted, ResourceExhausted)):
        return True
    return error.http_status >= 500
